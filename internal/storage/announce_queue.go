// Purpose: Queue provider announcements for post-heal (Phase 5.2).
// When partitioned, announcements are queued; when healed, queue is flushed.
// No Phase 2 dependencies.

package storage

import (
	"context"
	"sync"

	"github.com/ipfs/go-cid"
)

// AnnounceQueue holds CIDs to announce after network heal.
type AnnounceQueue struct {
	mu         sync.Mutex
	partitioned bool
	queued     []cid.Cid
}

// NewAnnounceQueue returns an empty queue. Starts non-partitioned.
func NewAnnounceQueue() *AnnounceQueue {
	return &AnnounceQueue{}
}

// SetPartitioned sets the partitioned state. When true, Add queues; when false, Add does nothing (caller announces directly).
func (q *AnnounceQueue) SetPartitioned(b bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.partitioned = b
}

// IsPartitioned returns whether we are in partitioned mode (announcements should be queued).
func (q *AnnounceQueue) IsPartitioned() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.partitioned
}

// Add queues the CID when partitioned. Caller should only call when IsPartitioned().
func (q *AnnounceQueue) Add(c cid.Cid) {
	if !c.Defined() {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.partitioned {
		return
	}
	q.queued = append(q.queued, c)
}

// Flush drains the queue and calls fn for each CID. Clears the queue.
func (q *AnnounceQueue) Flush(ctx context.Context, fn func(context.Context, cid.Cid)) {
	q.mu.Lock()
	drain := q.queued
	q.queued = nil
	q.mu.Unlock()
	for _, c := range drain {
		if ctx.Err() != nil {
			return
		}
		fn(ctx, c)
	}
}

// QueuedLen returns the number of CIDs currently queued.
func (q *AnnounceQueue) QueuedLen() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queued)
}
