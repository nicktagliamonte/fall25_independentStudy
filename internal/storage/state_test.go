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

type memBsvc struct{ m map[string]blocks.Block }

func (m *memBsvc) AddBlock(ctx context.Context, b blocks.Block) error {
	if m.m == nil {
		m.m = make(map[string]blocks.Block)
	}
	m.m[b.Cid().String()] = b
	return nil
}
func (m *memBsvc) GetBlock(ctx context.Context, c cid.Cid) (blocks.Block, error) {
	if m.m == nil {
		return nil, bstore.ErrHashMismatch
	}
	if b, ok := m.m[c.String()]; ok {
		return b, nil
	}
	return nil, bstore.ErrHashMismatch
}
func (m *memBsvc) AddBlocks(ctx context.Context, blks []blocks.Block) error {
	for _, b := range blks {
		if err := m.AddBlock(ctx, b); err != nil {
			return err
		}
	}
	return nil
}
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
func (m *memBsvc) Close() error                  { return nil }
func (m *memBsvc) Blockstore() bstore.Blockstore { return bstore.NewBlockstore(ds.NewMapDatastore()) }
func (m *memBsvc) Exchange() exch.Interface      { return nil }
func (m *memBsvc) DeleteBlock(ctx context.Context, c cid.Cid) error {
	if m.m == nil {
		return nil
	}
	delete(m.m, c.String())
	return nil
}
