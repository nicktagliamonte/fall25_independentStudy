// Purpose: Kademlia DHT initialization and bootstrap for token routing (key-based discovery).

package net

import (
	"context"

	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	record "github.com/libp2p/go-libp2p-record"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multiaddr"
)

// TokenDHTProtocolPrefix is used when token storage in DHT is needed.
// Custom prefix avoids /ipfs DHT validation (exactly pk+ipns); allows /tokens/ namespace.
const TokenDHTProtocolPrefix protocol.ID = "/sng40/kad/1.0.0"

// DHTMode selects server (full participant) or client (query-only).
type DHTMode int

const (
	// DHTModeServer runs the DHT as a full participant, storing and serving records
	// for other peers in addition to querying.
	DHTModeServer DHTMode = iota
	// DHTModeClient runs the DHT in query-only mode, without storing or serving
	// records for other peers.
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
	// Mode selects whether the DHT runs as a full server or a query-only client.
	Mode DHTMode
	// BootstrapAddrs are additional multiaddr strings (appended to DefaultDHTBootstrapAddrs)
	// used to seed the routing table when BootstrapPeers/BootstrapPeersFunc are not set.
	BootstrapAddrs []string
	// BootstrapPeers is an explicit list of bootstrap peers to use instead of BootstrapAddrs.
	BootstrapPeers []peer.AddrInfo
	// BootstrapPeersFunc, if set, is used for dynamic bootstrap (e.g. from PeerStore) instead
	// of the static BootstrapPeers/BootstrapAddrs lists.
	BootstrapPeersFunc func() []peer.AddrInfo
	// UseTokenDHT: when true, uses custom protocol prefix + /tokens/ validator for token storage.
	// Incompatible with standard /ipfs DHT; use only for token-routing tests or isolated networks.
	UseTokenDHT bool
}

// tokenRecordValidator validates /tokens/ namespace records for DHT token routing.
// It accepts any non-empty value; token content integrity is verified by callers
// via KeyFromData, not by this validator.
type tokenRecordValidator struct{}

var _ record.Validator = (*tokenRecordValidator)(nil)

// Validate implements record.Validator for the /tokens/ namespace. It only checks
// that the value is non-empty; it performs no schema or signature validation.
//
// Parameters:
//   - key (string): the DHT record key (unused).
//   - value ([]byte): the record value to validate.
//
// Returns:
//   - error: routing.ErrNotFound if value is empty, nil otherwise.
func (tokenRecordValidator) Validate(key string, value []byte) error {
	if len(value) == 0 {
		return routing.ErrNotFound
	}
	return nil
}

// Select implements record.Validator for the /tokens/ namespace. It always picks
// the first candidate value; callers are expected to merge/reconcile token
// versions themselves (see token conflict resolution via version+timestamp).
//
// Parameters:
//   - key (string): the DHT record key (unused).
//   - values ([][]byte): candidate record values for the same key.
//
// Returns:
//   - int: index of the selected value (always 0 when values is non-empty).
//   - error: routing.ErrNotFound if values is empty, nil otherwise.
func (tokenRecordValidator) Select(key string, values [][]byte) (int, error) {
	if len(values) == 0 {
		return -1, routing.ErrNotFound
	}
	return 0, nil
}

// DefaultBootstrapPeerInfos returns parsed AddrInfos for DefaultDHTBootstrapAddrs.
//
// Returns:
//   - []peer.AddrInfo: the parsed default bootstrap peers (entries that fail to parse are skipped).
func DefaultBootstrapPeerInfos() []peer.AddrInfo {
	return parseBootstrapAddrs(DefaultDHTBootstrapAddrs)
}

// NewDHT creates and bootstraps a Kademlia DHT on host h according to cfg. Mode
// selects server vs. client participation; UseTokenDHT switches to the custom
// /sng40/kad/1.0.0 protocol prefix with a permissive /tokens/ namespace validator
// instead of the standard /ipfs DHT. Bootstrap peers are resolved in priority order:
// BootstrapPeersFunc (dynamic), then explicit BootstrapPeers, then parsed
// BootstrapAddrs merged with DefaultDHTBootstrapAddrs. The DHT is closed and an
// error returned if Bootstrap fails.
//
// Parameters:
//   - ctx (context.Context): controls the lifetime of DHT construction and bootstrap.
//   - h (host.Host): the libp2p host the DHT attaches to.
//   - cfg (DHTConfig): mode, protocol prefix, and bootstrap peer configuration.
//
// Returns:
//   - *kaddht.IpfsDHT: the initialized and bootstrapped DHT.
//   - error: non-nil if DHT construction or bootstrap fails.
func NewDHT(ctx context.Context, h host.Host, cfg DHTConfig) (*kaddht.IpfsDHT, error) {
	opts := []kaddht.Option{}

	switch cfg.Mode {
	case DHTModeClient:
		opts = append(opts, kaddht.Mode(kaddht.ModeClient))
	default:
		opts = append(opts, kaddht.Mode(kaddht.ModeServer))
	}

	if cfg.UseTokenDHT {
		opts = append(opts, kaddht.ProtocolPrefix(TokenDHTProtocolPrefix))
		opts = append(opts, kaddht.NamespacedValidator("tokens", &tokenRecordValidator{}))
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

// parseBootstrapAddrs parses multiaddr strings (each expected to include a /p2p/<peerID>
// component) into peer.AddrInfo, deduplicating identical address strings and silently
// skipping empty strings, unparseable multiaddrs, and multiaddrs without a valid peer ID.
//
// Parameters:
//   - addrs ([]string): multiaddr strings to parse.
//
// Returns:
//   - []peer.AddrInfo: successfully parsed, deduplicated bootstrap peer infos.
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
