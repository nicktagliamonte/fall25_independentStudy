// Purpose: Tests for ECDH key derivation verification (Phase 6.1).

package net

import (
	"context"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/insecure"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
)

func TestECDH_ConnectionUsesSecureTransport(t *testing.T) {
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
	s, err := hA.NewStream(ctx, hB.ID(), HandshakeProtocolID)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	s.Close()

	if err := VerifyECDHKeyDerivationUsed(hA); err != nil {
		t.Fatalf("VerifyECDHKeyDerivationUsed: %v", err)
	}
	if err := EnsureAllTrafficEncrypted(hA); err != nil {
		t.Fatalf("EnsureAllTrafficEncrypted: %v", err)
	}
}

func TestEnsureAllTrafficEncrypted_RejectsPlaintext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	makeInsecure := func() (host.Host, error) {
		p, _, e := crypto.GenerateEd25519Key(nil)
		if e != nil {
			return nil, e
		}
		return libp2p.New(
			libp2p.Identity(p),
			libp2p.Transport(tcp.NewTCPTransport),
			libp2p.Security(insecure.ID, insecure.NewWithIdentity),
			libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		)
	}
	hInsecure, err := makeInsecure()
	if err != nil {
		t.Fatalf("NewHost insecure: %v", err)
	}
	defer hInsecure.Close()

	hPlaintext, err := makeInsecure()
	if err != nil {
		t.Fatalf("NewHost plaintext: %v", err)
	}
	defer hPlaintext.Close()

	policy := HandshakePolicy{Timeout: 5 * time.Second}
	local := HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}
	RegisterHandshake(hPlaintext, local, policy)

	if err := hInsecure.Connect(ctx, peer.AddrInfo{ID: hPlaintext.ID(), Addrs: hPlaintext.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if err := EnsureAllTrafficEncrypted(hInsecure); err == nil {
		t.Fatal("EnsureAllTrafficEncrypted should fail for plaintext connection")
	}
	if err := VerifyECDHKeyDerivationUsed(hInsecure); err == nil {
		t.Fatal("VerifyECDHKeyDerivationUsed should fail for plaintext connection")
	}
}
