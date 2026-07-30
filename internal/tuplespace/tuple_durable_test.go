package tuplespace

import (
	"context"
	"errors"
	"testing"
	"time"

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
