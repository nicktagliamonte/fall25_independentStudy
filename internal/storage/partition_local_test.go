// Purpose: Tests for partition-local operation tracking (Phase 5.2).

package storage

import (
	"context"
	"testing"

	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	dsmem "github.com/ipfs/go-datastore/sync"
	"github.com/multiformats/go-multihash"
)

func TestRecordPartitionLocalOp_List(t *testing.T) {
	ctx := context.Background()
	mem := dsmem.MutexWrap(ds.NewMapDatastore())

	pref, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("a"))
	if err := RecordPartitionLocalOp(ctx, mem, "put", pref); err != nil {
		t.Fatalf("RecordPartitionLocalOp: %v", err)
	}

	ops, err := ListPartitionLocalOps(ctx, mem, 10)
	if err != nil {
		t.Fatalf("ListPartitionLocalOps: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("want 1 op, got %d", len(ops))
	}
	if ops[0].Op != "put" || !ops[0].CID.Equals(pref) || ops[0].TsNano == 0 {
		t.Errorf("op: got %+v", ops[0])
	}
}
