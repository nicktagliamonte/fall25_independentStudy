// Purpose: Kademlia DHT initialization and bootstrap for content routing.

package net

import (
	"context"

	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// DHTMode selects server (full participant) or client (query-only).
type DHTMode int

const (
	DHTModeServer DHTMode = iota
	DHTModeClient
)

// DefaultDHTBootstrapAddrs are public libp2p bootstrap peers for DHT discovery.
var DefaultDHTBootstrapAddrs = []string{
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
	"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
}

// DHTConfig holds options for DHT initialization.
type DHTConfig struct {
	Mode              DHTMode
	BootstrapAddrs    []string
	BootstrapPeers    []peer.AddrInfo
	BootstrapPeersFunc func() []peer.AddrInfo // if set, used for dynamic bootstrap (e.g. from PeerStore)
}

// DefaultBootstrapPeerInfos returns parsed AddrInfos for DefaultDHTBootstrapAddrs.
func DefaultBootstrapPeerInfos() []peer.AddrInfo {
	return parseBootstrapAddrs(DefaultDHTBootstrapAddrs)
}

// NewDHT creates and bootstraps a Kademlia DHT.
func NewDHT(ctx context.Context, h host.Host, cfg DHTConfig) (*kaddht.IpfsDHT, error) {
	opts := []kaddht.Option{}

	switch cfg.Mode {
	case DHTModeClient:
		opts = append(opts, kaddht.Mode(kaddht.ModeClient))
	default:
		opts = append(opts, kaddht.Mode(kaddht.ModeServer))
	}

	if cfg.BootstrapPeersFunc != nil {
		opts = append(opts, kaddht.BootstrapPeersFunc(cfg.BootstrapPeersFunc))
	} else {
		bootstrappers := cfg.BootstrapPeers
		if len(bootstrappers) == 0 {
			bootstrappers = parseBootstrapAddrs(append(DefaultDHTBootstrapAddrs, cfg.BootstrapAddrs...))
		}
		if len(bootstrappers) > 0 {
			opts = append(opts, kaddht.BootstrapPeers(bootstrappers...))
		}
	}

	d, err := kaddht.New(ctx, h, opts...)
	if err != nil {
		return nil, err
	}

	if err := d.Bootstrap(ctx); err != nil {
		_ = d.Close()
		return nil, err
	}

	return d, nil
}

func parseBootstrapAddrs(addrs []string) []peer.AddrInfo {
	var out []peer.AddrInfo
	seen := make(map[string]struct{})
	for _, s := range addrs {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		ma, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			continue
		}
		if info.ID == "" {
			continue
		}
		out = append(out, *info)
	}
	return out
}
