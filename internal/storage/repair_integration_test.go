package storage

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

type noProviderRouter struct{}

func (noProviderRouter) Provide(context.Context, cid.Cid, bool) error { return nil }
func (noProviderRouter) FindProvidersAsync(context.Context, cid.Cid, int) <-chan peer.AddrInfo {
	out := make(chan peer.AddrInfo)
	close(out)
	return out
}

func TestAuditRepairsCrashStaleReplicaToRTTDiversePeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	hA, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	defer hA.Close()
	hC, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	defer hC.Close()
	if err := hA.Connect(ctx, peer.AddrInfo{ID: hC.ID(), Addrs: hC.Addrs()}); err != nil {
		t.Fatalf("connect A to C: %v", err)
	}

	stackA, err := NewStackWithRouter(ctx, hA, noProviderRouter{})
	if err != nil {
		t.Fatal(err)
	}
	defer stackA.Close()
	stackC, err := NewStackWithRouter(ctx, hC, noProviderRouter{})
	if err != nil {
		t.Fatal(err)
	}
	defer stackC.Close()
	tokenStore := newMockTokenDHT()
	stackA.TokenStore = tokenStore
	stackC.TokenStore = tokenStore

	sharedTuples := tuplespace.NewNativeTupleSpace()
	repairA := NewRepairProtocol(stackA, hA, sharedTuples, false)
	repairC := NewRepairProtocol(stackC, hC, sharedTuples, false)
	handlerErrors := make(chan error, 1)
	hC.SetStreamHandler(RepairProtocolID, func(stream network.Stream) {
		handlerErrors <- repairC.HandleRepairStream(stream)
	})
	repairA.rttProbe = func(pid peer.ID) (time.Duration, error) {
		if pid == hC.ID() {
			return 100 * time.Millisecond, nil
		}
		return 0, errors.New("peer unavailable")
	}
	repairA.storageAvailable.RTTMeasurer = repairA.MeasureRTT
	if err := repairA.storageAvailable.AdvertiseStorageAvailable(
		hC.ID(), 0, 1<<30, 1, time.Minute,
	); err != nil {
		t.Fatalf("advertise C: %v", err)
	}

	payload := bytes.Repeat([]byte("repairable-payload-"), 1024)
	key, c, err := stackA.PutPayload(ctx, payload)
	if err != nil {
		t.Fatalf("PutPayload A: %v", err)
	}
	stackA.UpdateRoutingTableOnPut(key, hA.ID(), nil, c)

	dead := tokenTestPeerID(t)
	deadAddr := tokenTestMultiaddr(t, "/ip4/127.0.0.1/tcp/65534")
	if err := SyncTokenOnReplication(ctx, tokenStore, stackA.RoutingTable, key, dead, deadAddr); err != nil {
		t.Fatalf("add stale provider: %v", err)
	}

	verification, result, err := repairA.AuditAndRepair(ctx, key, 2)
	if err != nil {
		t.Fatalf("AuditAndRepair: %v", err)
	}
	if len(verification.UnreachableProviders) != 1 ||
		verification.UnreachableProviders[0] != dead {
		t.Fatalf("unreachable providers = %v, want [%s]", verification.UnreachableProviders, dead)
	}
	if result == nil || result.TotalReplicasCreated != 1 ||
		len(result.ReplicatedPeers) != 1 || result.ReplicatedPeers[0] != hC.ID() {
		select {
		case handlerErr := <-handlerErrors:
			t.Fatalf("repair result = %+v, handler error = %v", result, handlerErr)
		default:
			t.Fatalf("repair result = %+v, want one replica on C", result)
		}
	}
	got, err := ResolvePayloadByKeyLocal(ctx, stackC.Datastore, stackC.BlockSvc, key)
	if err != nil {
		t.Fatalf("ResolvePayloadByKeyLocal C: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("repaired payload length = %d, want %d", len(got), len(payload))
	}
	token, err := GetToken(ctx, tokenStore, key)
	if err != nil {
		t.Fatalf("GetToken after repair: %v", err)
	}
	seen := make(map[peer.ID]bool)
	for _, location := range token.Locations {
		seen[location.ProviderID] = true
	}
	if seen[dead] || !seen[hA.ID()] || !seen[hC.ID()] {
		t.Fatalf("post-repair token providers = %v", seen)
	}
}
