// Purpose: Provider record tracking (legacy). Token routing (SyncTokenOnPut) is primary for discovery.

package storage

import (
	"context"
	"sync"
	"time"

	bstore "github.com/ipfs/boxo/blockstore"
	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
)

// LocalProviderRecords tracks CIDs for metrics; token routing handles discovery.
// Safe for concurrent use.
type LocalProviderRecords struct {
	mu   sync.RWMutex
	cids map[string]struct{}
}

// NewLocalProviderRecords returns an empty tracker.
//
// Returns:
//   - *LocalProviderRecords: a new tracker with no CIDs recorded.
func NewLocalProviderRecords() *LocalProviderRecords {
	return &LocalProviderRecords{cids: make(map[string]struct{})}
}

// Add records a CID (for metrics). Key-based token sync is primary for discovery;
// this tracker exists only to size/count local provider records.
//
// Parameters:
//   - c (cid.Cid): the CID to record; a no-op if undefined.
func (r *LocalProviderRecords) Add(c cid.Cid) {
	if !c.Defined() {
		return
	}
	r.mu.Lock()
	r.cids[c.String()] = struct{}{}
	r.mu.Unlock()
}

// Remove drops a CID from the tracker when block is deleted.
//
// Parameters:
//   - c (cid.Cid): the CID to remove; a no-op if undefined.
func (r *LocalProviderRecords) Remove(c cid.Cid) {
	if !c.Defined() {
		return
	}
	r.mu.Lock()
	delete(r.cids, c.String())
	r.mu.Unlock()
}

// AddAllFromDatastore populates the tracker from the manifest index (call once at startup).
// Errors from the underlying index listing are silently ignored (best-effort population).
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the index listing.
//   - d (ds.Batching): the datastore holding the manifest/CID index; a no-op if nil.
func (r *LocalProviderRecords) AddAllFromDatastore(ctx context.Context, d ds.Batching) {
	if d == nil {
		return
	}
	cids, _ := ListIndexedCIDs(ctx, d, 0, "")
	r.mu.Lock()
	for _, cidStr := range cids {
		if cidStr != "" {
			r.cids[cidStr] = struct{}{}
		}
	}
	r.mu.Unlock()
}

// Len returns the number of tracked CIDs.
//
// Returns:
//   - int: the current count of tracked CIDs.
func (r *LocalProviderRecords) Len() int {
	r.mu.RLock()
	n := len(r.cids)
	r.mu.RUnlock()
	return n
}

// Snapshot returns a copy of tracked CIDs for iteration. CID strings that fail
// to decode back into a cid.Cid are silently skipped.
//
// Returns:
//   - []cid.Cid: the currently tracked CIDs, in unspecified order.
func (r *LocalProviderRecords) Snapshot() []cid.Cid {
	r.mu.RLock()
	out := make([]cid.Cid, 0, len(r.cids))
	for cidStr := range r.cids {
		c, err := cid.Decode(cidStr)
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	r.mu.RUnlock()
	return out
}

// ProviderMetricsSink receives provider-related metrics (records count).
type ProviderMetricsSink interface {
	// SetProviderRecordsCount reports the current number of tracked provider records.
	SetProviderRecordsCount(n int)
}

// MessageMetricsSink receives P2P message counts per operation (put, get, lookup).
type MessageMetricsSink interface {
	// AddPutMessagesIn records n inbound put messages.
	AddPutMessagesIn(n int)
	// AddPutMessagesOut records n outbound put messages.
	AddPutMessagesOut(n int)
	// AddGetMessagesIn records n inbound get messages.
	AddGetMessagesIn(n int)
	// AddGetMessagesOut records n outbound get messages.
	AddGetMessagesOut(n int)
	// AddLookupMessagesIn records n inbound lookup messages.
	AddLookupMessagesIn(n int)
	// AddLookupMessagesOut records n outbound lookup messages.
	AddLookupMessagesOut(n int)
}

// NetworkHopsSink receives DHT lookup hop counts (peers queried during iterative lookup).
type NetworkHopsSink interface {
	// AddLookupHops records n additional peer hops observed during a lookup.
	AddLookupHops(n int)
}

// DefaultReannounceInterval is the default interval for periodic re-announcement.
const DefaultReannounceInterval = 12 * time.Hour

// StartPeriodicReannounce prunes provider records for blocks no longer in the blockstore (expiry)
// and updates metrics. Token routing is primary; this loop does not perform any
// DHT provider announcements itself — it only reconciles the in-memory tracker
// against the blockstore and reports its size. Runs in a background goroutine
// on a ticker until ctx is cancelled; the goroutine leaks if ctx is never cancelled.
//
// Parameters:
//   - ctx (context.Context): when cancelled, stops the background goroutine.
//   - records (*LocalProviderRecords): the tracker to prune; if nil, this is a no-op.
//   - bs (bstore.Blockstore): used to check whether each tracked CID still has
//     a block; records whose block is missing (or whose Has check errors) are removed.
//   - interval (time.Duration): the ticker period; if <= 0, this is a no-op.
//   - metrics (ProviderMetricsSink): optional; if non-nil, receives the tracker's
//     size after each pruning pass.
func StartPeriodicReannounce(ctx context.Context, records *LocalProviderRecords, bs bstore.Blockstore, interval time.Duration, metrics ProviderMetricsSink) {
	if records == nil || interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, c := range records.Snapshot() {
					if bs != nil {
						has, err := bs.Has(ctx, c)
						if err != nil || !has {
							records.Remove(c)
						}
					}
				}
				if metrics != nil {
					metrics.SetProviderRecordsCount(records.Len())
				}
			}
		}
	}()
}
