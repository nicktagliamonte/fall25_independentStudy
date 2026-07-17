package storage

import (
	"bytes"
	"context"
	"testing"

	bserv "github.com/ipfs/boxo/blockservice"
	bstore "github.com/ipfs/boxo/blockstore"
	exch "github.com/ipfs/boxo/exchange"
	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	dsmem "github.com/ipfs/go-datastore/sync"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	basicnode "github.com/ipld/go-ipld-prime/node/basicnode"
)

// TestSyncSuffix_EmptyCatchesUp verifies the "fresh node" path of
// SyncSuffix: starting from a completely empty local datastore (head/height
// keys deleted after building a 2-event remote chain via AppendPeerAdded),
// syncing against the remote head at height 2 should apply both events and
// leave the local head/height equal to the remote's, since an undefined
// local head is treated as trivially rooted (foundAncestor starts true in
// SyncSuffix).
func TestSyncSuffix_EmptyCatchesUp(t *testing.T) {
	ctx := context.Background()
	mem := dsmem.MutexWrap(ds.NewMapDatastore())
	var bsvc bserv.BlockService = &memBsvc{}

	// remote chain of two events
	c1, h1, err := AppendPeerAdded(ctx, mem, &bsvc, "peerA")
	if err != nil || !c1.Defined() || h1 != 1 {
		t.Fatalf("append1: %v", err)
	}
	c2, h2, err := AppendPeerAdded(ctx, mem, &bsvc, "peerB")
	if err != nil || !c2.Defined() || h2 != 2 {
		t.Fatalf("append2: %v", err)
	}

	// reset local head to empty
	_ = mem.Delete(ctx, ds.NewKey(stateHeadKey))
	_ = mem.Delete(ctx, ds.NewKey(stateHeightKey))

	applied, head, height, err := SyncSuffix(ctx, mem, &bsvc, c2, 2, SyncOptions{MaxDepth: 512})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if applied != 2 {
		t.Fatalf("applied=%d want 2", applied)
	}
	if !head.Equals(c2) || height != 2 {
		t.Fatalf("head/height incorrect")
	}
}

// TestSyncSuffix_CommonAncestor verifies the "partial sync" path: the local
// chain already has A1 -> A2 as its head (via two real AppendPeerAdded
// calls), and a remote block A3 is hand-constructed (bypassing
// AppendPeerAdded, building the DAG-CBOR map directly with dagcbor.Encode
// and a manually computed CIDv1/dag-cbor/sha2-256 CID) whose "prev" field
// points at a2. Syncing against remoteHead=A3/remoteHeight=3 should walk
// back exactly one step, recognize a2 as the local head (common ancestor),
// and apply exactly 1 new event (A3), advancing local head to A3 and height
// to 3.
func TestSyncSuffix_CommonAncestor(t *testing.T) {
	ctx := context.Background()
	mem := dsmem.MutexWrap(ds.NewMapDatastore())
	var bsvc bserv.BlockService = &memBsvc{}

	// build chain A: A1, A2 (local head at A2)
	_, _, _ = AppendPeerAdded(ctx, mem, &bsvc, "peerA1")
	a2, _, _ := AppendPeerAdded(ctx, mem, &bsvc, "peerA2")
	// create A3 block that points to a2, but don't update local head
	nb := basicnode.Prototype__Map{}.NewBuilder()
	ma, _ := nb.BeginMap(4)
	ma.AssembleKey().AssignString("type")
	ma.AssembleValue().AssignString("peer_added")
	ma.AssembleKey().AssignString("ts")
	ma.AssembleValue().AssignInt(0)
	ma.AssembleKey().AssignString("peer")
	ma.AssembleValue().AssignString("peerA3")
	ma.AssembleKey().AssignString("prev")
	ma.AssembleValue().AssignString(a2.String())
	ma.Finish()
	n := nb.Build()
	var buf bytes.Buffer
	if err := dagcbor.Encode(n, &buf); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	a3, err := cid.Prefix{Version: 1, Codec: cid.DagCBOR, MhType: 0x12, MhLength: -1}.Sum(data)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := blocks.NewBlockWithCid(data, a3)
	if err != nil {
		t.Fatal(err)
	}
	if err := bsvc.AddBlock(ctx, blk); err != nil {
		t.Fatal(err)
	}

	applied, head, height, err := SyncSuffix(ctx, mem, &bsvc, a3, 3, SyncOptions{MaxDepth: 512})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied=%d want 1", applied)
	}
	if !head.Equals(a3) || height != 3 {
		t.Fatalf("head/height incorrect")
	}
}

// TestSyncSuffix_LyingHead is the negative/security-relevant case: the
// local chain has one real event L1, and a completely disconnected event
// block (peer "U1", no "prev" field at all — root of its own unrelated
// chain) is stored directly in the mock blockservice. Syncing against that
// block as remoteHead (claiming remoteHeight=2, exceeding local height 1)
// must fail with an error, since the backward walk from it can never reach
// local head L1 within the given MaxDepth (2) — this guards against a peer
// lying about (or genuinely diverging on) chain history being able to
// silently splice an unrelated chain onto local state.
func TestSyncSuffix_LyingHead(t *testing.T) {
	ctx := context.Background()
	mem := dsmem.MutexWrap(ds.NewMapDatastore())
	var bsvc bserv.BlockService = &memBsvc{}

	// local chain L1
	_, _, _ = AppendPeerAdded(ctx, mem, &bsvc, "L1")

	// Build a standalone event block that does not link to local head
	nb := basicnode.Prototype__Map{}.NewBuilder()
	ma, _ := nb.BeginMap(3)
	ma.AssembleKey().AssignString("type")
	ma.AssembleValue().AssignString("peer_added")
	ma.AssembleKey().AssignString("ts")
	ma.AssembleValue().AssignInt(0)
	ma.AssembleKey().AssignString("peer")
	ma.AssembleValue().AssignString("U1")
	ma.Finish()
	n := nb.Build()
	var buf bytes.Buffer
	if err := dagcbor.Encode(n, &buf); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	c, err := cid.Prefix{Version: 1, Codec: cid.DagCBOR, MhType: 0x12, MhLength: -1}.Sum(data)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := blocks.NewBlockWithCid(data, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := bsvc.AddBlock(ctx, blk); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := SyncSuffix(ctx, mem, &bsvc, c, 2, SyncOptions{MaxDepth: 2}); err == nil {
		t.Fatalf("expected error on unrelated head")
	}
}

// memBsvc is a minimal in-memory implementation of bserv.BlockService used
// only in tests to avoid pulling in a real Bitswap/network stack. It is
// intentionally not safe for concurrent use (no locking around m) — fine
// for these sequential tests, but not a general-purpose substitute.
type memBsvc struct{ m map[string]blocks.Block }

// AddBlock stores b in the in-memory map, keyed by its CID's string form.
// Lazily initializes the map on first use. Always returns nil (storage
// never fails in this test double).
func (m *memBsvc) AddBlock(ctx context.Context, b blocks.Block) error {
	if m.m == nil {
		m.m = make(map[string]blocks.Block)
	}
	m.m[b.Cid().String()] = b
	return nil
}

// GetBlock looks up c in the in-memory map. Returns
// bstore.ErrHashMismatch (repurposed here simply as a generic "not found"
// sentinel, not because of an actual hash mismatch) if the map is
// uninitialized or c is not present.
func (m *memBsvc) GetBlock(ctx context.Context, c cid.Cid) (blocks.Block, error) {
	if m.m == nil {
		return nil, bstore.ErrHashMismatch
	}
	if b, ok := m.m[c.String()]; ok {
		return b, nil
	}
	return nil, bstore.ErrHashMismatch
}

// AddBlocks calls AddBlock for each block in blks sequentially, returning
// the first error encountered (if any); since AddBlock never errors here,
// this always returns nil in practice.
func (m *memBsvc) AddBlocks(ctx context.Context, blks []blocks.Block) error {
	for _, b := range blks {
		if err := m.AddBlock(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

// GetBlocks fetches each key in ks via GetBlock and streams found blocks
// on the returned channel from a background goroutine; the channel is
// buffered to len(ks) and closed once all keys have been attempted. Blocks
// that fail to fetch (not present) are silently omitted rather than
// producing an error on the channel.
func (m *memBsvc) GetBlocks(ctx context.Context, ks []cid.Cid) <-chan blocks.Block {
	out := make(chan blocks.Block, len(ks))
	go func() {
		defer close(out)
		for _, c := range ks {
			if b, err := m.GetBlock(ctx, c); err == nil {
				out <- b
			}
		}
	}()
	return out
}

// Close is a no-op satisfying the bserv.BlockService interface; always
// returns nil.
func (m *memBsvc) Close() error { return nil }

// Blockstore returns a fresh, empty in-memory blockstore on every call —
// it is NOT backed by memBsvc's own m map, so it does not reflect blocks
// added via AddBlock. Only present to satisfy the bserv.BlockService
// interface; tests do not rely on this method's contents.
func (m *memBsvc) Blockstore() bstore.Blockstore { return bstore.NewBlockstore(ds.NewMapDatastore()) }

// Exchange always returns nil, satisfying the bserv.BlockService interface
// without providing a real exchange (Bitswap) implementation.
func (m *memBsvc) Exchange() exch.Interface { return nil }

// DeleteBlock removes c from the in-memory map if present. Returns nil
// whether or not the map was initialized or c was present (deleting an
// absent key is a no-op, not an error).
func (m *memBsvc) DeleteBlock(ctx context.Context, c cid.Cid) error {
	if m.m == nil {
		return nil
	}
	delete(m.m, c.String())
	return nil
}
