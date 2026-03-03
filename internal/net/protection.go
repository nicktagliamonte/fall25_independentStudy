// Purpose: Eclipse/Sybil/DoS attack mitigation (Phase 6.3). Limits peers from same IP range and ASN.

package net

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	// DefaultMaxPeersPerSubnet limits peers from the same /24 (IPv4) or /48 (IPv6).
	DefaultMaxPeersPerSubnet = 5
	// DefaultMaxPeersPerASN limits peers from the same autonomous system.
	DefaultMaxPeersPerASN = 20
)

// ASNResolver maps an IP to its Autonomous System Number. Callers can plug in
// MaxMind GeoLite2 ASN or similar. Return (0, false) when ASN is unknown.
type ASNResolver interface {
	ResolveASN(ctx context.Context, ip net.IP) (asn uint32, ok bool)
}

// EclipseLimiter limits peers from the same IP range (subnet) and optionally per ASN
// to mitigate eclipse attacks.
type EclipseLimiter struct {
	mu                sync.RWMutex
	byPeer            map[peer.ID]peerLimits
	bySubnet          map[string]int
	byASN             map[uint32]int
	maxPerSubnet      int
	maxPerASN         int
	asnResolver       ASNResolver
}

type peerLimits struct {
	subnet string
	asn    uint32
}

// EclipseLimiterOption configures EclipseLimiter.
type EclipseLimiterOption func(*EclipseLimiter)

// MaxPeersPerSubnet sets the max peers per /24 (IPv4) or /48 (IPv6). Default 5.
func MaxPeersPerSubnet(n int) EclipseLimiterOption {
	return func(e *EclipseLimiter) {
		if n > 0 {
			e.maxPerSubnet = n
		}
	}
}

// MaxPeersPerASN sets the max peers per ASN. Default 20. Only applied when ASNResolver is set.
func MaxPeersPerASN(n int) EclipseLimiterOption {
	return func(e *EclipseLimiter) {
		if n > 0 {
			e.maxPerASN = n
		}
	}
}

// ASNResolverOption sets the ASN lookup. When nil, ASN limiting is skipped.
func ASNResolverOption(r ASNResolver) EclipseLimiterOption {
	return func(e *EclipseLimiter) {
		e.asnResolver = r
	}
}

// NewEclipseLimiter creates a limiter for eclipse attack mitigation.
func NewEclipseLimiter(opts ...EclipseLimiterOption) *EclipseLimiter {
	e := &EclipseLimiter{
		byPeer:       make(map[peer.ID]peerLimits),
		bySubnet:     make(map[string]int),
		byASN:        make(map[uint32]int),
		maxPerSubnet: DefaultMaxPeersPerSubnet,
		maxPerASN:    DefaultMaxPeersPerASN,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// subnetKey returns a string key for the given IP (IPv4 /24, IPv6 /48).
func subnetKey(ip net.IP) string {
	ip = ip.To16()
	if ip == nil {
		return ""
	}
	if ip4 := ip.To4(); ip4 != nil {
		return fmt.Sprintf("%d.%d.%d.0/24", ip4[0], ip4[1], ip4[2])
	}
	return fmt.Sprintf("%02x%02x%02x%02x%02x%02x/48", ip[0], ip[1], ip[2], ip[3], ip[4], ip[5])
}

// extractIPs returns the first IPv4 or IPv6 from each multiaddr.
func extractIPs(addrs []ma.Multiaddr) []net.IP {
	var out []net.IP
	seen := make(map[string]struct{})
	for _, a := range addrs {
		if v, err := a.ValueForProtocol(ma.P_IP4); err == nil && v != "" {
			ip := net.ParseIP(v)
			if ip != nil {
				k := ip.String()
				if _, ok := seen[k]; !ok {
					seen[k] = struct{}{}
					out = append(out, ip)
				}
			}
			continue
		}
		if v, err := a.ValueForProtocol(ma.P_IP6); err == nil && v != "" {
			ip := net.ParseIP(v)
			if ip != nil {
				k := ip.String()
				if _, ok := seen[k]; !ok {
					seen[k] = struct{}{}
					out = append(out, ip)
				}
			}
		}
	}
	return out
}

// primaryIP returns the first non-loopback IP, or the first IP if all loopback.
func primaryIP(ips []net.IP) net.IP {
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return ip
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return nil
}

// CanAllow returns true if registering this peer would not exceed limits.
func (e *EclipseLimiter) CanAllow(ctx context.Context, pid peer.ID, addrs []ma.Multiaddr) (bool, error) {
	ips := extractIPs(addrs)
	if len(ips) == 0 {
		return true, nil
	}
	ip := primaryIP(ips)
	subnet := subnetKey(ip)
	if subnet == "" {
		return true, nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	existing, exists := e.byPeer[pid]
	if exists {
		if existing.subnet == subnet {
			subnetCount := e.bySubnet[subnet]
			if e.asnResolver != nil {
				asn, ok := e.asnResolver.ResolveASN(ctx, ip)
				if ok && existing.asn == asn {
					asnCount := e.byASN[asn]
					return subnetCount <= e.maxPerSubnet && asnCount <= e.maxPerASN, nil
				}
			}
			return subnetCount <= e.maxPerSubnet, nil
		}
	}

	subnetCount := e.bySubnet[subnet]
	if subnetCount >= e.maxPerSubnet {
		return false, nil
	}

	if e.asnResolver != nil {
		asn, ok := e.asnResolver.ResolveASN(ctx, ip)
		if ok {
			asnCount := e.byASN[asn]
			if asnCount >= e.maxPerASN {
				return false, nil
			}
		}
	}

	return true, nil
}

// Register records a peer. Caller should ensure CanAllow returned true first.
func (e *EclipseLimiter) Register(ctx context.Context, pid peer.ID, addrs []ma.Multiaddr) error {
	ips := extractIPs(addrs)
	if len(ips) == 0 {
		return nil
	}
	ip := primaryIP(ips)
	subnet := subnetKey(ip)
	if subnet == "" {
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.byPeer[pid]; exists {
		return nil
	}

	pl := peerLimits{subnet: subnet}
	if e.asnResolver != nil {
		if asn, ok := e.asnResolver.ResolveASN(ctx, ip); ok {
			pl.asn = asn
		}
	}

	e.byPeer[pid] = pl
	e.bySubnet[subnet]++
	if pl.asn != 0 {
		e.byASN[pl.asn]++
	}
	return nil
}

// Unregister removes a peer.
func (e *EclipseLimiter) Unregister(pid peer.ID) {
	e.mu.Lock()
	defer e.mu.Unlock()

	pl, exists := e.byPeer[pid]
	if !exists {
		return
	}
	delete(e.byPeer, pid)
	if e.bySubnet[pl.subnet] > 1 {
		e.bySubnet[pl.subnet]--
	} else {
		delete(e.bySubnet, pl.subnet)
	}
	if pl.asn != 0 {
		if e.byASN[pl.asn] > 1 {
			e.byASN[pl.asn]--
		} else {
			delete(e.byASN, pl.asn)
		}
	}
}

const (
	// DefaultBucketSize is the default max addresses per bucket for Sybil-resistant storage.
	DefaultBucketSize = 64
)

// addrBucketEntry holds a peer and its addresses in a bucket.
type addrBucketEntry struct {
	pid   peer.ID
	addrs []ma.Multiaddr
}

// AddressBucketStore stores peer addresses in buckets (by subnet) with randomized eviction.
// Mitigates Sybil attacks by preventing a single network from filling a bucket and
// evicting deterministically.
type AddressBucketStore struct {
	mu           sync.RWMutex
	buckets      map[string][]addrBucketEntry
	byPeer       map[peer.ID]string
	maxPerBucket int
}

// AddressBucketStoreOption configures AddressBucketStore.
type AddressBucketStoreOption func(*AddressBucketStore)

// BucketSize sets the max entries per bucket. Default 64.
func BucketSize(n int) AddressBucketStoreOption {
	return func(s *AddressBucketStore) {
		if n > 0 {
			s.maxPerBucket = n
		}
	}
}

// NewAddressBucketStore creates a bucketed address store with randomized eviction.
func NewAddressBucketStore(opts ...AddressBucketStoreOption) *AddressBucketStore {
	s := &AddressBucketStore{
		buckets:      make(map[string][]addrBucketEntry),
		byPeer:       make(map[peer.ID]string),
		maxPerBucket: DefaultBucketSize,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Add inserts or updates a peer's addresses. If the bucket is full, evicts a random
// entry to make room. Returns the evicted peer ID if any.
func (s *AddressBucketStore) Add(pid peer.ID, addrs []ma.Multiaddr) (evicted peer.ID, ok bool) {
	ips := extractIPs(addrs)
	if len(ips) == 0 {
		return "", false
	}
	ip := primaryIP(ips)
	bucket := subnetKey(ip)
	if bucket == "" {
		return "", false
	}

	addrsCopy := make([]ma.Multiaddr, len(addrs))
	copy(addrsCopy, addrs)

	s.mu.Lock()
	defer s.mu.Unlock()

	if b, exists := s.byPeer[pid]; exists {
		ents := s.buckets[b]
		for i, e := range ents {
			if e.pid == pid {
				ents[i].addrs = addrsCopy
				return "", true
			}
		}
	}

	ents := s.buckets[bucket]
	if len(ents) >= s.maxPerBucket {
		idx, err := randInt(len(ents))
		if err != nil {
			return "", false
		}
		ev := ents[idx]
		evicted = ev.pid
		delete(s.byPeer, ev.pid)
		ents[idx] = ents[len(ents)-1]
		s.buckets[bucket] = ents[:len(ents)-1]
	}

	ent := addrBucketEntry{pid: pid, addrs: addrsCopy}
	s.buckets[bucket] = append(s.buckets[bucket], ent)
	s.byPeer[pid] = bucket
	return evicted, true
}

func randInt(n int) (int, error) {
	if n <= 0 {
		return 0, nil
	}
	max := big.NewInt(int64(n))
	x, err := rand.Int(rand.Reader, max)
	if err != nil {
		return 0, err
	}
	return int(x.Int64()), nil
}

// Remove removes a peer from the store.
func (s *AddressBucketStore) Remove(pid peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bucket, exists := s.byPeer[pid]
	if !exists {
		return
	}
	delete(s.byPeer, pid)
	ents := s.buckets[bucket]
	for i, e := range ents {
		if e.pid == pid {
			ents[i] = ents[len(ents)-1]
			s.buckets[bucket] = ents[:len(ents)-1]
			if len(s.buckets[bucket]) == 0 {
				delete(s.buckets, bucket)
			}
			return
		}
	}
}

// Get returns the addresses for a peer, or nil if not found.
func (s *AddressBucketStore) Get(pid peer.ID) []ma.Multiaddr {
	s.mu.RLock()
	defer s.mu.RUnlock()

	bucket, exists := s.byPeer[pid]
	if !exists {
		return nil
	}
	for _, e := range s.buckets[bucket] {
		if e.pid == pid {
			return e.addrs
		}
	}
	return nil
}

// Len returns the total number of peers stored.
func (s *AddressBucketStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byPeer)
}

// BucketCount returns the number of buckets (for tests).
func (s *AddressBucketStore) BucketCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.buckets)
}

const (
	// DefaultRateLimitBurst is the default burst size for rate limiting.
	DefaultRateLimitBurst = 100
	// DefaultRateLimitWindow is the default time window for rate limit reset.
	DefaultRateLimitWindow = time.Second
	// DefaultResourceCapStreams is the default max streams per peer.
	DefaultResourceCapStreams = 50
	// DefaultMisbehaviorThreshold is the score above which a peer may be disconnected.
	DefaultMisbehaviorThreshold = 100
	// DefaultMisbehaviorDecayPeriod is how often scores decay.
	DefaultMisbehaviorDecayPeriod = time.Minute
)

// PeerRateLimiter limits requests per peer per time window (token bucket).
type PeerRateLimiter struct {
	mu        sync.RWMutex
	byPeer    map[peer.ID]*rateLimitState
	burst     int
	window    time.Duration
	nowFunc   func() time.Time
}

type rateLimitState struct {
	tokens  int
	lastRefill time.Time
}

// PeerRateLimiterOption configures PeerRateLimiter.
type PeerRateLimiterOption func(*PeerRateLimiter)

// RateLimitBurst sets the burst size. Default 100.
func RateLimitBurst(n int) PeerRateLimiterOption {
	return func(r *PeerRateLimiter) {
		if n > 0 {
			r.burst = n
		}
	}
}

// RateLimitWindow sets the refill window. Default 1s.
func RateLimitWindow(d time.Duration) PeerRateLimiterOption {
	return func(r *PeerRateLimiter) {
		if d > 0 {
			r.window = d
		}
	}
}

// NewPeerRateLimiter creates a per-peer rate limiter.
func NewPeerRateLimiter(opts ...PeerRateLimiterOption) *PeerRateLimiter {
	r := &PeerRateLimiter{
		byPeer:  make(map[peer.ID]*rateLimitState),
		burst:   DefaultRateLimitBurst,
		window:  DefaultRateLimitWindow,
		nowFunc: time.Now,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Allow consumes one token for the peer. Returns false if rate limited.
func (r *PeerRateLimiter) Allow(pid peer.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFunc()
	st := r.byPeer[pid]
	if st == nil {
		st = &rateLimitState{tokens: r.burst - 1, lastRefill: now}
		r.byPeer[pid] = st
		return true
	}

	elapsed := now.Sub(st.lastRefill)
	refills := int(elapsed / r.window)
	if refills > 0 {
		st.tokens += refills
		if st.tokens > r.burst {
			st.tokens = r.burst
		}
		st.lastRefill = now
	}

	if st.tokens <= 0 {
		return false
	}
	st.tokens--
	return true
}

// PeerResourceCap tracks resource usage per peer and enforces caps.
type PeerResourceCap struct {
	mu     sync.RWMutex
	byPeer map[peer.ID]int
	cap    int
}

// PeerResourceCapOption configures PeerResourceCap.
type PeerResourceCapOption func(*PeerResourceCap)

// ResourceCap sets the max count per peer. Default 50.
func ResourceCap(n int) PeerResourceCapOption {
	return func(c *PeerResourceCap) {
		if n > 0 {
			c.cap = n
		}
	}
}

// NewPeerResourceCap creates a resource cap tracker (e.g. for streams).
func NewPeerResourceCap(opts ...PeerResourceCapOption) *PeerResourceCap {
	c := &PeerResourceCap{
		byPeer: make(map[peer.ID]int),
		cap:    DefaultResourceCapStreams,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Increment adds one to the peer's usage. Returns false if cap exceeded.
func (c *PeerResourceCap) Increment(pid peer.ID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := c.byPeer[pid]
	if n >= c.cap {
		return false
	}
	c.byPeer[pid] = n + 1
	return true
}

// Decrement subtracts one from the peer's usage.
func (c *PeerResourceCap) Decrement(pid peer.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := c.byPeer[pid]
	if n <= 1 {
		delete(c.byPeer, pid)
		return
	}
	c.byPeer[pid] = n - 1
}

// Usage returns the current usage for the peer.
func (c *PeerResourceCap) Usage(pid peer.ID) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.byPeer[pid]
}

// PeerMisbehaviorScorer tracks misbehavior score per peer. Higher score = worse behavior.
type PeerMisbehaviorScorer struct {
	mu         sync.RWMutex
	byPeer     map[peer.ID]int
	threshold  int
	decayScore int
	decayEvery time.Duration
	lastDecay  time.Time
	nowFunc    func() time.Time
}

// PeerMisbehaviorScorerOption configures PeerMisbehaviorScorer.
type PeerMisbehaviorScorerOption func(*PeerMisbehaviorScorer)

// MisbehaviorThreshold sets the score above which action may be taken. Default 100.
func MisbehaviorThreshold(n int) PeerMisbehaviorScorerOption {
	return func(m *PeerMisbehaviorScorer) {
		if n > 0 {
			m.threshold = n
		}
	}
}

// MisbehaviorDecay sets periodic score decay. Default -1 per minute.
func MisbehaviorDecay(amount int, every time.Duration) PeerMisbehaviorScorerOption {
	return func(m *PeerMisbehaviorScorer) {
		m.decayScore = amount
		if every > 0 {
			m.decayEvery = every
		}
	}
}

// NewPeerMisbehaviorScorer creates a misbehavior scorer.
func NewPeerMisbehaviorScorer(opts ...PeerMisbehaviorScorerOption) *PeerMisbehaviorScorer {
	m := &PeerMisbehaviorScorer{
		byPeer:     make(map[peer.ID]int),
		threshold:  DefaultMisbehaviorThreshold,
		decayScore: -1,
		decayEvery: DefaultMisbehaviorDecayPeriod,
		nowFunc:    time.Now,
	}
	for _, opt := range opts {
		opt(m)
	}
	m.lastDecay = m.nowFunc()
	return m
}

// AddMisbehavior adds points for the given peer.
func (m *PeerMisbehaviorScorer) AddMisbehavior(pid peer.ID, points int) {
	if points <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decayLocked()
	m.byPeer[pid] += points
}

// Score returns the current score for the peer.
func (m *PeerMisbehaviorScorer) Score(pid peer.ID) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byPeer[pid]
}

// ShouldDisconnect returns true if the peer's score exceeds the threshold.
func (m *PeerMisbehaviorScorer) ShouldDisconnect(pid peer.ID) bool {
	return m.Score(pid) >= m.threshold
}

// Decay applies score decay. Call periodically (e.g. every minute).
func (m *PeerMisbehaviorScorer) Decay() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decayLocked()
}

func (m *PeerMisbehaviorScorer) decayLocked() {
	now := m.nowFunc()
	if now.Sub(m.lastDecay) < m.decayEvery {
		return
	}
	m.lastDecay = now
	delta := m.decayScore
	for pid, s := range m.byPeer {
		s += delta
		if s <= 0 {
			delete(m.byPeer, pid)
		} else {
			m.byPeer[pid] = s
		}
	}
}

const (
	// DefaultBanDuration is the default ban duration for non-trustworthy peers.
	DefaultBanDuration = 24 * time.Hour
)

// BanList tracks banned peers with expiry. Bans expire after the configured duration.
type BanList struct {
	mu     sync.RWMutex
	byPeer map[peer.ID]time.Time
	dur    time.Duration
	nowFunc func() time.Time
}

// BanListOption configures BanList.
type BanListOption func(*BanList)

// BanDuration sets the ban duration. Default 24 hours.
func BanDuration(d time.Duration) BanListOption {
	return func(b *BanList) {
		if d > 0 {
			b.dur = d
		}
	}
}

// NewBanList creates a ban list with configurable duration.
func NewBanList(opts ...BanListOption) *BanList {
	b := &BanList{
		byPeer:  make(map[peer.ID]time.Time),
		dur:     DefaultBanDuration,
		nowFunc: time.Now,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Ban adds a peer to the ban list for the configured duration.
func (b *BanList) Ban(pid peer.ID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.byPeer[pid] = b.nowFunc().Add(b.dur)
}

// Unban removes a peer from the ban list.
func (b *BanList) Unban(pid peer.ID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.byPeer, pid)
}

// IsBanned returns true if the peer is currently banned (not expired).
func (b *BanList) IsBanned(pid peer.ID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	exp, exists := b.byPeer[pid]
	if !exists {
		return false
	}
	if b.nowFunc().After(exp) {
		delete(b.byPeer, pid)
		return false
	}
	return true
}

// AttackMitigation bundles protection components for handshake, dial, and connection logic.
type AttackMitigation struct {
	BanList            *BanList
	Eclipse            *EclipseLimiter
	RateLimiter        *PeerRateLimiter
	Misbehavior        *PeerMisbehaviorScorer
	AddressBucketStore *AddressBucketStore
	ResourceCap        *PeerResourceCap
}
