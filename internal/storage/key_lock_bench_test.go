// Purpose: Phase 7.3 benchmarks for lock acquisition overhead.

package storage

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	dsmem "github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func benchPeerID(b *testing.B) peer.ID {
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		b.Fatalf("GenerateEd25519Key: %v", err)
	}
	pid, err := peer.IDFromPublicKey(pub)
	if err != nil {
		b.Fatalf("IDFromPublicKey: %v", err)
	}
	return pid
}

// BenchmarkAcquireReleaseLock measures AcquireLock+ReleaseLock round-trip latency (lock overhead).
func BenchmarkAcquireReleaseLock(b *testing.B) {
	ctx := context.Background()
	d := dsmem.MutexWrap(ds.NewMapDatastore())
	mgr := NewKeyLockManagerFromDatastore(d)
	key := KeyFromData([]byte("lock overhead benchmark key"))
	holder := benchPeerID(b)
	ttl := 5 * time.Minute

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := mgr.AcquireLock(ctx, key, holder, ttl); err != nil {
			b.Fatalf("AcquireLock: %v", err)
		}
		if err := mgr.ReleaseLock(ctx, key, holder); err != nil {
			b.Fatalf("ReleaseLock: %v", err)
		}
	}
}
