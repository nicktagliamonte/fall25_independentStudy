package tuplespace

import (
	"context"
	"errors"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestDurableTupleStatePersistsMultisetAndMutationResults(t *testing.T) {
	store := &indexedTestStore{}
	owner := peer.ID("durable-owner")
	durable, err := newDurableTupleStore(
		owner,
		fixedOwnerResolver{owner: owner},
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	durable.setTiming(0, time.Minute, 0)
	ctx := context.Background()
	fence := tupleFence{Epoch: 1, Writer: owner.String()}

	put := tupleWireRequest{
		Operation: "put",
		Name:      "task:durable",
		Value:     []byte("payload"),
		RequestID: "put-once",
		Epoch:     fence.Epoch,
		Writer:    fence.Writer,
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := durable.apply(ctx, put); err != nil {
			t.Fatalf("put attempt %d: %v", attempt+1, err)
		}
	}

	get := tupleWireRequest{
		Operation: "get",
		Name:      put.Name,
		RequestID: "get-once",
		Epoch:     fence.Epoch,
		Writer:    fence.Writer,
	}
	value, err := durable.apply(ctx, get)
	if err != nil || string(value) != "payload" {
		t.Fatalf("first get = %q, %v", value, err)
	}

	// Reconstructing the owner simulates a process restart: both the empty
	// post-consume multiset and the successful Get result come from the store.
	restarted, err := newDurableTupleStore(
		owner,
		fixedOwnerResolver{owner: owner},
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	restarted.setTiming(0, time.Minute, 0)
	value, err = restarted.apply(ctx, get)
	if err != nil || string(value) != "payload" {
		t.Fatalf("retried get after restart = %q, %v", value, err)
	}
	secondGet := get
	secondGet.RequestID = "get-twice"
	if _, err := restarted.apply(ctx, secondGet); !errors.Is(err, ErrTupleNotFound) {
		t.Fatalf("second logical get = %v, want ErrTupleNotFound", err)
	}
}

func TestDurableTupleFailoverCopiesStateAndFencesOldOwner(t *testing.T) {
	store := &indexedTestStore{}
	ownerA := peer.ID("durable-owner-a")
	ownerB := peer.ID("durable-owner-b")
	resolver := failoverOwnerResolver{primary: ownerA, successor: ownerB}
	first, err := newDurableTupleStore(ownerA, resolver, store)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newDurableTupleStore(ownerB, resolver, store)
	if err != nil {
		t.Fatal(err)
	}
	first.setTiming(0, 20*time.Millisecond, 0)
	second.setTiming(0, 20*time.Millisecond, 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	initial := tupleFence{Epoch: 1, Writer: ownerA.String()}
	if _, err := first.apply(ctx, tupleWireRequest{
		Operation: "put",
		Name:      "task:failover",
		Value:     []byte("survives"),
		RequestID: "initial-put",
		Epoch:     initial.Epoch,
		Writer:    initial.Writer,
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	adopted, err := second.failover(ctx, "task:failover", initial)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Epoch != 2 || adopted.Writer != ownerB.String() {
		t.Fatalf("adopted fence = %+v", adopted)
	}

	value, err := second.apply(ctx, tupleWireRequest{
		Operation: "get",
		Name:      "task:failover",
		RequestID: "successor-get",
		Epoch:     adopted.Epoch,
		Writer:    adopted.Writer,
	})
	if err != nil || string(value) != "survives" {
		t.Fatalf("successor get = %q, %v", value, err)
	}
	if _, err := first.apply(ctx, tupleWireRequest{
		Operation: "put",
		Name:      "task:failover",
		Value:     []byte("stale"),
		RequestID: "stale-put",
		Epoch:     initial.Epoch,
		Writer:    initial.Writer,
	}); !errors.Is(err, errStaleTupleAuthority) {
		t.Fatalf("stale owner error = %v", err)
	}
}

func TestDurableTupleOwnerCacheAvoidsRedundantDHTReads(t *testing.T) {
	store := &indexedTestStore{}
	owner := peer.ID("cached-durable-owner")
	durable, err := newDurableTupleStore(
		owner,
		fixedOwnerResolver{owner: owner},
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	durable.setTiming(0, time.Minute, time.Second)
	ctx := context.Background()
	fence := tupleFence{Epoch: 1, Writer: owner.String()}
	if _, err := durable.apply(ctx, tupleWireRequest{
		Operation: "put",
		Name:      "task:cached-owner",
		Value:     []byte("payload"),
		RequestID: "cache-put",
		Epoch:     fence.Epoch,
		Writer:    fence.Writer,
	}); err != nil {
		t.Fatal(err)
	}
	readsAfterCommit := store.gets.Load()
	value, err := durable.apply(ctx, tupleWireRequest{
		Operation: "read",
		Name:      "task:cached-owner",
		RequestID: "cache-read",
		Epoch:     fence.Epoch,
		Writer:    fence.Writer,
	})
	if err != nil || string(value) != "payload" {
		t.Fatalf("cached read = %q, %v", value, err)
	}
	if got := store.gets.Load(); got != readsAfterCommit {
		t.Fatalf("cached owner read performed %d extra DHT reads", got-readsAfterCommit)
	}
}

func TestDurableTupleOwnerCacheExpiresBeforeLeaseRenewal(t *testing.T) {
	store := &indexedTestStore{}
	owner := peer.ID("expiring-durable-owner")
	durable, err := newDurableTupleStore(
		owner,
		fixedOwnerResolver{owner: owner},
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	durable.setTiming(0, 30*time.Millisecond, 5*time.Millisecond)
	ctx := context.Background()
	initial := tupleFence{Epoch: 1, Writer: owner.String()}
	if _, err := durable.apply(ctx, tupleWireRequest{
		Operation: "put",
		Name:      "task:expiring-cache",
		Value:     []byte("payload"),
		RequestID: "expiring-put",
		Epoch:     initial.Epoch,
		Writer:    initial.Writer,
	}); err != nil {
		t.Fatal(err)
	}
	readsAfterCommit := store.gets.Load()
	time.Sleep(30 * time.Millisecond)
	_, err = durable.apply(ctx, tupleWireRequest{
		Operation: "read",
		Name:      "task:expiring-cache",
		RequestID: "expiring-read",
		Epoch:     initial.Epoch,
		Writer:    initial.Writer,
	})
	var stale *tupleAuthorityError
	if !errors.As(err, &stale) {
		t.Fatalf("expired fence read = %v, want authority redirect", err)
	}
	if stale.Fence.Epoch <= initial.Epoch {
		t.Fatalf("renewed fence = %+v, want epoch after %+v", stale.Fence, initial)
	}
	if got := store.gets.Load(); got <= readsAfterCommit {
		t.Fatal("expired owner cache did not re-read durable state")
	}
}

func TestDistributedDurableTupleCachesFenceAndOwnerState(t *testing.T) {
	ownerHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer ownerHost.Close()
	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer clientHost.Close()
	connectTupleHosts(t, clientHost, ownerHost)

	store := &indexedTestStore{}
	resolver := fixedOwnerResolver{owner: ownerHost.ID()}
	owner, err := NewDistributedTupleSpace(ownerHost, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	client, err := NewDistributedTupleSpace(clientHost, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, space := range []*DistributedTupleSpace{owner, client} {
		if err := space.EnableDurableState(store); err != nil {
			t.Fatal(err)
		}
		space.SetDurableStateTiming(0, time.Minute, time.Second)
	}

	if _, err := client.TsPut("task:end-to-end-cache", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	readsAfterPut := store.gets.Load()
	value, err := client.TsRead("task:end-to-end-cache")
	if err != nil || string(value) != "payload" {
		t.Fatalf("cached distributed read = %q, %v", value, err)
	}
	if got := store.gets.Load(); got != readsAfterPut {
		t.Fatalf("cached distributed read performed %d extra DHT reads", got-readsAfterPut)
	}
	projected, err := owner.local.TsRead("task:end-to-end-cache")
	if err != nil || string(projected) != "payload" {
		t.Fatalf("local projection = %q, %v", projected, err)
	}
}
