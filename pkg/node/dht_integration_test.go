// Purpose: Integration tests for put on node A, get on node B via DHT.

package node

import (
	"bytes"
	"context"
	"testing"
	"time"

	bstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	kaddht "github.com/libp2p/go-libp2p-kad-dht"

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
			Mode: myhost.DHTModeServer,
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

	payload := []byte("integration test payload via DHT")
	c, err := mystore.PutRawBlockIndexed(ctx, stackA.Datastore, stackA.BlockSvc, payload)
	if err != nil {
		t.Fatalf("PutRawBlockIndexed: %v", err)
	}
	stackA.AnnounceProvider(ctx, c)

	time.Sleep(2 * time.Second)

	ctxProv, cancelProv := context.WithTimeout(ctx, 15*time.Second)
	provCh := stackB.Router.FindProvidersAsync(ctxProv, c, 5)
	var foundA bool
	for p := range provCh {
		if p.ID == hA.ID() {
			foundA = true
			break
		}
	}
	cancelProv()
	if !foundA {
		t.Fatal("DHT FindProviders did not return node A as provider; cannot verify Get")
	}

	got, err := mystore.GetBlockIndexed(ctx, stackB.Datastore, stackB.BlockSvc, c)
	if err != nil {
		t.Fatalf("GetBlockIndexed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

// TestPartitionAndRecovery verifies partition behavior (isolated node cannot discover providers)
// and recovery (after connecting, content is discoverable).
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
			Mode: myhost.DHTModeServer,
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

	stackA.ProviderRecords = mystore.NewLocalProviderRecords()

	payload := []byte("partition recovery test")
	c, err := mystore.PutRawBlockIndexed(ctx, stackA.Datastore, stackA.BlockSvc, payload)
	if err != nil {
		t.Fatalf("PutRawBlockIndexed: %v", err)
	}
	stackA.AnnounceProvider(ctx, c)

	time.Sleep(2 * time.Second)

	ctxProv, cancelProv := context.WithTimeout(ctx, 15*time.Second)
	provCh := stackB.Router.FindProvidersAsync(ctxProv, c, 5)
	var foundAFromB bool
	for p := range provCh {
		if p.ID == hA.ID() {
			foundAFromB = true
			break
		}
	}
	cancelProv()
	if !foundAFromB {
		t.Fatal("B (same partition as A) should find A as provider")
	}

	ctxProv2, cancelProv2 := context.WithTimeout(ctx, 5*time.Second)
	provChC := stackC.Router.FindProvidersAsync(ctxProv2, c, 5)
	var foundFromC bool
	for range provChC {
		foundFromC = true
		break
	}
	cancelProv2()
	if foundFromC {
		t.Fatal("C (partitioned) should NOT find providers before recovery")
	}

	if err := hC.Connect(ctx, infoB); err != nil {
		t.Fatalf("recovery connect C to B: %v", err)
	}

	time.Sleep(2 * time.Second)

	ctxProv3, cancelProv3 := context.WithTimeout(ctx, 15*time.Second)
	provChC2 := stackC.Router.FindProvidersAsync(ctxProv3, c, 5)
	var foundAfterRecovery bool
	for p := range provChC2 {
		if p.ID == hA.ID() || p.ID == hB.ID() {
			foundAfterRecovery = true
			break
		}
	}
	cancelProv3()
	if !foundAfterRecovery {
		t.Fatal("C should find providers after recovery (connect to B)")
	}

	got, err := mystore.GetBlockIndexed(ctx, stackC.Datastore, stackC.BlockSvc, c)
	if err != nil {
		t.Fatalf("GetBlockIndexed after recovery: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}
