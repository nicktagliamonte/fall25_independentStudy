// Purpose: Tests for local store operations (Phase 5.2).

package storage

import (
	"bytes"
	"context"
	"testing"

	bserv "github.com/ipfs/boxo/blockservice"
	ds "github.com/ipfs/go-datastore"
	dsmem "github.com/ipfs/go-datastore/sync"
)

func TestPutRawBlockIndexed_LocalStore(t *testing.T) {
	ctx := context.Background()
	mem := dsmem.MutexWrap(ds.NewMapDatastore())
	var bsvc bserv.BlockService = &memBsvc{}

	payload := []byte("local store test")
	key, c, err := PutRawBlockIndexed(ctx, mem, &bsvc, payload, nil)
	if err != nil {
		t.Fatalf("PutRawBlockIndexed: %v", err)
	}
	if key.IsZero() {
		t.Fatal("expected non-zero Key")
	}
	if !c.Defined() {
		t.Fatal("expected defined block identifier")
	}
	// Verify key matches hash of data
	expectedKey := KeyFromData(payload)
	if !key.Equal(expectedKey) {
		t.Errorf("key mismatch: got %s, want %s", key.String(), expectedKey.String())
	}
	got, err := GetBlockByKey(ctx, mem, &bsvc, key)
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}
