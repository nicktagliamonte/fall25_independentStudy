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
type LocalProviderRecords struct {
	mu   sync.RWMutex
	cids map[string]struct{}
}

// NewLocalProviderRecords returns an empty tracker.
func NewLocalProviderRecords() *LocalProviderRecords {
	return &LocalProviderRecords{cids: make(map[string]struct{})}
}

// Add records a CID (for metrics). Key-based token sync is primary.
func (r *LocalProviderRecords) Add(c cid.Cid) {
	if !c.Defined() {
		return
	}
	r.mu.Lock()
	r.cids[c.String()] = struct{}{}
	r.mu.Unlock()
}

// Remove drops a CID from the tracker when block is deleted.
func (r *LocalProviderRecords) Remove(c cid.Cid) {
	if !c.Defined() {
		return
	}
	r.mu.Lock()
	delete(r.cids, c.String())
	r.mu.Unlock()
}

// AddAllFromDatastore populates the tracker from the manifest index (call once at startup).
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
func (r *LocalProviderRecords) Len() int {
	r.mu.RLock()
	n := len(r.cids)
	r.mu.RUnlock()
	return n
}

// Snapshot returns a copy of tracked CIDs for iteration.
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
	SetProviderRecordsCount(n int)
}

// MessageMetricsSink receives P2P message counts per operation (put, get, lookup).
type MessageMetricsSink interface {
	AddPutMessagesIn(n int)
	AddPutMessagesOut(n int)
	AddGetMessagesIn(n int)
	AddGetMessagesOut(n int)
	AddLookupMessagesIn(n int)
	AddLookupMessagesOut(n int)
}

// NetworkHopsSink receives DHT lookup hop counts (peers queried during iterative lookup).
type NetworkHopsSink interface {
	AddLookupHops(n int)
}

// DefaultReannounceInterval is the default interval for periodic re-announcement.
const DefaultReannounceInterval = 12 * time.Hour

// StartPeriodicReannounce prunes provider records for blocks no longer in the blockstore (expiry)
// and updates metrics. Token routing is primary; no DHT provider announcements. Stops when ctx is cancelled.
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
