// Purpose: Prefix Hash Tree (PHT) node structure for navigable tree over DHT.

package pht

import "crypto/sha256"

// MAX_BUCKET_SIZE is the maximum entries in a leaf before splitting.
const MAX_BUCKET_SIZE = 16

// NodeKind distinguishes leaf nodes (hold entries) from internal nodes (routing).
type NodeKind int

const (
	KindLeaf NodeKind = iota
	KindInternal
)

// Node represents a PHT node. Leaf nodes hold indexed keys; internal nodes hold
// child pointers for tree navigation. The prefix is the path from root to this node.
type Node struct {
	// Version increases on every persisted mutation so DHT validators can
	// select the newest value for a prefix.
	Version uint64
	// Kind distinguishes a leaf (holds Entries) from an internal node (holds Children).
	Kind NodeKind
	// Prefix is the path from the tree root to this node.
	Prefix string

	// Children is valid when Kind == KindInternal. Key is the next path segment.
	Children map[string]*Node

	// Entries is valid when Kind == KindLeaf. Keys matching this node's prefix.
	Entries []string

	// Bloom summarizes n-grams of contained keys for substring query pruning.
	Bloom *BloomFilter
}

// NewLeaf returns a leaf node for the given prefix.
//
// Parameters:
//   - prefix (string): the path from root to this node.
//
// Returns:
//   - *Node: a new leaf node with an empty Entries slice.
func NewLeaf(prefix string) *Node {
	return &Node{
		Kind:    KindLeaf,
		Prefix:  prefix,
		Entries: make([]string, 0),
	}
}

// NewInternal returns an internal node for the given prefix.
//
// Parameters:
//   - prefix (string): the path from root to this node.
//
// Returns:
//   - *Node: a new internal node with an empty Children map.
func NewInternal(prefix string) *Node {
	return &Node{
		Kind:     KindInternal,
		Prefix:   prefix,
		Children: make(map[string]*Node),
	}
}

// IsLeaf returns true when the node holds entries.
//
// Returns:
//   - bool: true if n is non-nil and n.Kind == KindLeaf.
func (n *Node) IsLeaf() bool { return n != nil && n.Kind == KindLeaf }

// IsInternal returns true when the node has children for navigation.
//
// Returns:
//   - bool: true if n is non-nil and n.Kind == KindInternal.
func (n *Node) IsInternal() bool { return n != nil && n.Kind == KindInternal }

// HashPrefix returns the SHA-256 hash of a prefix for use as a DHT key.
//
// Parameters:
//   - prefix (string): the prefix string to hash.
//
// Returns:
//   - []byte: the 32-byte SHA-256 digest of prefix.
func HashPrefix(prefix string) []byte {
	h := sha256.Sum256([]byte(prefix))
	return h[:]
}

// IndexKey produces the sequence of prefix hashes for a key (e.g. filename).
// For "abc" returns [Hash(""), Hash("a"), Hash("ab"), Hash("abc")].
// Each hash corresponds to a level in the PHT from root to leaf.
//
// Parameters:
//   - key (string): the full key to index (e.g. a filename or tuple name).
//
// Returns:
//   - [][]byte: hashes of each prefix of key, from the empty prefix (root) to
//     the full key, in root-to-leaf order.
func IndexKey(key string) [][]byte {
	out := make([][]byte, 0, len(key)+1)
	for i := 0; i <= len(key); i++ {
		out = append(out, HashPrefix(key[:i]))
	}
	return out
}

// Navigate returns the node at the end of the prefix path from root, or nil if
// the path does not exist. For empty prefix returns root.
//
// Parameters:
//   - root (*Node): the tree root to navigate from.
//   - prefix (string): the path to descend, one character per tree level.
//
// Returns:
//   - *Node: the node at the end of prefix, or nil if root is nil or the path
//     does not exist (an intermediate node is a leaf or missing a required child).
func Navigate(root *Node, prefix string) *Node {
	if root == nil {
		return nil
	}
	cur := root
	for _, r := range prefix {
		if !cur.IsInternal() {
			return nil
		}
		seg := string(r)
		cur = cur.Children[seg]
		if cur == nil {
			return nil
		}
	}
	return cur
}

// CollectUnder returns all keys in the subtree rooted at n (for prefix query).
//
// Parameters:
//   - n (*Node): the subtree root to collect from.
//
// Returns:
//   - []string: all entries found in n's subtree; nil if n is nil.
func CollectUnder(n *Node) []string {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		out := make([]string, len(n.Entries))
		copy(out, n.Entries)
		return out
	}
	var out []string
	for _, child := range n.Children {
		out = append(out, CollectUnder(child)...)
	}
	return out
}

// PrefixQuery navigates to the prefix node and returns all keys under it.
//
// Parameters:
//   - root (*Node): the tree root to search from.
//   - prefix (string): the prefix to look up.
//
// Returns:
//   - []string: all keys in the subtree rooted at prefix; nil if the prefix does not exist.
func PrefixQuery(root *Node, prefix string) []string {
	n := Navigate(root, prefix)
	return CollectUnder(n)
}

// SplitLeaf converts an overflowing leaf to an internal node with children.
// Groups entries by the next character after the prefix and creates child
// leaves. Recursively splits children that exceed MAX_BUCKET_SIZE.
//
// Parameters:
//   - n (*Node): the leaf node to split in place. No-op if n is nil, not a
//     leaf, or does not exceed MAX_BUCKET_SIZE entries.
func SplitLeaf(n *Node) {
	if n == nil || !n.IsLeaf() || len(n.Entries) <= MAX_BUCKET_SIZE {
		return
	}
	groups := make(map[string][]string)
	for _, key := range n.Entries {
		if len(key) <= len(n.Prefix) {
			continue
		}
		seg := string(key[len(n.Prefix)])
		groups[seg] = append(groups[seg], key)
	}
	n.Kind = KindInternal
	n.Children = make(map[string]*Node)
	n.Entries = nil
	for seg, entries := range groups {
		child := NewLeaf(n.Prefix + seg)
		child.Entries = entries
		for len(child.Entries) > MAX_BUCKET_SIZE {
			SplitLeaf(child)
		}
		n.Children[seg] = child
	}
}

// MaybeSplit checks if the leaf exceeds MAX_BUCKET_SIZE and splits if so.
//
// Parameters:
//   - n (*Node): the node to check and possibly split (see SplitLeaf).
func MaybeSplit(n *Node) {
	SplitLeaf(n)
}
