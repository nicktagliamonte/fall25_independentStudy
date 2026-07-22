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
//
// Parameters:
//   - key ([]byte): the key bytes to hash (e.g. a CID's string encoding).
//
// Returns:
//   - uint64: the FNV-1a hash of key.
func KeyHash(key []byte) uint64 {
	h := fnv.New64a()
	h.Write(key)
	return h.Sum64()
}

// IBLTCell holds the accumulator for one cell in the IBLT. Multiple keys can
// hash to the same cell; XOR accumulation allows recovery via peeling when
// count is 1 (or -1 after a Subtract).
type IBLTCell struct {
	// Count is the number of items hashed into this cell (Insert increments,
	// Delete decrements; after Subtract it can be negative).
	Count int
	// KeySum is the XOR of the keyHash of every key mapped to this cell.
	KeySum uint64
	// HashSum is the XOR of the verification checksum (hashSum) of every key
	// mapped to this cell, used by Peel to confirm a recovered key is correct.
	HashSum uint64
}

// IBLT is an Invertible Bloom Lookup Table: an array of cells indexed by k
// hash functions, used for set reconciliation with a compact representation
// of the symmetric difference between two sets.
type IBLT struct {
	// CellCount is the number of cells in the Cells array.
	CellCount int
	// Cells is the cell array; Insert/Delete/Subtract/Peel operate on it.
	Cells []IBLTCell
	// HashCount is the number of hash functions (k) used to map a key to
	// cell indices; each key is inserted into and removed from exactly k cells.
	HashCount int
	h1        hash.Hash64 // scratch FNV-1a hash used for keyHash and cell-index derivation.
	h2        hash.Hash64 // scratch FNV-1 hash used for the verification checksum (hashSum).
}

// NewIBLT creates an empty IBLT with the given number of cells and
// DefaultHashCount hash functions. A cellCount <= 0 is replaced with 1024.
//
// Parameters:
//   - cellCount (int): number of cells to allocate; <= 0 defaults to 1024.
//
// Returns:
//   - *IBLT: a newly allocated, empty IBLT.
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

// cellIndex returns the cell index that hash function i maps key to.
//
// Parameters:
//   - key ([]byte): the key bytes to locate.
//   - i (int): which of the t.HashCount hash functions to use (0-indexed).
//
// Returns:
//   - int: the cell index in [0, t.CellCount).
func (t *IBLT) cellIndex(key []byte, i int) int {
	return t.cellIndexFromHash(t.keyHash(key), i)
}

// cellIndexFromHash returns the cell index that hash function i maps keyHash
// to, without needing the original key bytes. This lets Peel compute which
// cells to update when it has only recovered a keyHash (not the key itself).
//
// Parameters:
//   - keyHash (uint64): the key's keyHash value (as produced by t.keyHash).
//   - i (int): which of the t.HashCount hash functions to use (0-indexed).
//
// Returns:
//   - int: the cell index in [0, t.CellCount).
func (t *IBLT) cellIndexFromHash(keyHash uint64, i int) int {
	t.h1.Reset()
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:8], keyHash)
	binary.BigEndian.PutUint64(buf[8:16], uint64(i))
	t.h1.Write(buf[:])
	h := t.h1.Sum64()
	return int(h % uint64(t.CellCount))
}

// keyHash returns the 64-bit FNV-1a value of key used to XOR-accumulate into
// each cell's KeySum, and as the canonical identifier recovered by Peel.
//
// Parameters:
//   - key ([]byte): the key bytes to hash.
//
// Returns:
//   - uint64: the FNV-1a hash of key using t's scratch hasher.
func (t *IBLT) keyHash(key []byte) uint64 {
	t.h1.Reset()
	t.h1.Write(key)
	return t.h1.Sum64()
}

// hashSum returns the 64-bit verification checksum for key, XOR-accumulated
// into each cell's HashSum. It is derived from keyHash (not the raw key
// bytes) so Peel can independently recompute and verify it after recovering
// only a keyHash value.
//
// Parameters:
//   - key ([]byte): the key bytes to compute a checksum for.
//
// Returns:
//   - uint64: the verification checksum for key.
func (t *IBLT) hashSum(key []byte) uint64 {
	return t.hashSumFromKeyHash(t.keyHash(key))
}

// hashSumFromKeyHash returns the verification checksum for a given keyHash,
// used by Peel to confirm a recovered KeySum actually corresponds to a valid
// single key (rather than an unlucky XOR collision of multiple keys).
//
// Parameters:
//   - keyHash (uint64): the candidate key's keyHash value.
//
// Returns:
//   - uint64: the checksum that should match the cell's HashSum if keyHash is genuine.
func (t *IBLT) hashSumFromKeyHash(keyHash uint64) uint64 {
	t.h2.Reset()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], keyHash)
	t.h2.Write(buf[:])
	return t.h2.Sum64()
}

// Insert adds key to the IBLT. The key is hashed to t.HashCount cells; each
// such cell's Count is incremented and its KeySum/HashSum are XOR-accumulated
// with key's keyHash/hashSum. A nil receiver or empty Cells is a no-op.
//
// Parameters:
//   - key ([]byte): the key bytes to insert (e.g. a CID's string encoding).
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

// Delete removes key from the IBLT. The key is hashed to the same
// t.HashCount cells Insert would have used; each cell's Count is decremented
// and its KeySum/HashSum are XOR-removed (XOR is its own inverse, so this
// undoes a prior Insert). A nil receiver or empty Cells is a no-op.
//
// Parameters:
//   - key ([]byte): the key bytes to remove; must match a previously Inserted key for correctness.
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

// Subtract returns a new IBLT representing the cell-wise difference (t -
// other): each cell's Count is subtracted and KeySum/HashSum are XORed
// together. The result encodes the symmetric set difference between the two
// original sets — after Peel, Positive keys are those in t but not other, and
// Negative keys are those in other but not t. Both IBLTs must share the same
// CellCount and HashCount and have equal-length Cells; otherwise nil is
// returned.
//
// Parameters:
//   - other (*IBLT): the IBLT to subtract from t; must be structurally compatible with t.
//
// Returns:
//   - *IBLT: a new IBLT holding the cell-wise difference, or nil if t or other is nil or they are incompatible.
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

// PeelResult holds the key hashes recovered from peeling a difference IBLT
// (typically the result of IBLT.Subtract).
type PeelResult struct {
	// Positive holds recovered keyHash values for keys present in the
	// minuend set but not the subtrahend (i.e. keys in t but not other, for
	// diff := t.Subtract(other)).
	Positive []uint64
	// Negative holds recovered keyHash values for keys present in the
	// subtrahend set but not the minuend (i.e. keys in other but not t).
	Negative []uint64
}

// Peel recovers pure keys from a difference IBLT (as produced by Subtract) by
// iteratively finding "pure" cells — those with Count == 1 or Count == -1 —
// recovering the candidate keyHash from KeySum, verifying it against HashSum
// via hashSumFromKeyHash, and, if valid, removing that key's contribution
// from all t.HashCount cells it maps to. This can turn previously impure
// cells pure, so the process repeats until no cell yields a new key. Peel
// modifies t in place and is typically called on the result of Subtract, not
// on a live (non-difference) IBLT.
//
// Returns:
//   - PeelResult: the keyHash values recovered as Positive or Negative; the caller must map these back to actual keys/CIDs via its own key inventory (KeyHash is not invertible).
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

// HasUnpeeled reports whether any cell still has a non-zero Count, KeySum, or
// HashSum after a Peel pass. A true result means the peel was incomplete —
// the encoded difference was too large for the IBLT's capacity — and the
// caller should retry with a larger IBLT or fall back to a full sync.
//
// Returns:
//   - bool: true if at least one cell is non-empty; false if t is nil or fully peeled.
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
