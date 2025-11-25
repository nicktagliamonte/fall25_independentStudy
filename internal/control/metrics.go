// Purpose: Node metrics container and snapshot for /metrics endpoint.

package control

import "sync/atomic"

type NodeMetrics struct {
	DialsAttempted  int64
	DialsSucceeded  int64
	DialsFailed     int64
	PeersPruned     int64
	GossipLearned   int64
	RestoresStarted int64
	RestoresOK      int64
	RestoresFailed  int64
	RestoreBytes    int64
}

type MetricsSnapshot struct {
	DialsAttempted  int64 `json:"dials_attempted"`
	DialsSucceeded  int64 `json:"dials_succeeded"`
	DialsFailed     int64 `json:"dials_failed"`
	PeersPruned     int64 `json:"peers_pruned"`
	GossipLearned   int64 `json:"gossip_learned"`
	RestoresStarted int64 `json:"restores_started"`
	RestoresOK      int64 `json:"restores_ok"`
	RestoresFailed  int64 `json:"restores_failed"`
	RestoreBytes    int64 `json:"restore_bytes"`
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

func (m *NodeMetrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		DialsAttempted:  atomic.LoadInt64(&m.DialsAttempted),
		DialsSucceeded:  atomic.LoadInt64(&m.DialsSucceeded),
		DialsFailed:     atomic.LoadInt64(&m.DialsFailed),
		PeersPruned:     atomic.LoadInt64(&m.PeersPruned),
		GossipLearned:   atomic.LoadInt64(&m.GossipLearned),
		RestoresStarted: atomic.LoadInt64(&m.RestoresStarted),
		RestoresOK:      atomic.LoadInt64(&m.RestoresOK),
		RestoresFailed:  atomic.LoadInt64(&m.RestoresFailed),
		RestoreBytes:    atomic.LoadInt64(&m.RestoreBytes),
	}
}
