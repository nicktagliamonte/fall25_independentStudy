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

func NewHost(ctx context.Context, listenAddrs []string) (host.Host, error) {
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return nil, err
	}

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

	return libp2p.New(opts...)
}
