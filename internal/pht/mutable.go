// Purpose: Incremental maintenance for a DHT-backed Prefix Hash Tree.
package pht

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/routing"
)

// ErrStaleWriteFence means a mutation was correctly rejected because a newer
// distributed-writer epoch is already persisted. Callers may refresh the
// authority record and retry the same idempotent mutation.
var ErrStaleWriteFence = errors.New("stale PHT writer fence")

const (
	fenceAdoptionReadAttempts = 6
	fenceAdoptionReadBackoff  = 50 * time.Millisecond
)

// MutableIndex maintains tuple names in a PHT. A MutableIndex serializes local
// mutations; distributed callers must additionally route mutations through one
// authoritative index coordinator (or an equivalent distributed transaction
// mechanism) to prevent lost read-modify-write updates.
type MutableIndex struct {
	store *mutationCacheStore
	mu    sync.Mutex
}

// mutationCacheStore gives the elected writer read-after-write consistency
// while retaining the DHT as durable, queryable storage. A DHT Put can
// complete before a following distributed Get observes that version; without
// this cache, a serialized writer can read an older PHT node and overwrite a
// newer mutation. The cache belongs to one MutableIndex and is protected by
// its mutation mutex.
type mutationCacheStore struct {
	base   ValueStore
	values map[string][]byte
}

func newMutationCacheStore(base ValueStore) *mutationCacheStore {
	return &mutationCacheStore{
		base:   base,
		values: make(map[string][]byte),
	}
}

func (s *mutationCacheStore) PutValue(
	ctx context.Context,
	key string,
	value []byte,
	opts ...interface{},
) error {
	if err := s.base.PutValue(ctx, key, value, opts...); err != nil {
		return err
	}
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func (s *mutationCacheStore) GetValue(
	ctx context.Context,
	key string,
	opts ...interface{},
) ([]byte, error) {
	if value, ok := s.values[key]; ok {
		return append([]byte(nil), value...), nil
	}
	value, err := s.base.GetValue(ctx, key, opts...)
	if err != nil {
		return nil, err
	}
	s.values[key] = append([]byte(nil), value...)
	return append([]byte(nil), value...), nil
}

func (s *mutationCacheStore) reset() {
	s.values = make(map[string][]byte)
}

func NewMutableIndex(store ValueStore) (*MutableIndex, error) {
	if store == nil {
		return nil, errors.New("PHT store required")
	}
	return &MutableIndex{store: newMutationCacheStore(store)}, nil
}

// AdoptFence migrates every persisted node in the index to a new writer fence
// before that writer accepts mutations. Migrating the full tree is necessary:
// fencing only the root would still allow a stale writer to overwrite an
// untouched leaf referenced by the new root.
func (m *MutableIndex) AdoptFence(ctx context.Context, fence WriteFence) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// A different elected writer may have mutated the PHT since this process
	// last held authority. Fence adoption must therefore rebuild from the DHT,
	// not migrate a stale writer-local snapshot into the stronger epoch.
	m.store.reset()
	root, err := GetNode(ctx, m.store, "")
	if err != nil {
		if isMissingNode(err) {
			return nil
		}
		return fmt.Errorf("read PHT root for fence adoption: %w", err)
	}
	current := WriteFence{Epoch: root.Epoch, Writer: root.Writer}
	switch CompareWriteFences(fence, current) {
	case -1:
		return fmt.Errorf(
			"%w: cannot adopt epoch=%d writer=%q over epoch=%d writer=%q",
			ErrStaleWriteFence,
			fence.Epoch,
			fence.Writer,
			current.Epoch,
			current.Writer,
		)
	case 0:
		// PutNodeRecursive publishes children before the root, so an equal root
		// proves that a prior adoption completed.
		return nil
	}
	if err := m.loadDescendants(ctx, root, fence); err != nil {
		return err
	}
	stampWriteFence(root, fence)
	incrementVersions(root)
	return PutNodeRecursive(ctx, m.store, root)
}

func (m *MutableIndex) loadDescendants(ctx context.Context, n *Node, fence WriteFence) error {
	if n == nil || n.IsLeaf() {
		return requireCurrentFence(n, fence)
	}
	if err := requireCurrentFence(n, fence); err != nil {
		return err
	}
	for segment := range n.Children {
		prefix := n.Prefix + segment
		child, err := m.getDescendantForFenceAdoption(ctx, prefix)
		if err != nil {
			return fmt.Errorf("read PHT child %q for fence adoption: %w", prefix, err)
		}
		if err := m.loadDescendants(ctx, child, fence); err != nil {
			return err
		}
		n.Children[segment] = child
	}
	return nil
}

// A parent is published only after its children, but DHT reads performed by a
// newly elected owner can still observe the parent before a child has reached
// that owner's lookup path. Retry only missing descendants: fabricating a leaf
// would lose indexed names, while retrying arbitrary errors would mask real
// transport or validation failures.
func (m *MutableIndex) getDescendantForFenceAdoption(
	ctx context.Context,
	prefix string,
) (*Node, error) {
	var lastErr error
	for attempt := 0; attempt < fenceAdoptionReadAttempts; attempt++ {
		node, err := GetNode(ctx, m.store, prefix)
		if err == nil {
			return node, nil
		}
		if !isMissingNode(err) {
			return nil, err
		}
		lastErr = err
		if attempt == fenceAdoptionReadAttempts-1 {
			break
		}
		delay := fenceAdoptionReadBackoff << attempt
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

// Insert adds key to the index if it is not already present.
func (m *MutableIndex) Insert(ctx context.Context, key string) error {
	return m.InsertFenced(ctx, key, WriteFence{})
}

// InsertFenced adds key under an explicit distributed-writer fence.
func (m *MutableIndex) InsertFenced(ctx context.Context, key string, fence WriteFence) error {
	if key == "" {
		return errors.New("PHT key required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	root, err := GetNode(ctx, m.store, "")
	if err != nil {
		if !isMissingNode(err) {
			return fmt.Errorf("read PHT root: %w", err)
		}
		root = NewLeaf("")
	}
	cur := root
	ancestors := make([]*Node, 0, len(key))
	for cur.IsInternal() {
		if err := requireCurrentFence(cur, fence); err != nil {
			return err
		}
		if len(key) <= len(cur.Prefix) {
			return errors.New("PHT key terminates at an internal prefix")
		}
		seg := string(key[len(cur.Prefix)])
		ancestors = append(ancestors, cur)
		child, childErr := GetNode(ctx, m.store, cur.Prefix+seg)
		if childErr != nil {
			if !isMissingNode(childErr) {
				return fmt.Errorf("read PHT child %q: %w", cur.Prefix+seg, childErr)
			}
			child = NewLeaf(cur.Prefix + seg)
			cur.Children[seg] = child
		}
		cur = child
	}
	if err := requireCurrentFence(cur, fence); err != nil {
		return err
	}
	exists := false
	for _, existing := range cur.Entries {
		if existing == key {
			exists = true
			break
		}
	}
	currentFence := WriteFence{Epoch: cur.Epoch, Writer: cur.Writer}
	if exists && CompareWriteFences(fence, currentFence) == 0 {
		return nil
	}
	if !exists {
		cur.Entries = append(cur.Entries, key)
	}
	MaybeSplit(cur)
	BuildNodeBloom(cur, 0, 0, 0)
	stampWriteFence(cur, fence)
	incrementVersions(cur)
	if err := PutNodeRecursive(ctx, m.store, cur); err != nil {
		return err
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		addKeyToBloom(ancestors[i], key)
		stampWriteFence(ancestors[i], fence)
		ancestors[i].Version++
		if err := PutNode(ctx, m.store, ancestors[i]); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes key from its leaf. Bloom summaries above the leaf are allowed
// to retain stale bits: this can create false-positive traversal but never a
// false negative, and exact result filtering preserves correctness.
func (m *MutableIndex) Delete(ctx context.Context, key string) error {
	return m.DeleteFenced(ctx, key, WriteFence{})
}

// DeleteFenced removes key under an explicit distributed-writer fence.
func (m *MutableIndex) DeleteFenced(ctx context.Context, key string, fence WriteFence) error {
	if key == "" {
		return errors.New("PHT key required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	cur, err := GetNode(ctx, m.store, "")
	if err != nil {
		return nil
	}
	for cur.IsInternal() {
		if err := requireCurrentFence(cur, fence); err != nil {
			return err
		}
		if len(key) <= len(cur.Prefix) {
			return nil
		}
		seg := string(key[len(cur.Prefix)])
		cur, err = GetNode(ctx, m.store, cur.Prefix+seg)
		if err != nil {
			return nil
		}
	}
	if err := requireCurrentFence(cur, fence); err != nil {
		return err
	}
	for i, existing := range cur.Entries {
		if existing != key {
			continue
		}
		copy(cur.Entries[i:], cur.Entries[i+1:])
		cur.Entries = cur.Entries[:len(cur.Entries)-1]
		BuildNodeBloom(cur, 0, 0, 0)
		stampWriteFence(cur, fence)
		cur.Version++
		return PutNode(ctx, m.store, cur)
	}
	return nil
}

func requireCurrentFence(n *Node, requested WriteFence) error {
	if n == nil {
		return nil
	}
	current := WriteFence{Epoch: n.Epoch, Writer: n.Writer}
	if CompareWriteFences(requested, current) < 0 {
		return fmt.Errorf(
			"%w epoch=%d writer=%q; current epoch=%d writer=%q",
			ErrStaleWriteFence,
			requested.Epoch,
			requested.Writer,
			current.Epoch,
			current.Writer,
		)
	}
	return nil
}

func isMissingNode(err error) bool {
	return errors.Is(err, routing.ErrNotFound) ||
		strings.Contains(strings.ToLower(err.Error()), "not found")
}

func incrementVersions(n *Node) {
	if n == nil {
		return
	}
	n.Version++
	for _, child := range n.Children {
		incrementVersions(child)
	}
}

func addKeyToBloom(n *Node, key string) {
	if n.Bloom == nil {
		n.Bloom = NewBloomFilter(DefaultBloomSize, DefaultBloomHashes)
	}
	for _, ngram := range ExtractNGrams(key, DefaultNGramSize) {
		n.Bloom.AddString(ngram)
	}
}
