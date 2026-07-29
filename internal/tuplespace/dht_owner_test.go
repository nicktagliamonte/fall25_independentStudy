package tuplespace

import (
	"context"
	"errors"
	"testing"

	kbucket "github.com/libp2p/go-libp2p-kbucket"
	"github.com/libp2p/go-libp2p/core/peer"
)

type fakeClosestPeerFinder struct {
	peers []peer.ID
	err   error
}

func (f fakeClosestPeerFinder) GetClosestPeers(context.Context, string) ([]peer.ID, error) {
	return f.peers, f.err
}

func TestDHTTupleOwnerResolverChoosesXORClosestIncludingSelf(t *testing.T) {
	self := peer.ID("self-peer")
	peers := []peer.ID{peer.ID("peer-a"), peer.ID("peer-b"), peer.ID("peer-c")}
	resolver, err := NewDHTTupleOwnerResolver(self, fakeClosestPeerFinder{peers: peers})
	if err != nil {
		t.Fatal(err)
	}
	const key = "task:image:001"
	got, err := resolver.ResolveTupleOwner(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	want := self
	for _, candidate := range peers {
		if kbucket.Closer(candidate, want, key) {
			want = candidate
		}
	}
	if got != want {
		t.Fatalf("owner = %s, want %s", got, want)
	}
}

func TestDHTTupleOwnerResolverSingleNodeFallback(t *testing.T) {
	self := peer.ID("self-peer")
	resolver, err := NewDHTTupleOwnerResolver(self, fakeClosestPeerFinder{err: errors.New("empty routing table")})
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolver.ResolveTupleOwner(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if got != self {
		t.Fatalf("owner = %s, want self", got)
	}
}
