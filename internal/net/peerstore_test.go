package net

import (
	"crypto/rand"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// newMemDS returns a fresh in-memory, mutex-synchronized datastore suitable for
// constructing a PeerStore in tests without touching disk.
func newMemDS() ds.Batching {
	return dssync.MutexWrap(ds.NewMapDatastore())
}

// TestPeerStorePersistence verifies that a peer Upsert-ed into one PeerStore instance
// is recoverable (with correct PeerID) via GetDialCandidates from a second PeerStore
// instance opened against the same underlying datastore, confirming records survive a
// "reopen" (i.e. loadAll correctly reconstructs the in-memory index from persisted
// data).
func TestPeerStorePersistence(t *testing.T) {
	mem := newMemDS()
	ps, err := NewPeerStore(mem)
	if err != nil {
		t.Fatalf("new peerstore: %v", err)
	}
	priv, _, _ := crypto.GenerateEd25519Key(rand.Reader)
	pid, _ := peer.IDFromPrivateKey(priv)
	a1, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/4001")
	if err := ps.Upsert(pid, []ma.Multiaddr{a1}, 1, "seed"); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// reopen
	ps2, err := NewPeerStore(mem)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	infos, meta := ps2.GetDialCandidates(10, 0, nil)
	if len(infos) != 1 || len(meta) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(infos))
	}
	if infos[0].ID != pid {
		t.Fatalf("wrong pid: %s", infos[0].ID)
	}
}

// TestPeerStoreScoring verifies that GetDialCandidates ranks a peer with a matching
// service bit and a recent dial success (pidA) ahead of a peer with no service match
// and recorded dial failures (pidB), and that Prune, after tightening the max-failure
// policy via SetPolicy, removes at least the over-threshold peer (pidB).
func TestPeerStoreScoring(t *testing.T) {
	mem := newMemDS()
	ps, err := NewPeerStore(mem)
	if err != nil {
		t.Fatalf("new peerstore: %v", err)
	}
	privA, _, _ := crypto.GenerateEd25519Key(rand.Reader)
	pidA, _ := peer.IDFromPrivateKey(privA)
	privB, _, _ := crypto.GenerateEd25519Key(rand.Reader)
	pidB, _ := peer.IDFromPrivateKey(privB)
	a, _ := ma.NewMultiaddr("/ip4/10.0.0.1/tcp/4001")
	b, _ := ma.NewMultiaddr("/ip4/10.0.0.2/tcp/4001")
	if err := ps.Upsert(pidA, []ma.Multiaddr{a}, 1, "seed"); err != nil {
		t.Fatal(err)
	}
	if err := ps.Upsert(pidB, []ma.Multiaddr{b}, 0, "seed"); err != nil {
		t.Fatal(err)
	}

	// make A have recent success
	if err := ps.RecordDialSuccess(pidA); err != nil {
		t.Fatal(err)
	}
	// and B have failures
	_ = ps.RecordDialAttempt(pidB)
	_ = ps.RecordDialFailure(pidB)
	_ = ps.RecordDialFailure(pidB)

	infos, meta := ps.GetDialCandidates(2, 1, nil)
	if len(infos) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(infos))
	}
	// A should rank before B due to service match and success
	if meta[0].PeerID != pidA.String() {
		t.Fatalf("expected A first, got %s", meta[0].PeerID)
	}

	// Prune with low max failures should remove B
	ps.SetPolicy(time.Hour, 1)
	removed, err := ps.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if removed == 0 {
		t.Fatalf("expected at least 1 removed")
	}
}
