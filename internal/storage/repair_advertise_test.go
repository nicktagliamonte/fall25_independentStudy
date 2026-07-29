package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
)

type retryAdvertisementTupleSpace struct {
	mu        sync.Mutex
	attempts  int
	succeeded chan struct{}
}

func (m *retryAdvertisementTupleSpace) TsPut(string, []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	if m.attempts < 3 {
		return -1, errors.New("transient startup failure")
	}
	select {
	case <-m.succeeded:
	default:
		close(m.succeeded)
	}
	return 0, nil
}

func (m *retryAdvertisementTupleSpace) TsGet(string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (m *retryAdvertisementTupleSpace) TsRead(string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func TestStorageAvailabilityAdvertisementRetriesTransientFailure(t *testing.T) {
	ts := &retryAdvertisementTupleSpace{succeeded: make(chan struct{})}
	h, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	rp := &RepairProtocol{
		host:             h,
		storageAvailable: NewStorageAvailableProtocol(ts),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.StartAdvertisingStorageAvailability(ctx)

	select {
	case <-ts.succeeded:
	case <-time.After(2 * time.Second):
		t.Fatal("advertisement did not recover from transient failures")
	}
	ts.mu.Lock()
	attempts := ts.attempts
	ts.mu.Unlock()
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}
