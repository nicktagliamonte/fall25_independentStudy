// Purpose: IBLT (Invertible Bloom Lookup Table) for set reconciliation (Phase 4.1).

package sync

import (
	"encoding/binary"
	"hash"
	"hash/fnv"
)

// DefaultHashCount is the number of hash functions for IBLT (k=3 is empirically optimal).
const DefaultHashCount = 3

// KeyHash returns the 64-bit FNV-1a hash used for IBLT keys. Use for building
// hash-to-key maps when reconciling (e.g. Negative keyHashes -> resolve from peer).
func KeyHash(key []byte) uint64 {
	h := fnv.New64a()
	h.Write(key)
	return h.Sum64()
}

// IBLTCell holds the accumulator for one cell in the IBLT. Multiple keys can hash
// to the same cell; XOR accumulation allows recovery via peeling when count is 1.
type IBLTCell struct {
	Count   int    // number of items hashed to this cell
	KeySum  uint64 // XOR of all keys (or hash(key)) mapped to this cell
	HashSum uint64 // XOR of all key hashes (checksum for verification)
}

// IBLT is an Invertible Bloom Lookup Table: an array of cells indexed by hash
// functions, used for set reconciliation with compact difference representation.
type IBLT struct {
	CellCount int        // number of cells
	Cells     []IBLTCell // cell array
	HashCount int        // number of hash functions (k)
	h1        hash.Hash64
	h2        hash.Hash64
}

// NewIBLT creates an IBLT with the given number of cells.
func NewIBLT(cellCount int) *IBLT {
	if cellCount <= 0 {
		cellCount = 1024
	}
	k := DefaultHashCount
	return &IBLT{
		CellCount: cellCount,
		Cells:     make([]IBLTCell, cellCount),
		HashCount: k,
		h1:        fnv.New64a(),
		h2:        fnv.New64(),
	}
}

// cellIndex returns the cell index for hash function i given the key bytes.
func (t *IBLT) cellIndex(key []byte, i int) int {
	return t.cellIndexFromHash(t.keyHash(key), i)
}

// cellIndexFromHash returns the cell index for hash function i given keyHash.
// Allows Peel to compute which cells to update when recovering keyHash.
func (t *IBLT) cellIndexFromHash(keyHash uint64, i int) int {
	t.h1.Reset()
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:8], keyHash)
	binary.BigEndian.PutUint64(buf[8:16], uint64(i))
	t.h1.Write(buf[:])
	h := t.h1.Sum64()
	return int(h % uint64(t.CellCount))
}

// keyHash returns the 64-bit representation for KeySum (XOR accumulation).
func (t *IBLT) keyHash(key []byte) uint64 {
	t.h1.Reset()
	t.h1.Write(key)
	return t.h1.Sum64()
}

// hashSum returns the 64-bit checksum for HashSum (XOR accumulation).
// Uses keyHash so Peel can verify recovered keys without the original bytes.
func (t *IBLT) hashSum(key []byte) uint64 {
	return t.hashSumFromKeyHash(t.keyHash(key))
}

// hashSumFromKeyHash returns the checksum for a keyHash (used for verification in Peel).
func (t *IBLT) hashSumFromKeyHash(keyHash uint64) uint64 {
	t.h2.Reset()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], keyHash)
	t.h2.Write(buf[:])
	return t.h2.Sum64()
}

// Insert adds key to the IBLT. The key is hashed to k cells; each cell's count is
// incremented and KeySum/HashSum are XOR-accumulated.
func (t *IBLT) Insert(key []byte) {
	if t == nil || len(t.Cells) == 0 {
		return
	}
	k := t.HashCount
	if k <= 0 {
		k = DefaultHashCount
	}
	kh := t.keyHash(key)
	hh := t.hashSum(key)
	for i := 0; i < k; i++ {
		idx := t.cellIndex(key, i)
		t.Cells[idx].Count++
		t.Cells[idx].KeySum ^= kh
		t.Cells[idx].HashSum ^= hh
	}
}

// Delete removes key from the IBLT. The key is hashed to k cells; each cell's count
// is decremented and KeySum/HashSum are XOR-removed (XOR is self-inverse).
func (t *IBLT) Delete(key []byte) {
	if t == nil || len(t.Cells) == 0 {
		return
	}
	k := t.HashCount
	if k <= 0 {
		k = DefaultHashCount
	}
	kh := t.keyHash(key)
	hh := t.hashSum(key)
	for i := 0; i < k; i++ {
		idx := t.cellIndex(key, i)
		t.Cells[idx].Count--
		t.Cells[idx].KeySum ^= kh
		t.Cells[idx].HashSum ^= hh
	}
}

// Subtract returns a new IBLT representing the cell-wise difference (t - other).
// Encodes the symmetric set difference: keys in t but not other, minus keys in other
// but not t. Both IBLTs must have the same CellCount and HashCount. Returns nil if
// incompatible.
func (t *IBLT) Subtract(other *IBLT) *IBLT {
	if t == nil || other == nil {
		return nil
	}
	if t.CellCount != other.CellCount || t.HashCount != other.HashCount {
		return nil
	}
	if len(t.Cells) != len(other.Cells) {
		return nil
	}
	diff := NewIBLT(t.CellCount)
	for i := range t.Cells {
		diff.Cells[i].Count = t.Cells[i].Count - other.Cells[i].Count
		diff.Cells[i].KeySum = t.Cells[i].KeySum ^ other.Cells[i].KeySum
		diff.Cells[i].HashSum = t.Cells[i].HashSum ^ other.Cells[i].HashSum
	}
	return diff
}

// PeelResult holds recovered key hashes from Peel. Positive = keys in (t - other),
// Negative = keys in (other - t).
type PeelResult struct {
	Positive []uint64
	Negative []uint64
}

// Peel recovers pure keys from the difference IBLT. Iteratively finds cells with
// count 1 or -1 (pure cells), recovers keyHash from KeySum, verifies HashSum, removes
// the key from its k cells, and repeats until no progress. Returns recovered key
// hashes; caller maps these to actual keys. Modifies t in place.
func (t *IBLT) Peel() PeelResult {
	var pos, neg []uint64
	if t == nil || len(t.Cells) == 0 {
		return PeelResult{}
	}
	k := t.HashCount
	if k <= 0 {
		k = DefaultHashCount
	}
	for {
		var found bool
		for i := range t.Cells {
			c := &t.Cells[i]
			if c.Count == 0 && c.KeySum == 0 && c.HashSum == 0 {
				continue
			}
			if c.Count != 1 && c.Count != -1 {
				continue
			}
			keyHash := c.KeySum
			hashSumVal := c.HashSum
			indices := make([]int, k)
			for j := 0; j < k; j++ {
				indices[j] = t.cellIndexFromHash(keyHash, j)
			}
			seen := false
			for _, idx := range indices {
				if idx == i {
					seen = true
					break
				}
			}
			if !seen {
				continue
			}
			if t.hashSumFromKeyHash(keyHash) != hashSumVal {
				continue
			}
			found = true
			if c.Count == 1 {
				pos = append(pos, keyHash)
			} else {
				neg = append(neg, keyHash)
			}
			sign := 1
			if c.Count == -1 {
				sign = -1
			}
			for j := 0; j < k; j++ {
				idx := indices[j]
				t.Cells[idx].Count -= sign
				t.Cells[idx].KeySum ^= keyHash
				t.Cells[idx].HashSum ^= hashSumVal
			}
			break
		}
		if !found {
			break
		}
	}
	return PeelResult{Positive: pos, Negative: neg}
}

// HasUnpeeled returns true if any cell has non-zero Count, KeySum, or HashSum.
// Used to detect incomplete peel (difference too large for IBLT capacity).
func (t *IBLT) HasUnpeeled() bool {
	if t == nil {
		return false
	}
	for i := range t.Cells {
		c := &t.Cells[i]
		if c.Count != 0 || c.KeySum != 0 || c.HashSum != 0 {
			return true
		}
	}
	return false
}
