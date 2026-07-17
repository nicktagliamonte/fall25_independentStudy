// Purpose: File-backed datastore/blockstore for persistent storage.

package storage

import (
	bstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dsync "github.com/ipfs/go-datastore/sync"
	dsleveldb "github.com/ipfs/go-ds-leveldb"
)

// NewPersistentBlockstore creates a disk-backed blockstore and its underlying
// datastore rooted at path.
//
// Despite the package comment at the top of this file (which is stale — see
// note below), the backend is NOT flatfs; it is LevelDB via
// github.com/ipfs/go-ds-leveldb, opened with default *leveldb.Options
// (nil is passed for opts). go-ds-leveldb opens its accessor with
// syncWrites: true, so every Put/Delete issues a synchronous LevelDB write
// (fsync'd to the OS/WAL before returning) rather than being buffered in
// memory only; this makes writes durable/crash-safe at the cost of write
// latency compared to an async-write configuration.
//
// The returned datastore is wrapped with dsync.MutexWrap, which serializes
// all datastore operations behind a single mutex so the returned
// ds.Batching is safe for concurrent use from multiple goroutines. The
// returned bstore.Blockstore is built on top of that same synchronized
// datastore via bstore.NewBlockstore, so blockstore Put/Get/Has calls are
// also safe for concurrent use and — per boxo's default (non-writeThrough)
// Put behavior — storing a block whose CID already exists is a cheap no-op
// (a Has check short-circuits the write), which is what gives block storage
// in this package its idempotence.
//
// Parameters:
//   - path: filesystem directory where the LevelDB database lives. If path
//     is empty, go-ds-leveldb opens an in-memory store instead (see
//     go-ds-leveldb's NewDatastore), which is not persisted to disk.
//
// Returns:
//   - bstore.Blockstore: a content-addressed block store layered over the
//     LevelDB datastore, usable directly or wrapped in a BlockService.
//   - ds.Batching: the underlying (mutex-wrapped) key/value datastore, e.g.
//     for use with the head/height state keys in state.go or the manifest
//     index keys in store.go.
//   - error: non-nil if the LevelDB database at path could not be opened
//     (e.g. permission errors, or corruption that go-ds-leveldb's internal
//     RecoverFile attempt also failed to repair).
func NewPersistentBlockstore(path string) (bstore.Blockstore, ds.Batching, error) {
	d, err := dsleveldb.NewDatastore(path, nil)
	if err != nil {
		return nil, nil, err
	}
	safe := dsync.MutexWrap(d)
	bs := bstore.NewBlockstore(safe)
	return bs, safe, nil
}
