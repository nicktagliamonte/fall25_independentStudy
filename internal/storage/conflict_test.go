// Purpose: Tests for conflict version structure (Phase 5.3).

package storage

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
)

func TestTokenUpdateCanRemoveProvider(t *testing.T) {
	ctx := context.Background()
	store := newMockTokenDHT()
	key := KeyFromData([]byte("removal"))
	keep := tokenTestPeerID(t)
	remove := tokenTestPeerID(t)
	addr := tokenTestMultiaddr(t, "/ip4/127.0.0.1/tcp/4001")
	initial := Token{
		Key: key,
		Locations: []Location{
			{ProviderID: keep, Address: addr},
			{ProviderID: remove, Address: addr},
		},
		Timestamp: time.Now().UnixNano(),
		Version:   1,
	}
	if err := PutToken(ctx, store, key, initial); err != nil {
		t.Fatalf("PutToken: %v", err)
	}
	err := UpdateTokenWithConflictResolution(ctx, store, key, func(current Token) Token {
		current.Locations = []Location{current.Locations[0]}
		return current
	}, 3)
	if err != nil {
		t.Fatalf("UpdateTokenWithConflictResolution: %v", err)
	}
	got, err := GetToken(ctx, store, key)
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if len(got.Locations) != 1 || got.Locations[0].ProviderID != keep {
		t.Fatalf("locations = %+v, want only %s", got.Locations, keep)
	}
}

func TestVersion_Structure(t *testing.T) {
	pref, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("x"))
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	pid, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey: %v", err)
	}
	v := Version{Timestamp: 1234567890, NodeID: pid, Hash: pref}
	if v.Timestamp != 1234567890 || v.NodeID != pid || !v.Hash.Equals(pref) {
		t.Errorf("Version fields: got %+v", v)
	}
}

func TestCompareVersionsLastWriterWins(t *testing.T) {
	pref, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("x"))
	_, pub, _ := crypto.GenerateEd25519Key(rand.Reader)
	pid, _ := peer.IDFromPublicKey(pub)

	a := Version{Timestamp: 100, NodeID: pid, Hash: pref}
	b := Version{Timestamp: 200, NodeID: pid, Hash: pref}
	if got := CompareVersionsLastWriterWins(a, b); got != -1 {
		t.Errorf("later ts wins: want -1, got %d", got)
	}
	if got := CompareVersionsLastWriterWins(b, a); got != 1 {
		t.Errorf("later ts wins: want 1, got %d", got)
	}
	c := Version{Timestamp: 100, NodeID: pid, Hash: pref}
	if got := CompareVersionsLastWriterWins(a, c); got != 0 {
		t.Errorf("same version: want 0, got %d", got)
	}
}

func TestNoConflictForImmutable(t *testing.T) {
	pref, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("x"))
	other, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("y"))
	_, pub, _ := crypto.GenerateEd25519Key(rand.Reader)
	pid, _ := peer.IDFromPublicKey(pub)

	a := Version{Timestamp: 100, NodeID: pid, Hash: pref}
	b := Version{Timestamp: 200, NodeID: pid, Hash: pref}
	if !NoConflictForImmutable(a, b) {
		t.Error("same hash: want no conflict")
	}
	c := Version{Timestamp: 100, NodeID: pid, Hash: other}
	if NoConflictForImmutable(a, c) {
		t.Error("different hash: want conflict")
	}
}

func TestResolveMutableMetadata(t *testing.T) {
	pref, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("x"))
	other, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("y"))
	_, pub, _ := crypto.GenerateEd25519Key(rand.Reader)
	pid, _ := peer.IDFromPublicKey(pub)

	a := Version{Timestamp: 100, NodeID: pid, Hash: pref}
	b := Version{Timestamp: 200, NodeID: pid, Hash: other}
	got := ResolveMutableMetadata(a, b)
	if !got.Hash.Equals(other) || got.Timestamp != 200 {
		t.Errorf("later ts wins: got %+v", got)
	}
	got2 := ResolveMutableMetadata(b, a)
	if !got2.Hash.Equals(other) {
		t.Errorf("later ts wins (reversed): got %+v", got2)
	}
}
