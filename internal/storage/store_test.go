// Purpose: Tests for local store operations (Phase 5.2).

package storage

import (
	"bytes"
	"context"
	"testing"

	bserv "github.com/ipfs/boxo/blockservice"
	ds "github.com/ipfs/go-datastore"
	dsmem "github.com/ipfs/go-datastore/sync"
	kaddht "github.com/libp2p/go-libp2p-kad-dht"
)

func TestStackTokenValueStoreDoesNotWrapNilDHT(t *testing.T) {
	var nilDHT *kaddht.IpfsDHT
	stack := &Stack{DHT: nilDHT}

	if got := stack.tokenValueStore(); got != nil {
		t.Fatalf("tokenValueStore() = %#v, want nil", got)
	}

	explicit := newMockTokenDHT()
	stack.TokenStore = explicit
	if got := stack.tokenValueStore(); got != explicit {
		t.Fatalf("tokenValueStore() = %#v, want explicit token store", got)
	}
}

func TestPutPayloadUsesWholePayloadKeyAndCID(t *testing.T) {
	ctx := context.Background()
	mem := dsmem.MutexWrap(ds.NewMapDatastore())
	var bsvc bserv.BlockService = &memBsvc{}
	stack := &Stack{Datastore: mem, BlockSvc: &bsvc}
	payload := bytes.Repeat([]byte("tarsus-payload-"), DefaultContentChunkSize)

	key, c, err := stack.PutPayload(ctx, payload)
	if err != nil {
		t.Fatalf("PutPayload: %v", err)
	}
	if !key.Equal(KeyFromData(payload)) {
		t.Fatalf("key = %s, want whole-payload key", key.String())
	}
	mapped, err := GetCIDFromKey(ctx, mem, key)
	if err != nil {
		t.Fatalf("GetCIDFromKey: %v", err)
	}
	if !mapped.Equals(c) {
		t.Fatalf("mapped CID = %s, want %s", mapped, c)
	}
	got, err := GetBlockByKey(ctx, mem, &bsvc, key)
	if err != nil {
		t.Fatalf("GetBlockByKey: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("stored payload length = %d, want %d", len(got), len(payload))
	}
}

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
