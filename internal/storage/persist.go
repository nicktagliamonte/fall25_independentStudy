// Purpose: File-backed datastore/blockstore for persistent storage.

package storage

import (
	bstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dsync "github.com/ipfs/go-datastore/sync"
	dsleveldb "github.com/ipfs/go-ds-leveldb"
)

// NewPersistentBlockstore creates a blockstore backed by flatfs at path.
func NewPersistentBlockstore(path string) (bstore.Blockstore, ds.Batching, error) {
	d, err := dsleveldb.NewDatastore(path, nil)
	if err != nil {
		return nil, nil, err
	}
	safe := dsync.MutexWrap(d)
	bs := bstore.NewBlockstore(safe)
	return bs, safe, nil
}
