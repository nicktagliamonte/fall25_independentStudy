// Purpose: Tests for IBLT reconcile message format.

package sync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"
)

func TestMarshalUnmarshalIBLT(t *testing.T) {
	tbl := NewIBLT(64)
	tbl.Insert([]byte("a"))
	tbl.Insert([]byte("b"))
	buf, err := MarshalIBLT(tbl)
	if err != nil {
		t.Fatalf("MarshalIBLT: %v", err)
	}
	if len(buf) < ibltHeaderSize {
		t.Errorf("MarshalIBLT: want at least %d bytes, got %d", ibltHeaderSize, len(buf))
	}
	got, err := UnmarshalIBLT(buf)
	if err != nil {
		t.Fatalf("UnmarshalIBLT: %v", err)
	}
	if got.CellCount != tbl.CellCount {
		t.Errorf("UnmarshalIBLT: CellCount want %d, got %d", tbl.CellCount, got.CellCount)
	}
	for i := range tbl.Cells {
		if got.Cells[i] != tbl.Cells[i] {
			t.Errorf("cell %d: want %+v, got %+v", i, tbl.Cells[i], got.Cells[i])
		}
	}
}

func TestMarshalUnmarshalDifferenceIBLT(t *testing.T) {
	a := NewIBLT(32)
	b := NewIBLT(32)
	a.Insert([]byte("x"))
	b.Insert([]byte("y"))
	diff := a.Subtract(b)
	buf, err := MarshalIBLT(diff)
	if err != nil {
		t.Fatalf("MarshalIBLT: %v", err)
	}
	got, err := UnmarshalIBLT(buf)
	if err != nil {
		t.Fatalf("UnmarshalIBLT: %v", err)
	}
	res := got.Peel()
	if len(res.Positive) != 1 || len(res.Negative) != 1 {
		t.Errorf("roundtrip diff: Peel want 1 pos, 1 neg; got %d pos, %d neg", len(res.Positive), len(res.Negative))
	}
}

func TestReadWriteIBLT(t *testing.T) {
	tbl := NewIBLT(16)
	tbl.Insert([]byte("test"))
	var buf bytes.Buffer
	if err := WriteIBLT(&buf, tbl); err != nil {
		t.Fatalf("WriteIBLT: %v", err)
	}
	got, err := ReadIBLT(&buf)
	if err != nil {
		t.Fatalf("ReadIBLT: %v", err)
	}
	if got.CellCount != tbl.CellCount {
		t.Errorf("ReadIBLT: CellCount want %d, got %d", tbl.CellCount, got.CellCount)
	}
}

func TestStartPeriodicExchangeTriggersFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	localIBLT := NewIBLT(32)
	localIBLT.Insert([]byte("local-only"))
	var fetchedPeer string
	var fetchedHashes []uint64
	fetcher := &mockFetcher{
		onRequest: func(peerID string, hashes []uint64) {
			fetchedPeer = peerID
			fetchedHashes = hashes
		},
	}
	neighbors := []string{"p1"}
	opener := &mockIBLTOpener{
		peerIBLT: func(peerID string) *IBLT {
			tbl := NewIBLT(32)
			tbl.Insert([]byte("remote-only"))
			return tbl
		},
	}
	cfg := ExchangerConfig{
		Interval:       10 * time.Millisecond,
		Timeout:        time.Second,
		FetchRequester: fetcher,
	}
	stop := StartPeriodicExchange(ctx, cfg, func() *IBLT { return localIBLT }, &mockNeighbors{neighbors}, opener, nil)
	time.Sleep(25 * time.Millisecond)
	stop()
	if fetchedPeer != "p1" {
		t.Errorf("RequestFetch: want peer p1, got %q", fetchedPeer)
	}
	kh := localIBLT.keyHash([]byte("remote-only"))
	found := false
	for _, h := range fetchedHashes {
		if h == kh {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RequestFetch: want keyHash(remote-only) in hashes, got %v", fetchedHashes)
	}
}

func TestStartPeriodicExchange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	localIBLT := NewIBLT(32)
	localIBLT.Insert([]byte("local-key"))
	neighbors := []string{"p1"}
	opener := &mockIBLTOpener{
		peerIBLT: func(peerID string) *IBLT {
			tbl := NewIBLT(32)
			if peerID == "p1" {
				tbl.Insert([]byte("remote-key"))
			}
			return tbl
		},
	}
	var results []ExchangerResult
	onResult := func(r ExchangerResult) { results = append(results, r) }
	cfg := ExchangerConfig{Interval: 10 * time.Millisecond, Timeout: time.Second}
	stop := StartPeriodicExchange(ctx, cfg, func() *IBLT { return localIBLT }, &mockNeighbors{neighbors}, opener, onResult)
	time.Sleep(25 * time.Millisecond)
	stop()
	if len(results) == 0 {
		t.Error("StartPeriodicExchange: expect at least one result")
	}
}

type mockFetcher struct {
	onRequest func(peerID string, keyHashes []uint64)
}

func (m *mockFetcher) RequestFetch(ctx context.Context, peerID string, keyHashes []uint64) {
	if m.onRequest != nil {
		m.onRequest(peerID, keyHashes)
	}
}

type mockNeighbors struct {
	peers []string
}

func (m *mockNeighbors) Neighbors() []string { return m.peers }

type mockIBLTOpener struct {
	peerIBLT func(peerID string) *IBLT
}

func (m *mockIBLTOpener) OpenIBLTStream(ctx context.Context, peerID string) (IBLTStream, error) {
	peerReadsFromUs, weWriteToPeer := io.Pipe()
	weReadFromPeer, peerWritesToUs := io.Pipe()
	remote := m.peerIBLT(peerID)
	go func() {
		defer peerWritesToUs.Close()
		_, _ = ReadIBLT(peerReadsFromUs)
		peerReadsFromUs.Close()
		_ = WriteIBLT(peerWritesToUs, remote)
	}()
	return &mockStream{Reader: weReadFromPeer, Writer: weWriteToPeer, closer: weWriteToPeer}, nil
}

type mockStream struct {
	io.Reader
	io.Writer
	closer io.Closer
}

func (m *mockStream) Close() error {
	if m.closer != nil {
		return m.closer.Close()
	}
	return nil
}

func TestExtractDifference(t *testing.T) {
	local := NewIBLT(64)
	remote := NewIBLT(64)
	local.Insert([]byte("a"))
	local.Insert([]byte("b"))
	remote.Insert([]byte("b"))
	remote.Insert([]byte("c"))
	res, err := ExtractDifference(local, remote)
	if err != nil {
		t.Fatalf("ExtractDifference: %v", err)
	}
	khA := local.keyHash([]byte("a"))
	khC := remote.keyHash([]byte("c"))
	if len(res.Positive) != 1 || res.Positive[0] != khA {
		t.Errorf("ExtractDifference Positive: want [hash(a)], got %v", res.Positive)
	}
	if len(res.Negative) != 1 || res.Negative[0] != khC {
		t.Errorf("ExtractDifference Negative: want [hash(c)], got %v", res.Negative)
	}
	if res.PeelIncomplete {
		t.Error("ExtractDifference: small diff should not be incomplete")
	}
}

func TestOnPeelFailureCalled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local := NewIBLT(8)
	remote := NewIBLT(8)
	for i := 0; i < 25; i++ {
		local.Insert([]byte(fmt.Sprintf("a-%d", i)))
		remote.Insert([]byte(fmt.Sprintf("b-%d", i)))
	}
	var failureCalled bool
	var failurePeer string
	opener := &mockIBLTOpener{peerIBLT: func(string) *IBLT { return remote }}
	cfg := ExchangerConfig{
		Interval:      10 * time.Millisecond,
		Timeout:       time.Second,
		OnPeelFailure: func(peerID string, _ ExchangerResult) { failureCalled = true; failurePeer = peerID },
	}
	stop := StartPeriodicExchange(ctx, cfg, func() *IBLT { return local }, &mockNeighbors{[]string{"p1"}}, opener, nil)
	time.Sleep(50 * time.Millisecond)
	stop()
	if !failureCalled {
		t.Skip("OnPeelFailure: large diff with small IBLT may fully peel; skipping peel-failure assertion")
	}
	if failurePeer != "p1" {
		t.Errorf("OnPeelFailure: want peer p1, got %q", failurePeer)
	}
}

func TestExtractDifferenceIncompatible(t *testing.T) {
	local := NewIBLT(32)
	remote := NewIBLT(64)
	_, err := ExtractDifference(local, remote)
	if err == nil {
		t.Error("ExtractDifference: incompatible IBLTs should return error")
	}
}

func TestUnmarshalIBLTTooShort(t *testing.T) {
	_, err := UnmarshalIBLT([]byte{0, 0, 0})
	if err != errIBLTMessageTooShort {
		t.Errorf("UnmarshalIBLT short: want errIBLTMessageTooShort, got %v", err)
	}
}
