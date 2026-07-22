// Purpose: File-backed datastore/blockstore for persistent storage.

package storage

import (
	bstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dsync "github.com/ipfs/go-datastore/sync"
	dsleveldb "github.com/ipfs/go-ds-leveldb"
)

// NewPersistentBlockstore creates a blockstore backed by a LevelDB datastore at path.
// The datastore is wrapped with a mutex to make it safe for concurrent access,
// and a blockstore is layered on top of it.
//
// Parameters:
//   - path (string): filesystem directory where the LevelDB datastore is created/opened.
//
// Returns:
//   - bstore.Blockstore: the blockstore backed by the on-disk datastore at path.
//   - ds.Batching: the underlying mutex-wrapped, batching-capable datastore, for
//     callers that need direct datastore access (e.g. chunk indices, tokens).
//   - error: non-nil if opening the LevelDB datastore at path fails.
func NewPersistentBlockstore(path string) (bstore.Blockstore, ds.Batching, error) {
	d, err := dsleveldb.NewDatastore(path, nil)
	if err != nil {
		return nil, nil, err
	}
	safe := dsync.MutexWrap(d)
	bs := bstore.NewBlockstore(safe)
	return bs, safe, nil
}
