// Purpose: Per-peer nonce and message-hash cache with auto-expunge for anti-replay (Phase 6.2).

package net

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// ErrDuplicateMessageHash is returned when a message hash has already been recorded.
var ErrDuplicateMessageHash = errors.New("duplicate message hash")

// ErrExpiredTimestamp is returned when a timestamp is outside the acceptable window.
var ErrExpiredTimestamp = errors.New("expired timestamp")

// ErrReusedNonce is returned when a nonce has already been recorded (replay).
var ErrReusedNonce = errors.New("reused nonce")

const (
	// DefaultNonceExpungeAfter is the default time after which idle peer entries are removed.
	DefaultNonceExpungeAfter = 3 * time.Minute
	// MinNonceExpungeAfter is the minimum allowed expunge interval (1 min).
	MinNonceExpungeAfter = 1 * time.Minute
	// MaxNonceExpungeAfter is the maximum allowed expunge interval (5 min).
	MaxNonceExpungeAfter = 5 * time.Minute
	// DefaultNonceExpungeInterval is how often the expunge loop runs.
	DefaultNonceExpungeInterval = 30 * time.Second
	// DefaultTimestampWindow is how far in the past timestamps are accepted.
	DefaultTimestampWindow = 5 * time.Minute
	// DefaultTimestampFutureAllow is how far in the future to allow (clock skew).
	DefaultTimestampFutureAllow = 1 * time.Minute
)

// peerSetEntry tracks the set of items (nonces, hex-encoded hashes, ...) seen
// for a single peer and when that peer's entry was last updated (used to
// decide when to expunge it).
type peerSetEntry[K comparable] struct {
	items   map[K]struct{}
	updated time.Time
}

// expiringPeerCache is the shared per-peer, auto-expunging set implementation
// backing both NonceCache (K = uint64) and MessageHashCache (K = string, the
// hex-encoded message digest). It holds one set of K per peer.ID and removes a
// peer's entire entry once it has been idle for expungeAfter, checked every
// expungeInterval by Start's loop. NonceCache and MessageHashCache embed this
// type and expose their own typed, documented methods (RecordNonce/Add/Seen,
// RecordHash/SeenHash) on top of it; the exported Start/Stop are promoted
// as-is since expunge timing is identical for both.
type expiringPeerCache[K comparable] struct {
	mu              sync.RWMutex
	byPeer          map[peer.ID]*peerSetEntry[K]
	expungeAfter    time.Duration
	expungeInterval time.Duration
	stop            chan struct{}
	stopOnce        sync.Once
}

// add records key for pid and updates the peer's last-access time, without
// reporting whether key was already present.
//
// Parameters:
//   - pid (peer.ID): the peer the key is associated with.
//   - key (K): the item to record.
func (c *expiringPeerCache[K]) add(pid peer.ID, key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byPeer[pid]
	if !ok {
		e = &peerSetEntry[K]{items: make(map[K]struct{})}
		c.byPeer[pid] = e
	}
	e.items[key] = struct{}{}
	e.updated = time.Now()
}

// recordIfNew records key for pid unless already present.
//
// Parameters:
//   - pid (peer.ID): the peer the key is associated with.
//   - key (K): the item to check and record.
//
// Returns:
//   - bool: true if key was already recorded for pid (and thus was not re-recorded), false if it was newly recorded.
func (c *expiringPeerCache[K]) recordIfNew(pid peer.ID, key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byPeer[pid]
	if !ok {
		e = &peerSetEntry[K]{items: make(map[K]struct{})}
		c.byPeer[pid] = e
	}
	if _, seen := e.items[key]; seen {
		return true
	}
	e.items[key] = struct{}{}
	e.updated = time.Now()
	return false
}

// seen reports whether key has already been recorded for pid, without
// recording it.
//
// Parameters:
//   - pid (peer.ID): the peer to check.
//   - key (K): the item to check.
//
// Returns:
//   - bool: true if key has already been recorded for pid.
func (c *expiringPeerCache[K]) seen(pid peer.ID, key K) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byPeer[pid]
	if !ok {
		return false
	}
	_, seen := e.items[key]
	return seen
}

// peerCount returns the current number of peer entries.
//
// Returns:
//   - int: the number of distinct peers with tracked entries.
func (c *expiringPeerCache[K]) peerCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byPeer)
}

// Start runs the auto-expunge loop, removing peer entries idle for at least
// expungeAfter on every tick of expungeInterval. It exits when ctx is
// cancelled or Stop is called; intended to be run in its own goroutine.
//
// Parameters:
//   - ctx (context.Context): cancelling ctx stops the loop.
func (c *expiringPeerCache[K]) Start(ctx context.Context) {
	ticker := time.NewTicker(c.expungeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		case <-ticker.C:
			c.expunge()
		}
	}
}

// Stop stops the auto-expunge loop. Idempotent.
func (c *expiringPeerCache[K]) Stop() {
	c.stopOnce.Do(func() { close(c.stop) })
}

// expunge removes peer entries that have been idle for at least expungeAfter.
func (c *expiringPeerCache[K]) expunge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-c.expungeAfter)
	for pid, e := range c.byPeer {
		if e.updated.Before(cutoff) {
			delete(c.byPeer, pid)
		}
	}
}

// NonceCache stores nonces per peer with auto-expunge of stale entries.
// Peer entries with no activity for ExpungeAfter are removed. ExpungeAfter must be 1-5 min.
type NonceCache struct {
	expiringPeerCache[uint64]
}

// NonceCacheOption configures NonceCache.
type NonceCacheOption func(*NonceCache)

// NonceExpungeAfter sets the idle duration after which peer entries are expunged (1-5 min).
// Values outside [MinNonceExpungeAfter, MaxNonceExpungeAfter] are clamped.
//
// Parameters:
//   - d (time.Duration): desired idle duration; clamped to [MinNonceExpungeAfter, MaxNonceExpungeAfter].
//
// Returns:
//   - NonceCacheOption: an option that applies the clamped duration to a NonceCache.
func NonceExpungeAfter(d time.Duration) NonceCacheOption {
	return func(c *NonceCache) {
		if d < MinNonceExpungeAfter {
			d = MinNonceExpungeAfter
		}
		if d > MaxNonceExpungeAfter {
			d = MaxNonceExpungeAfter
		}
		c.expungeAfter = d
	}
}

// nonceExpungeAfterForTest sets expunge interval without clamping (test use only).
//
// Parameters:
//   - d (time.Duration): the idle duration to apply verbatim if positive.
//
// Returns:
//   - NonceCacheOption: an option that applies d to a NonceCache without clamping.
func nonceExpungeAfterForTest(d time.Duration) NonceCacheOption {
	return func(c *NonceCache) {
		if d > 0 {
			c.expungeAfter = d
		}
	}
}

// NonceExpungeInterval sets how often the expunge loop runs.
//
// Parameters:
//   - d (time.Duration): the polling interval for the expunge loop, applied if positive.
//
// Returns:
//   - NonceCacheOption: an option that applies the interval to a NonceCache.
func NonceExpungeInterval(d time.Duration) NonceCacheOption {
	return func(c *NonceCache) {
		if d > 0 {
			c.expungeInterval = d
		}
	}
}

// NewNonceCache creates a per-peer nonce cache with auto-expunge.
//
// Parameters:
//   - opts (...NonceCacheOption): functional options overriding the defaults (DefaultNonceExpungeAfter, DefaultNonceExpungeInterval).
//
// Returns:
//   - *NonceCache: a configured cache; call Start to run its expunge loop.
func NewNonceCache(opts ...NonceCacheOption) *NonceCache {
	c := &NonceCache{
		expiringPeerCache: expiringPeerCache[uint64]{
			byPeer:          make(map[peer.ID]*peerSetEntry[uint64]),
			expungeAfter:    DefaultNonceExpungeAfter,
			expungeInterval: DefaultNonceExpungeInterval,
			stop:            make(chan struct{}),
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Add records a nonce for the peer and updates last-access time. Unlike RecordNonce,
// it does not report whether the nonce was already present.
//
// Parameters:
//   - pid (peer.ID): the peer the nonce is associated with.
//   - nonce (uint64): the nonce value to record.
func (c *NonceCache) Add(pid peer.ID, nonce uint64) {
	c.add(pid, nonce)
}

// RecordNonce records a nonce for the peer. Returns ErrReusedNonce if already seen.
//
// Parameters:
//   - pid (peer.ID): the peer the nonce is associated with.
//   - nonce (uint64): the nonce value to check and record.
//
// Returns:
//   - error: ErrReusedNonce if this nonce was already recorded for pid, nil otherwise.
func (c *NonceCache) RecordNonce(pid peer.ID, nonce uint64) error {
	if c.recordIfNew(pid, nonce) {
		return ErrReusedNonce
	}
	return nil
}

// Seen returns true if the nonce was already recorded for this peer. Unlike
// RecordNonce, this is a read-only check that does not record the nonce.
//
// Parameters:
//   - pid (peer.ID): the peer to check.
//   - nonce (uint64): the nonce value to check.
//
// Returns:
//   - bool: true if nonce has already been recorded for pid.
func (c *NonceCache) Seen(pid peer.ID, nonce uint64) bool {
	return c.seen(pid, nonce)
}

// Peers returns the current number of peer entries (for tests).
//
// Returns:
//   - int: the number of distinct peers with tracked nonce entries.
func (c *NonceCache) Peers() int {
	return c.peerCount()
}

// MessageHashCache stores message hashes per peer and rejects duplicates.
// Uses the same auto-expunge semantics as NonceCache (1-5 min).
type MessageHashCache struct {
	expiringPeerCache[string]
}

// MessageHashCacheOption configures MessageHashCache.
type MessageHashCacheOption func(*MessageHashCache)

// MessageHashExpungeAfter sets the idle duration (1-5 min). Values outside
// [MinNonceExpungeAfter, MaxNonceExpungeAfter] are clamped.
//
// Parameters:
//   - d (time.Duration): desired idle duration; clamped to [MinNonceExpungeAfter, MaxNonceExpungeAfter].
//
// Returns:
//   - MessageHashCacheOption: an option that applies the clamped duration to a MessageHashCache.
func MessageHashExpungeAfter(d time.Duration) MessageHashCacheOption {
	return func(c *MessageHashCache) {
		if d < MinNonceExpungeAfter {
			d = MinNonceExpungeAfter
		}
		if d > MaxNonceExpungeAfter {
			d = MaxNonceExpungeAfter
		}
		c.expungeAfter = d
	}
}

// messageHashExpungeAfterForTest sets the expunge duration without clamping (test use only).
//
// Parameters:
//   - d (time.Duration): the idle duration to apply verbatim if positive.
//
// Returns:
//   - MessageHashCacheOption: an option that applies d to a MessageHashCache without clamping.
func messageHashExpungeAfterForTest(d time.Duration) MessageHashCacheOption {
	return func(c *MessageHashCache) {
		if d > 0 {
			c.expungeAfter = d
		}
	}
}

// MessageHashExpungeInterval sets how often the expunge loop runs.
//
// Parameters:
//   - d (time.Duration): the polling interval for the expunge loop, applied if positive.
//
// Returns:
//   - MessageHashCacheOption: an option that applies the interval to a MessageHashCache.
func MessageHashExpungeInterval(d time.Duration) MessageHashCacheOption {
	return func(c *MessageHashCache) {
		if d > 0 {
			c.expungeInterval = d
		}
	}
}

// NewMessageHashCache creates a per-peer message-hash cache with auto-expunge.
//
// Parameters:
//   - opts (...MessageHashCacheOption): functional options overriding the defaults (DefaultNonceExpungeAfter, DefaultNonceExpungeInterval).
//
// Returns:
//   - *MessageHashCache: a configured cache; call Start to run its expunge loop.
func NewMessageHashCache(opts ...MessageHashCacheOption) *MessageHashCache {
	c := &MessageHashCache{
		expiringPeerCache: expiringPeerCache[string]{
			byPeer:          make(map[peer.ID]*peerSetEntry[string]),
			expungeAfter:    DefaultNonceExpungeAfter,
			expungeInterval: DefaultNonceExpungeInterval,
			stop:            make(chan struct{}),
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// RecordHash records a message hash for the peer. Returns ErrDuplicateMessageHash if already seen.
// The hash bytes are hex-encoded for use as a map key.
//
// Parameters:
//   - pid (peer.ID): the peer the message is associated with.
//   - hash ([]byte): the message digest (e.g. SHA-256 sum) to check and record.
//
// Returns:
//   - error: ErrDuplicateMessageHash if this hash was already recorded for pid, nil otherwise.
func (c *MessageHashCache) RecordHash(pid peer.ID, hash []byte) error {
	if c.recordIfNew(pid, hex.EncodeToString(hash)) {
		return ErrDuplicateMessageHash
	}
	return nil
}

// SeenHash returns true if the hash was already recorded for this peer. Unlike
// RecordHash, this is a read-only check that does not record the hash.
//
// Parameters:
//   - pid (peer.ID): the peer to check.
//   - hash ([]byte): the message digest to check.
//
// Returns:
//   - bool: true if hash has already been recorded for pid.
func (c *MessageHashCache) SeenHash(pid peer.ID, hash []byte) bool {
	return c.seen(pid, hex.EncodeToString(hash))
}

// HashPeers returns the current number of peer entries (for tests).
//
// Returns:
//   - int: the number of distinct peers with tracked hash entries.
func (c *MessageHashCache) HashPeers() int {
	return c.peerCount()
}

// TimestampChecker validates message timestamps against a configurable window.
// Rejects timestamps too far in the past (replay) or too far in the future (clock skew).
type TimestampChecker struct {
	// Window is how far in the past a timestamp may be and still be accepted.
	Window time.Duration
	// FutureAllow is how far in the future a timestamp may be (to tolerate clock skew)
	// and still be accepted.
	FutureAllow time.Duration
	nowFunc     func() time.Time
}

// TimestampCheckerOption configures TimestampChecker.
type TimestampCheckerOption func(*TimestampChecker)

// TimestampWindow sets how far in the past timestamps are accepted.
//
// Parameters:
//   - d (time.Duration): the acceptable past-window duration, applied if positive.
//
// Returns:
//   - TimestampCheckerOption: an option that applies the window to a TimestampChecker.
func TimestampWindow(d time.Duration) TimestampCheckerOption {
	return func(c *TimestampChecker) {
		if d > 0 {
			c.Window = d
		}
	}
}

// TimestampFutureAllow sets how far in the future to allow (clock skew).
//
// Parameters:
//   - d (time.Duration): the acceptable future-skew duration, applied if non-negative.
//
// Returns:
//   - TimestampCheckerOption: an option that applies the future allowance to a TimestampChecker.
func TimestampFutureAllow(d time.Duration) TimestampCheckerOption {
	return func(c *TimestampChecker) {
		if d >= 0 {
			c.FutureAllow = d
		}
	}
}

// timestampNowFuncForTest injects time for tests.
//
// Parameters:
//   - fn (func() time.Time): replacement clock function, applied if non-nil.
//
// Returns:
//   - TimestampCheckerOption: an option that overrides the checker's clock source.
func timestampNowFuncForTest(fn func() time.Time) TimestampCheckerOption {
	return func(c *TimestampChecker) {
		if fn != nil {
			c.nowFunc = fn
		}
	}
}

// NewTimestampChecker creates a checker with configurable window.
//
// Parameters:
//   - opts (...TimestampCheckerOption): functional options overriding the defaults (DefaultTimestampWindow, DefaultTimestampFutureAllow).
//
// Returns:
//   - *TimestampChecker: a configured checker.
func NewTimestampChecker(opts ...TimestampCheckerOption) *TimestampChecker {
	c := &TimestampChecker{
		Window:      DefaultTimestampWindow,
		FutureAllow: DefaultTimestampFutureAllow,
		nowFunc:     time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// RejectExpired returns ErrExpiredTimestamp if t is outside the acceptable window.
//
// Parameters:
//   - t (time.Time): the timestamp to validate against [now-Window, now+FutureAllow].
//
// Returns:
//   - error: ErrExpiredTimestamp if t is before the window's oldest bound or after its newest bound, nil otherwise.
func (c *TimestampChecker) RejectExpired(t time.Time) error {
	now := c.nowFunc()
	oldest := now.Add(-c.Window)
	newest := now.Add(c.FutureAllow)
	if t.Before(oldest) || t.After(newest) {
		return ErrExpiredTimestamp
	}
	return nil
}

// RejectExpiredUnix returns ErrExpiredTimestamp if the Unix timestamp (seconds) is outside the window.
//
// Parameters:
//   - ts (int64): Unix timestamp in seconds to validate.
//
// Returns:
//   - error: ErrExpiredTimestamp if the corresponding time is outside the acceptable window, nil otherwise.
func (c *TimestampChecker) RejectExpiredUnix(ts int64) error {
	return c.RejectExpired(time.Unix(ts, 0))
}
