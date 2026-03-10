// Purpose: Shared helper to build storage stack with DHT and DynamicRouter fallback.

package node

import (
	"context"

	bstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// BuildStackWithDHT creates a storage stack with DHT and DynamicRouter fallback.
// peerStore must be populated with seeds before calling.
// Caller must close the returned DHT (stack does not own it).
func BuildStackWithDHT(ctx context.Context, h host.Host, bs bstore.Blockstore, datastore ds.Batching, peerStore *myhost.PeerStore, clientMode bool) (*mystore.Stack, *kaddht.IpfsDHT, *ctrl.DynamicRouter, error) {
	mode := myhost.DHTModeServer
	if clientMode {
		mode = myhost.DHTModeClient
	}
	dhtCfg := myhost.DHTConfig{
		Mode: mode,
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
	return stack, d, dynamicRouter, nil
}
