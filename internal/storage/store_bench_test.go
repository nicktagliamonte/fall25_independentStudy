// Purpose: Phase 7.3 benchmarks for key-based vs CID-based lookup.

package storage

import (
	"context"
	"testing"

	bserv "github.com/ipfs/boxo/blockservice"
	blocks "github.com/ipfs/go-block-format"
	ds "github.com/ipfs/go-datastore"
	dsmem "github.com/ipfs/go-datastore/sync"
)

// BenchmarkKeyBasedLookup measures GetBlockByKey latency (Key→datastore→CID→blockstore).
func BenchmarkKeyBasedLookup(b *testing.B) {
	ctx := context.Background()
	mem := dsmem.MutexWrap(ds.NewMapDatastore())
	var bsvc bserv.BlockService = &memBsvc{m: make(map[string]blocks.Block)}

	payload := []byte("key-based vs cid-based lookup benchmark payload")
	key, c, err := PutRawBlockIndexed(ctx, mem, &bsvc, payload, nil)
	if err != nil {
		b.Fatalf("PutRawBlockIndexed: %v", err)
	}
	_ = c

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := GetBlockByKey(ctx, mem, &bsvc, key)
		if err != nil {
			b.Fatalf("GetBlockByKey: %v", err)
		}
	}
}

// BenchmarkCIDBasedLookup measures GetBlockByCID latency (blockstore only).
func BenchmarkCIDBasedLookup(b *testing.B) {
	ctx := context.Background()
	mem := dsmem.MutexWrap(ds.NewMapDatastore())
	var bsvc bserv.BlockService = &memBsvc{m: make(map[string]blocks.Block)}

	payload := []byte("key-based vs cid-based lookup benchmark payload")
	_, c, err := PutRawBlockIndexed(ctx, mem, &bsvc, payload, nil)
	if err != nil {
		b.Fatalf("PutRawBlockIndexed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := GetBlockByCID(ctx, &bsvc, c)
		if err != nil {
			b.Fatalf("block lookup: %v", err)
		}
	}
}

// BenchmarkPutBlockWithoutLock measures PutRawBlockIndexed without lock (baseline).
func BenchmarkPutBlockWithoutLock(b *testing.B) {
	ctx := context.Background()
	mem := dsmem.MutexWrap(ds.NewMapDatastore())
	var bsvc bserv.BlockService = &memBsvc{m: make(map[string]blocks.Block)}

	payload := []byte("lock overhead benchmark payload baseline")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := append([]byte(nil), payload...)
		p = append(p, byte(i>>24), byte(i>>16), byte(i>>8), byte(i))
		_, _, err := PutRawBlockIndexed(ctx, mem, &bsvc, p, nil)
		if err != nil {
			b.Fatalf("PutRawBlockIndexed: %v", err)
		}
	}
}

// BenchmarkPutBlockWithLock measures PutRawBlockIndexed with lock (includes acquisition overhead).
func BenchmarkPutBlockWithLock(b *testing.B) {
	ctx := context.Background()
	mem := dsmem.MutexWrap(ds.NewMapDatastore())
	lockDS := dsmem.MutexWrap(ds.NewMapDatastore())
	var bsvc bserv.BlockService = &memBsvc{m: make(map[string]blocks.Block)}
	lockMgr := NewKeyLockManagerFromDatastore(lockDS)
	holder := benchPeerID(b)

	payload := []byte("lock overhead benchmark payload with lock")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := append([]byte(nil), payload...)
		p = append(p, byte(i>>24), byte(i>>16), byte(i>>8), byte(i))
		opts := &PutLockOpts{Manager: lockMgr, Holder: holder}
		_, _, err := PutRawBlockIndexed(ctx, mem, &bsvc, p, opts)
		if err != nil {
			b.Fatalf("PutRawBlockIndexed: %v", err)
		}
	}
}
