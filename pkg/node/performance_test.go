// Purpose: Phase 7.3 performance benchmarks. Token routing (GetToken) is primary; provider benchmark is legacy.

package node

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"

	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// BenchmarkTokenRoutingLatency measures GetToken lookup time (token routing path).
// Two nodes bootstrap; A stores token; B measures GetToken latency over b.N iterations.
func BenchmarkTokenRoutingLatency(b *testing.B) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	hA, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		b.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		b.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	infoA := peer.AddrInfo{ID: hA.ID(), Addrs: hA.Addrs()}
	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}

	if err := hA.Connect(ctx, infoB); err != nil {
		b.Fatalf("Connect A→B: %v", err)
	}
	if err := hB.Connect(ctx, infoA); err != nil {
		b.Fatalf("Connect B→A: %v", err)
	}

	dhtCfgA := myhost.DHTConfig{
		Mode:       myhost.DHTModeServer,
		UseTokenDHT: true,
		BootstrapPeersFunc: func() []peer.AddrInfo { return []peer.AddrInfo{infoB} },
	}
	dhtA, err := myhost.NewDHT(ctx, hA, dhtCfgA)
	if err != nil {
		b.Fatalf("NewDHT A: %v", err)
	}
	defer dhtA.Close()

	dhtCfgB := myhost.DHTConfig{
		Mode:       myhost.DHTModeServer,
		UseTokenDHT: true,
		BootstrapPeersFunc: func() []peer.AddrInfo { return []peer.AddrInfo{infoA} },
	}
	dhtB, err := myhost.NewDHT(ctx, hB, dhtCfgB)
	if err != nil {
		b.Fatalf("NewDHT B: %v", err)
	}
	defer dhtB.Close()

	time.Sleep(5 * time.Second)

	payload := []byte("performance benchmark token routing payload")
	key := mystore.KeyFromData(payload)
	addrs := hA.Addrs()
	if len(addrs) == 0 {
		b.Fatal("host A has no addresses")
	}
	token := mystore.Token{
		Key:       key,
		Locations: []mystore.Location{{ProviderID: hA.ID(), Address: addrs[0], RTT: 0}},
		Timestamp: time.Now().UnixNano(),
		Version:   1,
	}
	if err := mystore.PutToken(ctx, dhtA, key, token); err != nil {
		b.Fatalf("PutToken: %v", err)
	}
	time.Sleep(5 * time.Second)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := mystore.GetToken(ctx, dhtB, key)
		if err != nil {
			b.Fatalf("GetToken: %v", err)
		}
	}
}

// BenchmarkProviderAnnouncementLatency measures Router.Provide() time (legacy; token routing is primary).
// Two nodes bootstrap; A measures Provide latency over b.N iterations.
func BenchmarkProviderAnnouncementLatency(b *testing.B) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	hA, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		b.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		b.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	infoA := peer.AddrInfo{ID: hA.ID(), Addrs: hA.Addrs()}
	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}

	if err := hA.Connect(ctx, infoB); err != nil {
		b.Fatalf("Connect A→B: %v", err)
	}
	if err := hB.Connect(ctx, infoA); err != nil {
		b.Fatalf("Connect B→A: %v", err)
	}

	dhtCfgA := myhost.DHTConfig{
		Mode:       myhost.DHTModeServer,
		UseTokenDHT: true,
		BootstrapPeersFunc: func() []peer.AddrInfo { return []peer.AddrInfo{infoB} },
	}
	dhtA, err := myhost.NewDHT(ctx, hA, dhtCfgA)
	if err != nil {
		b.Fatalf("NewDHT A: %v", err)
	}
	defer dhtA.Close()

	dhtCfgB := myhost.DHTConfig{
		Mode:       myhost.DHTModeServer,
		UseTokenDHT: true,
		BootstrapPeersFunc: func() []peer.AddrInfo { return []peer.AddrInfo{infoA} },
	}
	dhtB, err := myhost.NewDHT(ctx, hB, dhtCfgB)
	if err != nil {
		b.Fatalf("NewDHT B: %v", err)
	}
	defer dhtB.Close()

	time.Sleep(5 * time.Second)

	blk := []byte("performance benchmark provider announcement payload")
	c, err := cid.Prefix{Version: 1, Codec: 0x55}.Sum(blk)
	if err != nil {
		b.Fatalf("create block identifier: %v", err)
	}

	router := ctrl.NewFallbackContentRouter(dhtA, ctrl.NewDynamicRouter())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := router.Provide(ctx, c, true); err != nil {
			b.Fatalf("Provide: %v", err)
		}
	}
}
