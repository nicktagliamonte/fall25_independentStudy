// Purpose: PHT integration with Kademlia DHT as underlying storage.

package pht

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// DHTNamespace is the key prefix for PHT records in the DHT.
const DHTNamespace = "/pht/"

// nodeWire is the serialized form of a Node for DHT storage.
// Internal nodes store child segments (prefix suffixes); children are stored separately.
type nodeWire struct {
	Kind     NodeKind `json:"kind"`
	Prefix   string   `json:"prefix"`
	Entries  []string `json:"entries,omitempty"`
	Segments []string `json:"segments,omitempty"`
	BloomB64 string   `json:"bloom,omitempty"`
}

// ValueStore provides Put/Get for PHT records. Implemented by routing.ValueStore (e.g. IpfsDHT).
type ValueStore interface {
	PutValue(ctx context.Context, key string, value []byte, opts ...interface{}) error
	GetValue(ctx context.Context, key string, opts ...interface{}) ([]byte, error)
}

// dhtKey returns the DHT key for a prefix.
func dhtKey(prefix string) string {
	return DHTNamespace + hex.EncodeToString(HashPrefix(prefix))
}

// PutNode stores a node in the DHT under its prefix.
func PutNode(ctx context.Context, store ValueStore, n *Node) error {
	if store == nil || n == nil {
		return errors.New("store and node required")
	}
	w := nodeWire{Kind: n.Kind, Prefix: n.Prefix}
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

// GetNode retrieves a node from the DHT by prefix.
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
	n := &Node{Kind: w.Kind, Prefix: w.Prefix}
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
func NavigateDHT(ctx context.Context, store ValueStore, prefix string) (*Node, error) {
	return GetNode(ctx, store, prefix)
}

// CollectUnderDHT recursively fetches nodes from the DHT and returns all keys
// in the subtree. For internal nodes, fetches each child by prefix.
func CollectUnderDHT(ctx context.Context, store ValueStore, n *Node) ([]string, error) {
	return collectUnderDHTInternal(ctx, store, n, nil)
}

// CollectUnderDHTWithPrune performs the same traversal as CollectUnderDHT but
// prunes branches whose Bloom filter does not contain all ngrams (for substring
// queries like *forest*). Fetches children in parallel and uses parallel Bloom
// checks to exclude impossible matches.
func CollectUnderDHTWithPrune(ctx context.Context, store ValueStore, n *Node, ngrams []string) ([]string, error) {
	return collectUnderDHTInternal(ctx, store, n, ngrams)
}

func collectUnderDHTInternal(ctx context.Context, store ValueStore, n *Node, ngrams []string) ([]string, error) {
	if n == nil {
		return nil, nil
	}
	if n.IsLeaf() {
		if len(ngrams) > 0 && !BloomContainsAll(n.Bloom, ngrams) {
			return nil, nil
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
		children = PruneByBloom(children, ngrams)
	}
	var parts [][]string
	for _, child := range children {
		if child == nil {
			continue
		}
		sub, err := collectUnderDHTInternal(ctx, store, child, ngrams)
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
func PrefixQueryDHT(ctx context.Context, store ValueStore, prefix string) ([]string, error) {
	n, err := NavigateDHT(ctx, store, prefix)
	if err != nil || n == nil {
		return nil, err
	}
	return CollectUnderDHT(ctx, store, n)
}

// PutNodeRecursive stores a node and all its descendants in the DHT.
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
