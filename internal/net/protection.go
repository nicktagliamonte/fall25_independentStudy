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
	// ResolveASN resolves ip to an autonomous system number.
	//
	// Parameters:
	//   - ctx (context.Context): controls cancellation/timeout of the resolution.
	//   - ip (net.IP): the address to resolve.
	//
	// Returns:
	//   - asn (uint32): the resolved ASN, or 0 if unknown.
	//   - ok (bool): true if an ASN was successfully resolved.
	ResolveASN(ctx context.Context, ip net.IP) (asn uint32, ok bool)
}

// EclipseLimiter limits peers from the same IP range (subnet) and optionally per ASN
// to mitigate eclipse attacks.
type EclipseLimiter struct {
	mu           sync.RWMutex
	byPeer       map[peer.ID]peerLimits
	bySubnet     map[string]int
	byASN        map[uint32]int
	maxPerSubnet int
	maxPerASN    int
	asnResolver  ASNResolver
}

// peerLimits records which subnet and ASN bucket a registered peer counts against,
// so Unregister can decrement the correct counters.
type peerLimits struct {
	subnet string
	asn    uint32
}

// EclipseLimiterOption configures EclipseLimiter.
type EclipseLimiterOption func(*EclipseLimiter)

// MaxPeersPerSubnet sets the max peers per /24 (IPv4) or /48 (IPv6). Default 5.
//
// Parameters:
//   - n (int): the max peers per subnet, applied if positive.
//
// Returns:
//   - EclipseLimiterOption: an option that applies the limit to an EclipseLimiter.
func MaxPeersPerSubnet(n int) EclipseLimiterOption {
	return func(e *EclipseLimiter) {
		if n > 0 {
			e.maxPerSubnet = n
		}
	}
}

// MaxPeersPerASN sets the max peers per ASN. Default 20. Only applied when ASNResolver is set.
//
// Parameters:
//   - n (int): the max peers per ASN, applied if positive.
//
// Returns:
//   - EclipseLimiterOption: an option that applies the limit to an EclipseLimiter.
func MaxPeersPerASN(n int) EclipseLimiterOption {
	return func(e *EclipseLimiter) {
		if n > 0 {
			e.maxPerASN = n
		}
	}
}

// ASNResolverOption sets the ASN lookup. When nil, ASN limiting is skipped.
//
// Parameters:
//   - r (ASNResolver): the resolver to use; nil disables ASN-based limiting.
//
// Returns:
//   - EclipseLimiterOption: an option that sets the resolver on an EclipseLimiter.
func ASNResolverOption(r ASNResolver) EclipseLimiterOption {
	return func(e *EclipseLimiter) {
		e.asnResolver = r
	}
}

// NewEclipseLimiter creates a limiter for eclipse attack mitigation, applying
// defaults (DefaultMaxPeersPerSubnet, DefaultMaxPeersPerASN, no ASN resolver)
// before applying opts.
//
// Parameters:
//   - opts (...EclipseLimiterOption): functional options overriding defaults.
//
// Returns:
//   - *EclipseLimiter: a configured, empty limiter.
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
//
// Parameters:
//   - ip (net.IP): the address to key by subnet.
//
// Returns:
//   - string: the subnet key (e.g. "1.2.3.0/24" for IPv4), or "" if ip cannot be normalized to 16 bytes.
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
//
// Parameters:
//   - addrs ([]ma.Multiaddr): multiaddrs to extract addresses from.
//
// Returns:
//   - []net.IP: deduplicated IP addresses found across addrs, in encounter order.
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
//
// Parameters:
//   - ips ([]net.IP): candidate addresses, typically from extractIPs.
//
// Returns:
//   - net.IP: the chosen representative address, or nil if ips is empty.
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

// CanAllow returns true if registering this peer would not exceed limits. If pid
// is already registered under the same subnet, it re-checks against the current
// counts (allowing already-counted peers to remain allowed at the boundary) rather
// than always requiring strictly-under-limit; new peers require bySubnet (and, if
// resolvable, byASN) counts to be strictly below their configured maximums. Peers
// with no extractable IP are always allowed (limits do not apply).
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the optional ASN resolution.
//   - pid (peer.ID): the peer being considered for registration.
//   - addrs ([]ma.Multiaddr): the peer's known addresses.
//
// Returns:
//   - bool: true if registering pid would not exceed the subnet/ASN limits.
//   - error: always nil; present for interface consistency and future extensibility.
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
// If pid has no extractable IP, or is already registered, this is a no-op.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the optional ASN resolution.
//   - pid (peer.ID): the peer to register.
//   - addrs ([]ma.Multiaddr): the peer's known addresses, used to determine subnet/ASN.
//
// Returns:
//   - error: always nil; present for interface consistency and future extensibility.
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

// Unregister removes a peer, decrementing its subnet and (if applicable) ASN
// counters. No-op if pid was not registered.
//
// Parameters:
//   - pid (peer.ID): the peer to remove.
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
	// pid is the peer ID stored in this bucket entry.
	pid peer.ID
	// addrs is the peer's known addresses at the time of insertion/update.
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
//
// Parameters:
//   - n (int): the max entries per bucket, applied if positive.
//
// Returns:
//   - AddressBucketStoreOption: an option that applies the limit to an AddressBucketStore.
func BucketSize(n int) AddressBucketStoreOption {
	return func(s *AddressBucketStore) {
		if n > 0 {
			s.maxPerBucket = n
		}
	}
}

// NewAddressBucketStore creates a bucketed address store with randomized eviction,
// applying the default DefaultBucketSize before applying opts.
//
// Parameters:
//   - opts (...AddressBucketStoreOption): functional options overriding defaults.
//
// Returns:
//   - *AddressBucketStore: a configured, empty store.
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
// entry to make room. Returns the evicted peer ID if any. If pid is already
// present, its addresses are updated in place and no eviction occurs. Peers with
// no extractable IP are rejected (ok=false).
//
// Parameters:
//   - pid (peer.ID): the peer to insert or update.
//   - addrs ([]ma.Multiaddr): the peer's addresses; a private copy is stored.
//
// Returns:
//   - evicted (peer.ID): the peer ID evicted to make room, or "" if none was evicted.
//   - ok (bool): true if pid was inserted or updated; false if it has no extractable IP or randomized eviction selection failed.
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

// randInt returns a cryptographically random integer in [0, n). Used to select an
// eviction victim without a deterministic/predictable pattern (Sybil resistance).
//
// Parameters:
//   - n (int): the exclusive upper bound; if <= 0, returns (0, nil) without consuming randomness.
//
// Returns:
//   - int: a random value in [0, n).
//   - error: non-nil if the crypto/rand source fails.
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

// Remove removes a peer from the store. No-op if pid is not present. If removal
// empties the peer's bucket, the bucket entry itself is deleted.
//
// Parameters:
//   - pid (peer.ID): the peer to remove.
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
//
// Parameters:
//   - pid (peer.ID): the peer to look up.
//
// Returns:
//   - []ma.Multiaddr: the peer's stored addresses, or nil if pid is not present.
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
//
// Returns:
//   - int: the number of distinct peers across all buckets.
func (s *AddressBucketStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byPeer)
}

// BucketCount returns the number of buckets (for tests).
//
// Returns:
//   - int: the number of distinct subnet buckets currently populated.
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
	mu      sync.RWMutex
	byPeer  map[peer.ID]*rateLimitState
	burst   int
	window  time.Duration
	nowFunc func() time.Time
}

// rateLimitState tracks a single peer's token-bucket state: remaining tokens and
// the last time the bucket was refilled.
type rateLimitState struct {
	tokens     int
	lastRefill time.Time
}

// PeerRateLimiterOption configures PeerRateLimiter.
type PeerRateLimiterOption func(*PeerRateLimiter)

// RateLimitBurst sets the burst size. Default 100.
//
// Parameters:
//   - n (int): the max token bucket size, applied if positive.
//
// Returns:
//   - PeerRateLimiterOption: an option that applies the burst size to a PeerRateLimiter.
func RateLimitBurst(n int) PeerRateLimiterOption {
	return func(r *PeerRateLimiter) {
		if n > 0 {
			r.burst = n
		}
	}
}

// RateLimitWindow sets the refill window. Default 1s.
//
// Parameters:
//   - d (time.Duration): the duration after which one token is refilled, applied if positive.
//
// Returns:
//   - PeerRateLimiterOption: an option that applies the window to a PeerRateLimiter.
func RateLimitWindow(d time.Duration) PeerRateLimiterOption {
	return func(r *PeerRateLimiter) {
		if d > 0 {
			r.window = d
		}
	}
}

// NewPeerRateLimiter creates a per-peer rate limiter, applying defaults
// (DefaultRateLimitBurst, DefaultRateLimitWindow) before applying opts.
//
// Parameters:
//   - opts (...PeerRateLimiterOption): functional options overriding defaults.
//
// Returns:
//   - *PeerRateLimiter: a configured, empty limiter.
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

// Allow consumes one token for the peer. Returns false if rate limited. New peers
// start with burst-1 tokens (the call itself consumes one). Elapsed time since the
// last refill is converted to whole-window refills, capped at burst.
//
// Parameters:
//   - pid (peer.ID): the peer requesting a token.
//
// Returns:
//   - bool: true if a token was available and consumed, false if the peer is rate limited.
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
//
// Parameters:
//   - n (int): the max resource count per peer, applied if positive.
//
// Returns:
//   - PeerResourceCapOption: an option that applies the cap to a PeerResourceCap.
func ResourceCap(n int) PeerResourceCapOption {
	return func(c *PeerResourceCap) {
		if n > 0 {
			c.cap = n
		}
	}
}

// NewPeerResourceCap creates a resource cap tracker (e.g. for streams), applying
// the default DefaultResourceCapStreams before applying opts.
//
// Parameters:
//   - opts (...PeerResourceCapOption): functional options overriding the default.
//
// Returns:
//   - *PeerResourceCap: a configured, empty tracker.
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
//
// Parameters:
//   - pid (peer.ID): the peer whose usage is incremented.
//
// Returns:
//   - bool: true if usage was incremented, false if the peer's usage was already at or above cap.
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

// Decrement subtracts one from the peer's usage. If usage would drop to zero (or
// is already at/below 1), the peer's entry is removed entirely rather than stored
// as 0.
//
// Parameters:
//   - pid (peer.ID): the peer whose usage is decremented.
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
//
// Parameters:
//   - pid (peer.ID): the peer to look up.
//
// Returns:
//   - int: the peer's current usage count, or 0 if not tracked.
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
//
// Parameters:
//   - n (int): the threshold score, applied if positive.
//
// Returns:
//   - PeerMisbehaviorScorerOption: an option that applies the threshold to a PeerMisbehaviorScorer.
func MisbehaviorThreshold(n int) PeerMisbehaviorScorerOption {
	return func(m *PeerMisbehaviorScorer) {
		if n > 0 {
			m.threshold = n
		}
	}
}

// MisbehaviorDecay sets periodic score decay. Default -1 per minute.
//
// Parameters:
//   - amount (int): the (typically negative) score delta applied to every tracked peer on each decay tick.
//   - every (time.Duration): the minimum interval between decay applications, applied if positive.
//
// Returns:
//   - PeerMisbehaviorScorerOption: an option that applies the decay settings to a PeerMisbehaviorScorer.
func MisbehaviorDecay(amount int, every time.Duration) PeerMisbehaviorScorerOption {
	return func(m *PeerMisbehaviorScorer) {
		m.decayScore = amount
		if every > 0 {
			m.decayEvery = every
		}
	}
}

// NewPeerMisbehaviorScorer creates a misbehavior scorer, applying defaults
// (DefaultMisbehaviorThreshold, -1 per DefaultMisbehaviorDecayPeriod) before
// applying opts.
//
// Parameters:
//   - opts (...PeerMisbehaviorScorerOption): functional options overriding defaults.
//
// Returns:
//   - *PeerMisbehaviorScorer: a configured, empty scorer.
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

// AddMisbehavior adds points for the given peer. Applies any pending decay first
// (via decayLocked) so accumulation and decay stay consistent. No-op if points <= 0.
//
// Parameters:
//   - pid (peer.ID): the peer to penalize.
//   - points (int): the score increment to add; ignored if <= 0.
func (m *PeerMisbehaviorScorer) AddMisbehavior(pid peer.ID, points int) {
	if points <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decayLocked()
	m.byPeer[pid] += points
}

// Score returns the current score for the peer. Note: unlike AddMisbehavior and
// Decay, this does not itself trigger a decay pass, so it may return a value that
// has not yet reflected an overdue decay tick.
//
// Parameters:
//   - pid (peer.ID): the peer to look up.
//
// Returns:
//   - int: the peer's current misbehavior score, or 0 if not tracked.
func (m *PeerMisbehaviorScorer) Score(pid peer.ID) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byPeer[pid]
}

// ShouldDisconnect returns true if the peer's score exceeds the threshold.
//
// Parameters:
//   - pid (peer.ID): the peer to check.
//
// Returns:
//   - bool: true if the peer's score is at or above the configured threshold.
func (m *PeerMisbehaviorScorer) ShouldDisconnect(pid peer.ID) bool {
	return m.Score(pid) >= m.threshold
}

// Decay applies score decay. Call periodically (e.g. every minute). Actual
// application is throttled internally by decayEvery, so calling this more often
// than that interval is harmless.
func (m *PeerMisbehaviorScorer) Decay() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decayLocked()
}

// decayLocked applies decayScore to every tracked peer's score if at least
// decayEvery has elapsed since the last decay, removing peers whose score drops
// to zero or below. Callers must hold m.mu.
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
	mu      sync.RWMutex
	byPeer  map[peer.ID]time.Time
	dur     time.Duration
	nowFunc func() time.Time
}

// BanListOption configures BanList.
type BanListOption func(*BanList)

// BanDuration sets the ban duration. Default 24 hours.
//
// Parameters:
//   - d (time.Duration): the ban duration, applied if positive.
//
// Returns:
//   - BanListOption: an option that applies the duration to a BanList.
func BanDuration(d time.Duration) BanListOption {
	return func(b *BanList) {
		if d > 0 {
			b.dur = d
		}
	}
}

// NewBanList creates a ban list with configurable duration, applying the default
// DefaultBanDuration before applying opts.
//
// Parameters:
//   - opts (...BanListOption): functional options overriding the default.
//
// Returns:
//   - *BanList: a configured, empty ban list.
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

// Ban adds a peer to the ban list for the configured duration. Calling Ban again
// on an already-banned peer resets its expiry to now+dur.
//
// Parameters:
//   - pid (peer.ID): the peer to ban.
func (b *BanList) Ban(pid peer.ID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.byPeer[pid] = b.nowFunc().Add(b.dur)
}

// Unban removes a peer from the ban list. No-op if pid was not banned.
//
// Parameters:
//   - pid (peer.ID): the peer to unban.
func (b *BanList) Unban(pid peer.ID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.byPeer, pid)
}

// IsBanned returns true if the peer is currently banned (not expired). If the ban
// has expired, the entry is lazily removed as a side effect of this call.
//
// Parameters:
//   - pid (peer.ID): the peer to check.
//
// Returns:
//   - bool: true if pid has an unexpired ban entry.
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
	// BanList tracks peers banned for misbehavior, gating handshake and connection acceptance.
	BanList *BanList
	// Eclipse limits peers per subnet/ASN to mitigate eclipse attacks.
	Eclipse *EclipseLimiter
	// RateLimiter throttles handshake/connection attempts per peer.
	RateLimiter *PeerRateLimiter
	// Misbehavior tracks per-peer misbehavior scores, driving BanList decisions.
	Misbehavior *PeerMisbehaviorScorer
	// AddressBucketStore provides Sybil-resistant, subnet-bucketed address storage
	// with randomized eviction, feeding evictions back into the PeerStore.
	AddressBucketStore *AddressBucketStore
	// ResourceCap limits per-peer resource usage (e.g. concurrent streams).
	ResourceCap *PeerResourceCap
}
