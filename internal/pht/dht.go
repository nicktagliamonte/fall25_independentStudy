// Purpose: PHT integration with Kademlia DHT as underlying storage.

package pht

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// QueryStats reports index work performed by one PHT query.
type QueryStats struct {
	NodesFetched       int
	BranchesConsidered int
	BranchesPruned     int
	Candidates         int
	Matches            int
}

type queryCounters struct {
	nodesFetched       atomic.Int64
	branchesConsidered atomic.Int64
	branchesPruned     atomic.Int64
	candidates         atomic.Int64
}

type countingValueStore struct {
	ValueStore
	counters *queryCounters
}

func (s countingValueStore) GetValue(ctx context.Context, key string, opts ...interface{}) ([]byte, error) {
	s.counters.nodesFetched.Add(1)
	return s.ValueStore.GetValue(ctx, key, opts...)
}

// DHTNamespace is the key prefix for PHT records in the DHT.
const DHTNamespace = "/pht/"

// nodeWire is the serialized form of a Node for DHT storage.
// Internal nodes store child segments (prefix suffixes); children are stored separately.
type nodeWire struct {
	Version  uint64   `json:"version"`
	Kind     NodeKind `json:"kind"`
	Prefix   string   `json:"prefix"`
	Entries  []string `json:"entries,omitempty"`
	Segments []string `json:"segments,omitempty"`
	BloomB64 string   `json:"bloom,omitempty"`
}

// ValueStore provides Put/Get for PHT records. Implemented by routing.ValueStore (e.g. IpfsDHT).
type ValueStore interface {
	// PutValue stores value under key.
	//
	// Parameters:
	//   - ctx (context.Context): cancels/deadlines the underlying store call.
	//   - key (string): the namespaced record key.
	//   - value ([]byte): the record payload to store.
	//   - opts (...interface{}): implementation-specific options.
	//
	// Returns:
	//   - error: non-nil if the store operation failed.
	PutValue(ctx context.Context, key string, value []byte, opts ...interface{}) error

	// GetValue retrieves the value stored under key.
	//
	// Parameters:
	//   - ctx (context.Context): cancels/deadlines the underlying store call.
	//   - key (string): the namespaced record key.
	//   - opts (...interface{}): implementation-specific options.
	//
	// Returns:
	//   - []byte: the stored value.
	//   - error: non-nil if the key is not found or the retrieval failed.
	GetValue(ctx context.Context, key string, opts ...interface{}) ([]byte, error)
}

// dhtKey returns the DHT key for a prefix.
//
// Parameters:
//   - prefix (string): the PHT node prefix.
//
// Returns:
//   - string: DHTNamespace concatenated with the hex-encoded hash of prefix.
func dhtKey(prefix string) string {
	return DHTNamespace + hex.EncodeToString(HashPrefix(prefix))
}

// PutNode stores a node in the DHT under its prefix. Leaf nodes are stored
// with their Entries; internal nodes are stored with their child path
// segments (Segments) rather than the children themselves — callers must
// store children separately (see PutNodeRecursive).
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT put.
//   - store (ValueStore): the DHT-backed store to write to.
//   - n (*Node): the node to serialize and store.
//
// Returns:
//   - error: non-nil if store or n is nil, Bloom marshaling failed, JSON
//     marshaling failed, or the DHT put failed.
func PutNode(ctx context.Context, store ValueStore, n *Node) error {
	if store == nil || n == nil {
		return errors.New("store and node required")
	}
	w := nodeWire{Version: n.Version, Kind: n.Kind, Prefix: n.Prefix}
	if n.IsLeaf() {
		w.Entries = make([]string, len(n.Entries))
		copy(w.Entries, n.Entries)
	} else {
		for seg := range n.Children {
			w.Segments = append(w.Segments, seg)
		}
	}
	if n.Bloom != nil {
		bloomData, err := n.Bloom.MarshalBinary()
		if err != nil {
			return fmt.Errorf("marshal bloom: %w", err)
		}
		w.BloomB64 = base64.StdEncoding.EncodeToString(bloomData)
	}
	data, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("marshal node: %w", err)
	}
	return store.PutValue(ctx, dhtKey(n.Prefix), data)
}

// GetNode retrieves a node from the DHT by prefix. For internal nodes, the
// returned Node's Children map is populated with keys for each stored child
// segment but nil *Node values — callers must fetch each child separately
// (e.g. via GetNode at n.Prefix+seg, as collectUnderDHTInternal does).
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT get.
//   - store (ValueStore): the DHT-backed store to read from.
//   - prefix (string): the node's prefix/key.
//
// Returns:
//   - *Node: the deserialized node.
//   - error: non-nil if store is nil, the DHT get failed, or the stored value
//     could not be unmarshaled as JSON.
func GetNode(ctx context.Context, store ValueStore, prefix string) (*Node, error) {
	if store == nil {
		return nil, errors.New("store required")
	}
	data, err := store.GetValue(ctx, dhtKey(prefix))
	if err != nil {
		return nil, err
	}
	var w nodeWire
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("unmarshal node: %w", err)
	}
	n := &Node{Version: w.Version, Kind: w.Kind, Prefix: w.Prefix}
	if n.Kind == KindLeaf {
		n.Entries = w.Entries
		if n.Entries == nil {
			n.Entries = []string{}
		}
	} else {
		n.Children = make(map[string]*Node)
		for _, seg := range w.Segments {
			n.Children[seg] = nil
		}
	}
	if w.BloomB64 != "" {
		bloomData, err := base64.StdEncoding.DecodeString(w.BloomB64)
		if err == nil {
			n.Bloom, _ = NewBloomFilterFromBinary(bloomData)
		}
	}
	return n, nil
}

// NavigateDHT fetches the node at the given prefix from the DHT.
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT get.
//   - store (ValueStore): the DHT-backed store to read from.
//   - prefix (string): the node's prefix/key.
//
// Returns:
//   - *Node: the deserialized node.
//   - error: non-nil if the underlying GetNode call failed.
func NavigateDHT(ctx context.Context, store ValueStore, prefix string) (*Node, error) {
	return GetNode(ctx, store, prefix)
}

// CollectUnderDHT recursively fetches nodes from the DHT and returns all keys
// in the subtree. For internal nodes, fetches each child by prefix.
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT fetches.
//   - store (ValueStore): the DHT-backed store to read from.
//   - n (*Node): the subtree root (as already fetched from the DHT) to collect under.
//
// Returns:
//   - []string: all keys found in n's subtree.
//   - error: currently always nil at the top level (per-child fetch errors
//     are swallowed and that child's branch is skipped).
func CollectUnderDHT(ctx context.Context, store ValueStore, n *Node) ([]string, error) {
	return collectUnderDHTInternal(ctx, store, n, nil, nil)
}

// CollectUnderDHTWithPrune performs the same traversal as CollectUnderDHT but
// prunes branches whose Bloom filter does not contain all ngrams (for substring
// queries like *forest*). Fetches children in parallel and uses parallel Bloom
// checks to exclude impossible matches.
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT fetches.
//   - store (ValueStore): the DHT-backed store to read from.
//   - n (*Node): the subtree root to collect under.
//   - ngrams ([]string): n-grams that must all be present in a branch's Bloom
//     filter for that branch to be traversed.
//
// Returns:
//   - []string: keys found in the subtree, restricted to branches that pass the Bloom check.
//   - error: currently always nil at the top level (per-child fetch errors are swallowed).
func CollectUnderDHTWithPrune(ctx context.Context, store ValueStore, n *Node, ngrams []string) ([]string, error) {
	return collectUnderDHTInternal(ctx, store, n, ngrams, nil)
}

// collectUnderDHTInternal is the shared recursive implementation behind
// CollectUnderDHT and CollectUnderDHTWithPrune. For leaves, returns Entries
// (after a Bloom check when ngrams is non-empty). For internal nodes, fetches
// all children concurrently (one goroutine per child segment), optionally
// prunes them via PruneByBloom, recurses into the remaining children, and
// merges results with CombineResults. Child fetch/recursion errors are
// swallowed per-branch (that branch is simply excluded from the result)
// rather than aborting the whole traversal.
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT fetches (not
//     actively checked between goroutine dispatch and collection).
//   - store (ValueStore): the DHT-backed store to read from.
//   - n (*Node): the subtree root to collect under; nil returns (nil, nil).
//   - ngrams ([]string): n-grams for Bloom-filter pruning; empty disables pruning.
//
// Returns:
//   - []string: the deduplicated union of keys found across all traversed branches.
//   - error: always nil (reserved for future use; per-branch errors are swallowed).
func collectUnderDHTInternal(ctx context.Context, store ValueStore, n *Node, ngrams []string, counters *queryCounters) ([]string, error) {
	if n == nil {
		return nil, nil
	}
	if n.IsLeaf() {
		if len(ngrams) > 0 && !BloomContainsAll(n.Bloom, ngrams) {
			if counters != nil {
				counters.branchesPruned.Add(1)
			}
			return nil, nil
		}
		if counters != nil {
			counters.candidates.Add(int64(len(n.Entries)))
		}
		out := make([]string, len(n.Entries))
		copy(out, n.Entries)
		return out, nil
	}
	segs := make([]string, 0, len(n.Children))
	for seg := range n.Children {
		segs = append(segs, seg)
	}
	children := make([]*Node, len(segs))
	if counters != nil {
		counters.branchesConsidered.Add(int64(len(segs)))
	}
	type result struct {
		i   int
		err error
	}
	results := make(chan result, len(segs))
	for i, seg := range segs {
		go func(i int, seg string) {
			child, err := GetNode(ctx, store, n.Prefix+seg)
			if err != nil {
				results <- result{i: i, err: err}
				return
			}
			children[i] = child
			results <- result{i: i}
		}(i, seg)
	}
	for range segs {
		<-results
	}
	if len(ngrams) > 0 {
		before := len(children)
		children = PruneByBloom(children, ngrams)
		if counters != nil {
			counters.branchesPruned.Add(int64(before - len(children)))
		}
	}
	var parts [][]string
	for _, child := range children {
		if child == nil {
			continue
		}
		sub, err := collectUnderDHTInternal(ctx, store, child, ngrams, counters)
		if err != nil {
			continue
		}
		if len(sub) > 0 {
			parts = append(parts, sub)
		}
	}
	return CombineResults(parts...), nil
}

// PrefixQueryDHT fetches the node at prefix from the DHT and returns all keys under it.
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT fetches.
//   - store (ValueStore): the DHT-backed store to read from.
//   - prefix (string): the prefix to look up.
//
// Returns:
//   - []string: all keys in the subtree rooted at prefix.
//   - error: non-nil if fetching the prefix node failed; nil, nil if the node
//     does not exist or is nil.
func PrefixQueryDHT(ctx context.Context, store ValueStore, prefix string) ([]string, error) {
	return prefixQueryDHT(ctx, store, prefix, nil)
}

// PrefixQueryDHTWithStats is PrefixQueryDHT with direct index-work metrics.
func PrefixQueryDHTWithStats(ctx context.Context, store ValueStore, prefix string) ([]string, QueryStats, error) {
	counters := &queryCounters{}
	counted := countingValueStore{ValueStore: store, counters: counters}
	rows, err := prefixQueryDHT(ctx, counted, prefix, counters)
	stats := counters.snapshot()
	stats.Matches = len(rows)
	return rows, stats, err
}

func prefixQueryDHT(ctx context.Context, store ValueStore, prefix string, counters *queryCounters) ([]string, error) {
	n, err := NavigateDHT(ctx, store, "")
	if err != nil || n == nil {
		return nil, err
	}
	for n.IsInternal() && len(n.Prefix) < len(prefix) {
		seg := string(prefix[len(n.Prefix)])
		n, err = GetNode(ctx, store, n.Prefix+seg)
		if err != nil || n == nil {
			return nil, err
		}
	}
	rows, err := collectUnderDHTInternal(ctx, store, n, nil, counters)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, key := range rows {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	return out, nil
}

func (c *queryCounters) snapshot() QueryStats {
	return QueryStats{
		NodesFetched:       int(c.nodesFetched.Load()),
		BranchesConsidered: int(c.branchesConsidered.Load()),
		BranchesPruned:     int(c.branchesPruned.Load()),
		Candidates:         int(c.candidates.Load()),
	}
}

// PutNodeRecursive stores a node and all its descendants in the DHT.
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT puts.
//   - store (ValueStore): the DHT-backed store to write to.
//   - n (*Node): the (sub)tree root to store, with in-memory Children populated for internal nodes.
//
// Returns:
//   - error: non-nil if storing n itself or any descendant failed (stops at the first failure).
func PutNodeRecursive(ctx context.Context, store ValueStore, n *Node) error {
	if err := PutNode(ctx, store, n); err != nil {
		return err
	}
	if n.IsInternal() {
		for _, child := range n.Children {
			if err := PutNodeRecursive(ctx, store, child); err != nil {
				return err
			}
		}
	}
	return nil
}
