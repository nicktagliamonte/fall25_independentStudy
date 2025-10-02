// Dataastore/blockstore/bitswap/blockservice wiring

package storage

import (
	"context"

	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"

	bs "github.com/ipfs/boxo/bitswap"
	bsnet "github.com/ipfs/boxo/bitswap/network"
	bserv "github.com/ipfs/boxo/blockservice"
	bstore "github.com/ipfs/boxo/blockstore"

	ds "github.com/ipfs/go-datastore"
	dsync "github.com/ipfs/go-datastore/sync"

	"github.com/libp2p/go-libp2p/core/host"
)

type Stack struct {
	Datastore  ds.Batching
	Blockstore bstore.Blockstore
	Bitswap    *bs.Bitswap
	BlockSvc   *bserv.BlockService
}

func NewStack(ctx context.Context, h host.Host) (*Stack, error) {
	// In-mem DS/BS for PoC
	raw := ds.NewMapDatastore()
	safe := dsync.MutexWrap(raw)

	blkstore := bstore.NewBlockstore(safe)

	// Bitswap network over libp2p host
	network := bsnet.NewFromLibp2pHost(h, nil) // no routing/DHT for PoC

	engine := bs.New(ctx, network, blkstore)
	bsvc := bserv.New(blkstore, engine) // BlockService backed by Bitswap

	return &Stack{
		Datastore:  safe,
		Blockstore: blkstore,
		Bitswap:    engine,
		BlockSvc:   &bsvc,
	}, nil
}

func PutRawBlock(ctx context.Context, bsvc *bserv.BlockService, data []byte) (cid.Cid, error) {
	blk, err := blocks.NewBlockWithCid(data, cid.Undef) // Will cuto-compute CID
	if err != nil {
		return cid.Undef, err
	}
	if err := (*bsvc).AddBlock(ctx, blk); err != nil {
		return cid.Cid{}, err
	}
	return blk.Cid(), nil
}

func GetBlock(ctx context.Context, bsvc *bserv.BlockService, c cid.Cid) ([]byte, error) {
	blk, err := (*bsvc).GetBlock(ctx, c)
	if err != nil {
		return nil, err
	}
	return blk.RawData(), nil
}
