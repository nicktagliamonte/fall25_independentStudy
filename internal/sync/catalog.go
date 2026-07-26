// Purpose: CID set reconciliation helpers (Phase 4.3). Build IBLT from content catalog
// and resolve key hashes back to CIDs for fetch protocol.

package sync

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/ipfs/go-cid"
)

// errFetchCountTooLarge is returned by the fetch-request/response wire
// codecs when an encoded or decoded item count exceeds the caller-supplied
// (or default) limit, guarding against unbounded allocation from a
// malicious or corrupted peer.
var errFetchCountTooLarge = errors.New("fetch request/response count too large")

// BuildIBLTFromCIDs builds an IBLT representing a node's content catalog for
// reconciliation. Each defined CID is inserted using cid.String() as the
// IBLT key, so both sides of an exchange must use the same string encoding
// for a CID to consider it the same key.
//
// Parameters:
//   - cids ([]cid.Cid): the CID set to encode; undefined CIDs are skipped.
//   - cellCount (int): number of IBLT cells to allocate; <= 0 defaults to 256 (see NewIBLT).
//
// Returns:
//   - *IBLT: a new IBLT with every defined CID in cids inserted.
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

// CIDsForKeyHashes returns the subset of cids whose KeyHash(cid.String())
// matches one of the requested keyHashes. It is used by the IBLT fetch
// protocol's responder side to resolve a peer's requested key hashes back to
// concrete CIDs it can serve.
//
// Parameters:
//   - cids ([]cid.Cid): the local catalog to search; undefined CIDs are skipped.
//   - keyHashes ([]uint64): the key hashes to resolve; if empty, returns nil immediately.
//
// Returns:
//   - []cid.Cid: CIDs from cids whose key hash is in keyHashes (order follows cids; duplicates possible only if cids has duplicates).
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

// WriteFetchRequest encodes keyHashes to w in the fetch-request wire format:
// a little-endian uint32 count, followed by that many little-endian uint64
// key hashes. Fails without writing further if len(keyHashes) exceeds 1<<20.
//
// Parameters:
//   - w (io.Writer): destination for the encoded request.
//   - keyHashes ([]uint64): key hashes to request (typically an ExchangerResult.Negative list).
//
// Returns:
//   - error: errFetchCountTooLarge if len(keyHashes) > 1<<20, or any underlying write error.
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

// ReadFetchRequest decodes a fetch-request message from r, as written by
// WriteFetchRequest: a little-endian uint32 count followed by that many
// little-endian uint64 key hashes.
//
// Parameters:
//   - r (io.Reader): source of the encoded request.
//   - maxCount (int): maximum accepted key-hash count; <= 0 defaults to 65536.
//
// Returns:
//   - []uint64: the decoded key hashes.
//   - error: errFetchCountTooLarge if the encoded count is negative or exceeds maxCount, or any underlying read error.
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

// WriteFetchResponse encodes cids to w in the fetch-response wire format: a
// little-endian uint32 count, followed by, for each CID, a little-endian
// uint32 length and that many bytes of its string encoding. Any individual
// CID whose string encoding exceeds 65535 bytes is excluded up front so the
// emitted count always matches the number of entries actually written
// (ReadFetchResponse relies on the count being exact to know when to stop).
//
// Parameters:
//   - w (io.Writer): destination for the encoded response.
//   - cids ([]cid.Cid): CIDs to encode (typically the result of CIDsForKeyHashes).
//
// Returns:
//   - error: any underlying write error.
func WriteFetchResponse(w io.Writer, cids []cid.Cid) error {
	encoded := make([][]byte, 0, len(cids))
	for _, c := range cids {
		b := []byte(c.String())
		if len(b) > 65535 {
			continue
		}
		encoded = append(encoded, b)
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(len(encoded)))
	if _, err := w.Write(buf[:]); err != nil {
		return err
	}
	for _, b := range encoded {
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

// ReadFetchResponse decodes a fetch-response message from r, as written by
// WriteFetchResponse: a little-endian uint32 count, followed by that many
// entries of (little-endian uint32 length, that many bytes). Each entry's
// bytes are cid.Decode'd; entries that fail to decode are silently dropped
// from the result (not an error). Per-entry length is additionally capped at
// 1024 bytes regardless of maxCount.
//
// Parameters:
//   - r (io.Reader): source of the encoded response.
//   - maxCount (int): maximum accepted entry count; <= 0 defaults to 65536.
//
// Returns:
//   - []cid.Cid: successfully decoded CIDs (may be fewer than the encoded count if some entries failed to decode).
//   - error: errFetchCountTooLarge if the encoded count is negative, exceeds maxCount, or an entry's length exceeds 1024 bytes; or any underlying read error.
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
