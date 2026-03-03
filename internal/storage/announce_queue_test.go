// Purpose: Tests for AnnounceQueue (Phase 5.2).

package storage

import (
	"context"
	"testing"

	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

func TestAnnounceQueue_QueuesWhenPartitioned(t *testing.T) {
	q := NewAnnounceQueue()
	q.SetPartitioned(true)
	pref, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("x"))
	q.Add(pref)
	if q.QueuedLen() != 1 {
		t.Errorf("QueuedLen want 1, got %d", q.QueuedLen())
	}
}

func TestAnnounceQueue_NoQueueWhenNotPartitioned(t *testing.T) {
	q := NewAnnounceQueue()
	q.SetPartitioned(false)
	pref, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("x"))
	q.Add(pref)
	if q.QueuedLen() != 0 {
		t.Errorf("QueuedLen want 0 when not partitioned, got %d", q.QueuedLen())
	}
}

func TestAnnounceQueue_FlushDrainsAndCalls(t *testing.T) {
	ctx := context.Background()
	q := NewAnnounceQueue()
	q.SetPartitioned(true)
	pref, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("y"))
	q.Add(pref)
	var flushed []cid.Cid
	q.Flush(ctx, func(_ context.Context, c cid.Cid) { flushed = append(flushed, c) })
	if len(flushed) != 1 || !flushed[0].Equals(pref) {
		t.Errorf("Flush: want 1 CID, got %v", flushed)
	}
	if q.QueuedLen() != 0 {
		t.Errorf("after Flush QueuedLen want 0, got %d", q.QueuedLen())
	}
}
