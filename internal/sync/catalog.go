// Purpose: CID set reconciliation helpers (Phase 4.3). Build IBLT from content catalog
// and resolve key hashes back to CIDs for fetch protocol.

package sync

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/ipfs/go-cid"
)

var errFetchCountTooLarge = errors.New("fetch request/response count too large")

// BuildIBLTFromCIDs builds an IBLT from a CID set for reconciliation. Uses cid.String()
// as the key so both sides share the same representation.
func BuildIBLTFromCIDs(cids []cid.Cid, cellCount int) *IBLT {
	if cellCount <= 0 {
		cellCount = 256
	}
	t := NewIBLT(cellCount)
	for _, c := range cids {
		if c.Defined() {
			t.Insert([]byte(c.String()))
		}
	}
	return t
}

// CIDsForKeyHashes returns CIDs from the given set whose KeyHash matches any requested hash.
// Used by fetch protocol responder to resolve keyHashes to CIDs.
func CIDsForKeyHashes(cids []cid.Cid, keyHashes []uint64) []cid.Cid {
	if len(keyHashes) == 0 {
		return nil
	}
	want := make(map[uint64]struct{}, len(keyHashes))
	for _, h := range keyHashes {
		want[h] = struct{}{}
	}
	var out []cid.Cid
	for _, c := range cids {
		if !c.Defined() {
			continue
		}
		kh := KeyHash([]byte(c.String()))
		if _, ok := want[kh]; ok {
			out = append(out, c)
		}
	}
	return out
}

// IBLTFetchProtocolID is the libp2p protocol ID for resolving key hashes to CIDs.
const IBLTFetchProtocolID = "/sng40/iblt-fetch/1.0.0"

// WriteFetchRequest encodes keyHashes to w: uint32 count (LE) then count×uint64 (LE).
func WriteFetchRequest(w io.Writer, keyHashes []uint64) error {
	if len(keyHashes) > 1<<20 {
		return errFetchCountTooLarge
	}
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[:4], uint32(len(keyHashes)))
	if _, err := w.Write(buf[:4]); err != nil {
		return err
	}
	for _, h := range keyHashes {
		binary.LittleEndian.PutUint64(buf[:], h)
		if _, err := w.Write(buf[:]); err != nil {
			return err
		}
	}
	return nil
}

// ReadFetchRequest reads keyHashes from r.
func ReadFetchRequest(r io.Reader, maxCount int) ([]uint64, error) {
	if maxCount <= 0 {
		maxCount = 65536
	}
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:4]); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(buf[:4]))
	if n < 0 || n > maxCount {
		return nil, errFetchCountTooLarge
	}
	out := make([]uint64, n)
	for i := 0; i < n; i++ {
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return nil, err
		}
		out[i] = binary.LittleEndian.Uint64(buf[:])
	}
	return out, nil
}

// WriteFetchResponse encodes CIDs to w: uint32 count then for each uint32 len + bytes.
func WriteFetchResponse(w io.Writer, cids []cid.Cid) error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(len(cids)))
	if _, err := w.Write(buf[:]); err != nil {
		return err
	}
	for _, c := range cids {
		s := c.String()
		b := []byte(s)
		if len(b) > 65535 {
			continue
		}
		binary.LittleEndian.PutUint32(buf[:], uint32(len(b)))
		if _, err := w.Write(buf[:]); err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// ReadFetchResponse reads CIDs from r.
func ReadFetchResponse(r io.Reader, maxCount int) ([]cid.Cid, error) {
	if maxCount <= 0 {
		maxCount = 65536
	}
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(buf[:]))
	if n < 0 || n > maxCount {
		return nil, errFetchCountTooLarge
	}
	out := make([]cid.Cid, 0, n)
	for i := 0; i < n; i++ {
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return nil, err
		}
		ln := int(binary.LittleEndian.Uint32(buf[:]))
		if ln > 1024 {
			return nil, errFetchCountTooLarge
		}
		b := make([]byte, ln)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		c, err := cid.Decode(string(b))
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}
