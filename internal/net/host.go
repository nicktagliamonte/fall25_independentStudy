// libp2p host wiring (tcp/quic, noise/tls, yamux)

package net

import (
	"context"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	tlssec "github.com/libp2p/go-libp2p/p2p/security/tls"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
)

// NewHost creates a libp2p host with a freshly generated, ephemeral Ed25519 identity
// key (not persisted anywhere — the host's PeerID will differ on every call/process
// restart) and the given listenAddrs (multiaddr strings). ctx is accepted for API
// symmetry with NewHostWithPriv but is not currently used to bound key generation.
// Returns the constructed host.Host, or a non-nil error if key generation or host
// construction (via NewHostWithPriv) fails. Callers that need a stable PeerID across
// restarts should use LoadOrCreatePrivateKey + NewHostWithPriv instead.
func NewHost(ctx context.Context, listenAddrs []string) (host.Host, error) {
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return nil, err
	}
	return NewHostWithPriv(ctx, listenAddrs, priv)
}

// NewHostWithPriv constructs a libp2p host using priv as the node's identity key and
// listenAddrs as the multiaddr strings to listen on (each is passed through
// libp2p.ListenAddrStrings; an invalid entry will cause libp2p.New to fail). The host
// is configured with:
//   - TCP and QUIC transports;
//   - both Noise and TLS security transports (negotiated per-connection);
//   - the Yamux stream multiplexer;
//   - NAT port-mapping (EnableNATService) and client-side relay (EnableRelay) enabled
//     by default.
//
// ctx is accepted for API consistency with other constructors but is not passed into
// libp2p.New in the current implementation (host lifecycle is controlled via the
// returned host.Host's Close method, not ctx cancellation). Returns the constructed
// host.Host, or a non-nil error if libp2p.New fails (e.g. invalid listen address,
// unable to bind).
func NewHostWithPriv(ctx context.Context, listenAddrs []string, priv crypto.PrivKey) (host.Host, error) {
	opts := []libp2p.Option{
		libp2p.Identity(priv),

		// Transports
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(libp2pquic.NewTransport),

		// Security (both)
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
