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

// Unmarshal deserializes JSON bytes into KeyLock.
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

// lockStore abstracts lock storage (DHT or datastore).
type lockStore interface {
	get(ctx context.Context, key string) ([]byte, error)
	put(ctx context.Context, key string, val []byte) error
}

type dhtLockStore struct{ dht routing.ValueStore }

func (s dhtLockStore) get(ctx context.Context, key string) ([]byte, error) {
	return s.dht.GetValue(ctx, key)
}
func (s dhtLockStore) put(ctx context.Context, key string, val []byte) error {
	return s.dht.PutValue(ctx, key, val)
}

type dsLockStore struct{ d ds.Datastore }

func (s dsLockStore) get(ctx context.Context, key string) ([]byte, error) {
	val, err := s.d.Get(ctx, ds.NewKey(key))
	if err == ds.ErrNotFound {
		return nil, nil
	}
	return val, err
}
func (s dsLockStore) put(ctx context.Context, key string, val []byte) error {
	return s.d.Put(ctx, ds.NewKey(key), val)
}

// KeyLockManager manages locks for mutual exclusion.
type KeyLockManager struct {
	store      lockStore
	DefaultTTL time.Duration
}

// KeyLockManagerOption configures KeyLockManager.
type KeyLockManagerOption func(*KeyLockManager)

// WithDefaultTTL sets the default lock TTL.
func WithDefaultTTL(ttl time.Duration) KeyLockManagerOption {
	return func(m *KeyLockManager) {
		m.DefaultTTL = ttl
	}
}

// NewKeyLockManager creates a KeyLockManager backed by DHT ValueStore.
func NewKeyLockManager(dht routing.ValueStore, opts ...KeyLockManagerOption) *KeyLockManager {
	m := &KeyLockManager{store: dhtLockStore{dht: dht}, DefaultTTL: DefaultLockTTL}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// NewKeyLockManagerFromDatastore creates a KeyLockManager backed by local datastore.
func NewKeyLockManagerFromDatastore(d ds.Datastore, opts ...KeyLockManagerOption) *KeyLockManager {
	m := &KeyLockManager{store: dsLockStore{d: d}, DefaultTTL: DefaultLockTTL}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func lockDHTKey(k Key) string {
	return LockNamespace + k.String()
}

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

// AcquireLock acquires a lock on the key for the holder. Returns error if already locked by another peer.
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
	InitialBackoff time.Duration // first sleep between retries
	MaxBackoff     time.Duration // cap on sleep
	Timeout        time.Duration // fail if lock not acquired within this (0 = default)
}

// AcquireLockWithRetry acquires a lock with exponential backoff retry on ErrLockHeldByAnother.
// Non-retriable errors (e.g. key zero, store failure) return immediately.
// Respects ctx cancellation. When cfg is nil, uses default backoff and 30s timeout.
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

// IsLocked returns whether the key is locked and the holder peer ID (empty if not locked or expired).
func (m *KeyLockManager) IsLocked(key Key) (bool, peer.ID) {
	ctx := context.Background()
	lock, err := m.getLock(ctx, key)
	if err != nil || lock == nil {
		return false, ""
	}
	if lock.ExpiresAt.Before(time.Now()) {
		return false, ""
	}
	return true, lock.LockHolder
}

// ExtendLock extends the TTL of an existing lock held by the holder.
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
