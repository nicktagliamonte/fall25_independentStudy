// Purpose: Tests for per-peer nonce cache with auto-expunge.

package net

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func mustPeerID(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestNonceCache_AddAndSeen(t *testing.T) {
	c := NewNonceCache()
	pid := mustPeerID(t)

	if c.Seen(pid, 1) {
		t.Fatal("nonce 1 should not be seen before Add")
	}
	c.Add(pid, 1)
	if !c.Seen(pid, 1) {
		t.Fatal("nonce 1 should be seen after Add")
	}
	if c.Seen(pid, 2) {
		t.Fatal("nonce 2 should not be seen")
	}
	c.Add(pid, 2)
	if !c.Seen(pid, 2) {
		t.Fatal("nonce 2 should be seen after Add")
	}
}

func TestNonceCache_RejectReusedNonce(t *testing.T) {
	c := NewNonceCache()
	pid := mustPeerID(t)

	if err := c.RecordNonce(pid, 42); err != nil {
		t.Fatalf("first RecordNonce: %v", err)
	}
	if err := c.RecordNonce(pid, 42); !errors.Is(err, ErrReusedNonce) {
		t.Fatalf("reused nonce: expected ErrReusedNonce, got %v", err)
	}
}

func TestNonceCache_PerPeer(t *testing.T) {
	c := NewNonceCache()
	pidA := mustPeerID(t)
	pidB := mustPeerID(t)

	c.Add(pidA, 42)
	if !c.Seen(pidA, 42) {
		t.Fatal("A: nonce 42 should be seen")
	}
	if c.Seen(pidB, 42) {
		t.Fatal("B: nonce 42 should not be seen (different peer)")
	}
}

func TestNonceCache_ExpungeRemovesIdlePeers(t *testing.T) {
	c := NewNonceCache(nonceExpungeAfterForTest(100 * time.Millisecond))
	pid := mustPeerID(t)

	c.Add(pid, 1)
	if c.Peers() != 1 {
		t.Fatalf("expected 1 peer, got %d", c.Peers())
	}

	c.expunge()
	if c.Peers() != 1 {
		t.Fatalf("immediate expunge: entry not idle, should keep 1 peer, got %d", c.Peers())
	}

	time.Sleep(150 * time.Millisecond)
	c.expunge()
	if c.Peers() != 0 {
		t.Fatalf("after idle: expected 0 peers, got %d", c.Peers())
	}
}

func TestNonceCache_AutoExpunge(t *testing.T) {
	c := NewNonceCache(
		nonceExpungeAfterForTest(100*time.Millisecond),
		NonceExpungeInterval(30*time.Millisecond),
	)
	pid := mustPeerID(t)

	c.Add(pid, 1)
	if c.Peers() != 1 {
		t.Fatalf("expected 1 peer, got %d", c.Peers())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Start(ctx)
	defer cancel()

	time.Sleep(200 * time.Millisecond)
	if c.Peers() != 0 {
		t.Fatalf("after idle: expected 0 peers, got %d", c.Peers())
	}
}

func TestNonceCache_ExpungeAfterBounds(t *testing.T) {
	c := NewNonceCache(
		NonceExpungeAfter(0),
		NonceExpungeAfter(10*time.Minute),
	)
	if c.expungeAfter < MinNonceExpungeAfter || c.expungeAfter > MaxNonceExpungeAfter {
		t.Fatalf("expungeAfter %v not in 1-5 min", c.expungeAfter)
	}
}

func TestMessageHashCache_RejectDuplicate(t *testing.T) {
	c := NewMessageHashCache(messageHashExpungeAfterForTest(5 * time.Minute))
	pid := mustPeerID(t)
	hash := []byte("msg-hash-abc123")

	if err := c.RecordHash(pid, hash); err != nil {
		t.Fatalf("first RecordHash: %v", err)
	}
	if err := c.RecordHash(pid, hash); !errors.Is(err, ErrDuplicateMessageHash) {
		t.Fatalf("duplicate RecordHash: expected ErrDuplicateMessageHash, got %v", err)
	}
}

func TestMessageHashCache_PerPeer(t *testing.T) {
	c := NewMessageHashCache(messageHashExpungeAfterForTest(5 * time.Minute))
	pidA := mustPeerID(t)
	pidB := mustPeerID(t)
	hash := []byte("same-hash")

	if err := c.RecordHash(pidA, hash); err != nil {
		t.Fatalf("A RecordHash: %v", err)
	}
	if err := c.RecordHash(pidB, hash); err != nil {
		t.Fatalf("B RecordHash (different peer, same hash allowed): %v", err)
	}
}

func TestTimestampChecker_RejectExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	c := NewTimestampChecker(
		TimestampWindow(5*time.Minute),
		TimestampFutureAllow(1*time.Minute),
		timestampNowFuncForTest(func() time.Time { return now }),
	)

	if err := c.RejectExpired(now); err != nil {
		t.Fatalf("current time should be accepted: %v", err)
	}
	if err := c.RejectExpired(now.Add(-4 * time.Minute)); err != nil {
		t.Fatalf("4 min ago should be accepted: %v", err)
	}
	if err := c.RejectExpired(now.Add(-6 * time.Minute)); !errors.Is(err, ErrExpiredTimestamp) {
		t.Fatalf("6 min ago should be rejected: %v", err)
	}
	if err := c.RejectExpired(now.Add(30 * time.Second)); err != nil {
		t.Fatalf("30s in future should be accepted: %v", err)
	}
	if err := c.RejectExpired(now.Add(2 * time.Minute)); !errors.Is(err, ErrExpiredTimestamp) {
		t.Fatalf("2 min in future should be rejected: %v", err)
	}
}

func TestTimestampChecker_RejectExpiredUnix(t *testing.T) {
	now := time.Unix(1000, 0)
	c := NewTimestampChecker(
		TimestampWindow(5*time.Minute),
		TimestampFutureAllow(1*time.Minute),
		timestampNowFuncForTest(func() time.Time { return now }),
	)

	if err := c.RejectExpiredUnix(now.Unix()); err != nil {
		t.Fatalf("current Unix should be accepted: %v", err)
	}
	if err := c.RejectExpiredUnix(now.Add(-6 * time.Minute).Unix()); !errors.Is(err, ErrExpiredTimestamp) {
		t.Fatalf("6 min ago Unix should be rejected: %v", err)
	}
}
