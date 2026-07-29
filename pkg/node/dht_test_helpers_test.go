package node

import (
	"context"
	"testing"
	"time"

	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// connectAndAwaitTestDHT replaces timing-based sleeps with an observable
// readiness condition: both hosts are connected, both DHTs have been
// bootstrapped after their protocols exist, and both routing tables contain a
// peer.
func connectAndAwaitTestDHT(
	t *testing.T,
	ctx context.Context,
	hA, hB host.Host,
	dhtA, dhtB *kaddht.IpfsDHT,
) {
	t.Helper()
	infoA := peer.AddrInfo{ID: hA.ID(), Addrs: hA.Addrs()}
	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}
	deadline := time.Now().Add(10 * time.Second)
	var lastConnectErr error
	for len(hA.Network().Peers()) == 0 || len(hB.Network().Peers()) == 0 {
		lastConnectErr = hA.Connect(ctx, infoB)
		if len(hA.Network().Peers()) == 0 || len(hB.Network().Peers()) == 0 {
			lastConnectErr = hB.Connect(ctx, infoA)
		}
		if len(hA.Network().Peers()) > 0 && len(hB.Network().Peers()) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect %s <-> %s: %v", hA.ID(), hB.ID(), lastConnectErr)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context ended awaiting connection: %v", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	if err := dhtA.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap DHT A: %v", err)
	}
	if err := dhtB.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap DHT B: %v", err)
	}

	deadline = time.Now().Add(10 * time.Second)
	for {
		if dhtA.RoutingTable().Size() > 0 && dhtB.RoutingTable().Size() > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"DHT routing tables not ready: A=%d B=%d connected=(%d,%d)",
				dhtA.RoutingTable().Size(),
				dhtB.RoutingTable().Size(),
				len(hA.Network().Peers()),
				len(hB.Network().Peers()),
			)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context ended awaiting DHT readiness: %v", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func awaitTestToken(
	t *testing.T,
	ctx context.Context,
	dht *kaddht.IpfsDHT,
	key mystore.Key,
	minLocations int,
) mystore.Token {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for {
		token, err := mystore.GetToken(ctx, dht, key)
		if err == nil && len(token.Locations) >= minLocations {
			return token
		}
		lastErr = err
		if time.Now().After(deadline) {
			t.Fatalf(
				"token not ready: locations=%d want>=%d last_error=%v",
				len(token.Locations),
				minLocations,
				lastErr,
			)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context ended awaiting token: %v", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}
