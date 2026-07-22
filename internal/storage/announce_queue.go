// Purpose: Queue provider announcements for post-heal (Phase 5.2).
// When partitioned, announcements are queued; when healed, queue is flushed.
// No Phase 2 dependencies.

package storage

import (
	"context"
	"sync"

	"github.com/ipfs/go-cid"
)

// AnnounceQueue holds CIDs to announce after network heal. Safe for concurrent use.
type AnnounceQueue struct {
	mu          sync.Mutex
	partitioned bool
	queued      []cid.Cid
}

// NewAnnounceQueue returns an empty queue. Starts non-partitioned.
//
// Returns:
//   - *AnnounceQueue: a new, empty announce queue with partitioned=false.
func NewAnnounceQueue() *AnnounceQueue {
	return &AnnounceQueue{}
}

// SetPartitioned sets the partitioned state. When true, Add queues CIDs for
// later flushing; when false, Add is a no-op (the caller is expected to
// announce directly instead).
//
// Parameters:
//   - b (bool): the new partitioned state.
func (q *AnnounceQueue) SetPartitioned(b bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.partitioned = b
}

// IsPartitioned returns whether we are in partitioned mode (announcements should be queued).
//
// Returns:
//   - bool: the current partitioned state.
func (q *AnnounceQueue) IsPartitioned() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.partitioned
}

// Add queues the CID when partitioned. Caller should only call when IsPartitioned()
// is true; if the queue is not currently partitioned, or c is undefined, Add is a no-op.
//
// Parameters:
//   - c (cid.Cid): the CID to queue for later announcement.
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

// Flush drains the queue and calls fn for each CID, in FIFO order. Clears the
// queue up front (under the lock) before invoking fn, so concurrent Add calls
// during the flush populate a fresh queue rather than being lost or double-processed.
// Stops early if ctx is cancelled between calls to fn.
//
// Parameters:
//   - ctx (context.Context): checked for cancellation before each fn invocation.
//   - fn (func(context.Context, cid.Cid)): callback invoked once per queued CID.
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
//
// Returns:
//   - int: the current queue length.
func (q *AnnounceQueue) QueuedLen() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queued)
}
