// Purpose: Integration test for IBLT-based CID set reconciliation between two nodes.

package node

import (
	"bytes"
	"context"
	"testing"
	"time"

	bstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

func TestIBLTCatalogSync(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	hA, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	infoA := peer.AddrInfo{ID: hA.ID(), Addrs: hA.Addrs()}
	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}

	buildStack := func(h host.Host, bootstrap peer.AddrInfo, bs bstore.Blockstore, datastore ds.Batching) (*mystore.Stack, *kaddht.IpfsDHT, error) {
		dhtCfg := myhost.DHTConfig{
			Mode: myhost.DHTModeServer,
			BootstrapPeersFunc: func() []peer.AddrInfo {
				if bootstrap.ID == h.ID() {
					return nil
				}
				return []peer.AddrInfo{bootstrap}
			},
		}
		d, err := myhost.NewDHT(ctx, h, dhtCfg)
		if err != nil {
			return nil, nil, err
		}
		dr := ctrl.NewDynamicRouter()
		router := ctrl.NewFallbackContentRouter(d, dr)
		stack, err := mystore.NewStackFromBlockstore(ctx, h, bs, datastore, router)
		if err != nil {
			_ = d.Close()
			return nil, nil, err
		}
		return stack, d, nil
	}

	bsA, dsA := mystore.NewEphemeralBlockstore()
	bsB, dsB := mystore.NewEphemeralBlockstore()

	stackA, dhtA, err := buildStack(hA, infoB, bsA, dsA)
	if err != nil {
		t.Fatalf("buildStack A: %v", err)
	}
	defer dhtA.Close()
	defer stackA.Close()

	stackB, dhtB, err := buildStack(hB, infoA, bsB, dsB)
	if err != nil {
		t.Fatalf("buildStack B: %v", err)
	}
	defer dhtB.Close()
	defer stackB.Close()

	stackA.ProviderRecords = mystore.NewLocalProviderRecords()
	stackB.ProviderRecords = mystore.NewLocalProviderRecords()

	payload := []byte("iblt catalog sync test payload")
	_, c, err := mystore.PutRawBlockIndexed(ctx, stackA.Datastore, stackA.BlockSvc, payload, nil)
	if err != nil {
		t.Fatalf("PutRawBlockIndexed: %v", err)
	}
	stackA.AnnounceProvider(ctx, c)

	time.Sleep(2 * time.Second)
	for i := 0; i < 30; i++ {
		if hA.Network().Connectedness(hB.ID()) != network.Connected {
			_ = hA.Connect(ctx, infoB)
		}
		if hA.Network().Connectedness(hB.ID()) == network.Connected {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if hA.Network().Connectedness(hB.ID()) != network.Connected {
		t.Fatal("A and B failed to connect")
	}

	stopA := InstallCatalogIBLT(ctx, hA, stackA, CatalogIBLTInterval(100*time.Millisecond))
	defer stopA()
	stopB := InstallCatalogIBLT(ctx, hB, stackB, CatalogIBLTInterval(100*time.Millisecond))
	defer stopB()

	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		has, err := stackB.Blockstore.Has(ctx, c)
		if err != nil {
			continue
		}
		if has {
			got, err := mystore.GetBlockByCID(ctx, stackB.BlockSvc, c)
			if err != nil {
				t.Fatalf("GetBlock: %v", err)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("got %q, want %q", got, payload)
			}
			return
		}
	}
	t.Fatal("B did not receive block via IBLT catalog sync within timeout")
}
