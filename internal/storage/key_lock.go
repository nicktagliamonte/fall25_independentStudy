// Purpose: Key lock structure and manager for mutual exclusion on keys. Per planTwo Phase 4.1:
// mutual exclusion by locking the key for h(data). Lock key: "/locks/" + hex(key).
// Uses datastore for local locking (DHT restricts custom namespaces).

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	ds "github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
)

// LockNamespace is the DHT key prefix for lock records.
const LockNamespace = "/locks/"

// DefaultLockTTL is the default lock TTL (5 minutes per planTwo).
const DefaultLockTTL = 5 * time.Minute

// ErrLockHeldByAnother indicates the key is locked by another peer (retriable).
var ErrLockHeldByAnother = errors.New("lock held by another peer")

// ErrLockTimeout indicates the lock was not acquired within the configured timeout.
var ErrLockTimeout = errors.New("lock not acquired within timeout")

// Default retry parameters for AcquireLockWithRetry.
const (
	DefaultLockRetryInitialBackoff = 50 * time.Millisecond
	DefaultLockRetryMaxBackoff     = 5 * time.Second
	DefaultLockRetryTimeout        = 30 * time.Second
)

// KeyLock represents a lock held on a key for mutual exclusion during concurrent writes.
type KeyLock struct {
	// Key is the key being locked (hash of data).
	Key Key
	// LockHolder is the peer ID of the holder.
	LockHolder peer.ID
	// AcquiredAt is when the lock was acquired.
	AcquiredAt time.Time
	// ExpiresAt is when the lock expires (TTL).
	ExpiresAt time.Time
	// Version is for conflict resolution.
	Version int
}

// keyLockJSON is a helper struct for JSON serialization.
type keyLockJSON struct {
	Key        string `json:"key"`
	LockHolder string `json:"lock_holder"`
	AcquiredAt int64  `json:"acquired_at_ns"`
	ExpiresAt  int64  `json:"expires_at_ns"`
	Version    int    `json:"version"`
}

// Marshal serializes KeyLock to JSON bytes.
//
// Returns:
//   - []byte: the JSON encoding of the lock, or nil if l is nil.
//   - error: non-nil if JSON marshaling fails.
func (l *KeyLock) Marshal() ([]byte, error) {
	if l == nil {
		return nil, nil
	}
	j := keyLockJSON{
		Key:        l.Key.String(),
		LockHolder: l.LockHolder.String(),
		AcquiredAt: l.AcquiredAt.UnixNano(),
		ExpiresAt:  l.ExpiresAt.UnixNano(),
		Version:    l.Version,
	}
	return json.Marshal(j)
}

// Unmarshal deserializes JSON bytes into KeyLock, overwriting its fields in place.
//
// Parameters:
//   - data ([]byte): the JSON-encoded lock; a zero-length slice is treated as a no-op.
//
// Returns:
//   - error: non-nil if data cannot be unmarshaled, or if the embedded key or
//     lock holder peer ID fail to parse.
func (l *KeyLock) Unmarshal(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	var j keyLockJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	key, err := ParseKey(j.Key)
	if err != nil {
		return err
	}
	holder, err := peer.Decode(j.LockHolder)
	if err != nil {
		return err
	}
	l.Key = key
	l.LockHolder = holder
	l.AcquiredAt = time.Unix(0, j.AcquiredAt)
	l.ExpiresAt = time.Unix(0, j.ExpiresAt)
	l.Version = j.Version
	return nil
}

// lockStore abstracts lock storage (DHT or datastore) so KeyLockManager can be
// backed by either without duplicating lock logic.
type lockStore interface {
	// get retrieves the raw bytes stored at key, or (nil, nil) if absent.
	get(ctx context.Context, key string) ([]byte, error)
	// put stores val at key, overwriting any existing value.
	put(ctx context.Context, key string, val []byte) error
}

// dhtLockStore implements lockStore on top of a libp2p DHT routing.ValueStore.
type dhtLockStore struct{ dht routing.ValueStore }

// get retrieves the value at key from the DHT.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the DHT read.
//   - key (string): the DHT key to read.
//
// Returns:
//   - []byte: the stored value.
//   - error: non-nil if the underlying DHT GetValue call fails.
func (s dhtLockStore) get(ctx context.Context, key string) ([]byte, error) {
	return s.dht.GetValue(ctx, key)
}

// put stores val at key in the DHT.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the DHT write.
//   - key (string): the DHT key to write.
//   - val ([]byte): the value to store.
//
// Returns:
//   - error: non-nil if the underlying DHT PutValue call fails.
func (s dhtLockStore) put(ctx context.Context, key string, val []byte) error {
	return s.dht.PutValue(ctx, key, val)
}

// dsLockStore implements lockStore on top of a local ds.Datastore.
type dsLockStore struct{ d ds.Datastore }

// get retrieves the value at key from the datastore, translating ds.ErrNotFound
// into a (nil, nil) "absent" result rather than an error.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the datastore read.
//   - key (string): the datastore key to read.
//
// Returns:
//   - []byte: the stored value, or nil if not found.
//   - error: non-nil if the read fails for a reason other than "not found".
func (s dsLockStore) get(ctx context.Context, key string) ([]byte, error) {
	val, err := s.d.Get(ctx, ds.NewKey(key))
	if err == ds.ErrNotFound {
		return nil, nil
	}
	return val, err
}

// put stores val at key in the datastore.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the datastore write.
//   - key (string): the datastore key to write.
//   - val ([]byte): the value to store.
//
// Returns:
//   - error: non-nil if the underlying datastore Put call fails.
func (s dsLockStore) put(ctx context.Context, key string, val []byte) error {
	return s.d.Put(ctx, ds.NewKey(key), val)
}

// KeyLockManager manages locks for mutual exclusion, backed by either a DHT or
// a local datastore depending on how it was constructed.
type KeyLockManager struct {
	store lockStore
	// DefaultTTL is the lock duration used when callers pass ttl <= 0 to
	// AcquireLock/AcquireLockWithRetry/ExtendLock.
	DefaultTTL time.Duration
}

// KeyLockManagerOption configures KeyLockManager at construction time.
type KeyLockManagerOption func(*KeyLockManager)

// WithDefaultTTL returns a KeyLockManagerOption that sets the manager's default lock TTL.
//
// Parameters:
//   - ttl (time.Duration): the default TTL to apply when callers don't specify one.
//
// Returns:
//   - KeyLockManagerOption: an option that sets m.DefaultTTL to ttl.
func WithDefaultTTL(ttl time.Duration) KeyLockManagerOption {
	return func(m *KeyLockManager) {
		m.DefaultTTL = ttl
	}
}

// NewKeyLockManager creates a KeyLockManager backed by a DHT ValueStore, so
// locks are visible to (and contested by) all peers sharing the same DHT.
//
// Parameters:
//   - dht (routing.ValueStore): the DHT value store to read/write lock records to.
//   - opts (...KeyLockManagerOption): optional configuration, applied in order.
//
// Returns:
//   - *KeyLockManager: a manager using DefaultLockTTL unless overridden by opts.
func NewKeyLockManager(dht routing.ValueStore, opts ...KeyLockManagerOption) *KeyLockManager {
	m := &KeyLockManager{store: dhtLockStore{dht: dht}, DefaultTTL: DefaultLockTTL}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// NewKeyLockManagerFromDatastore creates a KeyLockManager backed by a local
// datastore, suitable for single-node use or tests where locks need not be
// shared across peers.
//
// Parameters:
//   - d (ds.Datastore): the local datastore to read/write lock records to.
//   - opts (...KeyLockManagerOption): optional configuration, applied in order.
//
// Returns:
//   - *KeyLockManager: a manager using DefaultLockTTL unless overridden by opts.
func NewKeyLockManagerFromDatastore(d ds.Datastore, opts ...KeyLockManagerOption) *KeyLockManager {
	m := &KeyLockManager{store: dsLockStore{d: d}, DefaultTTL: DefaultLockTTL}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// lockDHTKey returns the storage key for a key's lock record.
//
// Parameters:
//   - k (Key): the content key being locked.
//
// Returns:
//   - string: the fully-qualified lock key ("/locks/" + hex(k)).
func lockDHTKey(k Key) string {
	return LockNamespace + k.String()
}

// getLock reads and unmarshals the current lock record for key, if any.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the underlying read.
//   - key (Key): the content key whose lock record to fetch; must be non-zero.
//
// Returns:
//   - *KeyLock: the current lock record, or nil if none exists or the stored
//     value is empty/unreadable (treated as "no lock" rather than an error).
//   - error: non-nil if m.store is nil, key is zero, or the stored bytes fail
//     to unmarshal into a KeyLock.
func (m *KeyLockManager) getLock(ctx context.Context, key Key) (*KeyLock, error) {
	if m.store == nil {
		return nil, errors.New("lock store required")
	}
	if key.IsZero() {
		return nil, errors.New("key cannot be zero")
	}
	data, err := m.store.get(ctx, lockDHTKey(key))
	if err != nil || len(data) == 0 {
		return nil, nil
	}
	var lock KeyLock
	if err := lock.Unmarshal(data); err != nil {
		return nil, fmt.Errorf("unmarshal lock: %w", err)
	}
	return &lock, nil
}

// putLock marshals and writes a lock record to the backing store.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the underlying write.
//   - lock (*KeyLock): the lock record to store; must be non-nil with a non-zero Key.
//
// Returns:
//   - error: non-nil if m.store is nil, lock is invalid, marshaling fails, or
//     the underlying store write fails.
func (m *KeyLockManager) putLock(ctx context.Context, lock *KeyLock) error {
	if m.store == nil {
		return errors.New("lock store required")
	}
	if lock == nil || lock.Key.IsZero() {
		return errors.New("invalid lock")
	}
	data, err := lock.Marshal()
	if err != nil {
		return fmt.Errorf("marshal lock: %w", err)
	}
	return m.store.put(ctx, lockDHTKey(lock.Key), data)
}

// AcquireLock acquires a lock on the key for the holder, failing immediately
// if another peer already holds an unexpired lock. If the current holder is
// the same peer.ID (or no lock exists, or the existing lock has expired), a
// new lock record is written with a bumped version and a fresh AcquiredAt/ExpiresAt.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the underlying read/write.
//   - key (Key): the content key to lock; must be non-zero.
//   - holder (peer.ID): the peer requesting/renewing the lock.
//   - ttl (time.Duration): lock duration; values <= 0 use m.DefaultTTL, falling
//     back to DefaultLockTTL if that is also unset.
//
// Returns:
//   - error: ErrLockHeldByAnother (wrapped) if an unexpired lock is held by a
//     different peer; otherwise nil on success, or a store error if the read/write fails.
func (m *KeyLockManager) AcquireLock(ctx context.Context, key Key, holder peer.ID, ttl time.Duration) error {
	if key.IsZero() {
		return errors.New("key cannot be zero")
	}
	if ttl <= 0 {
		ttl = m.DefaultTTL
		if ttl <= 0 {
			ttl = DefaultLockTTL
		}
	}
	existing, err := m.getLock(ctx, key)
	if err != nil {
		return err
	}
	now := time.Now()
	if existing != nil && existing.ExpiresAt.After(now) && existing.LockHolder != holder {
		return fmt.Errorf("key locked by %s: %w", existing.LockHolder, ErrLockHeldByAnother)
	}
	version := 0
	if existing != nil {
		version = existing.Version + 1
	}
	lock := &KeyLock{
		Key:        key,
		LockHolder: holder,
		AcquiredAt: now,
		ExpiresAt:  now.Add(ttl),
		Version:    version,
	}
	return m.putLock(ctx, lock)
}

// LockRetryConfig configures exponential backoff for lock acquisition retries.
type LockRetryConfig struct {
	// InitialBackoff is the first sleep duration between retries.
	InitialBackoff time.Duration
	// MaxBackoff caps the sleep duration; backoff doubles each retry up to this cap.
	MaxBackoff time.Duration
	// Timeout is the overall deadline for acquisition; 0 means use the default.
	Timeout time.Duration
}

// AcquireLockWithRetry acquires a lock with exponential backoff retry on ErrLockHeldByAnother.
// Non-retriable errors (e.g. key zero, store failure) are returned immediately
// without retrying. The effective deadline is the earlier of (now + timeout)
// and ctx's own deadline, if any; retries stop and ErrLockTimeout is returned
// once that deadline passes. Respects ctx cancellation during the backoff sleep.
//
// Parameters:
//   - ctx (context.Context): controls cancellation and can supply an earlier deadline.
//   - key (Key): the content key to lock; must be non-zero.
//   - holder (peer.ID): the peer requesting the lock.
//   - ttl (time.Duration): lock duration passed through to each AcquireLock attempt.
//   - cfg (*LockRetryConfig): backoff/timeout configuration; nil uses
//     DefaultLockRetryInitialBackoff, DefaultLockRetryMaxBackoff, and
//     DefaultLockRetryTimeout. Zero-valued fields within a non-nil cfg also
//     fall back to those defaults individually.
//
// Returns:
//   - error: nil on success; ErrLockTimeout (wrapped, with the last underlying
//     error) if not acquired before the deadline; the ctx error (wrapped) if
//     cancelled during backoff; or any non-retriable error from AcquireLock.
func (m *KeyLockManager) AcquireLockWithRetry(ctx context.Context, key Key, holder peer.ID, ttl time.Duration, cfg *LockRetryConfig) error {
	initial := DefaultLockRetryInitialBackoff
	maxB := DefaultLockRetryMaxBackoff
	timeout := DefaultLockRetryTimeout
	if cfg != nil {
		if cfg.InitialBackoff > 0 {
			initial = cfg.InitialBackoff
		}
		if cfg.MaxBackoff > 0 {
			maxB = cfg.MaxBackoff
		}
		if cfg.Timeout > 0 {
			timeout = cfg.Timeout
		}
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	backoff := initial
	for {
		err := m.AcquireLock(ctx, key, holder, ttl)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrLockHeldByAnother) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: not acquired within %v (last: %v)", ErrLockTimeout, timeout, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		case <-time.After(backoff):
			backoff *= 2
			if backoff > maxB {
				backoff = maxB
			}
		}
	}
}

// ReleaseLock releases the lock on the key if the holder matches.
// A no-op (nil error) if there is no lock, or the existing lock has already expired.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the underlying read/write.
//   - key (Key): the content key to release; must be non-zero.
//   - holder (peer.ID): the peer that must match the current lock holder.
//
// Returns:
//   - error: non-nil if key is zero, the lock read fails, or the lock is held
//     by a different peer than holder; nil otherwise (including the no-op cases).
func (m *KeyLockManager) ReleaseLock(ctx context.Context, key Key, holder peer.ID) error {
	if key.IsZero() {
		return errors.New("key cannot be zero")
	}
	existing, err := m.getLock(ctx, key)
	if err != nil {
		return err
	}
	if existing == nil || existing.ExpiresAt.Before(time.Now()) {
		return nil
	}
	if existing.LockHolder != holder {
		return fmt.Errorf("lock held by %s, not %s", existing.LockHolder, holder)
	}
	return m.store.put(ctx, lockDHTKey(key), []byte{})
}

// IsLocked returns whether the key is locked and the holder peer ID (empty if
// not locked or expired).
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the underlying read.
//   - key (Key): the content key to check.
//
// Returns:
//   - bool: true if an unexpired lock record exists for key.
//   - peer.ID: the current holder, or the empty peer.ID if not locked, expired,
//     or the lookup errored.
func (m *KeyLockManager) IsLocked(ctx context.Context, key Key) (bool, peer.ID) {
	lock, err := m.getLock(ctx, key)
	if err != nil || lock == nil {
		return false, ""
	}
	if lock.ExpiresAt.Before(time.Now()) {
		return false, ""
	}
	return true, lock.LockHolder
}

// ExtendLock extends the TTL of an existing lock held by the holder, bumping
// its version and refreshing ExpiresAt from now while preserving AcquiredAt.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the underlying read/write.
//   - key (Key): the content key whose lock to extend; must be non-zero.
//   - holder (peer.ID): the peer that must match the current lock holder.
//   - ttl (time.Duration): the new TTL duration from now; values <= 0 use
//     m.DefaultTTL, falling back to DefaultLockTTL if that is also unset.
//
// Returns:
//   - error: non-nil if key is zero, the lock read fails, no active
//     (unexpired) lock exists, or the lock is held by a different peer than holder.
func (m *KeyLockManager) ExtendLock(ctx context.Context, key Key, holder peer.ID, ttl time.Duration) error {
	if key.IsZero() {
		return errors.New("key cannot be zero")
	}
	if ttl <= 0 {
		ttl = m.DefaultTTL
		if ttl <= 0 {
			ttl = DefaultLockTTL
		}
	}
	existing, err := m.getLock(ctx, key)
	if err != nil {
		return err
	}
	if existing == nil || existing.ExpiresAt.Before(time.Now()) {
		return errors.New("no active lock to extend")
	}
	if existing.LockHolder != holder {
		return fmt.Errorf("lock held by %s", existing.LockHolder)
	}
	now := time.Now()
	lock := &KeyLock{
		Key:        key,
		LockHolder: holder,
		AcquiredAt: existing.AcquiredAt,
		ExpiresAt:  now.Add(ttl),
		Version:    existing.Version + 1,
	}
	return m.putLock(ctx, lock)
}
