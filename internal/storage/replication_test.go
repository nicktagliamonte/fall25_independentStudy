package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestReplicationTargetsPreserveExactFactor(t *testing.T) {
	near, midrange, farFlung := ReplicationTargets(DefaultReplicationVector(), 7)
	if near != 3 || midrange != 2 || farFlung != 2 {
		t.Fatalf("targets = (%d,%d,%d), want (3,2,2)", near, midrange, farFlung)
	}
	if near+midrange+farFlung != 7 {
		t.Fatalf("target sum = %d, want 7", near+midrange+farFlung)
	}
}

func TestClassifyDistanceDoesNotTreatUnknownAsNear(t *testing.T) {
	if got := ClassifyDistanceByRTT(0, nil); got != DistanceUnknown {
		t.Fatalf("zero RTT classified as %s, want Unknown", got)
	}
}

func TestVerifyReplicaStateExcludesUnreachableProviders(t *testing.T) {
	ctx := context.Background()
	store := newMockTokenDHT()
	key := KeyFromData([]byte("replica liveness"))
	local := tokenTestPeerID(t)
	midrange := tokenTestPeerID(t)
	unreachable := tokenTestPeerID(t)
	addr := tokenTestMultiaddr(t, "/ip4/127.0.0.1/tcp/4001")
	token := Token{
		Key: key,
		Locations: []Location{
			{ProviderID: local, Address: addr},
			{ProviderID: midrange, Address: addr},
			{ProviderID: unreachable, Address: addr},
		},
		Timestamp: time.Now().UnixNano(),
		Version:   1,
	}
	if err := PutToken(ctx, store, key, token); err != nil {
		t.Fatalf("PutToken: %v", err)
	}

	measure := func(pid peer.ID) (time.Duration, error) {
		switch pid {
		case midrange:
			return 100 * time.Millisecond, nil
		case unreachable:
			return 0, errors.New("peer unavailable")
		default:
			return 0, errors.New("unexpected probe")
		}
	}
	verification, err := VerifyKeyStateWithRepVector(
		ctx, key, nil, store, local, measure, 3, nil,
	)
	if err != nil {
		t.Fatalf("VerifyKeyStateWithRepVector: %v", err)
	}
	if verification.ActualCounts.Near != 1 ||
		verification.ActualCounts.Midrange != 1 ||
		verification.ActualCounts.FarFlung != 0 ||
		verification.ActualCounts.Total != 2 {
		t.Fatalf("unexpected actual counts: %+v", verification.ActualCounts)
	}
	if len(verification.UnreachableProviders) != 1 ||
		verification.UnreachableProviders[0] != unreachable {
		t.Fatalf("unreachable providers = %v, want [%s]", verification.UnreachableProviders, unreachable)
	}
	if verification.IsSynchronized {
		t.Fatal("verification reported synchronized with an unreachable far-flung replica")
	}
	if len(verification.MissingCategories) != 1 ||
		verification.MissingCategories[0] != DistanceFarFlung {
		t.Fatalf("missing categories = %v, want [Far-flung]", verification.MissingCategories)
	}
}

func TestVerifyReplicaCountHealthyWithBestEffortDistancePlacement(t *testing.T) {
	ctx := context.Background()
	store := newMockTokenDHT()
	key := KeyFromData([]byte("best effort distance placement"))
	local := tokenTestPeerID(t)
	addr := tokenTestMultiaddr(t, "/ip4/127.0.0.1/tcp/4001")
	locations := []Location{{ProviderID: local, Address: addr}}
	for i := 0; i < 6; i++ {
		locations = append(locations, Location{
			ProviderID: tokenTestPeerID(t),
			Address:    addr,
		})
	}
	token := Token{
		Key:       key,
		Locations: locations,
		Timestamp: time.Now().UnixNano(),
		Version:   1,
	}
	if err := PutToken(ctx, store, key, token); err != nil {
		t.Fatal(err)
	}
	verification, err := VerifyKeyStateWithRepVector(
		ctx,
		key,
		nil,
		store,
		local,
		func(peer.ID) (time.Duration, error) {
			return time.Millisecond, nil
		},
		7,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if verification.ActualCounts.Total != 7 ||
		verification.ActualCounts.Near != 7 {
		t.Fatalf("actual counts = %+v", verification.ActualCounts)
	}
	if !verification.IsSynchronized {
		t.Fatal("seven reachable near replicas did not satisfy the fixed durability target")
	}
	if len(verification.MissingCategories) != 2 ||
		verification.MissingCategories[0] != DistanceMidrange ||
		verification.MissingCategories[1] != DistanceFarFlung {
		t.Fatalf("missing categories = %v, want midrange and far-flung", verification.MissingCategories)
	}
}
