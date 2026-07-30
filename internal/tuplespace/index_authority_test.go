package tuplespace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/pht"
)

type failoverOwnerResolver struct {
	primary   peer.ID
	successor peer.ID
}

type delayedAuthorityReadStore struct {
	pht.ValueStore
	delay time.Duration
}

func (s delayedAuthorityReadStore) GetValue(
	ctx context.Context,
	key string,
	opts ...interface{},
) ([]byte, error) {
	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	return s.ValueStore.GetValue(ctx, key, opts...)
}

func (r failoverOwnerResolver) ResolveTupleOwner(context.Context, string) (peer.ID, error) {
	return r.primary, nil
}

func (r failoverOwnerResolver) ResolveTupleOwnerAfter(
	_ context.Context,
	_ string,
	excluded string,
) (peer.ID, error) {
	if excluded != r.primary.String() {
		return "", errors.New("unexpected excluded owner")
	}
	return r.successor, nil
}

func TestIndexAuthorityImmediateRouteFailoverAdvancesFence(t *testing.T) {
	store := &indexedTestStore{}
	stores, err := pht.NewShardStores(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	ownerA := peer.ID("authority-owner-a")
	ownerB := peer.ID("authority-owner-b")
	resolver := failoverOwnerResolver{primary: ownerA, successor: ownerB}
	managerA, err := newIndexAuthorityManager(ownerA, resolver, stores)
	if err != nil {
		t.Fatal(err)
	}
	managerB, err := newIndexAuthorityManager(ownerB, resolver, stores)
	if err != nil {
		t.Fatal(err)
	}
	managerA.setTiming(0, time.Minute, 0)
	managerB.setTiming(0, time.Minute, 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	first, err := managerA.resolve(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := managerB.failover(ctx, 0, first)
	if err != nil {
		t.Fatal(err)
	}
	if second.Epoch != first.Epoch+1 || second.Writer != ownerB.String() {
		t.Fatalf("failover fence = %+v after %+v", second, first)
	}
	managerA.states[0].validatedAt = time.Time{}
	if err := managerA.validateForApply(ctx, 0, first); !errors.Is(err, errStaleIndexAuthority) {
		t.Fatalf("previous writer was not fenced: %v", err)
	}
	if err := managerB.validateForApply(ctx, 0, second); err != nil {
		t.Fatalf("successor rejected: %v", err)
	}
}

func TestIndexAuthorityLeaseFailoverFencesPreviousOwner(t *testing.T) {
	store := &indexedTestStore{}
	stores, err := pht.NewShardStores(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	ownerA := peer.ID("authority-owner-a")
	ownerB := peer.ID("authority-owner-b")
	managerA, err := newIndexAuthorityManager(
		ownerA,
		fixedOwnerResolver{owner: ownerA},
		stores,
	)
	if err != nil {
		t.Fatal(err)
	}
	managerB, err := newIndexAuthorityManager(
		ownerB,
		fixedOwnerResolver{owner: ownerB},
		stores,
	)
	if err != nil {
		t.Fatal(err)
	}
	managerA.setTiming(0, 30*time.Millisecond, 2*time.Millisecond)
	managerB.setTiming(0, 30*time.Millisecond, 2*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, err := managerA.resolve(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Epoch != 1 || first.Writer != ownerA.String() {
		t.Fatalf("first authority = %+v", first)
	}
	beforeExpiry, err := managerB.resolve(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if beforeExpiry != first {
		t.Fatalf("live authority changed before expiry: first=%+v got=%+v", first, beforeExpiry)
	}

	time.Sleep(35 * time.Millisecond)
	second, err := managerB.resolve(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Epoch != 2 || second.Writer != ownerB.String() {
		t.Fatalf("failover authority = %+v", second)
	}
	if err := managerA.validateForApply(ctx, 0, first); !errors.Is(err, errStaleIndexAuthority) {
		t.Fatalf("old authority validation error = %v, want stale authority", err)
	}
	if err := managerB.validateForApply(ctx, 0, second); err != nil {
		t.Fatalf("new authority rejected: %v", err)
	}
}

func TestIndexAuthorityApplyReadsAndRejectsSameEpochWinner(t *testing.T) {
	store := &indexedTestStore{}
	stores, err := pht.NewShardStores(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	ownerA := peer.ID("authority-owner-a")
	ownerB := peer.ID("authority-owner-b")
	manager, err := newIndexAuthorityManager(
		ownerA,
		fixedOwnerResolver{owner: ownerA},
		stores,
	)
	if err != nil {
		t.Fatal(err)
	}
	manager.setTiming(0, time.Minute, 0)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, err := manager.resolve(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	winner := indexAuthorityRecord{
		Epoch:      first.Epoch,
		Writer:     ownerB.String(),
		Version:    1,
		ValidAfter: now.Add(-time.Second).UnixNano(),
		ExpiresAt:  now.Add(time.Minute).UnixNano(),
	}
	if err := manager.write(ctx, 0, winner); err != nil {
		t.Fatal(err)
	}
	// Force the periodic DHT check; between checks, the PHT's per-node fence
	// remains the commit boundary.
	manager.states[0].validatedAt = time.Time{}

	err = manager.validateForApply(ctx, 0, first)
	if !errors.Is(err, errStaleIndexAuthority) {
		t.Fatalf("same-epoch losing authority validation error = %v, want stale", err)
	}
	if manager.states[0].cached == nil ||
		manager.states[0].cached.Writer != ownerB.String() {
		t.Fatalf("cached authority was not replaced with winner: %+v", manager.states[0].cached)
	}
}

func TestIndexAuthorityResolveRefreshesTimeAfterRecordRead(t *testing.T) {
	base := &indexedTestStore{}
	baseStores, err := pht.NewShardStores(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	stores := []pht.ValueStore{delayedAuthorityReadStore{
		ValueStore: baseStores[0],
		delay:      20 * time.Millisecond,
	}}
	owner := peer.ID("authority-owner")
	manager, err := newIndexAuthorityManager(
		owner,
		fixedOwnerResolver{owner: owner},
		stores,
	)
	if err != nil {
		t.Fatal(err)
	}
	manager.setTiming(0, time.Minute, 0)

	now := time.Now()
	record := indexAuthorityRecord{
		Epoch:      7,
		Writer:     owner.String(),
		Version:    1,
		ValidAfter: now.Add(10 * time.Millisecond).UnixNano(),
		ExpiresAt:  now.Add(time.Minute).UnixNano(),
	}
	if err := manager.write(context.Background(), 0, record); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	fence, err := manager.resolve(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fence != record.fence() {
		t.Fatalf("resolved fence = %+v, want %+v", fence, record.fence())
	}
}

func TestIndexAuthorityApplyReusesRecentlyValidatedLease(t *testing.T) {
	store := &indexedTestStore{}
	stores, err := pht.NewShardStores(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	owner := peer.ID("authority-owner")
	manager, err := newIndexAuthorityManager(
		owner,
		fixedOwnerResolver{owner: owner},
		stores,
	)
	if err != nil {
		t.Fatal(err)
	}
	manager.setTiming(0, time.Minute, 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	fence, err := manager.resolve(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}

	manager.states[0].validatedAt = time.Time{}
	before := store.gets.Load()
	if err := manager.validateForApply(ctx, 0, fence); err != nil {
		t.Fatal(err)
	}
	afterRevalidation := store.gets.Load()
	if afterRevalidation != before+1 {
		t.Fatalf("DHT reads after forced validation = %d, want %d", afterRevalidation, before+1)
	}
	if err := manager.validateForApply(ctx, 0, fence); err != nil {
		t.Fatal(err)
	}
	if got := store.gets.Load(); got != afterRevalidation {
		t.Fatalf("recent cached lease performed another DHT read: got %d, want %d", got, afterRevalidation)
	}
}
