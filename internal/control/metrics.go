// Purpose: Node metrics container and snapshot for /metrics endpoint.

package control

import "sync/atomic"

type NodeMetrics struct {
	DialsAttempted int64
	DialsSucceeded int64
	DialsFailed    int64
	PeersPruned    int64
	GossipLearned  int64
}

type MetricsSnapshot struct {
	DialsAttempted int64 `json:"dials_attempted"`
	DialsSucceeded int64 `json:"dials_succeeded"`
	DialsFailed    int64 `json:"dials_failed"`
	PeersPruned    int64 `json:"peers_pruned"`
	GossipLearned  int64 `json:"gossip_learned"`
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

func (m *NodeMetrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		DialsAttempted: atomic.LoadInt64(&m.DialsAttempted),
		DialsSucceeded: atomic.LoadInt64(&m.DialsSucceeded),
		DialsFailed:    atomic.LoadInt64(&m.DialsFailed),
		PeersPruned:    atomic.LoadInt64(&m.PeersPruned),
		GossipLearned:  atomic.LoadInt64(&m.GossipLearned),
	}
}
