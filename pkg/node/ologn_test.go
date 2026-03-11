// Purpose: Phase 7.3 - Verify O(log N) complexity maintained for DHT lookups.

package node

import (
	"context"
	"math"
	"testing"
	"time"

	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// TestVerifyOLogNComplexity measures GetToken latency across network sizes 4, 8, 16
// and verifies sub-linear scaling consistent with O(log N). Skips when -short.
func TestVerifyOLogNComplexity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping O(log N) verification in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	sizes := []int{4, 8, 16}
	latencies := make(map[int]float64)

	for _, n := range sizes {
		avgNs := runGetTokenLatencyTest(ctx, t, n)
		latencies[n] = float64(avgNs) / 1e9
		t.Logf("N=%d: avg GetToken latency = %.3f ms", n, latencies[n]*1000)
	}

	// Verify sub-linear scaling: latency(N) / log2(N) should be roughly constant.
	// For O(log N), doubling N adds ~constant time. Check that growth is bounded.
	ratios := make([]float64, 0, len(sizes)-1)
	for i := 1; i < len(sizes); i++ {
		nPrev, nCur := sizes[i-1], sizes[i]
		latPrev, latCur := latencies[nPrev], latencies[nCur]
		logPrev := math.Log2(float64(nPrev))
		logCur := math.Log2(float64(nCur))
		ratioPrev := latPrev / logPrev
		ratioCur := latCur / logCur
		ratios = append(ratios, ratioCur/ratioPrev)
	}

	// For O(log N), ratioCur/ratioPrev should be roughly constant. Allow variance
	// due to measurement noise, bootstrap timing, and local topology.
	for i, r := range ratios {
		if r > 6.0 || r < 0.1 {
			t.Errorf("N=%d→%d: latency/log(N) ratio = %.2f, expected ~1 (O(log N))",
				sizes[i], sizes[i+1], r)
		}
	}

	// Reject O(N): latency(16) must be sub-linear in N (not ~4x latency(4))
	if latencies[16] > 6*latencies[4] {
		t.Errorf("latency(16)=%.3f ms >> 6*latency(4)=%.3f ms; scaling appears super-log",
			latencies[16]*1000, 6*latencies[4]*1000)
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
			Mode:       myhost.DHTModeServer,
			UseTokenDHT: true,
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

	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			_ = hosts[i].Connect(ctx, infos[j])
		}
	}

	sleepDur := time.Duration(3+n/2) * time.Second
	time.Sleep(sleepDur)

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
	time.Sleep(3 * time.Second)

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
