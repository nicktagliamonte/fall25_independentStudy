// Purpose: libp2p host wiring (tcp/quic, noise/tls, yamux) and DHT integration.

package net

import (
	"context"
	"fmt"

	libp2p "github.com/libp2p/go-libp2p"
	kaddht "github.com/libp2p/go-libp2p-kad-dht"
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

// isSecureProtocol reports whether id is one of the ECDH-based security protocols
// (Noise or TLS) this package configures on hosts.
//
// Parameters:
//   - id (string): a libp2p security protocol ID, as reported by a connection's state.
//
// Returns:
//   - bool: true if id is noise.ID or tlssec.ID.
func isSecureProtocol(id string) bool {
	return id == noise.ID || id == tlssec.ID
}

// EnsureAllTrafficEncrypted verifies all connections on the host use AEAD-encrypted security
// (Noise or TLS). Returns an error if any connection uses plaintext or unknown security.
//
// Parameters:
//   - h (host.Host): the libp2p host whose active connections are inspected.
//
// Returns:
//   - error: describes the first connection found using plaintext or unrecognized security, nil if all are encrypted.
func EnsureAllTrafficEncrypted(h host.Host) error {
	return verifyConnectionSecurity(h, func(id string) bool { return isSecureProtocol(id) }, "encrypted")
}

// VerifyECDHKeyDerivationUsed verifies all connections on the host use ECDH-derived keys
// (Noise X25519 or TLS ECDHE). Returns an error if any connection uses plaintext or non-ECDH.
//
// Parameters:
//   - h (host.Host): the libp2p host whose active connections are inspected.
//
// Returns:
//   - error: describes the first connection found not using ECDH-derived security, nil if all qualify.
func VerifyECDHKeyDerivationUsed(h host.Host) error {
	return verifyConnectionSecurity(h, isSecureProtocol, "ECDH")
}

// verifyConnectionSecurity walks h's active connections and checks each one's negotiated
// security protocol against allow. Connections that are not *swarm.Conn (and thus expose
// no inspectable ConnState) are skipped rather than treated as failures.
//
// Parameters:
//   - h (host.Host): the libp2p host whose connections are inspected.
//   - allow (func(string) bool): predicate over the negotiated security protocol ID; returns true if acceptable.
//   - label (string): human-readable description of the required transport, used in the error message.
//
// Returns:
//   - error: identifies the remote peer and offending security protocol for the first connection that fails allow, nil if all pass.
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

// NewHost creates a libp2p host with a freshly generated, ephemeral Ed25519 identity
// key. Use NewHostWithPriv directly when the private key must be persisted or reused
// across restarts.
//
// Parameters:
//   - ctx (context.Context): passed through to NewHostWithPriv (currently unused by the underlying libp2p.New call, retained for API consistency).
//   - listenAddrs ([]string): multiaddr strings the host should listen on.
//
// Returns:
//   - host.Host: the constructed libp2p host.
//   - error: non-nil if key generation or host construction fails.
func NewHost(ctx context.Context, listenAddrs []string) (host.Host, error) {
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return nil, err
	}
	return NewHostWithPriv(ctx, listenAddrs, priv)
}

// NewHostWithPriv creates a libp2p host using the given private key as its identity,
// wired with TCP and QUIC transports, Noise and TLS security (both ECDH-based, see
// ECDHSecurityProtocolIDs), the yamux stream muxer, NAT service, and client relay
// enabled. listenAddrs are added as ListenAddrStrings options.
//
// Parameters:
//   - ctx (context.Context): reserved for future use; not currently passed to libp2p.New.
//   - listenAddrs ([]string): multiaddr strings the host should listen on.
//   - priv (crypto.PrivKey): the host's identity key.
//
// Returns:
//   - host.Host: the constructed libp2p host.
//   - error: non-nil if libp2p host construction fails.
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
// If DHT construction fails, the host is closed before returning the error.
//
// Parameters:
//   - ctx (context.Context): controls the lifetime of host creation and DHT bootstrap.
//   - listenAddrs ([]string): multiaddr strings the host should listen on.
//   - priv (crypto.PrivKey): the host's identity key.
//   - dhtCfg (DHTConfig): DHT mode, protocol prefix, and bootstrap configuration; see NewDHT.
//
// Returns:
//   - host.Host: the constructed libp2p host.
//   - *kaddht.IpfsDHT: the bootstrapped DHT attached to the host.
//   - error: non-nil if host construction or DHT bootstrap fails.
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
