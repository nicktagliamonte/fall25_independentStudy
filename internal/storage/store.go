// Datastore/blockstore/bitswap/blockservice wiring

package storage

import (
	"context"

	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"

	bitswap "github.com/ipfs/boxo/bitswap"
	bsnet "github.com/ipfs/boxo/bitswap/network/bsnet"
	bserv "github.com/ipfs/boxo/blockservice"
	bstore "github.com/ipfs/boxo/blockstore"

	ds "github.com/ipfs/go-datastore"
	dsync "github.com/ipfs/go-datastore/sync"

	routinghelpers "github.com/libp2p/go-libp2p-routing-helpers"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/routing"
)

type Stack struct {
	Datastore  ds.Batching
	Blockstore bstore.Blockstore
	Bitswap    *bitswap.Bitswap
	BlockSvc   *bserv.BlockService
}

func NewStack(ctx context.Context, h host.Host) (*Stack, error) {
	// In-memory DS/BS for PoC
	raw := ds.NewMapDatastore()
	safe := dsync.MutexWrap(raw)

	bs := bstore.NewBlockstore(safe)

	// Bitswap network over our libp2p host (no routing/DHT for PoC)
	network := bsnet.NewFromIpfsHost(h)

	// No-op content discovery (so Bitswap won’t try to use DHT/IPNI)
	nullRouter := routinghelpers.Null{}

	engine := bitswap.New(ctx, network, nullRouter, bs)
	bsvc := bserv.New(bs, engine) // BlockService backed by Bitswap

	return &Stack{
		Datastore:  safe,
		Blockstore: bs,
		Bitswap:    engine,
		BlockSvc:   &bsvc,
	}, nil
}

// NewStackWithRouter is like NewStack but allows supplying a ContentRouting implementation.
func NewStackWithRouter(ctx context.Context, h host.Host, router routing.ContentRouting) (*Stack, error) {
	// In-memory DS/BS for PoC
	raw := ds.NewMapDatastore()
	safe := dsync.MutexWrap(raw)

	bs := bstore.NewBlockstore(safe)

	// Bitswap network over our libp2p host
	network := bsnet.NewFromIpfsHost(h)

	engine := bitswap.New(ctx, network, router, bs)
	bsvc := bserv.New(bs, engine)

	return &Stack{
		Datastore:  safe,
		Blockstore: bs,
		Bitswap:    engine,
		BlockSvc:   &bsvc,
	}, nil
}

// NewStackFromBlockstore builds a stack from a provided blockstore and datastore.
func NewStackFromBlockstore(ctx context.Context, h host.Host, bs bstore.Blockstore, d ds.Batching, router routing.ContentRouting) (*Stack, error) {
	network := bsnet.NewFromIpfsHost(h)
	engine := bitswap.New(ctx, network, router, bs)
	bsvc := bserv.New(bs, engine)
	return &Stack{Datastore: d, Blockstore: bs, Bitswap: engine, BlockSvc: &bsvc}, nil
}

func PutRawBlock(ctx context.Context, bsvc *bserv.BlockService, data []byte) (cid.Cid, error) {
	blk := blocks.NewBlock(data) // <- compute a proper CID
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
