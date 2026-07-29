// Purpose: Incremental maintenance for a DHT-backed Prefix Hash Tree.
package pht

import (
	"context"
	"errors"
	"sync"
)

// MutableIndex maintains tuple names in a PHT. A MutableIndex serializes local
// mutations; distributed callers must additionally route mutations through one
// authoritative index coordinator (or an equivalent distributed transaction
// mechanism) to prevent lost read-modify-write updates.
type MutableIndex struct {
	store ValueStore
	mu    sync.Mutex
}

func NewMutableIndex(store ValueStore) (*MutableIndex, error) {
	if store == nil {
		return nil, errors.New("PHT store required")
	}
	return &MutableIndex{store: store}, nil
}

// Insert adds key to the index if it is not already present.
func (m *MutableIndex) Insert(ctx context.Context, key string) error {
	if key == "" {
		return errors.New("PHT key required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	root, err := GetNode(ctx, m.store, "")
	if err != nil {
		root = NewLeaf("")
	}
	cur := root
	ancestors := make([]*Node, 0, len(key))
	for cur.IsInternal() {
		if len(key) <= len(cur.Prefix) {
			return errors.New("PHT key terminates at an internal prefix")
		}
		seg := string(key[len(cur.Prefix)])
		ancestors = append(ancestors, cur)
		child, childErr := GetNode(ctx, m.store, cur.Prefix+seg)
		if childErr != nil {
			child = NewLeaf(cur.Prefix + seg)
			cur.Children[seg] = child
		}
		cur = child
	}
	for _, existing := range cur.Entries {
		if existing == key {
			return nil
		}
	}
	cur.Entries = append(cur.Entries, key)
	MaybeSplit(cur)
	BuildNodeBloom(cur, 0, 0, 0)
	incrementVersions(cur)
	if err := PutNodeRecursive(ctx, m.store, cur); err != nil {
		return err
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		addKeyToBloom(ancestors[i], key)
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
		if len(key) <= len(cur.Prefix) {
			return nil
		}
		seg := string(key[len(cur.Prefix)])
		cur, err = GetNode(ctx, m.store, cur.Prefix+seg)
		if err != nil {
			return nil
		}
	}
	for i, existing := range cur.Entries {
		if existing != key {
			continue
		}
		copy(cur.Entries[i:], cur.Entries[i+1:])
		cur.Entries = cur.Entries[:len(cur.Entries)-1]
		BuildNodeBloom(cur, 0, 0, 0)
		cur.Version++
		return PutNode(ctx, m.store, cur)
	}
	return nil
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
