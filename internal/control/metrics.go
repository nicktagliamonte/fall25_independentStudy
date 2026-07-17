// Purpose: Node metrics container and snapshot for /metrics endpoint.

package control

import "sync/atomic"

// NodeMetrics holds process-wide, atomically-updated counters for a single
// node's networking and restore activity. All fields are int64 and must be
// read/written only through the atomic.AddInt64/LoadInt64-based methods on
// this type (IncDialsAttempted, AddRestoreBytes, Snapshot, etc.) rather than
// direct field access, since NodeMetrics is shared across goroutines (e.g.
// the HTTP handlers in server.go and background dialing/gossip code
// elsewhere in the node) without any additional locking. A zero-value
// NodeMetrics (e.g. &NodeMetrics{}) is ready to use, with all counters
// starting at 0.
type NodeMetrics struct {
	// DialsAttempted counts every outbound connection attempt made.
	DialsAttempted int64
	// DialsSucceeded counts outbound connection attempts that succeeded.
	DialsSucceeded int64
	// DialsFailed counts outbound connection attempts that failed.
	DialsFailed int64
	// PeersPruned counts peer records removed from the local peer store
	// (e.g. due to expiry or excessive failures).
	PeersPruned int64
	// GossipLearned counts peer records learned via gossip/peer-exchange.
	GossipLearned int64
	// RestoresStarted counts /restore jobs that have been created.
	RestoresStarted int64
	// RestoresOK counts individual block restores (per-CID) that
	// succeeded across all restore jobs.
	RestoresOK int64
	// RestoresFailed counts individual block restores (per-CID) that
	// failed (decode error or fetch error) across all restore jobs.
	RestoresFailed int64
	// RestoreBytes accumulates the total number of bytes successfully
	// fetched by restore jobs.
	RestoreBytes int64
}

// MetricsSnapshot is a point-in-time, JSON-serializable copy of a
// NodeMetrics' counter values. It is the exact response body returned by
// the /metrics HTTP endpoint (see Start in server.go), with each field
// tagged for the corresponding snake_case JSON key.
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

// IncDialsAttempted atomically increments DialsAttempted by 1.
func (m *NodeMetrics) IncDialsAttempted() { atomic.AddInt64(&m.DialsAttempted, 1) }

// IncDialsSucceeded atomically increments DialsSucceeded by 1.
func (m *NodeMetrics) IncDialsSucceeded() { atomic.AddInt64(&m.DialsSucceeded, 1) }

// IncDialsFailed atomically increments DialsFailed by 1.
func (m *NodeMetrics) IncDialsFailed() { atomic.AddInt64(&m.DialsFailed, 1) }

// AddPeersPruned atomically adds n to PeersPruned. Values of n <= 0 are
// ignored (no-op), so this counter is monotonically non-decreasing.
func (m *NodeMetrics) AddPeersPruned(n int) {
	if n > 0 {
		atomic.AddInt64(&m.PeersPruned, int64(n))
	}
}

// AddGossipLearned atomically adds n to GossipLearned. Values of n <= 0 are
// ignored (no-op), so this counter is monotonically non-decreasing.
func (m *NodeMetrics) AddGossipLearned(n int) {
	if n > 0 {
		atomic.AddInt64(&m.GossipLearned, int64(n))
	}
}

// IncRestoresStarted atomically increments RestoresStarted by 1. Called
// once per accepted POST /restore request (see Start in server.go).
func (m *NodeMetrics) IncRestoresStarted() { atomic.AddInt64(&m.RestoresStarted, 1) }

// AddRestoresOK atomically adds n to RestoresOK. Values of n <= 0 are
// ignored (no-op).
func (m *NodeMetrics) AddRestoresOK(n int) {
	if n > 0 {
		atomic.AddInt64(&m.RestoresOK, int64(n))
	}
}

// AddRestoresFailed atomically adds n to RestoresFailed. Values of n <= 0
// are ignored (no-op).
func (m *NodeMetrics) AddRestoresFailed(n int) {
	if n > 0 {
		atomic.AddInt64(&m.RestoresFailed, int64(n))
	}
}

// AddRestoreBytes atomically adds n to RestoreBytes. Values of n <= 0 are
// ignored (no-op).
func (m *NodeMetrics) AddRestoreBytes(n int64) {
	if n > 0 {
		atomic.AddInt64(&m.RestoreBytes, n)
	}
}

// Snapshot atomically loads every counter on m and returns them as a
// MetricsSnapshot. Because each field is loaded independently (rather than
// under a single lock), the result is not a perfectly atomic snapshot
// across all fields if counters are being updated concurrently, but each
// individual value is race-free.
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
