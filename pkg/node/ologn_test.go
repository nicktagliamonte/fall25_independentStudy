// Purpose: Phase 7.3 - Verify O(log N) complexity maintained for DHT lookups.

package node

import (
	"context"
	"testing"
	"time"

	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// TestDHTLookupAcrossNetworkSizes verifies token lookup at network sizes 4, 8,
// and 16 and logs non-normative local latency. Wall-clock ratios on one host,
// with warm caches and scheduler contention, cannot prove asymptotic
// complexity; routing-work experiments provide that evidence separately.
func TestDHTLookupAcrossNetworkSizes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-size DHT lookup test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	sizes := []int{4, 8, 16}
	for _, n := range sizes {
		avgNs := runGetTokenLatencyTest(ctx, t, n)
		t.Logf("N=%d: warm local-host GetToken mean = %.3f ms", n, float64(avgNs)/1e6)
	}
}

func runGetTokenLatencyTest(ctx context.Context, t *testing.T, n int) int64 {
	hosts := make([]host.Host, n)
	stacks := make([]*mystore.Stack, n)
	dhts := make([]*kaddht.IpfsDHT, n)

	for i := 0; i < n; i++ {
		h, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
		if err != nil {
			t.Fatalf("NewHost[%d]: %v", i, err)
		}
		hosts[i] = h
		defer h.Close()
	}

	infos := make([]peer.AddrInfo, n)
	for i := 0; i < n; i++ {
		infos[i] = peer.AddrInfo{ID: hosts[i].ID(), Addrs: hosts[i].Addrs()}
	}

	for i := 0; i < n; i++ {
		bootstrapPeers := []peer.AddrInfo{infos[0]}
		if i == 0 && n > 1 {
			bootstrapPeers = []peer.AddrInfo{infos[1]}
		}
		bs, dstore := mystore.NewEphemeralBlockstore()
		dhtCfg := myhost.DHTConfig{
			Mode:               myhost.DHTModeServer,
			UseTokenDHT:        true,
			BootstrapPeersFunc: func() []peer.AddrInfo { return bootstrapPeers },
		}
		d, err := myhost.NewDHT(ctx, hosts[i], dhtCfg)
		if err != nil {
			t.Fatalf("NewDHT[%d]: %v", i, err)
		}
		dhts[i] = d
		defer d.Close()

		dr := ctrl.NewDynamicRouter()
		router := ctrl.NewFallbackContentRouter(d, dr)
		stack, err := mystore.NewStackFromBlockstore(ctx, hosts[i], bs, dstore, router)
		if err != nil {
			t.Fatalf("NewStack[%d]: %v", i, err)
		}
		stack.DHT = d
		stacks[i] = stack
		defer stack.Close()
	}

	for i := 1; i < n; i++ {
		connectAndAwaitTestDHT(t, ctx, hosts[0], hosts[i], dhts[0], dhts[i])
	}

	payload := []byte("o(log n) complexity verification test payload")
	key := mystore.KeyFromData(payload)
	addrs := hosts[0].Addrs()
	if len(addrs) == 0 {
		t.Fatal("host 0 has no addresses")
	}
	token := mystore.Token{
		Key:       key,
		Locations: []mystore.Location{{ProviderID: hosts[0].ID(), Address: addrs[0], RTT: 0}},
		Timestamp: time.Now().UnixNano(),
		Version:   1,
	}
	if err := mystore.PutToken(ctx, dhts[0], key, token); err != nil {
		t.Fatalf("PutToken: %v", err)
	}
	awaitTestToken(t, ctx, dhts[n-1], key, 1)

	runs := 8
	var totalNs int64
	readerIdx := n - 1
	for i := 0; i < runs; i++ {
		start := time.Now()
		_, err := mystore.GetToken(ctx, dhts[readerIdx], key)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("GetToken (N=%d, run %d): %v", n, i, err)
		}
		totalNs += elapsed.Nanoseconds()
	}
	return totalNs / int64(runs)
}
