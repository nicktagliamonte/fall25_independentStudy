// Purpose: Tests for eclipse attack mitigation (IP range and ASN limiting).

package net

import (
	"context"
	"net"
	"testing"
	"time"

	ma "github.com/multiformats/go-multiaddr"
)

func addr(s string) ma.Multiaddr {
	m, _ := ma.NewMultiaddr(s)
	return m
}

func TestEclipseLimiter_IPRangeLimit(t *testing.T) {
	ctx := context.Background()
	e := NewEclipseLimiter(MaxPeersPerSubnet(2))

	addrs1 := []ma.Multiaddr{addr("/ip4/10.0.0.1/tcp/4001")}
	addrs2 := []ma.Multiaddr{addr("/ip4/10.0.0.2/tcp/4001")}
	addrs3 := []ma.Multiaddr{addr("/ip4/10.0.0.3/tcp/4001")}

	pid1 := mustPeerID(t)
	pid2 := mustPeerID(t)
	pid3 := mustPeerID(t)

	ok, _ := e.CanAllow(ctx, pid1, addrs1)
	if !ok {
		t.Fatal("first peer should be allowed")
	}
	_ = e.Register(ctx, pid1, addrs1)

	ok, _ = e.CanAllow(ctx, pid2, addrs2)
	if !ok {
		t.Fatal("second peer same /24 should be allowed")
	}
	_ = e.Register(ctx, pid2, addrs2)

	ok, _ = e.CanAllow(ctx, pid3, addrs3)
	if ok {
		t.Fatal("third peer same /24 should be rejected (max 2)")
	}
}

func TestEclipseLimiter_DifferentSubnets(t *testing.T) {
	ctx := context.Background()
	e := NewEclipseLimiter(MaxPeersPerSubnet(1))

	addrsA := []ma.Multiaddr{addr("/ip4/10.0.0.1/tcp/4001")}
	addrsB := []ma.Multiaddr{addr("/ip4/10.0.1.1/tcp/4001")}

	pidA := mustPeerID(t)
	pidB := mustPeerID(t)

	_ = e.Register(ctx, pidA, addrsA)

	ok, _ := e.CanAllow(ctx, pidB, addrsB)
	if !ok {
		t.Fatal("peer from different /24 should be allowed")
	}
}

func TestEclipseLimiter_Unregister(t *testing.T) {
	ctx := context.Background()
	e := NewEclipseLimiter(MaxPeersPerSubnet(2))

	addrs := []ma.Multiaddr{addr("/ip4/10.0.0.1/tcp/4001")}
	pid1 := mustPeerID(t)
	pid2 := mustPeerID(t)
	pid3 := mustPeerID(t)

	_ = e.Register(ctx, pid1, addrs)
	_ = e.Register(ctx, pid2, addrs)

	ok, _ := e.CanAllow(ctx, pid3, addrs)
	if ok {
		t.Fatal("third peer should be rejected")
	}

	e.Unregister(pid2)

	ok, _ = e.CanAllow(ctx, pid3, addrs)
	if !ok {
		t.Fatal("after unregister, third peer should be allowed")
	}
}

type mockASNResolverNet struct {
	m map[string]uint32
}

func (r *mockASNResolverNet) ResolveASN(ctx context.Context, ip net.IP) (uint32, bool) {
	asn, ok := r.m[ip.String()]
	return asn, ok
}

func TestAddressBucketStore_BucketingAndEviction(t *testing.T) {
	s := NewAddressBucketStore(BucketSize(2))

	addrs := []ma.Multiaddr{addr("/ip4/10.0.0.1/tcp/4001")}
	pid1 := mustPeerID(t)
	pid2 := mustPeerID(t)
	pid3 := mustPeerID(t)

	ev, ok := s.Add(pid1, addrs)
	if !ok || ev != "" {
		t.Fatalf("first add: ok=%v ev=%v", ok, ev)
	}
	ev, ok = s.Add(pid2, addrs)
	if !ok || ev != "" {
		t.Fatalf("second add: ok=%v ev=%v", ok, ev)
	}
	ev, ok = s.Add(pid3, addrs)
	if !ok || ev == "" {
		t.Fatalf("third add should evict one: ok=%v ev=%v", ok, ev)
	}

	if s.Len() != 2 {
		t.Fatalf("expected 2 entries after eviction, got %d", s.Len())
	}
	if s.BucketCount() != 1 {
		t.Fatalf("expected 1 bucket, got %d", s.BucketCount())
	}
}

func TestAddressBucketStore_RemoveAndGet(t *testing.T) {
	s := NewAddressBucketStore(BucketSize(10))

	addrs := []ma.Multiaddr{addr("/ip4/10.0.0.1/tcp/4001")}
	pid := mustPeerID(t)

	s.Add(pid, addrs)
	if got := s.Get(pid); len(got) != 1 {
		t.Fatalf("Get: expected 1 addr, got %d", len(got))
	}

	s.Remove(pid)
	if got := s.Get(pid); got != nil {
		t.Fatalf("Get after Remove: expected nil, got %v", got)
	}
}

func TestPeerRateLimiter_AllowsWithinBurst(t *testing.T) {
	r := NewPeerRateLimiter(RateLimitBurst(3))
	pid := mustPeerID(t)

	for i := 0; i < 3; i++ {
		if !r.Allow(pid) {
			t.Fatalf("allow %d: expected true", i+1)
		}
	}
	if r.Allow(pid) {
		t.Fatal("4th allow: expected false (rate limited)")
	}
}

func TestPeerResourceCap_EnforcesCap(t *testing.T) {
	c := NewPeerResourceCap(ResourceCap(2))
	pid := mustPeerID(t)

	if !c.Increment(pid) || !c.Increment(pid) {
		t.Fatal("first two increments should succeed")
	}
	if c.Increment(pid) {
		t.Fatal("third increment should fail (cap)")
	}
	c.Decrement(pid)
	if !c.Increment(pid) {
		t.Fatal("increment after decrement should succeed")
	}
}

func TestBanList_Expiry(t *testing.T) {
	now := time.Unix(1000, 0)
	b := NewBanList(BanDuration(24*time.Hour))
	b.nowFunc = func() time.Time { return now }

	pid := mustPeerID(t)
	b.Ban(pid)
	if !b.IsBanned(pid) {
		t.Fatal("just banned: should be banned")
	}

	b.nowFunc = func() time.Time { return now.Add(25 * time.Hour) }
	if b.IsBanned(pid) {
		t.Fatal("after expiry: should not be banned")
	}
}

func TestBanList_Unban(t *testing.T) {
	b := NewBanList()
	pid := mustPeerID(t)

	b.Ban(pid)
	b.Unban(pid)
	if b.IsBanned(pid) {
		t.Fatal("after unban: should not be banned")
	}
}

func TestPeerMisbehaviorScorer_ShouldDisconnect(t *testing.T) {
	m := NewPeerMisbehaviorScorer(MisbehaviorThreshold(10))
	pid := mustPeerID(t)

	m.AddMisbehavior(pid, 5)
	if m.ShouldDisconnect(pid) {
		t.Fatal("score 5 should not trigger disconnect")
	}
	m.AddMisbehavior(pid, 5)
	if !m.ShouldDisconnect(pid) {
		t.Fatal("score 10 should trigger disconnect")
	}
}

func TestEclipseLimiter_ASNLimit(t *testing.T) {
	ctx := context.Background()
	resolver := &mockASNResolverNet{
		m: map[string]uint32{
			"10.0.0.1": 65001,
			"10.0.0.2": 65001,
			"10.0.0.3": 65001,
		},
	}
	e := NewEclipseLimiter(
		MaxPeersPerSubnet(10),
		MaxPeersPerASN(2),
		ASNResolverOption(resolver),
	)

	addrs1 := []ma.Multiaddr{addr("/ip4/10.0.0.1/tcp/4001")}
	addrs2 := []ma.Multiaddr{addr("/ip4/10.0.0.2/tcp/4001")}
	addrs3 := []ma.Multiaddr{addr("/ip4/10.0.0.3/tcp/4001")}

	pid1 := mustPeerID(t)
	pid2 := mustPeerID(t)
	pid3 := mustPeerID(t)

	_ = e.Register(ctx, pid1, addrs1)
	_ = e.Register(ctx, pid2, addrs2)

	ok, _ := e.CanAllow(ctx, pid3, addrs3)
	if ok {
		t.Fatal("third peer same ASN should be rejected (max 2 per ASN)")
	}
}
