// Purpose: Shared helper to build storage stack with DHT and DynamicRouter fallback.

package node

import (
	"context"

	bstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// BuildStackWithDHT creates a storage.Stack backed by a Kademlia DHT (with a
// DynamicRouter fallback layered underneath it) and wires the DHT's bootstrap
// peer function to also draw candidates from peerStore.
//
// The returned router chain is: ReachablePartitionRouter -> FallbackContentRouter
// (DHT primary, DynamicRouter fallback) -> stack. The DHT is configured with
// UseTokenDHT so the node can read/write the /tokens/ namespace used by
// SyncTokenOnPut, GetToken, and replication. peerStore must already be
// populated with seed peers before calling, since the DHT bootstrap function
// reads from it. The caller is responsible for closing the returned DHT; the
// returned stack does not take ownership of it (stack.Close does not close
// the DHT).
//
// Parameters:
//   - ctx (context.Context): governs DHT construction and its background routing table refresh.
//   - h (host.Host): the libp2p host the DHT and stack will operate over.
//   - bs (bstore.Blockstore): the blockstore backing the storage stack.
//   - datastore (ds.Batching): the datastore backing the blockstore and peerstore-adjacent state.
//   - peerStore (*myhost.PeerStore): source of additional DHT bootstrap candidates beyond the built-in defaults.
//   - clientMode (bool): if true, run the DHT in client (query-only) mode; if false, run as a full server.
//
// Returns:
//   - *mystore.Stack: the assembled storage stack, with DHT and KeyLockManager already attached.
//   - *kaddht.IpfsDHT: the constructed DHT instance; the caller must Close it.
//   - *ctrl.DynamicRouter: the fallback router used when the DHT cannot resolve a request.
//   - error: non-nil if DHT creation or stack construction fails.
func BuildStackWithDHT(ctx context.Context, h host.Host, bs bstore.Blockstore, datastore ds.Batching, peerStore *myhost.PeerStore, clientMode bool) (*mystore.Stack, *kaddht.IpfsDHT, *ctrl.DynamicRouter, error) {
	mode := myhost.DHTModeServer
	if clientMode {
		mode = myhost.DHTModeClient
	}
	dhtCfg := myhost.DHTConfig{
		Mode:        mode,
		UseTokenDHT: true, // required for /tokens/ namespace (SyncTokenOnPut, GetToken, replication)
		// BootstrapPeersFunc supplies the DHT's bootstrap candidate list on
		// demand: it merges the built-in default bootstrap peers with up to
		// 50 dial candidates drawn from peerStore, excluding self and
		// de-duplicating by peer ID.
		BootstrapPeersFunc: func() []peer.AddrInfo {
			defaults := myhost.DefaultBootstrapPeerInfos()
			fromStore, _ := peerStore.GetDialCandidates(50, 0, nil)
			seen := make(map[peer.ID]struct{})
			var out []peer.AddrInfo
			for _, info := range append(defaults, fromStore...) {
				if info.ID == h.ID() {
					continue
				}
				if _, ok := seen[info.ID]; ok {
					continue
				}
				seen[info.ID] = struct{}{}
				out = append(out, info)
			}
			return out
		},
	}
	d, err := myhost.NewDHT(ctx, h, dhtCfg)
	if err != nil {
		return nil, nil, nil, err
	}
	dynamicRouter := ctrl.NewDynamicRouter()
	composedRouter := ctrl.NewFallbackContentRouter(d, dynamicRouter)
	reachableRouter := ctrl.NewReachablePartitionRouter(h, composedRouter)
	stack, err := mystore.NewStackFromBlockstore(ctx, h, bs, datastore, reachableRouter)
	if err != nil {
		_ = d.Close()
		return nil, nil, nil, err
	}
	stack.DHT = d
	stack.KeyLockManager = mystore.NewKeyLockManagerFromDatastore(stack.Datastore)
	return stack, d, dynamicRouter, nil
}
