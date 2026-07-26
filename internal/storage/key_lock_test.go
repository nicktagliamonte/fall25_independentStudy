// Purpose: Tests for KeyLockManager, AcquireLock, ReleaseLock, and AcquireLockWithRetry.

package storage

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	dsmem "github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func lockTestPeerID(t *testing.T) peer.ID {
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	pid, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey: %v", err)
	}
	return pid
}

func TestAcquireLock_ReleaseLock_Roundtrip(t *testing.T) {
	ctx := context.Background()
	d := dsmem.MutexWrap(ds.NewMapDatastore())
	mgr := NewKeyLockManagerFromDatastore(d)
	key := KeyFromData([]byte("lock-test"))
	holder := lockTestPeerID(t)
	ttl := 5 * time.Minute

	if err := mgr.AcquireLock(ctx, key, holder, ttl); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	locked, h := mgr.IsLocked(ctx, key)
	if !locked || h != holder {
		t.Errorf("IsLocked: want true/%s, got %v/%s", holder, locked, h)
	}

	if err := mgr.ReleaseLock(ctx, key, holder); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	locked, _ = mgr.IsLocked(ctx, key)
	if locked {
		t.Error("ReleaseLock: key should not be locked")
	}
}

func TestAcquireLock_FailsWhenHeldByAnother(t *testing.T) {
	ctx := context.Background()
	d := dsmem.MutexWrap(ds.NewMapDatastore())
	mgr := NewKeyLockManagerFromDatastore(d)
	key := KeyFromData([]byte("contended"))
	holderA := lockTestPeerID(t)
	holderB := lockTestPeerID(t)

	if err := mgr.AcquireLock(ctx, key, holderA, time.Minute); err != nil {
		t.Fatalf("holderA AcquireLock: %v", err)
	}

	err := mgr.AcquireLock(ctx, key, holderB, time.Minute)
	if err == nil {
		t.Fatal("AcquireLock by holderB should fail")
	}
	if !errors.Is(err, ErrLockHeldByAnother) {
		t.Errorf("expected ErrLockHeldByAnother, got %v", err)
	}
}

func TestReleaseLock_NoopWhenNotLocked(t *testing.T) {
	ctx := context.Background()
	d := dsmem.MutexWrap(ds.NewMapDatastore())
	mgr := NewKeyLockManagerFromDatastore(d)
	key := KeyFromData([]byte("unlocked"))
	holder := lockTestPeerID(t)

	if err := mgr.ReleaseLock(ctx, key, holder); err != nil {
		t.Errorf("ReleaseLock on unlocked key should succeed (no-op): %v", err)
	}
}

func TestReleaseLock_FailsWhenHeldByAnother(t *testing.T) {
	ctx := context.Background()
	d := dsmem.MutexWrap(ds.NewMapDatastore())
	mgr := NewKeyLockManagerFromDatastore(d)
	key := KeyFromData([]byte("wrong-holder"))
	holderA := lockTestPeerID(t)
	holderB := lockTestPeerID(t)

	if err := mgr.AcquireLock(ctx, key, holderA, time.Minute); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	err := mgr.ReleaseLock(ctx, key, holderB)
	if err == nil {
		t.Fatal("ReleaseLock by non-holder should fail")
	}
}

func TestAcquireLockWithRetry_SucceedsWhenAvailable(t *testing.T) {
	ctx := context.Background()
	d := dsmem.MutexWrap(ds.NewMapDatastore())
	mgr := NewKeyLockManagerFromDatastore(d)
	key := KeyFromData([]byte("test"))
	holder := lockTestPeerID(t)

	err := mgr.AcquireLockWithRetry(ctx, key, holder, 0, nil)
	if err != nil {
		t.Fatalf("AcquireLockWithRetry: %v", err)
	}
}

func TestAcquireLockWithRetry_RetriesAndSucceedsWhenReleased(t *testing.T) {
	ctx := context.Background()
	d := dsmem.MutexWrap(ds.NewMapDatastore())
	mgr := NewKeyLockManagerFromDatastore(d)
	key := KeyFromData([]byte("test"))
	holderA := lockTestPeerID(t)
	holderB := lockTestPeerID(t)

	if err := mgr.AcquireLock(ctx, key, holderA, time.Minute); err != nil {
		t.Fatalf("holderA acquire: %v", err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = mgr.ReleaseLock(context.Background(), key, holderA)
	}()

	cfg := &LockRetryConfig{
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     200 * time.Millisecond,
		Timeout:        2 * time.Second,
	}
	err := mgr.AcquireLockWithRetry(ctx, key, holderB, 0, cfg)
	if err != nil {
		t.Fatalf("AcquireLockWithRetry: %v", err)
	}
}

func TestAcquireLockWithRetry_FailsAfterTimeout(t *testing.T) {
	ctx := context.Background()
	d := dsmem.MutexWrap(ds.NewMapDatastore())
	mgr := NewKeyLockManagerFromDatastore(d)
	key := KeyFromData([]byte("test"))
	holderA := lockTestPeerID(t)
	holderB := lockTestPeerID(t)

	if err := mgr.AcquireLock(ctx, key, holderA, time.Minute); err != nil {
		t.Fatalf("holderA acquire: %v", err)
	}

	cfg := &LockRetryConfig{
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
		Timeout:        150 * time.Millisecond,
	}
	err := mgr.AcquireLockWithRetry(ctx, key, holderB, 0, cfg)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, ErrLockTimeout) {
		t.Errorf("expected ErrLockTimeout, got %v", err)
	}
}

func TestAcquireLockWithRetry_RespectsContextDeadline(t *testing.T) {
	d := dsmem.MutexWrap(ds.NewMapDatastore())
	mgr := NewKeyLockManagerFromDatastore(d)
	key := KeyFromData([]byte("test"))
	holderA := lockTestPeerID(t)
	holderB := lockTestPeerID(t)

	if err := mgr.AcquireLock(context.Background(), key, holderA, time.Minute); err != nil {
		t.Fatalf("holderA acquire: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	cfg := &LockRetryConfig{
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		Timeout:        10 * time.Second,
	}
	err := mgr.AcquireLockWithRetry(ctx, key, holderB, 0, cfg)
	if err == nil {
		t.Fatal("expected error (ctx deadline)")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if errors.Is(err, ErrLockTimeout) {
		return
	}
	t.Errorf("expected DeadlineExceeded or ErrLockTimeout, got %v", err)
}
