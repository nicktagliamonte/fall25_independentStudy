// Purpose: Bloom filter for n-gram substring query support (e.g. *forest*).

package pht

import (
	"encoding/binary"
	"hash"
	"hash/fnv"
	"math"
	"sync"
)

// BloomFilter provides configurable-size, configurable-hash-count probabilistic membership.
type BloomFilter struct {
	bits      []uint64
	hashCount int
	hash      hash.Hash64
}

// NewBloomFilter creates a Bloom filter with size bits and hashCount hash functions.
func NewBloomFilter(size int, hashCount int) *BloomFilter {
	if size <= 0 {
		size = DefaultBloomSize
	}
	if hashCount <= 0 {
		hashCount = DefaultBloomHashes
	}
	if hashCount > 32 {
		hashCount = 32
	}
	n := (size + 63) / 64
	return &BloomFilter{
		bits:      make([]uint64, n),
		hashCount: hashCount,
		hash:      fnv.New64a(),
	}
}

// Add adds an item to the filter.
func (b *BloomFilter) Add(item []byte) {
	for i := 0; i < b.hashCount; i++ {
		idx := b.hashIndex(item, i)
		word := idx / 64
		bit := uint64(1) << (idx % 64)
		b.bits[word] |= bit
	}
}

// AddString adds a string to the filter.
func (b *BloomFilter) AddString(s string) {
	b.Add([]byte(s))
}

// Contains returns true if the item may be in the filter (false positives possible).
func (b *BloomFilter) Contains(item []byte) bool {
	for i := 0; i < b.hashCount; i++ {
		idx := b.hashIndex(item, i)
		word := idx / 64
		bit := uint64(1) << (idx % 64)
		if b.bits[word]&bit == 0 {
			return false
		}
	}
	return true
}

// ContainsString returns true if the string may be in the filter.
func (b *BloomFilter) ContainsString(s string) bool {
	return b.Contains([]byte(s))
}

// Size returns the number of bits.
func (b *BloomFilter) Size() int {
	return len(b.bits) * 64
}

// HashCount returns the number of hash functions.
func (b *BloomFilter) HashCount() int {
	return b.hashCount
}

func (b *BloomFilter) hashIndex(item []byte, i int) uint64 {
	b.hash.Reset()
	b.hash.Write(item)
	b.hash.Write([]byte{byte(i), byte(i >> 8)})
	h := b.hash.Sum64()
	return h % uint64(len(b.bits)*64)
}

// Or merges other into b in place. Both must have same Size and HashCount.
func (b *BloomFilter) Or(other *BloomFilter) {
	if other == nil || len(b.bits) != len(other.bits) || b.hashCount != other.hashCount {
		return
	}
	for i := range b.bits {
		b.bits[i] |= other.bits[i]
	}
}

// MarshalBinary encodes the filter for storage (hashCount + bits as little-endian uint64s).
func (b *BloomFilter) MarshalBinary() ([]byte, error) {
	out := make([]byte, 4+8*len(b.bits))
	binary.LittleEndian.PutUint32(out[0:4], uint32(b.hashCount))
	for i, w := range b.bits {
		binary.LittleEndian.PutUint64(out[4+8*i:4+8*(i+1)], w)
	}
	return out, nil
}

// UnmarshalBinary decodes a filter from storage.
func (b *BloomFilter) UnmarshalBinary(data []byte) error {
	if len(data) < 4 {
		return nil
	}
	hashCount := int(binary.LittleEndian.Uint32(data[0:4]))
	remain := data[4:]
	n := len(remain) / 8
	bits := make([]uint64, n)
	for i := 0; i < n; i++ {
		bits[i] = binary.LittleEndian.Uint64(remain[8*i : 8*(i+1)])
	}
	b.bits = bits
	b.hashCount = hashCount
	b.hash = fnv.New64a()
	return nil
}

// NewBloomFilterFromBinary creates a BloomFilter from MarshalBinary output.
func NewBloomFilterFromBinary(data []byte) (*BloomFilter, error) {
	b := &BloomFilter{}
	if err := b.UnmarshalBinary(data); err != nil {
		return nil, err
	}
	return b, nil
}

// DefaultNGramSize is the default n-gram length for Bloom filters.
const DefaultNGramSize = 3

// DefaultBloomSize is the default filter size in bits. Chosen for ~500 n-grams
// (typical leaf with MAX_BUCKET_SIZE=16 keys) at ~1% false positive rate.
const DefaultBloomSize = 4096

// DefaultBloomHashes is the default hash count. Optimal for DefaultBloomSize
// and ~500 items: k ≈ (m/n)*ln(2).
const DefaultBloomHashes = 6

// OptimalBloomParams returns (sizeBits, hashCount) to achieve targetFalsePositive
// given expectedItems. Uses m = -n*ln(p)/ln(2)^2 and k = round((m/n)*ln(2)).
// Clamps size to [64, 2^20] and hashCount to [1, 32].
func OptimalBloomParams(expectedItems int, targetFalsePositive float64) (sizeBits, hashCount int) {
	if expectedItems <= 0 {
		expectedItems = 1
	}
	if targetFalsePositive <= 0 || targetFalsePositive >= 1 {
		targetFalsePositive = 0.01
	}
	ln2 := math.Ln2
	m := -float64(expectedItems) * math.Log(targetFalsePositive) / (ln2 * ln2)
	k := m / float64(expectedItems) * ln2
	sizeBits = int(math.Ceil(m / 64)) * 64
	if sizeBits < 64 {
		sizeBits = 64
	}
	if sizeBits > 1<<20 {
		sizeBits = 1 << 20
	}
	hashCount = int(math.Round(k))
	if hashCount < 1 {
		hashCount = 1
	}
	if hashCount > 32 {
		hashCount = 32
	}
	return sizeBits, hashCount
}

// EstimatedFalsePositiveRate returns the approximate false positive probability
// for a Bloom filter with sizeBits, hashCount and itemCount inserts.
// Formula: p ≈ (1 - e^(-k*n/m))^k
func EstimatedFalsePositiveRate(sizeBits, hashCount, itemCount int) float64 {
	if sizeBits <= 0 || hashCount <= 0 || itemCount <= 0 {
		return 1
	}
	m := float64(sizeBits)
	k := float64(hashCount)
	n := float64(itemCount)
	x := -k * n / m
	if x < -700 {
		return 0
	}
	p := math.Pow(1-math.Exp(x), k)
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// BuildNodeBloom populates n.Bloom from its contained content. For leaves, adds
// n-grams of each entry. For internal nodes, ORs children's Bloom filters.
func BuildNodeBloom(n *Node, nGram int, bloomSize, bloomHashes int) {
	if n == nil {
		return
	}
	if nGram <= 0 {
		nGram = DefaultNGramSize
	}
	if bloomSize <= 0 {
		bloomSize = DefaultBloomSize
	}
	if bloomHashes <= 0 {
		bloomHashes = DefaultBloomHashes
	}
	if n.IsLeaf() {
		bf := NewBloomFilter(bloomSize, bloomHashes)
		for _, key := range n.Entries {
			for _, ng := range ExtractNGrams(key, nGram) {
				bf.AddString(ng)
			}
		}
		n.Bloom = bf
	} else {
		bf := NewBloomFilter(bloomSize, bloomHashes)
		for _, child := range n.Children {
			if child != nil {
				BuildNodeBloom(child, nGram, bloomSize, bloomHashes)
				if child.Bloom != nil {
					bf.Or(child.Bloom)
				}
			}
		}
		n.Bloom = bf
	}
}

// BloomContainsAll returns true if b contains all ngrams. Used for query pruning:
// if false, the node's subtree cannot contain keys matching the substring.
func BloomContainsAll(b *BloomFilter, ngrams []string) bool {
	if b == nil || len(ngrams) == 0 {
		return true
	}
	for _, ng := range ngrams {
		if !b.ContainsString(ng) {
			return false
		}
	}
	return true
}

// PruneByBloom checks each node's Bloom filter in parallel and returns only
// nodes that might contain the substring (all ngrams present). Pruned nodes
// are excluded from further traversal.
func PruneByBloom(nodes []*Node, ngrams []string) []*Node {
	if len(ngrams) == 0 {
		return nodes
	}
	pass := make([]bool, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func(i int, node *Node) {
			defer wg.Done()
			pass[i] = node != nil && BloomContainsAll(node.Bloom, ngrams)
		}(i, n)
	}
	wg.Wait()
	var out []*Node
	for i, p := range pass {
		if p {
			out = append(out, nodes[i])
		}
	}
	return out
}

// ExtractNGrams returns all n-character substrings of s.
// For "forest" and n=3 returns ["for", "ore", "res", "est"].
func ExtractNGrams(s string, n int) []string {
	if n <= 0 || len(s) < n {
		return nil
	}
	out := make([]string, 0, len(s)-n+1)
	for i := 0; i <= len(s)-n; i++ {
		out = append(out, s[i:i+n])
	}
	return out
}
