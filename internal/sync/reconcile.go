// Purpose: IBLT-based set reconciliation protocol (Phase 4.2).

package sync

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"
)

// IBLTProtocolID is the libp2p protocol ID for IBLT exchange.
const IBLTProtocolID = "/sng40/iblt/1.0.0"

// NeighborProvider supplies the set of peer IDs a node should periodically
// exchange IBLTs with (typically its currently connected peers).
type NeighborProvider interface {
	// Neighbors returns string-encoded peer IDs to exchange IBLTs with.
	Neighbors() []string
}

// IBLTStream is a bidirectional byte stream to a single peer used for one
// IBLT (or IBLT-fetch) protocol exchange.
type IBLTStream interface {
	io.ReadWriteCloser
}

// IBLTStreamOpener opens a stream to a given peer for the IBLT protocol.
// Implementations typically wrap a libp2p host's NewStream on IBLTProtocolID.
type IBLTStreamOpener interface {
	// OpenIBLTStream opens a stream to peerID for exchanging IBLTs.
	//
	// Parameters:
	//   - ctx (context.Context): bounds the stream-open attempt.
	//   - peerID (string): string-encoded peer ID to connect to.
	//
	// Returns:
	//   - IBLTStream: the opened stream.
	//   - error: non-nil if the stream could not be opened.
	OpenIBLTStream(ctx context.Context, peerID string) (IBLTStream, error)
}

// FetchRequester is called when a reconciliation round discovers differences:
// Negative key hashes indicate content the peer has that this node needs.
// Implementations should trigger a content fetch (e.g. via Bitswap or a
// dedicated fetch protocol) for the corresponding items.
type FetchRequester interface {
	// RequestFetch asks the implementation to fetch the content identified by
	// keyHashes from peerID.
	//
	// Parameters:
	//   - ctx (context.Context): bounds the fetch operation.
	//   - peerID (string): string-encoded peer ID reported as having the missing content.
	//   - keyHashes ([]uint64): IBLT key hashes of the content to fetch.
	RequestFetch(ctx context.Context, peerID string, keyHashes []uint64)
}

// ExchangerConfig holds the parameters governing StartPeriodicExchange's
// behavior.
type ExchangerConfig struct {
	// Interval is how often to run an exchange round with all neighbors;
	// <= 0 defaults to 5 minutes.
	Interval time.Duration
	// CellCount is the IBLT cell count used when sizing (informational here;
	// the actual local IBLT is built by the caller's buildLocal function).
	// <= 0 defaults to 256.
	CellCount int
	// Timeout bounds each individual peer exchange (stream open, write,
	// read); <= 0 defaults to 30 seconds.
	Timeout time.Duration
	// FetchRequester, when set, is invoked with each peer's Negative key
	// hashes after a successful peel, to trigger fetching that content.
	FetchRequester FetchRequester
	// OnPeelFailure, when set, is invoked when a peer's exchange result has
	// PeelIncomplete set (the difference was too large to fully recover).
	OnPeelFailure OnPeelFailure
}

// ExchangerResult holds the outcome of a single IBLT exchange with one
// neighbor.
type ExchangerResult struct {
	// PeerID is the string-encoded peer ID this result is for.
	PeerID string
	// Positive holds recovered key hashes present locally but not on the peer.
	Positive []uint64
	// Negative holds recovered key hashes present on the peer but not locally.
	Negative []uint64
	// PeelOK is true if the exchange completed and the difference IBLT was peeled (successfully or not); false if the exchange itself failed (see Err).
	PeelOK bool
	// PeelIncomplete is true if peeling finished without fully recovering the
	// difference (it was too large for the IBLT's capacity).
	PeelIncomplete bool
	// Err holds the error from a failed stream open, write, read, or
	// difference extraction; nil on success.
	Err error
}

// OnPeelFailure is called when peeling fails to fully recover a difference
// (the difference was too large for the IBLT's capacity). The caller may
// increase IBLT size for future exchanges or trigger a fallback (e.g. full
// sync) for the affected peer.
type OnPeelFailure func(peerID string, res ExchangerResult)

// StartPeriodicExchange starts a background goroutine that, every
// cfg.Interval, rebuilds the local IBLT via buildLocal (so it reflects the
// current catalog) and then, for each peer returned by neighbors.Neighbors(),
// opens a stream via opener, exchanges IBLTs with exchangeWithPeer, and acts
// on the result: if PeelIncomplete and cfg.OnPeelFailure is set, it is
// called; if the peel succeeded with Negative keys and cfg.FetchRequester is
// set, RequestFetch is called for those keys; and if onResult is non-nil, it
// is called with every result regardless of outcome. Config fields left at
// their zero values are defaulted (Interval 5m, CellCount 256, Timeout 30s).
// The loop stops when ctx is done or the returned stop function is called.
//
// Parameters:
//   - ctx (context.Context): governs the loop's lifetime; canceling it stops the loop.
//   - cfg (ExchangerConfig): exchange interval/timeout and optional fetch/failure hooks.
//   - buildLocal (func() *IBLT): called once per round to produce the current local IBLT; a nil result skips that round.
//   - neighbors (NeighborProvider): supplies the peer IDs to exchange with each round.
//   - opener (IBLTStreamOpener): opens the stream used for each peer's exchange.
//   - onResult (func(ExchangerResult)): optional callback invoked with every per-peer result; may be nil.
//
// Returns:
//   - func(): a stop function that signals the loop to exit and blocks until it has.
func StartPeriodicExchange(ctx context.Context, cfg ExchangerConfig, buildLocal func() *IBLT, neighbors NeighborProvider, opener IBLTStreamOpener, onResult func(ExchangerResult)) func() {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.CellCount <= 0 {
		cfg.CellCount = 256
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				local := buildLocal()
				if local == nil {
					continue
				}
				for _, peerID := range neighbors.Neighbors() {
					res := exchangeWithPeer(ctx, cfg, local, peerID, opener)
					if res.PeelIncomplete && cfg.OnPeelFailure != nil {
						cfg.OnPeelFailure(peerID, res)
					}
					if res.PeelOK && len(res.Negative) > 0 && cfg.FetchRequester != nil {
						cfg.FetchRequester.RequestFetch(ctx, peerID, res.Negative)
					}
					if onResult != nil {
						onResult(res)
					}
				}
			}
		}
	}()
	return func() {
		close(stop)
		wg.Wait()
	}
}

// exchangeWithPeer performs one IBLT exchange with a single peer: it opens a
// stream via opener (bounded by cfg.Timeout), writes the local IBLT, reads
// the peer's IBLT in response, and computes+peels the difference via
// ExtractDifference. Any failure at any step is captured in the returned
// result's Err field rather than returned separately.
//
// Parameters:
//   - ctx (context.Context): parent context; a cfg.Timeout deadline is derived from it.
//   - cfg (ExchangerConfig): supplies the per-exchange timeout.
//   - local (*IBLT): this node's current IBLT snapshot to send.
//   - peerID (string): string-encoded peer ID to exchange with.
//   - opener (IBLTStreamOpener): used to open the exchange stream.
//
// Returns:
//   - ExchangerResult: the outcome of the exchange, including any error.
func exchangeWithPeer(ctx context.Context, cfg ExchangerConfig, local *IBLT, peerID string, opener IBLTStreamOpener) ExchangerResult {
	res := ExchangerResult{PeerID: peerID}
	ctx2, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	stream, err := opener.OpenIBLTStream(ctx2, peerID)
	if err != nil {
		res.Err = err
		return res
	}
	defer stream.Close()
	if err := WriteIBLT(stream, local); err != nil {
		res.Err = err
		return res
	}
	remote, err := ReadIBLT(stream)
	if err != nil {
		res.Err = err
		return res
	}
	extracted, err := ExtractDifference(local, remote)
	if err != nil {
		res.Err = err
		return res
	}
	res.Positive = extracted.Positive
	res.Negative = extracted.Negative
	res.PeelOK = true
	res.PeelIncomplete = extracted.PeelIncomplete
	return res
}

// ExtractDifferenceResult holds the result of computing and peeling the
// difference between two IBLTs.
type ExtractDifferenceResult struct {
	// Positive holds recovered key hashes present in local but not remote.
	Positive []uint64
	// Negative holds recovered key hashes present in remote but not local.
	Negative []uint64
	// PeelIncomplete is true if the difference was too large to fully peel.
	PeelIncomplete bool
}

// ExtractDifference computes local.Subtract(remote), peels the resulting
// difference IBLT, and returns the recovered key hashes: Positive for keys in
// local but not remote, Negative for keys in remote but not local.
//
// Parameters:
//   - local (*IBLT): this node's IBLT snapshot.
//   - remote (*IBLT): the peer's IBLT snapshot; must be structurally compatible with local (same CellCount/HashCount).
//
// Returns:
//   - ExtractDifferenceResult: recovered Positive/Negative key hashes and whether the peel was incomplete.
//   - error: non-nil if local or remote is nil, or they are structurally incompatible (IBLT.Subtract returns nil).
func ExtractDifference(local, remote *IBLT) (ExtractDifferenceResult, error) {
	var zero ExtractDifferenceResult
	if local == nil || remote == nil {
		return zero, errors.New("local and remote IBLTs required")
	}
	diff := local.Subtract(remote)
	if diff == nil {
		return zero, errors.New("incompatible IBLT (cell count or hash count mismatch)")
	}
	peeled := diff.Peel()
	return ExtractDifferenceResult{
		Positive:       peeled.Positive,
		Negative:       peeled.Negative,
		PeelIncomplete: diff.HasUnpeeled(),
	}, nil
}

// IBLT exchange message format (binary, little-endian):
//
//	Offset  Size    Field
//	0       4       cellCount (uint32)
//	4       1       hashCount (uint8)
//	5       -       cells: for each of cellCount cells:
//	                   count (int32)
//	                   keySum (uint64)
//	                   hashSum (uint64)
//	Per cell: 4 + 8 + 8 = 20 bytes. Example: 256 cells ≈ 5.1 KB.
const (
	ibltHeaderSize = 5
	ibltCellSize   = 20
)

// errIBLTMessageTooShort is returned by UnmarshalIBLT/ReadIBLT when the
// supplied bytes are shorter than the header or the declared cell payload
// requires, guarding against truncated or corrupted messages.
var errIBLTMessageTooShort = errors.New("iblt message too short")

// MarshalIBLT encodes t into the IBLT exchange message format described
// above (4-byte cell count, 1-byte hash count, then cellCount cells of
// count/keySum/hashSum). If t.HashCount is out of the valid [0,32] range it
// is replaced with DefaultHashCount in the encoded output (t itself is not
// mutated).
//
// Parameters:
//   - t (*IBLT): the IBLT to encode; if nil or has no cells, returns (nil, nil).
//
// Returns:
//   - []byte: the encoded message bytes (without the length prefix used by WriteIBLT).
//   - error: non-nil if t has more cells than fit in a uint32.
func MarshalIBLT(t *IBLT) ([]byte, error) {
	if t == nil || len(t.Cells) == 0 {
		return nil, nil
	}
	n := len(t.Cells)
	if n > 0x7fffffff {
		return nil, errors.New("iblt too large to marshal")
	}
	size := ibltHeaderSize + n*ibltCellSize
	buf := make([]byte, size)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(n))
	buf[4] = byte(t.HashCount)
	if t.HashCount < 0 || t.HashCount > 32 {
		buf[4] = byte(DefaultHashCount)
	}
	for i, c := range t.Cells {
		off := ibltHeaderSize + i*ibltCellSize
		binary.LittleEndian.PutUint32(buf[off:off+4], uint32(int32(c.Count)))
		binary.LittleEndian.PutUint64(buf[off+4:off+12], c.KeySum)
		binary.LittleEndian.PutUint64(buf[off+12:off+20], c.HashSum)
	}
	return buf, nil
}

// UnmarshalIBLT decodes an IBLT from the exchange message format written by
// MarshalIBLT. A decoded hashCount outside [1,32] is replaced with
// DefaultHashCount.
//
// Parameters:
//   - data ([]byte): the encoded message bytes (without any length prefix).
//
// Returns:
//   - *IBLT: the decoded IBLT.
//   - error: errIBLTMessageTooShort if data is shorter than the header or the declared cell payload.
func UnmarshalIBLT(data []byte) (*IBLT, error) {
	if len(data) < ibltHeaderSize {
		return nil, errIBLTMessageTooShort
	}
	cellCount := int(binary.LittleEndian.Uint32(data[0:4]))
	hashCount := int(data[4])
	if hashCount <= 0 || hashCount > 32 {
		hashCount = DefaultHashCount
	}
	need := ibltHeaderSize + cellCount*ibltCellSize
	if len(data) < need {
		return nil, errIBLTMessageTooShort
	}
	t := NewIBLT(cellCount)
	t.HashCount = hashCount
	for i := 0; i < cellCount; i++ {
		off := ibltHeaderSize + i*ibltCellSize
		t.Cells[i].Count = int(int32(binary.LittleEndian.Uint32(data[off : off+4])))
		t.Cells[i].KeySum = binary.LittleEndian.Uint64(data[off+4 : off+12])
		t.Cells[i].HashSum = binary.LittleEndian.Uint64(data[off+12 : off+20])
	}
	return t, nil
}

// ReadIBLT reads and decodes an IBLT message from r, as written by WriteIBLT:
// a 4-byte little-endian uint32 length prefix followed by that many bytes of
// MarshalIBLT-encoded payload.
//
// Parameters:
//   - r (io.Reader): source of the length-prefixed encoded message.
//
// Returns:
//   - *IBLT: the decoded IBLT.
//   - error: non-nil if the length prefix indicates a payload over 1<<20 bytes, the read fails, or the payload fails to decode.
func ReadIBLT(r io.Reader) (*IBLT, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(lenBuf[:])
	if n > 1<<20 {
		return nil, errors.New("iblt message length too large")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return UnmarshalIBLT(buf)
}

// WriteIBLT encodes t via MarshalIBLT and writes it to w prefixed with a
// 4-byte little-endian uint32 length. If t encodes to an empty payload
// (nil/empty IBLT), nothing is written and nil is returned.
//
// Parameters:
//   - w (io.Writer): destination for the length-prefixed encoded message.
//   - t (*IBLT): the IBLT to encode and write.
//
// Returns:
//   - error: non-nil if encoding fails or the underlying writes fail.
func WriteIBLT(w io.Writer, t *IBLT) error {
	buf, err := MarshalIBLT(t)
	if err != nil {
		return err
	}
	if len(buf) == 0 {
		return nil
	}
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(buf)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = w.Write(buf)
	return err
}
