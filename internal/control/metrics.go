// Purpose: Node metrics container and snapshot for /metrics endpoint.

package control

import (
	"sync/atomic"

	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// NodeMetrics is a live, concurrency-safe counter set for a running node. All
// fields are updated exclusively through the atomic sync/atomic operations on
// the methods below (never by direct field assignment from other packages),
// so a NodeMetrics value can be read and written concurrently from HTTP
// handlers, background goroutines, and the DHT/storage stack. Snapshot
// produces a point-in-time, JSON-serializable copy for the /metrics endpoint.
type NodeMetrics struct {
	// DialsAttempted counts outbound peer dial attempts.
	DialsAttempted int64
	// DialsSucceeded counts outbound peer dials that succeeded.
	DialsSucceeded int64
	// DialsFailed counts outbound peer dials that failed.
	DialsFailed int64
	// PeersPruned counts peers removed from the peer store (e.g. by staleness/failure policy).
	PeersPruned int64
	// GossipLearned counts peer records learned via gossip.
	GossipLearned int64
	// RestoresStarted counts /restore jobs started.
	RestoresStarted int64
	// RestoresOK counts individual block restores that succeeded across all /restore jobs.
	RestoresOK int64
	// RestoresFailed counts individual block restores that failed across all /restore jobs.
	RestoresFailed int64
	// RestoreBytes sums the byte size of successfully restored blocks.
	RestoreBytes int64
	// DHTBootstrapPeers records the current count of DHT bootstrap peers (a gauge, not a monotonic counter).
	DHTBootstrapPeers int64
	// ProviderAnnounceCount counts provider announcements made (stack.OnAnnounce callback).
	ProviderAnnounceCount int64
	// ProviderDiscoveryLatencyNs records the latency (nanoseconds) of the most recent /get provider-discovery step (a gauge, set by SetProviderDiscoveryLatencyNs).
	ProviderDiscoveryLatencyNs int64
	// ProviderRecordsCount records the current number of entries in the stack's ProviderRecords table (a gauge).
	ProviderRecordsCount int64
	// PutMessagesIn counts inbound put-related protocol messages.
	PutMessagesIn int64
	// PutMessagesOut counts outbound put-related protocol messages.
	PutMessagesOut int64
	// GetMessagesIn counts inbound get-related protocol messages (incremented once per /get request).
	GetMessagesIn int64
	// GetMessagesOut counts outbound get-related protocol messages (e.g. per DirectFetch attempt in fetchBlockFromToken).
	GetMessagesOut int64
	// LookupMessagesIn counts inbound lookup-related protocol messages.
	LookupMessagesIn int64
	// LookupMessagesOut counts outbound lookup-related protocol messages.
	LookupMessagesOut int64
	// LookupHopsLast is the network_hops value from the most recently completed lookup (a gauge).
	LookupHopsLast int64
	// LookupHopsCount counts how many lookups have contributed to LookupHopsSum (for computing an average).
	LookupHopsCount int64
	// LookupHopsSum accumulates network_hops across all recorded lookups.
	LookupHopsSum int64
}

// MetricsSnapshot is the JSON-serializable, point-in-time copy of a
// NodeMetrics produced by NodeMetrics.Snapshot; it is what the /metrics
// endpoint returns. Fields mirror NodeMetrics one-to-one (see there for field
// semantics) but are plain (non-atomic) int64 values safe to encode directly.
type MetricsSnapshot struct {
	DialsAttempted             int64 `json:"dials_attempted"`
	DialsSucceeded             int64 `json:"dials_succeeded"`
	DialsFailed                int64 `json:"dials_failed"`
	PeersPruned                int64 `json:"peers_pruned"`
	GossipLearned              int64 `json:"gossip_learned"`
	RestoresStarted            int64 `json:"restores_started"`
	RestoresOK                 int64 `json:"restores_ok"`
	RestoresFailed             int64 `json:"restores_failed"`
	RestoreBytes               int64 `json:"restore_bytes"`
	DHTBootstrapPeers          int64 `json:"dht_bootstrap_peers"`
	ProviderAnnounceCount      int64 `json:"provider_announce_count"`
	ProviderDiscoveryLatencyNs int64 `json:"provider_discovery_latency_ns"`
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

// IncDialsAttempted atomically increments DialsAttempted by 1.
func (m *NodeMetrics) IncDialsAttempted() { atomic.AddInt64(&m.DialsAttempted, 1) }

// IncDialsSucceeded atomically increments DialsSucceeded by 1.
func (m *NodeMetrics) IncDialsSucceeded() { atomic.AddInt64(&m.DialsSucceeded, 1) }

// IncDialsFailed atomically increments DialsFailed by 1.
func (m *NodeMetrics) IncDialsFailed() { atomic.AddInt64(&m.DialsFailed, 1) }

// AddPeersPruned atomically adds n to PeersPruned.
//
// Parameters:
//   - n (int): number of peers pruned; values <= 0 are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddPeersPruned(n int) {
	if n > 0 {
		atomic.AddInt64(&m.PeersPruned, int64(n))
	}
}

// AddGossipLearned atomically adds n to GossipLearned.
//
// Parameters:
//   - n (int): number of gossip-learned peer records; values <= 0 are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddGossipLearned(n int) {
	if n > 0 {
		atomic.AddInt64(&m.GossipLearned, int64(n))
	}
}

// IncRestoresStarted atomically increments RestoresStarted by 1.
func (m *NodeMetrics) IncRestoresStarted() { atomic.AddInt64(&m.RestoresStarted, 1) }

// AddRestoresOK atomically adds n to RestoresOK.
//
// Parameters:
//   - n (int): number of successfully restored blocks; values <= 0 are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddRestoresOK(n int) {
	if n > 0 {
		atomic.AddInt64(&m.RestoresOK, int64(n))
	}
}

// AddRestoresFailed atomically adds n to RestoresFailed.
//
// Parameters:
//   - n (int): number of failed block restores; values <= 0 are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddRestoresFailed(n int) {
	if n > 0 {
		atomic.AddInt64(&m.RestoresFailed, int64(n))
	}
}

// AddRestoreBytes atomically adds n to RestoreBytes.
//
// Parameters:
//   - n (int64): number of bytes successfully restored; values <= 0 are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddRestoreBytes(n int64) {
	if n > 0 {
		atomic.AddInt64(&m.RestoreBytes, n)
	}
}

// SetDHTBootstrapPeers atomically overwrites DHTBootstrapPeers with n (a gauge, not a running total).
//
// Parameters:
//   - n (int64): the current DHT bootstrap peer count.
//
// Returns: (none)
func (m *NodeMetrics) SetDHTBootstrapPeers(n int64) { atomic.StoreInt64(&m.DHTBootstrapPeers, n) }

// IncProviderAnnounceCount atomically increments ProviderAnnounceCount by 1.
func (m *NodeMetrics) IncProviderAnnounceCount() { atomic.AddInt64(&m.ProviderAnnounceCount, 1) }

// SetProviderDiscoveryLatencyNs atomically overwrites ProviderDiscoveryLatencyNs with n (a gauge).
//
// Parameters:
//   - n (int64): latency of the most recent provider-discovery step, in nanoseconds.
//
// Returns: (none)
func (m *NodeMetrics) SetProviderDiscoveryLatencyNs(n int64) {
	atomic.StoreInt64(&m.ProviderDiscoveryLatencyNs, n)
}

// SetProviderRecordsCount atomically overwrites ProviderRecordsCount with n (a gauge).
//
// Parameters:
//   - n (int64): the current number of entries in the provider records table.
//
// Returns: (none)
func (m *NodeMetrics) SetProviderRecordsCount(n int64) { atomic.StoreInt64(&m.ProviderRecordsCount, n) }

// AddPutMessagesIn atomically adds n to PutMessagesIn.
//
// Parameters:
//   - n (int): number of inbound put-related messages; values <= 0 are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddPutMessagesIn(n int) {
	if n > 0 {
		atomic.AddInt64(&m.PutMessagesIn, int64(n))
	}
}

// AddPutMessagesOut atomically adds n to PutMessagesOut.
//
// Parameters:
//   - n (int): number of outbound put-related messages; values <= 0 are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddPutMessagesOut(n int) {
	if n > 0 {
		atomic.AddInt64(&m.PutMessagesOut, int64(n))
	}
}

// AddGetMessagesIn atomically adds n to GetMessagesIn.
//
// Parameters:
//   - n (int): number of inbound get-related messages; values <= 0 are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddGetMessagesIn(n int) {
	if n > 0 {
		atomic.AddInt64(&m.GetMessagesIn, int64(n))
	}
}

// AddGetMessagesOut atomically adds n to GetMessagesOut.
//
// Parameters:
//   - n (int): number of outbound get-related messages; values <= 0 are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddGetMessagesOut(n int) {
	if n > 0 {
		atomic.AddInt64(&m.GetMessagesOut, int64(n))
	}
}

// AddLookupMessagesIn atomically adds n to LookupMessagesIn.
//
// Parameters:
//   - n (int): number of inbound lookup-related messages; values <= 0 are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddLookupMessagesIn(n int) {
	if n > 0 {
		atomic.AddInt64(&m.LookupMessagesIn, int64(n))
	}
}

// AddLookupMessagesOut atomically adds n to LookupMessagesOut.
//
// Parameters:
//   - n (int): number of outbound lookup-related messages; values <= 0 are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddLookupMessagesOut(n int) {
	if n > 0 {
		atomic.AddInt64(&m.LookupMessagesOut, int64(n))
	}
}

// AddLookupHops records the hop count of a completed lookup: it stores n as
// the new LookupHopsLast gauge and accumulates it into LookupHopsCount and
// LookupHopsSum for computing a running average.
//
// Parameters:
//   - n (int): the network_hops value of the completed lookup; negative values are ignored (no-op).
//
// Returns: (none)
func (m *NodeMetrics) AddLookupHops(n int) {
	if n >= 0 {
		atomic.StoreInt64(&m.LookupHopsLast, int64(n))
		atomic.AddInt64(&m.LookupHopsCount, 1)
		atomic.AddInt64(&m.LookupHopsSum, int64(n))
	}
}

// NodeMetricsProviderSink returns a ProviderMetricsSink that forwards to m, or nil if m is nil.
// This lets the internal/storage package report provider-record counts back
// into the control server's metrics without importing the control package
// directly (mystore.ProviderMetricsSink is defined in internal/storage).
//
// Parameters:
//   - m (*NodeMetrics): the metrics instance to forward calls to; nil disables the sink.
//
// Returns:
//   - (mystore.ProviderMetricsSink): a forwarding sink, or nil if m is nil.
func NodeMetricsProviderSink(m *NodeMetrics) mystore.ProviderMetricsSink {
	if m == nil {
		return nil
	}
	return &nodeMetricsSink{m: m}
}

// nodeMetricsSink adapts NodeMetrics to the mystore.ProviderMetricsSink interface.
type nodeMetricsSink struct{ m *NodeMetrics }

// SetProviderRecordsCount forwards n to the wrapped NodeMetrics.
//
// Parameters:
//   - n (int): the current number of provider records.
//
// Returns: (none)
func (s *nodeMetricsSink) SetProviderRecordsCount(n int) { s.m.SetProviderRecordsCount(int64(n)) }

// NodeMetricsMessageSink returns a MessageMetricsSink that forwards to m, or nil if m is nil.
// Used by internal/storage to report put/get/lookup message counts without
// importing the control package directly.
//
// Parameters:
//   - m (*NodeMetrics): the metrics instance to forward calls to; nil disables the sink.
//
// Returns:
//   - (mystore.MessageMetricsSink): a forwarding sink, or nil if m is nil.
func NodeMetricsMessageSink(m *NodeMetrics) mystore.MessageMetricsSink {
	if m == nil {
		return nil
	}
	return &nodeMessageMetricsSink{m: m}
}

// nodeMessageMetricsSink adapts NodeMetrics to the mystore.MessageMetricsSink interface.
type nodeMessageMetricsSink struct{ m *NodeMetrics }

// AddPutMessagesIn forwards n to the wrapped NodeMetrics.
func (s *nodeMessageMetricsSink) AddPutMessagesIn(n int) { s.m.AddPutMessagesIn(n) }

// AddPutMessagesOut forwards n to the wrapped NodeMetrics.
func (s *nodeMessageMetricsSink) AddPutMessagesOut(n int) { s.m.AddPutMessagesOut(n) }

// AddGetMessagesIn forwards n to the wrapped NodeMetrics.
func (s *nodeMessageMetricsSink) AddGetMessagesIn(n int) { s.m.AddGetMessagesIn(n) }

// AddGetMessagesOut forwards n to the wrapped NodeMetrics.
func (s *nodeMessageMetricsSink) AddGetMessagesOut(n int) { s.m.AddGetMessagesOut(n) }

// AddLookupMessagesIn forwards n to the wrapped NodeMetrics.
func (s *nodeMessageMetricsSink) AddLookupMessagesIn(n int) { s.m.AddLookupMessagesIn(n) }

// AddLookupMessagesOut forwards n to the wrapped NodeMetrics.
func (s *nodeMessageMetricsSink) AddLookupMessagesOut(n int) { s.m.AddLookupMessagesOut(n) }

// NodeMetricsHopSink returns a NetworkHopsSink that forwards to m, or nil if m is nil.
// Used by internal/storage to report DHT lookup hop counts without importing
// the control package directly.
//
// Parameters:
//   - m (*NodeMetrics): the metrics instance to forward calls to; nil disables the sink.
//
// Returns:
//   - (mystore.NetworkHopsSink): a forwarding sink, or nil if m is nil.
func NodeMetricsHopSink(m *NodeMetrics) mystore.NetworkHopsSink {
	if m == nil {
		return nil
	}
	return &nodeHopSink{m: m}
}

// nodeHopSink adapts NodeMetrics to the mystore.NetworkHopsSink interface.
type nodeHopSink struct{ m *NodeMetrics }

// AddLookupHops forwards n to the wrapped NodeMetrics.
func (s *nodeHopSink) AddLookupHops(n int) { s.m.AddLookupHops(n) }

// Snapshot atomically reads every counter/gauge field and returns them as a
// plain MetricsSnapshot value, suitable for JSON encoding by the /metrics
// endpoint. The read of each field is individually atomic, but the snapshot
// as a whole is not a single atomic transaction across fields.
//
// Returns:
//   - (MetricsSnapshot): a point-in-time copy of all metrics fields.
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
