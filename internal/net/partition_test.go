// Purpose: Tests for partition detection via peer connectivity monitoring.

package net

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestPeerConnectivityMonitor_ConnectedCount(t *testing.T) {
	ctx := context.Background()
	h, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	defer h.Close()
	m := NewPeerConnectivityMonitor(h)
	if m.ConnectedCount() != 0 {
		t.Errorf("empty host: want 0, got %d", m.ConnectedCount())
	}
}

func TestPeerConnectivityMonitor_DetectsDrop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	hA, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}
	if err := hA.Connect(ctx, infoB); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if hA.Network().Connectedness(hB.ID()) != network.Connected {
		t.Fatal("expected connected")
	}

	dropped := make(chan struct{}, 1)
	events := make(chan PartitionEvent, 1)
	m := NewPeerConnectivityMonitor(hA,
		PartitionMonitorInterval(100*time.Millisecond),
		PartitionMonitorMinPeers(1),
		PartitionMonitorDropPct(50),
		PartitionMonitorOnDrop(func(prev, now int) {
			select {
			case dropped <- struct{}{}:
			default:
			}
		}),
		PartitionMonitorOnPartitionEvent(func(e PartitionEvent) {
			select {
			case events <- e:
			default:
			}
		}))
	go m.Start(ctx)

	if m.ConnectedCount() < 1 {
		t.Errorf("after connect: want >= 1, got %d", m.ConnectedCount())
	}

	time.Sleep(250 * time.Millisecond)
	hA.Network().ClosePeer(hB.ID())

	select {
	case <-dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("OnDrop not called within 5s")
	}
	select {
	case e := <-events:
		if e.Kind != PartitionEventConnectivity || e.PrevCount != 1 || e.NowCount != 0 {
			t.Errorf("partition event: want Kind=connectivity Prev=1 Now=0, got %+v", e)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnPartitionEvent not called")
	}
}

func TestPeerConnectivityMonitor_OnRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hA, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	recovered := make(chan struct{}, 1)
	m := NewPeerConnectivityMonitor(hA,
		PartitionMonitorInterval(100*time.Millisecond),
		PartitionMonitorMinPeers(1),
		PartitionMonitorOnRecovery(func() { recovered <- struct{}{} }))
	go m.Start(ctx)

	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}
	if err := hA.Connect(ctx, infoB); err != nil {
		t.Fatalf("connect: %v", err)
	}

	select {
	case <-recovered:
	case <-time.After(3 * time.Second):
		t.Fatal("OnRecovery not called after connect")
	}
}

func TestKBucketLastSeenTracker_NilDHT(t *testing.T) {
	tr := NewKBucketLastSeenTracker(nil)
	if tr.Snapshot() != nil {
		t.Error("Snapshot with nil DHT should return nil")
	}
	if _, ok := tr.LastSeen(peer.ID("")); ok {
		t.Error("LastSeen with nil DHT should return false")
	}
}

func TestKBucketLastSeenTracker_EmptyRoutingTable(t *testing.T) {
	ctx := context.Background()
	h, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	defer h.Close()
	dht, err := NewDHT(ctx, h, DHTConfig{
		Mode: DHTModeServer,
		BootstrapPeersFunc: func() []peer.AddrInfo { return nil },
	})
	if err != nil {
		t.Fatalf("NewDHT: %v", err)
	}
	defer dht.Close()
	tr := NewKBucketLastSeenTracker(dht)
	snap := tr.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot should not be nil for valid DHT")
	}
	if len(snap) != 0 {
		t.Errorf("empty routing table: want 0 peers, got %d", len(snap))
	}
}

func TestDHTNeighborMonitor_NilDHT(t *testing.T) {
	m := NewDHTNeighborMonitor(nil)
	if m.NeighborCount() != 0 {
		t.Errorf("nil DHT: want 0, got %d", m.NeighborCount())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	m.Start(ctx)
}

func TestDHTNeighborMonitor_EmptyDHT(t *testing.T) {
	ctx := context.Background()
	h, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	defer h.Close()
	dht, err := NewDHT(ctx, h, DHTConfig{
		Mode: DHTModeServer,
		BootstrapPeersFunc: func() []peer.AddrInfo { return nil },
	})
	if err != nil {
		t.Fatalf("NewDHT: %v", err)
	}
	defer dht.Close()
	mon := NewDHTNeighborMonitor(dht)
	if mon.NeighborCount() != 0 {
		t.Errorf("empty DHT: want 0 neighbors, got %d", mon.NeighborCount())
	}
	ctx2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	mon.Start(ctx2)
}
