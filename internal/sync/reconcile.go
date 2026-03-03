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

// NeighborProvider returns peer IDs to exchange IBLTs with.
type NeighborProvider interface {
	Neighbors() []string
}

// IBLTStream opens a bidirectional stream to a peer for IBLT exchange.
type IBLTStream interface {
	io.ReadWriteCloser
}

// IBLTStreamOpener opens a stream to the given peer for IBLT protocol.
type IBLTStreamOpener interface {
	OpenIBLTStream(ctx context.Context, peerID string) (IBLTStream, error)
}

// FetchRequester is called when differences are discovered: Negative key hashes
// indicate content the peer has that we need. The implementation should trigger
// a content fetch (e.g. via Bitswap) for the corresponding items.
type FetchRequester interface {
	RequestFetch(ctx context.Context, peerID string, keyHashes []uint64)
}

// ExchangerConfig holds parameters for periodic IBLT exchange.
type ExchangerConfig struct {
	Interval       time.Duration
	CellCount      int
	Timeout        time.Duration
	FetchRequester FetchRequester  // when set, triggers fetch for Negative keys
	OnPeelFailure  OnPeelFailure   // when set, called on PeelIncomplete (fallback)
}

// ExchangerResult holds the outcome of a single exchange with one neighbor.
type ExchangerResult struct {
	PeerID         string
	Positive       []uint64
	Negative       []uint64
	PeelOK         bool
	PeelIncomplete bool
	Err            error
}

// OnPeelFailure is called when peeling fails (difference too large). The caller may
// increase IBLT size for future exchanges or trigger a fallback (e.g. full sync).
type OnPeelFailure func(peerID string, res ExchangerResult)

// StartPeriodicExchange runs IBLT exchange with neighbors at the given interval.
// localIBLT is rebuilt each cycle by buildLocal (caller provides current set).
// For each neighbor, opens stream, exchanges IBLT, computes diff, peels, and calls
// onResult. Stops when ctx is done. Returns a stop function to cancel the loop.
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

// ExtractDifferenceResult holds the result of difference extraction.
type ExtractDifferenceResult struct {
	Positive      []uint64
	Negative      []uint64
	PeelIncomplete bool
}

// ExtractDifference computes local - remote, peels the difference IBLT, and returns
// recovered key hashes. Positive = keys in local but not remote; Negative = keys in
// remote but not local. PeelIncomplete is true when the difference was too large to
// fully peel (caller should retry with larger IBLT or fall back).
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

var errIBLTMessageTooShort = errors.New("iblt message too short")

// MarshalIBLT encodes t into the IBLT exchange message format.
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

// UnmarshalIBLT decodes an IBLT from the exchange message format.
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

// ReadIBLT reads and decodes an IBLT message from r. The message is prefixed with
// a 4-byte length (uint32, little-endian) indicating the payload size.
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

// WriteIBLT encodes t and writes it to w with a 4-byte length prefix.
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
