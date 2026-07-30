package node

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/pht"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

type integrationFixedOwner struct {
	mu    sync.RWMutex
	owner peer.ID
}

func (r *integrationFixedOwner) ResolveTupleOwner(context.Context, string) (peer.ID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.owner, nil
}

func (r *integrationFixedOwner) set(owner peer.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owner = owner
}

func TestFencedIndexMutationAcrossLiveDHT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	hA, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	defer hA.Close()
	hB, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	defer hB.Close()

	infoA := peer.AddrInfo{ID: hA.ID(), Addrs: hA.Addrs()}
	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}
	dhtA, err := myhost.NewDHT(ctx, hA, myhost.DHTConfig{
		Mode:           myhost.DHTModeServer,
		UseTokenDHT:    true,
		BootstrapPeers: []peer.AddrInfo{infoB},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dhtA.Close()
	dhtB, err := myhost.NewDHT(ctx, hB, myhost.DHTConfig{
		Mode:           myhost.DHTModeServer,
		UseTokenDHT:    true,
		BootstrapPeers: []peer.AddrInfo{infoA},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dhtB.Close()
	connectAndAwaitTestDHT(t, ctx, hA, hB, dhtA, dhtB)

	storesA, err := pht.NewShardStores(tuplespace.NewDHTValueStoreAdapter(dhtA), 2)
	if err != nil {
		t.Fatal(err)
	}
	storesB, err := pht.NewShardStores(tuplespace.NewDHTValueStoreAdapter(dhtB), 2)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &integrationFixedOwner{owner: hA.ID()}
	owner, err := tuplespace.NewIndexCoordinator(hA, resolver, storesA)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	client, err := tuplespace.NewIndexCoordinator(hB, resolver, storesB)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	owner.SetAuthorityTiming(20*time.Millisecond, 2*time.Second, 100*time.Millisecond)
	client.SetAuthorityTiming(20*time.Millisecond, 2*time.Second, 100*time.Millisecond)

	const entries = 24
	var wg sync.WaitGroup
	errs := make(chan error, entries)
	for i := 0; i < entries; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- client.Insert(ctx, fmt.Sprintf("task:live-dht:%03d", i))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	resolver.set(hB.ID())
	time.Sleep(2200 * time.Millisecond)
	if err := client.Insert(ctx, "task:live-dht:after-failover"); err != nil {
		t.Fatalf("insert after authority failover: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		var parts [][]string
		for _, store := range storesB {
			rows, queryErr := pht.PrefixQueryDHT(ctx, store, "task:live-dht:")
			if queryErr == nil {
				parts = append(parts, rows)
			}
		}
		if got := len(pht.CombineResults(parts...)); got == entries+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live DHT index did not converge to %d entries", entries+1)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snapshot := client.Snapshot(); snapshot.Failures != 0 ||
		snapshot.AuthorityClaims < 2 ||
		snapshot.AuthorityTransitions == 0 {
		t.Fatalf("client mutation snapshot = %+v", snapshot)
	}
}
