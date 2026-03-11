// Purpose: Tests for Key and KeyFromData (Phase 7.1).

package storage

import (
	"bytes"
	"testing"

	blocks "github.com/ipfs/go-block-format"
)

func TestKeyFromData_GeneratesConsistentKeys(t *testing.T) {
	data := []byte("test payload for key generation")
	k1 := KeyFromData(data)
	k2 := KeyFromData(data)
	if k1 != k2 {
		t.Errorf("same data must produce same key: got %s vs %s", k1.String(), k2.String())
	}
	if !k1.Equal(k2) {
		t.Error("keys must be equal via Equal()")
	}
}

func TestKeyFromData_DifferentDataDifferentKeys(t *testing.T) {
	k1 := KeyFromData([]byte("foo"))
	k2 := KeyFromData([]byte("bar"))
	if k1 == k2 {
		t.Error("different data must produce different keys")
	}
	if k1.Equal(k2) {
		t.Error("keys must differ via Equal()")
	}
}

func TestKeyFromData_EmptyData(t *testing.T) {
	// SHA256 of empty input is deterministic
	k1 := KeyFromData(nil)
	k2 := KeyFromData([]byte{})
	if k1 != k2 {
		t.Errorf("nil and empty must produce same key: got %s vs %s", k1.String(), k2.String())
	}
	if len(k1) != 32 {
		t.Errorf("key must be 32 bytes, got %d", len(k1))
	}
}

func TestKeyFromData_DeterministicAcrossCalls(t *testing.T) {
	payloads := [][]byte{
		[]byte("a"),
		[]byte("hello world"),
		bytes.Repeat([]byte{0xff}, 4096),
	}
	for i, p := range payloads {
		a := KeyFromData(p)
		b := KeyFromData(p)
		if a != b {
			t.Errorf("payload %d len=%d: inconsistent keys", i, len(p))
		}
	}
}

func TestKeyNotEqualToCID(t *testing.T) {
	data := []byte("same data, different identifier schemes")
	key := KeyFromData(data)
	blk := blocks.NewBlock(data)
	c := blk.Cid()

	if key.String() == c.String() {
		t.Error("Key and CID must have different string representations")
	}
	if bytes.Equal(key.Bytes(), c.Bytes()) {
		t.Error("Key and CID must have different byte representations")
	}
	if len(key) != 32 {
		t.Errorf("Key must be 32 bytes, got %d", len(key))
	}
	if len(c.Bytes()) == 32 {
		t.Error("CID includes multicodec/multihash prefix; should not be 32 bytes")
	}
}
