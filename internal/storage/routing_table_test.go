// Purpose: Tests for routing table multi-provider support (Phase 7.1).

package storage

import (
	"crypto/rand"
	"testing"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func mustPeerID(t *testing.T) peer.ID {
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	pid, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey: %v", err)
	}
	return pid
}

func TestRoutingTable_MultiProviderSupport(t *testing.T) {
	rt := NewRoutingTable()
	k := KeyFromData([]byte("test block data"))
	rv := DefaultReplicationVector()
	c, _ := cid.Prefix{Version: 1, Codec: 0x55}.Sum([]byte("test block data"))

	p1 := mustPeerID(t)
	p2 := mustPeerID(t)
	p3 := mustPeerID(t)

	rt.Set(k, p1, rv, c)
	if rt.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", rt.Len())
	}
	provs := rt.GetProviders(k)
	if len(provs) != 1 || provs[0].ProviderID != p1 {
		t.Errorf("expected 1 provider (p1), got %v", provs)
	}

	rt.Set(k, p2, rv, c)
	provs = rt.GetProviders(k)
	if len(provs) != 2 {
		t.Errorf("Set merge: expected 2 providers, got %d", len(provs))
	}
	seen := make(map[peer.ID]bool)
	for _, p := range provs {
		seen[p.ProviderID] = true
	}
	if !seen[p1] || !seen[p2] {
		t.Errorf("expected both p1 and p2, got %v", provs)
	}

	rt.AddProvider(k, p3, DistanceNear)
	provs = rt.GetProviders(k)
	if len(provs) != 3 {
		t.Errorf("AddProvider: expected 3 providers, got %d", len(provs))
	}

	near := rt.GetProvidersByCategory(k, DistanceNear)
	if len(near) != 1 || near[0] != p3 {
		t.Errorf("GetProvidersByCategory Near: expected [p3], got %v", near)
	}

	rt.RemoveProvider(k, p2)
	provs = rt.GetProviders(k)
	if len(provs) != 2 {
		t.Errorf("RemoveProvider: expected 2 providers, got %d", len(provs))
	}
	for _, p := range provs {
		if p.ProviderID == p2 {
			t.Error("p2 should be removed")
		}
	}
}

func TestRoutingTable_SetDoesNotDuplicateProvider(t *testing.T) {
	rt := NewRoutingTable()
	k := KeyFromData([]byte("dup test"))
	rv := DefaultReplicationVector()
	p := mustPeerID(t)

	rt.Set(k, p, rv, cid.Cid{})
	rt.Set(k, p, rv, cid.Cid{})
	rt.Set(k, p, rv, cid.Cid{})

	provs := rt.GetProviders(k)
	if len(provs) != 1 {
		t.Errorf("same provider Set multiple times: expected 1, got %d", len(provs))
	}
}
