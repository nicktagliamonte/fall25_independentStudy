package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	basicconnmgr "github.com/libp2p/go-libp2p/p2p/net/connmgr"
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

func TestReplicaLivenessRequiresSeparatedFailures(t *testing.T) {
	h, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	pid := tokenTestPeerID(t)
	addr := tokenTestMultiaddr(t, "/ip4/127.0.0.1/tcp/65534")
	rp := &RepairProtocol{
		host:                                h,
		livenessFailures:                    make(map[peer.ID]livenessFailureEvidence),
		livenessFailureThreshold:            2,
		livenessFailureConfirmationInterval: time.Minute,
	}
	probeErr := errors.New("transient liveness failure")

	rtt, err := rp.applyLivenessFailureEvidence(pid, addr, 0, probeErr)
	if err != nil || rtt <= 0 {
		t.Fatalf("first failure = (%s, %v), want positive fallback RTT and no error", rtt, err)
	}
	rtt, err = rp.applyLivenessFailureEvidence(pid, addr, 0, probeErr)
	if err != nil || rtt <= 0 {
		t.Fatalf("unseparated repeat = (%s, %v), want continued suspicion", rtt, err)
	}
	if got := rp.livenessFailures[pid].count; got != 1 {
		t.Fatalf("unseparated failure count = %d, want 1", got)
	}

	rp.livenessMu.Lock()
	evidence := rp.livenessFailures[pid]
	evidence.last = time.Now().Add(-2 * rp.livenessFailureConfirmationInterval)
	rp.livenessFailures[pid] = evidence
	rp.livenessMu.Unlock()

	if _, err := rp.applyLivenessFailureEvidence(pid, addr, 0, probeErr); !errors.Is(err, probeErr) {
		t.Fatalf("separated repeat error = %v, want %v", err, probeErr)
	}
}

func TestReplicaLivenessSuccessClearsSuspicion(t *testing.T) {
	h, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	pid := tokenTestPeerID(t)
	addr := tokenTestMultiaddr(t, "/ip4/127.0.0.1/tcp/65534")
	rp := &RepairProtocol{
		host:                                h,
		livenessFailures:                    make(map[peer.ID]livenessFailureEvidence),
		livenessFailureThreshold:            2,
		livenessFailureConfirmationInterval: 0,
	}
	probeErr := errors.New("transient liveness failure")

	if _, err := rp.applyLivenessFailureEvidence(pid, addr, 0, probeErr); err != nil {
		t.Fatalf("first failure: %v", err)
	}
	rp.clearLivenessFailureEvidence(pid)
	if _, err := rp.applyLivenessFailureEvidence(pid, addr, 0, probeErr); err != nil {
		t.Fatalf("failure after successful probe reset: %v", err)
	}
	if got := rp.livenessFailures[pid].count; got != 1 {
		t.Fatalf("failure count after successful probe reset = %d, want 1", got)
	}
}

func TestConcurrentReplicaProbesUseIndependentConnectionProtections(t *testing.T) {
	manager, err := basicconnmgr.NewConnManager(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	h, err := libp2p.New(
		libp2p.NoListenAddrs,
		libp2p.ConnectionManager(manager),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	pid := tokenTestPeerID(t)
	rp := &RepairProtocol{host: h}
	firstTag, releaseFirst := rp.protectReplicaProbe(pid)
	secondTag, releaseSecond := rp.protectReplicaProbe(pid)
	if firstTag == "" || secondTag == "" || firstTag == secondTag {
		t.Fatalf("probe protection tags = %q, %q; want unique non-empty tags", firstTag, secondTag)
	}
	if !manager.IsProtected(pid, firstTag) || !manager.IsProtected(pid, secondTag) {
		t.Fatal("concurrent probe protections were not both active")
	}

	releaseFirst()
	if manager.IsProtected(pid, firstTag) {
		t.Fatal("first probe protection remained after release")
	}
	if !manager.IsProtected(pid, secondTag) {
		t.Fatal("releasing the first probe removed the concurrent protection")
	}
	releaseSecond()
	if manager.IsProtected(pid, "") {
		t.Fatal("probe protection remained after both probes completed")
	}
}
