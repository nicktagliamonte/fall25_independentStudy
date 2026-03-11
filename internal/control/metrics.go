// Purpose: Node metrics container and snapshot for /metrics endpoint.

package control

import (
	"sync/atomic"

	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

type NodeMetrics struct {
	DialsAttempted             int64
	DialsSucceeded             int64
	DialsFailed                int64
	PeersPruned                int64
	GossipLearned              int64
	RestoresStarted            int64
	RestoresOK                 int64
	RestoresFailed             int64
	RestoreBytes               int64
	DHTBootstrapPeers          int64
	ProviderAnnounceCount      int64
	ProviderDiscoveryLatencyNs int64
	ProviderRecordsCount       int64
	PutMessagesIn              int64
	PutMessagesOut             int64
	GetMessagesIn              int64
	GetMessagesOut             int64
	LookupMessagesIn           int64
	LookupMessagesOut          int64
	LookupHopsLast             int64
	LookupHopsCount            int64
	LookupHopsSum              int64
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
	ProviderAnnounceCount      int64 `json:"provider_announce_count"`
	ProviderDiscoveryLatencyNs  int64 `json:"provider_discovery_latency_ns"`
	ProviderRecordsCount       int64 `json:"provider_records_count"`
	PutMessagesIn              int64 `json:"put_messages_in"`
	PutMessagesOut             int64 `json:"put_messages_out"`
	GetMessagesIn              int64 `json:"get_messages_in"`
	GetMessagesOut             int64 `json:"get_messages_out"`
	LookupMessagesIn           int64 `json:"lookup_messages_in"`
	LookupMessagesOut          int64 `json:"lookup_messages_out"`
	LookupHopsLast             int64 `json:"lookup_hops_last"`
	LookupHopsCount            int64 `json:"lookup_hops_count"`
	LookupHopsSum              int64 `json:"lookup_hops_sum"`
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
func (m *NodeMetrics) SetProviderRecordsCount(n int64) { atomic.StoreInt64(&m.ProviderRecordsCount, n) }

func (m *NodeMetrics) AddPutMessagesIn(n int) {
	if n > 0 {
		atomic.AddInt64(&m.PutMessagesIn, int64(n))
	}
}
func (m *NodeMetrics) AddPutMessagesOut(n int) {
	if n > 0 {
		atomic.AddInt64(&m.PutMessagesOut, int64(n))
	}
}
func (m *NodeMetrics) AddGetMessagesIn(n int) {
	if n > 0 {
		atomic.AddInt64(&m.GetMessagesIn, int64(n))
	}
}
func (m *NodeMetrics) AddGetMessagesOut(n int) {
	if n > 0 {
		atomic.AddInt64(&m.GetMessagesOut, int64(n))
	}
}
func (m *NodeMetrics) AddLookupMessagesIn(n int) {
	if n > 0 {
		atomic.AddInt64(&m.LookupMessagesIn, int64(n))
	}
}
func (m *NodeMetrics) AddLookupMessagesOut(n int) {
	if n > 0 {
		atomic.AddInt64(&m.LookupMessagesOut, int64(n))
	}
}

func (m *NodeMetrics) AddLookupHops(n int) {
	if n >= 0 {
		atomic.StoreInt64(&m.LookupHopsLast, int64(n))
		atomic.AddInt64(&m.LookupHopsCount, 1)
		atomic.AddInt64(&m.LookupHopsSum, int64(n))
	}
}

// NodeMetricsProviderSink returns a ProviderMetricsSink that forwards to m, or nil if m is nil.
func NodeMetricsProviderSink(m *NodeMetrics) mystore.ProviderMetricsSink {
	if m == nil {
		return nil
	}
	return &nodeMetricsSink{m: m}
}

type nodeMetricsSink struct{ m *NodeMetrics }

func (s *nodeMetricsSink) SetProviderRecordsCount(n int) { s.m.SetProviderRecordsCount(int64(n)) }

// NodeMetricsMessageSink returns a MessageMetricsSink that forwards to m, or nil if m is nil.
func NodeMetricsMessageSink(m *NodeMetrics) mystore.MessageMetricsSink {
	if m == nil {
		return nil
	}
	return &nodeMessageMetricsSink{m: m}
}

type nodeMessageMetricsSink struct{ m *NodeMetrics }

func (s *nodeMessageMetricsSink) AddPutMessagesIn(n int)    { s.m.AddPutMessagesIn(n) }
func (s *nodeMessageMetricsSink) AddPutMessagesOut(n int)   { s.m.AddPutMessagesOut(n) }
func (s *nodeMessageMetricsSink) AddGetMessagesIn(n int)    { s.m.AddGetMessagesIn(n) }
func (s *nodeMessageMetricsSink) AddGetMessagesOut(n int)   { s.m.AddGetMessagesOut(n) }
func (s *nodeMessageMetricsSink) AddLookupMessagesIn(n int) { s.m.AddLookupMessagesIn(n) }
func (s *nodeMessageMetricsSink) AddLookupMessagesOut(n int) { s.m.AddLookupMessagesOut(n) }

// NodeMetricsHopSink returns a NetworkHopsSink that forwards to m, or nil if m is nil.
func NodeMetricsHopSink(m *NodeMetrics) mystore.NetworkHopsSink {
	if m == nil {
		return nil
	}
	return &nodeHopSink{m: m}
}

type nodeHopSink struct{ m *NodeMetrics }

func (s *nodeHopSink) AddLookupHops(n int) { s.m.AddLookupHops(n) }

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
		PutMessagesIn:              atomic.LoadInt64(&m.PutMessagesIn),
		PutMessagesOut:             atomic.LoadInt64(&m.PutMessagesOut),
		GetMessagesIn:              atomic.LoadInt64(&m.GetMessagesIn),
		GetMessagesOut:             atomic.LoadInt64(&m.GetMessagesOut),
		LookupMessagesIn:           atomic.LoadInt64(&m.LookupMessagesIn),
		LookupMessagesOut:          atomic.LoadInt64(&m.LookupMessagesOut),
		LookupHopsLast:             atomic.LoadInt64(&m.LookupHopsLast),
		LookupHopsCount:            atomic.LoadInt64(&m.LookupHopsCount),
		LookupHopsSum:              atomic.LoadInt64(&m.LookupHopsSum),
	}
}
