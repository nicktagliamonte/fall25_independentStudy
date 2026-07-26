// Purpose: Tests for ReachablePartitionRouter (Phase 5.2).

package control

import (
	"context"
	"testing"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multihash"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
)

type staticRouter struct {
	peers []peer.AddrInfo
}

func (s *staticRouter) Provide(ctx context.Context, c cid.Cid, b bool) error  { return nil }
func (s *staticRouter) ProvideMany(ctx context.Context, keys []cid.Cid) error { return nil }
func (s *staticRouter) FindProvidersAsync(ctx context.Context, c cid.Cid, count int) <-chan peer.AddrInfo {
	out := make(chan peer.AddrInfo, len(s.peers)+1)
	for _, p := range s.peers {
		out <- p
	}
	close(out)
	return out
}

func TestReachablePartitionRouter_PrefersConnected(t *testing.T) {
	ctx := context.Background()
	hA, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}
	if err := hA.Connect(ctx, infoB); err != nil {
		t.Fatalf("connect A->B: %v", err)
	}

	pref, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("test"))
	underlying := &staticRouter{peers: []peer.AddrInfo{infoB}}
	r := NewReachablePartitionRouter(hA, underlying)

	var got []peer.AddrInfo
	for info := range r.FindProvidersAsync(ctx, pref, 5) {
		got = append(got, info)
	}
	if len(got) != 1 || got[0].ID != hB.ID() {
		t.Errorf("want 1 connected provider (B), got %v", got)
	}
}

func TestReachablePartitionRouter_PassthroughWhenNoneConnected(t *testing.T) {
	ctx := context.Background()
	hA, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}
	underlying := &staticRouter{peers: []peer.AddrInfo{infoB}}
	r := NewReachablePartitionRouter(hA, underlying)

	pref, _ := cid.Prefix{Version: 1, Codec: 0x55, MhType: multihash.SHA2_256, MhLength: 32}.Sum([]byte("test"))
	var got []peer.AddrInfo
	for info := range r.FindProvidersAsync(ctx, pref, 5) {
		got = append(got, info)
	}
	if len(got) != 1 || got[0].ID != hB.ID() {
		t.Errorf("passthrough: want 1 provider (B) for discovery, got %v", got)
	}
}

func TestReachablePartitionRouter_ImplementsRouting(t *testing.T) {
	var _ routing.ContentRouting = (*ReachablePartitionRouter)(nil)
}
