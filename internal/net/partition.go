// Purpose: Partition detection via peer connectivity monitoring (Phase 5.1).

package net

import (
	"context"
	"sync"
	"time"

	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PartitionEvent is emitted for upper layers when partition is detected.
type PartitionEvent struct {
	PrevCount int
	NowCount  int
	Kind      string
}

const (
	PartitionEventConnectivity  = "connectivity"
	PartitionEventDHTNeighbors  = "dht_neighbors"
)

// OnPartitionEvent is the callback type for partition events. Upper layers register
// this to react to partition detection.
type OnPartitionEvent func(PartitionEvent)

// PeerConnectivityMonitor samples connected peer count and detects sudden loss (partition).
// Uses host.Network() only. No Phase 2 dependencies.
type PeerConnectivityMonitor struct {
	h               host.Host
	interval        time.Duration
	minPeers        int
	dropPct         int
	onDrop          func(prev, now int)
	onPartitionEvent OnPartitionEvent
	onRecovery      func()
	mu              sync.Mutex
	prev            int
}

// PartitionMonitorOption configures PeerConnectivityMonitor.
type PartitionMonitorOption func(*PeerConnectivityMonitor)

// PartitionMonitorInterval sets the sampling interval. Default 10s.
func PartitionMonitorInterval(d time.Duration) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) { m.interval = d }
}

// PartitionMonitorMinPeers sets the minimum peer count before we consider drops significant.
// If prev < minPeers, we do not emit. Default 2.
func PartitionMonitorMinPeers(n int) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) { m.minPeers = n }
}

// PartitionMonitorDropPct sets the drop threshold as a percentage (0-100).
// Emit when we lose at least this percentage in one sample. Default 50.
func PartitionMonitorDropPct(pct int) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) {
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		m.dropPct = pct
	}
}

// PartitionMonitorOnDrop sets the callback invoked when partition (significant drop) is detected.
func PartitionMonitorOnDrop(fn func(prev, now int)) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) { m.onDrop = fn }
}

// PartitionMonitorOnPartitionEvent sets the callback for partition events (for upper layers).
func PartitionMonitorOnPartitionEvent(fn OnPartitionEvent) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) { m.onPartitionEvent = fn }
}

// PartitionMonitorOnRecovery sets the callback when peer count increases (post-heal).
func PartitionMonitorOnRecovery(fn func()) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) { m.onRecovery = fn }
}

// NewPeerConnectivityMonitor creates a monitor for the given host.
func NewPeerConnectivityMonitor(h host.Host, opts ...PartitionMonitorOption) *PeerConnectivityMonitor {
	m := &PeerConnectivityMonitor{
		h:        h,
		interval: 10 * time.Second,
		minPeers: 2,
		dropPct:  50,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// connectedCount returns the number of connected peers (excluding self).
func (m *PeerConnectivityMonitor) connectedCount() int {
	peers := m.h.Network().Peers()
	n := 0
	for _, pid := range peers {
		if pid == m.h.ID() {
			continue
		}
		if m.h.Network().Connectedness(pid) == network.Connected {
			n++
		}
	}
	return n
}

// ConnectedCount returns the current connected peer count. Safe to call from any goroutine.
func (m *PeerConnectivityMonitor) ConnectedCount() int {
	return m.connectedCount()
}

// Start runs the monitoring loop. Stops when ctx is done.
func (m *PeerConnectivityMonitor) Start(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	m.mu.Lock()
	m.prev = m.connectedCount()
	m.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := m.connectedCount()
			m.mu.Lock()
			prev := m.prev
			m.prev = now
			m.mu.Unlock()
			if prev >= m.minPeers && now < prev {
				drop := prev - now
				pct := 0
				if prev > 0 {
					pct = (drop * 100) / prev
				}
				if pct >= m.dropPct {
					if m.onDrop != nil {
						m.onDrop(prev, now)
					}
					if m.onPartitionEvent != nil {
						m.onPartitionEvent(PartitionEvent{PrevCount: prev, NowCount: now, Kind: PartitionEventConnectivity})
					}
				}
			} else if now > prev && m.onRecovery != nil {
				m.onRecovery()
			}
		}
	}
}

// PeerLastSeen holds a k-bucket peer and its last-seen timestamp.
type PeerLastSeen struct {
	Peer     peer.ID
	LastSeen time.Time
}

// KBucketLastSeenTracker exposes last-seen timestamps for DHT k-bucket peers.
// Uses dht.RoutingTable().GetPeerInfos() only. No Phase 2 dependencies.
type KBucketLastSeenTracker struct {
	dht *kaddht.IpfsDHT
}

// NewKBucketLastSeenTracker creates a tracker for the given DHT.
func NewKBucketLastSeenTracker(dht *kaddht.IpfsDHT) *KBucketLastSeenTracker {
	return &KBucketLastSeenTracker{dht: dht}
}

// Snapshot returns all k-bucket peers with their last-seen timestamp.
// LastSeen is the later of LastUsefulAt and LastSuccessfulOutboundQueryAt from the routing table.
func (t *KBucketLastSeenTracker) Snapshot() []PeerLastSeen {
	if t.dht == nil {
		return nil
	}
	rt := t.dht.RoutingTable()
	if rt == nil {
		return nil
	}
	infos := rt.GetPeerInfos()
	out := make([]PeerLastSeen, 0, len(infos))
	for _, pi := range infos {
		last := pi.LastUsefulAt
		if pi.LastSuccessfulOutboundQueryAt.After(last) {
			last = pi.LastSuccessfulOutboundQueryAt
		}
		out = append(out, PeerLastSeen{Peer: pi.Id, LastSeen: last})
	}
	return out
}

// LastSeen returns the last-seen time for the given peer, or zero and false if not in k-bucket.
func (t *KBucketLastSeenTracker) LastSeen(p peer.ID) (time.Time, bool) {
	for _, ps := range t.Snapshot() {
		if ps.Peer == p {
			return ps.LastSeen, true
		}
	}
	return time.Time{}, false
}

// DHTNeighborMonitor samples DHT k-bucket peer count and detects sudden loss.
// Uses dht.RoutingTable() only. No Phase 2 dependencies.
type DHTNeighborMonitor struct {
	dht              *kaddht.IpfsDHT
	interval         time.Duration
	minPeers         int
	dropPct          int
	onLoss           func(prev, now int)
	onPartitionEvent OnPartitionEvent
	onRecovery       func()
	mu               sync.Mutex
	prev             int
}

// DHTNeighborMonitorOption configures DHTNeighborMonitor.
type DHTNeighborMonitorOption func(*DHTNeighborMonitor)

// DHTNeighborMonitorInterval sets the sampling interval. Default 10s.
func DHTNeighborMonitorInterval(d time.Duration) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) { m.interval = d }
}

// DHTNeighborMonitorMinPeers sets the minimum neighbor count before we consider drops significant.
func DHTNeighborMonitorMinPeers(n int) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) { m.minPeers = n }
}

// DHTNeighborMonitorDropPct sets the drop threshold as a percentage (0-100).
func DHTNeighborMonitorDropPct(pct int) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) {
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		m.dropPct = pct
	}
}

// DHTNeighborMonitorOnLoss sets the callback invoked when sudden loss is detected.
func DHTNeighborMonitorOnLoss(fn func(prev, now int)) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) { m.onLoss = fn }
}

// DHTNeighborMonitorOnPartitionEvent sets the callback for partition events (for upper layers).
func DHTNeighborMonitorOnPartitionEvent(fn OnPartitionEvent) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) { m.onPartitionEvent = fn }
}

// DHTNeighborMonitorOnRecovery sets the callback when neighbor count increases (post-heal).
func DHTNeighborMonitorOnRecovery(fn func()) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) { m.onRecovery = fn }
}

// NewDHTNeighborMonitor creates a monitor for the given DHT.
func NewDHTNeighborMonitor(dht *kaddht.IpfsDHT, opts ...DHTNeighborMonitorOption) *DHTNeighborMonitor {
	m := &DHTNeighborMonitor{
		dht:      dht,
		interval: 10 * time.Second,
		minPeers: 2,
		dropPct:  50,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// neighborCount returns the number of peers in the DHT routing table.
func (m *DHTNeighborMonitor) neighborCount() int {
	if m.dht == nil {
		return 0
	}
	rt := m.dht.RoutingTable()
	if rt == nil {
		return 0
	}
	return rt.Size()
}

// NeighborCount returns the current DHT neighbor count. Safe to call from any goroutine.
func (m *DHTNeighborMonitor) NeighborCount() int {
	return m.neighborCount()
}

// Start runs the monitoring loop. Stops when ctx is done.
func (m *DHTNeighborMonitor) Start(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	m.mu.Lock()
	m.prev = m.neighborCount()
	m.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := m.neighborCount()
			m.mu.Lock()
			prev := m.prev
			m.prev = now
			m.mu.Unlock()
			if prev >= m.minPeers && now < prev {
				drop := prev - now
				pct := 0
				if prev > 0 {
					pct = (drop * 100) / prev
				}
				if pct >= m.dropPct {
					if m.onLoss != nil {
						m.onLoss(prev, now)
					}
					if m.onPartitionEvent != nil {
						m.onPartitionEvent(PartitionEvent{PrevCount: prev, NowCount: now, Kind: PartitionEventDHTNeighbors})
					}
				}
			} else if now > prev && m.onRecovery != nil {
				m.onRecovery()
			}
		}
	}
}
