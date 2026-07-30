package tuplespace

import (
	"context"
	"errors"
	"testing"

	kbucket "github.com/libp2p/go-libp2p-kbucket"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

type fakeClosestPeerFinder struct {
	peers []peer.ID
	err   error
}

func (f fakeClosestPeerFinder) GetClosestPeers(context.Context, string) ([]peer.ID, error) {
	return f.peers, f.err
}

type fakeStablePeerFinder struct {
	info peer.AddrInfo
}

func (f fakeStablePeerFinder) StablePeerInfo(id peer.ID) (peer.AddrInfo, bool) {
	return f.info, f.info.ID == id && len(f.info.Addrs) > 0
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

func TestDHTTupleOwnerResolverFailoverExcludesPreviousOwner(t *testing.T) {
	self := peer.ID("self-peer")
	peers := []peer.ID{peer.ID("peer-a"), peer.ID("peer-b"), peer.ID("peer-c")}
	resolver, err := NewDHTTupleOwnerResolver(self, fakeClosestPeerFinder{peers: peers})
	if err != nil {
		t.Fatal(err)
	}
	const key = "index-shard"
	first, err := resolver.ResolveTupleOwner(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolver.ResolveTupleOwnerAfter(context.Background(), key, first.String())
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("failover reselected excluded owner %s", first)
	}
	candidates := append([]peer.ID{self}, peers...)
	var want peer.ID
	for _, candidate := range candidates {
		if candidate == first {
			continue
		}
		if want == "" || kbucket.Closer(candidate, want, key) {
			want = candidate
		}
	}
	if second != want {
		t.Fatalf("successor = %s, want %s", second, want)
	}
}

func TestDHTTupleOwnerResolverRejectsUnderpopulatedElectionView(t *testing.T) {
	self := peer.ID("self-peer")
	resolver, err := NewDHTTupleOwnerResolver(
		self,
		fakeClosestPeerFinder{peers: []peer.ID{peer.ID("peer-a"), peer.ID("peer-b")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolver.SetMinimumCandidates(3)
	if _, err := resolver.ResolveTupleOwner(context.Background(), "task"); err == nil {
		t.Fatal("under-populated ownership view was accepted")
	}
}

func TestDHTTupleOwnerResolverPrefersStableAdvertisedAddress(t *testing.T) {
	self := peer.ID("self-peer")
	target := peer.ID("target-peer")
	stable, err := multiaddr.NewMultiaddr("/ip4/172.20.0.52/tcp/4001")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewDHTTupleOwnerResolver(
		self,
		fakeClosestPeerFinder{peers: []peer.ID{target}},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolver.SetStablePeerFinder(fakeStablePeerFinder{
		info: peer.AddrInfo{ID: target, Addrs: []multiaddr.Multiaddr{stable}},
	})

	info, err := resolver.FindPeer(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Addrs) != 1 || !info.Addrs[0].Equal(stable) {
		t.Fatalf("resolved addresses = %v, want [%s]", info.Addrs, stable)
	}
}
