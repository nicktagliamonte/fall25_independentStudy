// Purpose: libp2p host wiring (tcp/quic, noise/tls, yamux) and DHT integration.

package net

import (
	"context"
	"fmt"

	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	tlssec "github.com/libp2p/go-libp2p/p2p/security/tls"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
)

// ECDHSecurityProtocolIDs are the libp2p security protocol IDs that use ECDH key derivation.
// Noise uses X25519; TLS 1.3 uses ECDHE. NewHostWithPriv configures both.
var ECDHSecurityProtocolIDs = []string{noise.ID, tlssec.ID}

const plaintextID = "/plaintext/2.0.0"

func isSecureProtocol(id string) bool {
	return id == noise.ID || id == tlssec.ID
}

// EnsureAllTrafficEncrypted verifies all connections on the host use AEAD-encrypted security
// (Noise or TLS). Returns an error if any connection uses plaintext or unknown security.
func EnsureAllTrafficEncrypted(h host.Host) error {
	return verifyConnectionSecurity(h, func(id string) bool { return isSecureProtocol(id) }, "encrypted")
}

// VerifyECDHKeyDerivationUsed verifies all connections on the host use ECDH-derived keys
// (Noise X25519 or TLS ECDHE). Returns an error if any connection uses plaintext or non-ECDH.
func VerifyECDHKeyDerivationUsed(h host.Host) error {
	return verifyConnectionSecurity(h, isSecureProtocol, "ECDH")
}

func verifyConnectionSecurity(h host.Host, allow func(string) bool, label string) error {
	for _, c := range h.Network().Conns() {
		swc, ok := c.(*swarm.Conn)
		if !ok {
			continue
		}
		state := swc.ConnState()
		sec := string(state.Security)
		if sec == "" || sec == plaintextID || !allow(sec) {
			return fmt.Errorf("connection to %s uses %q, expected %s transport (noise/tls)", c.RemotePeer(), sec, label)
		}
	}
	return nil
}

func NewHost(ctx context.Context, listenAddrs []string) (host.Host, error) {
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return nil, err
	}
	return NewHostWithPriv(ctx, listenAddrs, priv)
}

func NewHostWithPriv(ctx context.Context, listenAddrs []string, priv crypto.PrivKey) (host.Host, error) {
	opts := []libp2p.Option{
		libp2p.Identity(priv),

		// Transports
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(libp2pquic.NewTransport),

		// Security: Noise (ChaCha20-Poly1305) and TLS (AES-GCM/ChaCha20-Poly1305)
		libp2p.Security(noise.ID, noise.New),
		libp2p.Security(tlssec.ID, tlssec.New),

		// Stream muxer
		libp2p.Muxer(yamux.ID, yamux.DefaultTransport),

		// Listen addrs
	}
	for _, a := range listenAddrs {
		opts = append(opts, libp2p.ListenAddrStrings(a))
	}
	// NAT service on by default; client relay enabled
	opts = append(opts,
		libp2p.EnableNATService(),
		libp2p.EnableRelay(),
	)

	return libp2p.New(opts...)
}

// NewHostWithDHT creates a host and bootstraps a DHT. Returns (host, dht, error).
// Uses NewHostWithPriv internally; callers must resolve priv (KeyPath, EphemeralSeed, etc.).
func NewHostWithDHT(ctx context.Context, listenAddrs []string, priv crypto.PrivKey, dhtCfg DHTConfig) (host.Host, *kaddht.IpfsDHT, error) {
	h, err := NewHostWithPriv(ctx, listenAddrs, priv)
	if err != nil {
		return nil, nil, err
	}
	d, err := NewDHT(ctx, h, dhtCfg)
	if err != nil {
		_ = h.Close()
		return nil, nil, err
	}
	return h, d, nil
}
