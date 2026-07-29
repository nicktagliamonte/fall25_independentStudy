// Purpose: Integration tests for put on node A, get on node B via key-based token routing.

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

func TestPutGetViaDHT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

	buildStack := func(h host.Host, other peer.AddrInfo, bs bstore.Blockstore, datastore ds.Batching) (*mystore.Stack, *kaddht.IpfsDHT, error) {
		dhtCfg := myhost.DHTConfig{
			Mode:        myhost.DHTModeServer,
			UseTokenDHT: true,
			BootstrapPeersFunc: func() []peer.AddrInfo {
				if other.ID == h.ID() {
					return nil
				}
				return []peer.AddrInfo{other}
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
		stack.DHT = d
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

	connectAndAwaitTestDHT(t, ctx, hA, hB, dhtA, dhtB)

	hA.SetStreamHandler(mystore.DirectFetchProtocolID, func(stream network.Stream) {
		_ = mystore.HandleDirectFetchStream(stream, stackA)
	})

	payload := []byte("integration test payload via DHT")
	key, c, err := stackA.PutBlock(ctx, payload)
	if err != nil {
		t.Fatalf("PutBlock: %v", err)
	}
	stackA.UpdateRoutingTableOnPut(key, hA.ID(), nil, c)

	awaitTestToken(t, ctx, dhtB, key, 1)

	got, _, err := stackB.GetBlock(ctx, key)
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

// TestPartitionAndRecovery verifies partition behavior (isolated node cannot discover via token)
// and recovery (after connecting, content is discoverable via token routing).
func TestPartitionAndRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

	hC, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost C: %v", err)
	}
	defer hC.Close()

	infoA := peer.AddrInfo{ID: hA.ID(), Addrs: hA.Addrs()}
	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}

	buildStack := func(h host.Host, bootstrapPeers []peer.AddrInfo, bs bstore.Blockstore, datastore ds.Batching) (*mystore.Stack, *kaddht.IpfsDHT, error) {
		dhtCfg := myhost.DHTConfig{
			Mode:        myhost.DHTModeServer,
			UseTokenDHT: true,
			BootstrapPeersFunc: func() []peer.AddrInfo {
				var out []peer.AddrInfo
				for _, p := range bootstrapPeers {
					if p.ID != h.ID() {
						out = append(out, p)
					}
				}
				return out
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
		stack.DHT = d
		return stack, d, nil
	}

	bsA, dsA := mystore.NewEphemeralBlockstore()
	bsB, dsB := mystore.NewEphemeralBlockstore()
	bsC, dsC := mystore.NewEphemeralBlockstore()

	stackA, dhtA, err := buildStack(hA, []peer.AddrInfo{infoB}, bsA, dsA)
	if err != nil {
		t.Fatalf("buildStack A: %v", err)
	}
	defer dhtA.Close()
	defer stackA.Close()

	stackB, dhtB, err := buildStack(hB, []peer.AddrInfo{infoA}, bsB, dsB)
	if err != nil {
		t.Fatalf("buildStack B: %v", err)
	}
	defer dhtB.Close()
	defer stackB.Close()

	stackC, dhtC, err := buildStack(hC, nil, bsC, dsC)
	if err != nil {
		t.Fatalf("buildStack C: %v", err)
	}
	defer dhtC.Close()
	defer stackC.Close()

	hA.SetStreamHandler(mystore.DirectFetchProtocolID, func(stream network.Stream) {
		_ = mystore.HandleDirectFetchStream(stream, stackA)
	})
	hB.SetStreamHandler(mystore.DirectFetchProtocolID, func(stream network.Stream) {
		_ = mystore.HandleDirectFetchStream(stream, stackB)
	})

	connectAndAwaitTestDHT(t, ctx, hA, hB, dhtA, dhtB)

	payload := []byte("partition recovery test")
	key, c, err := stackA.PutBlock(ctx, payload)
	if err != nil {
		t.Fatalf("PutBlock: %v", err)
	}
	stackA.UpdateRoutingTableOnPut(key, hA.ID(), nil, c)

	awaitTestToken(t, ctx, dhtB, key, 1)

	gotB, _, err := stackB.GetBlock(ctx, key)
	if err != nil {
		t.Fatalf("B GetBlock: %v", err)
	}
	if !bytes.Equal(gotB, payload) {
		t.Errorf("B got %q, want %q", gotB, payload)
	}

	_, _, err = stackC.GetBlock(ctx, key)
	if err == nil {
		t.Fatal("C (partitioned) should fail to get before recovery")
	}

	if err := hC.Connect(ctx, infoB); err != nil {
		t.Fatalf("recovery connect C to B: %v", err)
	}
	connectAndAwaitTestDHT(t, ctx, hC, hB, dhtC, dhtB)

	gotC, _, err := stackC.GetBlock(ctx, key)
	if err != nil {
		t.Fatalf("C GetBlock after recovery: %v", err)
	}
	if !bytes.Equal(gotC, payload) {
		t.Errorf("C got %q, want %q", gotC, payload)
	}
}
