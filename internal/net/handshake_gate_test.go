package net

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestHandshakeGateClearsVerificationAfterLastDisconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	local := HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0)}
	policy := HandshakePolicy{
		MinAgentVersion: "sng40/0.1.0",
		ServicesAllow:   ^uint64(0),
		Timeout:         time.Second,
	}
	RegisterHandshake(server, local, policy)
	gate := InstallHandshakeGate(client, local, policy)
	if err := client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatal(err)
	}
	awaitHandshakeGateState(t, gate, server.ID(), true)

	if err := client.Network().ClosePeer(server.ID()); err != nil {
		t.Fatal(err)
	}
	awaitHandshakeGateState(t, gate, server.ID(), false)
	if tagInfo := client.ConnManager().GetTagInfo(server.ID()); tagInfo != nil &&
		tagInfo.Tags[handshakeOkTag] > 0 {
		t.Fatalf("verification tag survived last disconnect: %+v", tagInfo.Tags)
	}
}

func TestHandshakeResponderReportsAcceptedPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	local := HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0)}
	policy := HandshakePolicy{
		MinAgentVersion: "sng40/0.1.0",
		ServicesAllow:   ^uint64(0),
		Timeout:         time.Second,
	}
	var accepted atomic.Bool
	RegisterHandshakeWithPeersAndCallback(server, local, policy, nil, func(pid peer.ID) {
		if pid == client.ID() {
			accepted.Store(true)
		}
	})
	if err := client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatal(err)
	}
	if _, err := PerformHandshake(ctx, client, server.ID(), policy, local); err != nil {
		t.Fatal(err)
	}
	if !accepted.Load() {
		t.Fatal("successful responder handshake did not report the accepted peer")
	}
}

func awaitHandshakeGateState(t *testing.T, gate *HandshakeGate, pid peer.ID, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if gate.isVerified(pid) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("verified(%s) = %v, want %v", pid, gate.isVerified(pid), want)
}
