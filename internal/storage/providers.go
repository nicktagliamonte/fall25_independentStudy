// Purpose: Provider record management for DHT content routing.

package storage

import (
	"context"
	"sync"
	"time"

	bstore "github.com/ipfs/boxo/blockstore"
	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p/core/routing"
)

// Announce records this peer as a provider of the given CID in the DHT.
// Called after successful Put so other nodes can discover this peer.
func Announce(ctx context.Context, router routing.ContentRouting, c cid.Cid) {
	if router == nil || !c.Defined() {
		return
	}
	_ = router.Provide(ctx, c, true)
}

// LocalProviderRecords tracks CIDs we have announced for efficient re-announcement.
type LocalProviderRecords struct {
	mu   sync.RWMutex
	cids map[string]struct{}
}

// NewLocalProviderRecords returns an empty tracker.
func NewLocalProviderRecords() *LocalProviderRecords {
	return &LocalProviderRecords{cids: make(map[string]struct{})}
}

// Add records a CID as locally provided.
func (r *LocalProviderRecords) Add(c cid.Cid) {
	if !c.Defined() {
		return
	}
	r.mu.Lock()
	r.cids[c.String()] = struct{}{}
	r.mu.Unlock()
}

// Remove drops a CID from the tracker (e.g. block no longer present).
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

// ProviderMetricsSink receives provider-related metrics (e.g. announce count, records count).
type ProviderMetricsSink interface {
	IncAnnounceCount()
	SetProviderRecordsCount(n int)
}

// DefaultReannounceInterval is the default interval for periodic re-announcement.
const DefaultReannounceInterval = 12 * time.Hour

// StartPeriodicReannounce starts a goroutine that re-announces tracked CIDs to the DHT at the given interval.
// Records for blocks no longer in the blockstore are removed (expiry). Stops when ctx is cancelled.
func StartPeriodicReannounce(ctx context.Context, router routing.ContentRouting, records *LocalProviderRecords, bs bstore.Blockstore, interval time.Duration, metrics ProviderMetricsSink) {
	if router == nil || records == nil || interval <= 0 {
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
							continue
						}
					}
					Announce(ctx, router, c)
					if metrics != nil {
						metrics.IncAnnounceCount()
					}
				}
				if metrics != nil {
					metrics.SetProviderRecordsCount(records.Len())
				}
			}
		}
	}()
}
