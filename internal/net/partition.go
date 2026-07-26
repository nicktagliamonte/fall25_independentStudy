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
	// PrevCount is the sampled peer/neighbor count before the drop.
	PrevCount int
	// NowCount is the sampled peer/neighbor count after the drop.
	NowCount int
	// Kind identifies the detector that produced the event: PartitionEventConnectivity
	// or PartitionEventDHTNeighbors.
	Kind string
}

const (
	// PartitionEventConnectivity marks a PartitionEvent produced by PeerConnectivityMonitor
	// (based on libp2p host connection count).
	PartitionEventConnectivity = "connectivity"
	// PartitionEventDHTNeighbors marks a PartitionEvent produced by DHTNeighborMonitor
	// (based on DHT routing table k-bucket size).
	PartitionEventDHTNeighbors = "dht_neighbors"
)

// OnPartitionEvent is the callback type for partition events. Upper layers register
// this to react to partition detection.
type OnPartitionEvent func(PartitionEvent)

// partitionMonitorCore holds the sampling/threshold/callback logic shared by
// PeerConnectivityMonitor and DHTNeighborMonitor. The two monitors differ only
// in where their peer/neighbor count comes from (host connection count vs.
// DHT k-bucket size); countFunc abstracts that away to a plain func() int so
// this type can implement the sampling loop, drop-percentage threshold check,
// and recovery/partition-event dispatch exactly once. kind is the
// PartitionEvent.Kind value this core reports (PartitionEventConnectivity or
// PartitionEventDHTNeighbors).
type partitionMonitorCore struct {
	countFunc        func() int
	kind             string
	interval         time.Duration
	minPeers         int
	dropPct          int
	onThreshold      func(prev, now int)
	onPartitionEvent OnPartitionEvent
	onRecovery       func()
	mu               sync.Mutex
	prev             int
}

// newPartitionMonitorCore creates a core with the default interval (10s),
// minPeers (2), and dropPct (50), sampling via countFunc and tagging emitted
// PartitionEvents with kind.
//
// Parameters:
//   - countFunc (func() int): returns the current peer/neighbor count when called.
//   - kind (string): the PartitionEvent.Kind to attach to emitted events.
//
// Returns:
//   - *partitionMonitorCore: a configured, unstarted core.
func newPartitionMonitorCore(countFunc func() int, kind string) *partitionMonitorCore {
	return &partitionMonitorCore{
		countFunc: countFunc,
		kind:      kind,
		interval:  10 * time.Second,
		minPeers:  2,
		dropPct:   50,
	}
}

// Start runs the monitoring loop: it records the initial count, then on each
// tick of the configured interval compares the new count against the previous
// sample. A drop meeting both minPeers and dropPct thresholds triggers
// onThreshold and onPartitionEvent (tagged with kind); an increase over the
// previous sample triggers onRecovery. Stops when ctx is done; intended to be
// run in its own goroutine.
//
// Parameters:
//   - ctx (context.Context): cancelling ctx stops the monitoring loop.
func (c *partitionMonitorCore) Start(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	c.mu.Lock()
	c.prev = c.countFunc()
	c.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := c.countFunc()
			c.mu.Lock()
			prev := c.prev
			c.prev = now
			c.mu.Unlock()
			if prev >= c.minPeers && now < prev {
				drop := prev - now
				pct := 0
				if prev > 0 {
					pct = (drop * 100) / prev
				}
				if pct >= c.dropPct {
					if c.onThreshold != nil {
						c.onThreshold(prev, now)
					}
					if c.onPartitionEvent != nil {
						c.onPartitionEvent(PartitionEvent{PrevCount: prev, NowCount: now, Kind: c.kind})
					}
				}
			} else if now > prev && c.onRecovery != nil {
				c.onRecovery()
			}
		}
	}
}

// PeerConnectivityMonitor samples connected peer count and detects sudden loss (partition).
// Uses host.Network() only. No Phase 2 dependencies. A drop is significant when the
// previous count was at least minPeers and the percentage lost in one sampling
// interval is at least dropPct; an increase after a drop is treated as recovery.
type PeerConnectivityMonitor struct {
	h    host.Host
	core *partitionMonitorCore
}

// PartitionMonitorOption configures PeerConnectivityMonitor.
type PartitionMonitorOption func(*PeerConnectivityMonitor)

// PartitionMonitorInterval sets the sampling interval. Default 10s.
//
// Parameters:
//   - d (time.Duration): the interval between connectivity samples.
//
// Returns:
//   - PartitionMonitorOption: an option that applies the interval to a PeerConnectivityMonitor.
func PartitionMonitorInterval(d time.Duration) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) { m.core.interval = d }
}

// PartitionMonitorMinPeers sets the minimum peer count before we consider drops significant.
// If prev < minPeers, we do not emit. Default 2.
//
// Parameters:
//   - n (int): the minimum previous peer count required before a drop is evaluated.
//
// Returns:
//   - PartitionMonitorOption: an option that applies the threshold to a PeerConnectivityMonitor.
func PartitionMonitorMinPeers(n int) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) { m.core.minPeers = n }
}

// PartitionMonitorDropPct sets the drop threshold as a percentage (0-100).
// Emit when we lose at least this percentage in one sample. Default 50. Values
// outside [0, 100] are clamped.
//
// Parameters:
//   - pct (int): the drop percentage threshold; clamped to [0, 100].
//
// Returns:
//   - PartitionMonitorOption: an option that applies the threshold to a PeerConnectivityMonitor.
func PartitionMonitorDropPct(pct int) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) {
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		m.core.dropPct = pct
	}
}

// PartitionMonitorOnDrop sets the callback invoked when partition (significant drop) is detected.
//
// Parameters:
//   - fn (func(prev, now int)): callback receiving the peer count before and after the drop.
//
// Returns:
//   - PartitionMonitorOption: an option that registers the callback on a PeerConnectivityMonitor.
func PartitionMonitorOnDrop(fn func(prev, now int)) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) { m.core.onThreshold = fn }
}

// PartitionMonitorOnPartitionEvent sets the callback for partition events (for upper layers).
//
// Parameters:
//   - fn (OnPartitionEvent): callback receiving the PartitionEvent describing the drop.
//
// Returns:
//   - PartitionMonitorOption: an option that registers the callback on a PeerConnectivityMonitor.
func PartitionMonitorOnPartitionEvent(fn OnPartitionEvent) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) { m.core.onPartitionEvent = fn }
}

// PartitionMonitorOnRecovery sets the callback when peer count increases (post-heal).
//
// Parameters:
//   - fn (func()): callback invoked when the sampled peer count rises above the previous sample.
//
// Returns:
//   - PartitionMonitorOption: an option that registers the callback on a PeerConnectivityMonitor.
func PartitionMonitorOnRecovery(fn func()) PartitionMonitorOption {
	return func(m *PeerConnectivityMonitor) { m.core.onRecovery = fn }
}

// NewPeerConnectivityMonitor creates a monitor for the given host, applying defaults
// (10s interval, minPeers 2, dropPct 50) before applying opts.
//
// Parameters:
//   - h (host.Host): the libp2p host whose connections are sampled.
//   - opts (...PartitionMonitorOption): functional options overriding defaults.
//
// Returns:
//   - *PeerConnectivityMonitor: a configured, unstarted monitor.
func NewPeerConnectivityMonitor(h host.Host, opts ...PartitionMonitorOption) *PeerConnectivityMonitor {
	m := &PeerConnectivityMonitor{h: h}
	m.core = newPartitionMonitorCore(m.connectedCount, PartitionEventConnectivity)
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// connectedCount returns the number of connected peers (excluding self).
//
// Returns:
//   - int: count of peers currently in network.Connected state, excluding the local host's own ID.
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
//
// Returns:
//   - int: the current number of connected peers, excluding self.
func (m *PeerConnectivityMonitor) ConnectedCount() int {
	return m.connectedCount()
}

// Start runs the monitoring loop: it records the initial peer count, then on each
// tick of the configured interval compares the new count against the previous
// sample. A drop meeting both minPeers and dropPct thresholds triggers onDrop and
// onPartitionEvent (kind PartitionEventConnectivity); an increase over the previous
// sample triggers onRecovery. Stops when ctx is done; intended to be run in its own
// goroutine.
//
// Parameters:
//   - ctx (context.Context): cancelling ctx stops the monitoring loop.
func (m *PeerConnectivityMonitor) Start(ctx context.Context) {
	m.core.Start(ctx)
}

// PeerLastSeen holds a k-bucket peer and its last-seen timestamp.
type PeerLastSeen struct {
	// Peer is the DHT routing table peer's ID.
	Peer peer.ID
	// LastSeen is the most recent activity timestamp recorded for Peer.
	LastSeen time.Time
}

// KBucketLastSeenTracker exposes last-seen timestamps for DHT k-bucket peers.
// Uses dht.RoutingTable().GetPeerInfos() only. No Phase 2 dependencies.
type KBucketLastSeenTracker struct {
	dht *kaddht.IpfsDHT
}

// NewKBucketLastSeenTracker creates a tracker for the given DHT.
//
// Parameters:
//   - dht (*kaddht.IpfsDHT): the DHT whose routing table is queried; may be nil (Snapshot/LastSeen then return empty results).
//
// Returns:
//   - *KBucketLastSeenTracker: a tracker wrapping dht.
func NewKBucketLastSeenTracker(dht *kaddht.IpfsDHT) *KBucketLastSeenTracker {
	return &KBucketLastSeenTracker{dht: dht}
}

// Snapshot returns all k-bucket peers with their last-seen timestamp.
// LastSeen is the later of LastUsefulAt and LastSuccessfulOutboundQueryAt from the routing table.
//
// Returns:
//   - []PeerLastSeen: one entry per routing-table peer; nil if the tracker's DHT or routing table is nil.
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
// Implemented as a linear scan over Snapshot(); intended for occasional lookups, not hot paths.
//
// Parameters:
//   - p (peer.ID): the peer to look up.
//
// Returns:
//   - time.Time: the peer's last-seen timestamp, or the zero value if not found.
//   - bool: true if p is present in the current k-bucket snapshot.
func (t *KBucketLastSeenTracker) LastSeen(p peer.ID) (time.Time, bool) {
	for _, ps := range t.Snapshot() {
		if ps.Peer == p {
			return ps.LastSeen, true
		}
	}
	return time.Time{}, false
}

// DHTNeighborMonitor samples DHT k-bucket peer count and detects sudden loss.
// Uses dht.RoutingTable() only. No Phase 2 dependencies. Threshold semantics mirror
// PeerConnectivityMonitor: a drop is significant when the previous count was at
// least minPeers and the percentage lost is at least dropPct.
type DHTNeighborMonitor struct {
	dht  *kaddht.IpfsDHT
	core *partitionMonitorCore
}

// DHTNeighborMonitorOption configures DHTNeighborMonitor.
type DHTNeighborMonitorOption func(*DHTNeighborMonitor)

// DHTNeighborMonitorInterval sets the sampling interval. Default 10s.
//
// Parameters:
//   - d (time.Duration): the interval between neighbor-count samples.
//
// Returns:
//   - DHTNeighborMonitorOption: an option that applies the interval to a DHTNeighborMonitor.
func DHTNeighborMonitorInterval(d time.Duration) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) { m.core.interval = d }
}

// DHTNeighborMonitorMinPeers sets the minimum neighbor count before we consider drops significant.
//
// Parameters:
//   - n (int): the minimum previous neighbor count required before a drop is evaluated.
//
// Returns:
//   - DHTNeighborMonitorOption: an option that applies the threshold to a DHTNeighborMonitor.
func DHTNeighborMonitorMinPeers(n int) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) { m.core.minPeers = n }
}

// DHTNeighborMonitorDropPct sets the drop threshold as a percentage (0-100). Values
// outside [0, 100] are clamped.
//
// Parameters:
//   - pct (int): the drop percentage threshold; clamped to [0, 100].
//
// Returns:
//   - DHTNeighborMonitorOption: an option that applies the threshold to a DHTNeighborMonitor.
func DHTNeighborMonitorDropPct(pct int) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) {
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		m.core.dropPct = pct
	}
}

// DHTNeighborMonitorOnLoss sets the callback invoked when sudden loss is detected.
//
// Parameters:
//   - fn (func(prev, now int)): callback receiving the neighbor count before and after the drop.
//
// Returns:
//   - DHTNeighborMonitorOption: an option that registers the callback on a DHTNeighborMonitor.
func DHTNeighborMonitorOnLoss(fn func(prev, now int)) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) { m.core.onThreshold = fn }
}

// DHTNeighborMonitorOnPartitionEvent sets the callback for partition events (for upper layers).
//
// Parameters:
//   - fn (OnPartitionEvent): callback receiving the PartitionEvent describing the drop.
//
// Returns:
//   - DHTNeighborMonitorOption: an option that registers the callback on a DHTNeighborMonitor.
func DHTNeighborMonitorOnPartitionEvent(fn OnPartitionEvent) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) { m.core.onPartitionEvent = fn }
}

// DHTNeighborMonitorOnRecovery sets the callback when neighbor count increases (post-heal).
//
// Parameters:
//   - fn (func()): callback invoked when the sampled neighbor count rises above the previous sample.
//
// Returns:
//   - DHTNeighborMonitorOption: an option that registers the callback on a DHTNeighborMonitor.
func DHTNeighborMonitorOnRecovery(fn func()) DHTNeighborMonitorOption {
	return func(m *DHTNeighborMonitor) { m.core.onRecovery = fn }
}

// NewDHTNeighborMonitor creates a monitor for the given DHT, applying defaults
// (10s interval, minPeers 2, dropPct 50) before applying opts.
//
// Parameters:
//   - dht (*kaddht.IpfsDHT): the DHT whose routing table is sampled.
//   - opts (...DHTNeighborMonitorOption): functional options overriding defaults.
//
// Returns:
//   - *DHTNeighborMonitor: a configured, unstarted monitor.
func NewDHTNeighborMonitor(dht *kaddht.IpfsDHT, opts ...DHTNeighborMonitorOption) *DHTNeighborMonitor {
	m := &DHTNeighborMonitor{dht: dht}
	m.core = newPartitionMonitorCore(m.neighborCount, PartitionEventDHTNeighbors)
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// neighborCount returns the number of peers in the DHT routing table.
//
// Returns:
//   - int: the routing table's current peer count, or 0 if the DHT or its routing table is nil.
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
//
// Returns:
//   - int: the current DHT routing table peer count.
func (m *DHTNeighborMonitor) NeighborCount() int {
	return m.neighborCount()
}

// Start runs the monitoring loop: it records the initial neighbor count, then on
// each tick of the configured interval compares the new count against the previous
// sample. A drop meeting both minPeers and dropPct thresholds triggers onLoss and
// onPartitionEvent (kind PartitionEventDHTNeighbors); an increase over the previous
// sample triggers onRecovery. Stops when ctx is done; intended to be run in its own
// goroutine.
//
// Parameters:
//   - ctx (context.Context): cancelling ctx stops the monitoring loop.
func (m *DHTNeighborMonitor) Start(ctx context.Context) {
	m.core.Start(ctx)
}
