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

// NonceCache stores nonces per peer with auto-expunge of stale entries.
// Peer entries with no activity for ExpungeAfter are removed. ExpungeAfter must be 1-5 min.
type NonceCache struct {
	mu               sync.RWMutex
	byPeer           map[peer.ID]*peerNonceEntry
	expungeAfter     time.Duration
	expungeInterval  time.Duration
	stop             chan struct{}
	stopOnce         sync.Once
}

type peerNonceEntry struct {
	nonces map[uint64]struct{}
	updated time.Time
}

// NonceCacheOption configures NonceCache.
type NonceCacheOption func(*NonceCache)

// NonceExpungeAfter sets the idle duration after which peer entries are expunged (1-5 min).
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
func nonceExpungeAfterForTest(d time.Duration) NonceCacheOption {
	return func(c *NonceCache) {
		if d > 0 {
			c.expungeAfter = d
		}
	}
}

// NonceExpungeInterval sets how often the expunge loop runs.
func NonceExpungeInterval(d time.Duration) NonceCacheOption {
	return func(c *NonceCache) {
		if d > 0 {
			c.expungeInterval = d
		}
	}
}

// NewNonceCache creates a per-peer nonce cache with auto-expunge.
func NewNonceCache(opts ...NonceCacheOption) *NonceCache {
	c := &NonceCache{
		byPeer:          make(map[peer.ID]*peerNonceEntry),
		expungeAfter:    DefaultNonceExpungeAfter,
		expungeInterval: DefaultNonceExpungeInterval,
		stop:            make(chan struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Add records a nonce for the peer and updates last-access time.
func (c *NonceCache) Add(pid peer.ID, nonce uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byPeer[pid]
	if !ok {
		e = &peerNonceEntry{nonces: make(map[uint64]struct{})}
		c.byPeer[pid] = e
	}
	e.nonces[nonce] = struct{}{}
	e.updated = time.Now()
}

// RecordNonce records a nonce for the peer. Returns ErrReusedNonce if already seen.
func (c *NonceCache) RecordNonce(pid peer.ID, nonce uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byPeer[pid]
	if !ok {
		e = &peerNonceEntry{nonces: make(map[uint64]struct{})}
		c.byPeer[pid] = e
	}
	if _, seen := e.nonces[nonce]; seen {
		return ErrReusedNonce
	}
	e.nonces[nonce] = struct{}{}
	e.updated = time.Now()
	return nil
}

// Seen returns true if the nonce was already recorded for this peer.
func (c *NonceCache) Seen(pid peer.ID, nonce uint64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byPeer[pid]
	if !ok {
		return false
	}
	_, seen := e.nonces[nonce]
	return seen
}

// Start runs the auto-expunge loop. It exits when ctx is cancelled.
func (c *NonceCache) Start(ctx context.Context) {
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
func (c *NonceCache) Stop() {
	c.stopOnce.Do(func() { close(c.stop) })
}

// expunge removes peer entries that have been idle for at least expungeAfter.
func (c *NonceCache) expunge() {
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

// Peers returns the current number of peer entries (for tests).
func (c *NonceCache) Peers() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byPeer)
}

// MessageHashCache stores message hashes per peer and rejects duplicates.
// Uses the same auto-expunge semantics as NonceCache (1-5 min).
type MessageHashCache struct {
	mu               sync.RWMutex
	byPeer           map[peer.ID]*peerHashEntry
	expungeAfter     time.Duration
	expungeInterval  time.Duration
	stop             chan struct{}
	stopOnce         sync.Once
}

type peerHashEntry struct {
	hashes  map[string]struct{}
	updated time.Time
}

// MessageHashCacheOption configures MessageHashCache.
type MessageHashCacheOption func(*MessageHashCache)

// MessageHashExpungeAfter sets the idle duration (1-5 min).
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

func messageHashExpungeAfterForTest(d time.Duration) MessageHashCacheOption {
	return func(c *MessageHashCache) {
		if d > 0 {
			c.expungeAfter = d
		}
	}
}

// MessageHashExpungeInterval sets how often the expunge loop runs.
func MessageHashExpungeInterval(d time.Duration) MessageHashCacheOption {
	return func(c *MessageHashCache) {
		if d > 0 {
			c.expungeInterval = d
		}
	}
}

// NewMessageHashCache creates a per-peer message-hash cache with auto-expunge.
func NewMessageHashCache(opts ...MessageHashCacheOption) *MessageHashCache {
	c := &MessageHashCache{
		byPeer:          make(map[peer.ID]*peerHashEntry),
		expungeAfter:    DefaultNonceExpungeAfter,
		expungeInterval: DefaultNonceExpungeInterval,
		stop:            make(chan struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// RecordHash records a message hash for the peer. Returns ErrDuplicateMessageHash if already seen.
func (c *MessageHashCache) RecordHash(pid peer.ID, hash []byte) error {
	key := hex.EncodeToString(hash)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byPeer[pid]
	if !ok {
		e = &peerHashEntry{hashes: make(map[string]struct{})}
		c.byPeer[pid] = e
	}
	if _, seen := e.hashes[key]; seen {
		return ErrDuplicateMessageHash
	}
	e.hashes[key] = struct{}{}
	e.updated = time.Now()
	return nil
}

// SeenHash returns true if the hash was already recorded for this peer.
func (c *MessageHashCache) SeenHash(pid peer.ID, hash []byte) bool {
	key := hex.EncodeToString(hash)
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byPeer[pid]
	if !ok {
		return false
	}
	_, seen := e.hashes[key]
	return seen
}

// Start runs the auto-expunge loop. Exits when ctx is cancelled.
func (c *MessageHashCache) Start(ctx context.Context) {
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
func (c *MessageHashCache) Stop() {
	c.stopOnce.Do(func() { close(c.stop) })
}

func (c *MessageHashCache) expunge() {
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

// HashPeers returns the current number of peer entries (for tests).
func (c *MessageHashCache) HashPeers() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byPeer)
}

// TimestampChecker validates message timestamps against a configurable window.
// Rejects timestamps too far in the past (replay) or too far in the future (clock skew).
type TimestampChecker struct {
	Window      time.Duration
	FutureAllow time.Duration
	nowFunc     func() time.Time
}

// TimestampCheckerOption configures TimestampChecker.
type TimestampCheckerOption func(*TimestampChecker)

// TimestampWindow sets how far in the past timestamps are accepted.
func TimestampWindow(d time.Duration) TimestampCheckerOption {
	return func(c *TimestampChecker) {
		if d > 0 {
			c.Window = d
		}
	}
}

// TimestampFutureAllow sets how far in the future to allow (clock skew).
func TimestampFutureAllow(d time.Duration) TimestampCheckerOption {
	return func(c *TimestampChecker) {
		if d >= 0 {
			c.FutureAllow = d
		}
	}
}

// timestampNowFuncForTest injects time for tests.
func timestampNowFuncForTest(fn func() time.Time) TimestampCheckerOption {
	return func(c *TimestampChecker) {
		if fn != nil {
			c.nowFunc = fn
		}
	}
}

// NewTimestampChecker creates a checker with configurable window.
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
func (c *TimestampChecker) RejectExpiredUnix(ts int64) error {
	return c.RejectExpired(time.Unix(ts, 0))
}
