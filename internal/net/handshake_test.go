// Purpose: Tests for handshake including challenge-response with signed nonce (Phase 6.1).

package net

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestHandshake_ChallengeResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hA, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	policy := HandshakePolicy{Timeout: 5 * time.Second}
	local := HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}

	RegisterHandshake(hB, local, policy)
	if err := hA.Connect(ctx, peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	learned, err := PerformHandshake(ctx, hA, hB.ID(), policy, local)
	if err != nil {
		t.Fatalf("PerformHandshake: %v", err)
	}
	_ = learned
}

func TestHandshake_WithAntiReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hA, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	policy := HandshakePolicy{Timeout: 5 * time.Second}
	stop := EnableAntiReplay(ctx, &policy)
	defer stop()
	local := HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}

	RegisterHandshake(hB, local, policy)
	if err := hA.Connect(ctx, peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	learned, err := PerformHandshake(ctx, hA, hB.ID(), policy, local)
	if err != nil {
		t.Fatalf("PerformHandshake with anti-replay: %v", err)
	}
	_ = learned
}
