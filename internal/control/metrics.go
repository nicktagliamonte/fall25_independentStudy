// Purpose: Node metrics container and snapshot for /metrics endpoint.

package control

import (
	"sync/atomic"

	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

type NodeMetrics struct {
	DialsAttempted            int64
	DialsSucceeded            int64
	DialsFailed               int64
	PeersPruned               int64
	GossipLearned             int64
	RestoresStarted           int64
	RestoresOK                int64
	RestoresFailed            int64
	RestoreBytes              int64
	DHTBootstrapPeers         int64
	ProviderAnnounceCount     int64
	ProviderDiscoveryLatencyNs int64
	ProviderRecordsCount      int64
}

type MetricsSnapshot struct {
	DialsAttempted            int64 `json:"dials_attempted"`
	DialsSucceeded            int64 `json:"dials_succeeded"`
	DialsFailed               int64 `json:"dials_failed"`
	PeersPruned               int64 `json:"peers_pruned"`
	GossipLearned             int64 `json:"gossip_learned"`
	RestoresStarted           int64 `json:"restores_started"`
	RestoresOK                int64 `json:"restores_ok"`
	RestoresFailed            int64 `json:"restores_failed"`
	RestoreBytes              int64 `json:"restore_bytes"`
	DHTBootstrapPeers         int64 `json:"dht_bootstrap_peers"`
	ProviderAnnounceCount     int64 `json:"provider_announce_count"`
	ProviderDiscoveryLatencyNs int64 `json:"provider_discovery_latency_ns"`
	ProviderRecordsCount      int64 `json:"provider_records_count"`
}

func (m *NodeMetrics) IncDialsAttempted() { atomic.AddInt64(&m.DialsAttempted, 1) }
func (m *NodeMetrics) IncDialsSucceeded() { atomic.AddInt64(&m.DialsSucceeded, 1) }
func (m *NodeMetrics) IncDialsFailed()    { atomic.AddInt64(&m.DialsFailed, 1) }
func (m *NodeMetrics) AddPeersPruned(n int) {
	if n > 0 {
		atomic.AddInt64(&m.PeersPruned, int64(n))
	}
}
func (m *NodeMetrics) AddGossipLearned(n int) {
	if n > 0 {
		atomic.AddInt64(&m.GossipLearned, int64(n))
	}
}

func (m *NodeMetrics) IncRestoresStarted() { atomic.AddInt64(&m.RestoresStarted, 1) }
func (m *NodeMetrics) AddRestoresOK(n int) {
	if n > 0 {
		atomic.AddInt64(&m.RestoresOK, int64(n))
	}
}
func (m *NodeMetrics) AddRestoresFailed(n int) {
	if n > 0 {
		atomic.AddInt64(&m.RestoresFailed, int64(n))
	}
}
func (m *NodeMetrics) AddRestoreBytes(n int64) {
	if n > 0 {
		atomic.AddInt64(&m.RestoreBytes, n)
	}
}

func (m *NodeMetrics) SetDHTBootstrapPeers(n int64)        { atomic.StoreInt64(&m.DHTBootstrapPeers, n) }
func (m *NodeMetrics) IncProviderAnnounceCount()          { atomic.AddInt64(&m.ProviderAnnounceCount, 1) }
func (m *NodeMetrics) SetProviderDiscoveryLatencyNs(n int64) { atomic.StoreInt64(&m.ProviderDiscoveryLatencyNs, n) }
func (m *NodeMetrics) SetProviderRecordsCount(n int64)    { atomic.StoreInt64(&m.ProviderRecordsCount, n) }

// NodeMetricsProviderSink returns a ProviderMetricsSink that forwards to m, or nil if m is nil.
func NodeMetricsProviderSink(m *NodeMetrics) mystore.ProviderMetricsSink {
	if m == nil {
		return nil
	}
	return &nodeMetricsSink{m: m}
}

type nodeMetricsSink struct{ m *NodeMetrics }

func (s *nodeMetricsSink) IncAnnounceCount()             { s.m.IncProviderAnnounceCount() }
func (s *nodeMetricsSink) SetProviderRecordsCount(n int) { s.m.SetProviderRecordsCount(int64(n)) }

func (m *NodeMetrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		DialsAttempted:             atomic.LoadInt64(&m.DialsAttempted),
		DialsSucceeded:             atomic.LoadInt64(&m.DialsSucceeded),
		DialsFailed:                atomic.LoadInt64(&m.DialsFailed),
		PeersPruned:                atomic.LoadInt64(&m.PeersPruned),
		GossipLearned:              atomic.LoadInt64(&m.GossipLearned),
		RestoresStarted:            atomic.LoadInt64(&m.RestoresStarted),
		RestoresOK:                 atomic.LoadInt64(&m.RestoresOK),
		RestoresFailed:             atomic.LoadInt64(&m.RestoresFailed),
		RestoreBytes:               atomic.LoadInt64(&m.RestoreBytes),
		DHTBootstrapPeers:          atomic.LoadInt64(&m.DHTBootstrapPeers),
		ProviderAnnounceCount:      atomic.LoadInt64(&m.ProviderAnnounceCount),
		ProviderDiscoveryLatencyNs: atomic.LoadInt64(&m.ProviderDiscoveryLatencyNs),
		ProviderRecordsCount:       atomic.LoadInt64(&m.ProviderRecordsCount),
	}
}
