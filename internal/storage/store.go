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
	"github.com/ipfs/go-datastore/query"
	dsync "github.com/ipfs/go-datastore/sync"

	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/routing"

	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
)

type Stack struct {
	Datastore        ds.Batching
	Blockstore       bstore.Blockstore
	Bitswap          *bitswap.Bitswap
	BlockSvc         *bserv.BlockService
	DHT              *kaddht.IpfsDHT
	Router           routing.ContentRouting
	ProviderRecords  *LocalProviderRecords
	OnAnnounce       func() // called after each AnnounceProvider (optional)
	AnnounceQueue    *AnnounceQueue // when set and partitioned, announcements are queued for post-heal
}

// NewEphemeralBlockstore creates an in-memory blockstore and datastore.
func NewEphemeralBlockstore() (bstore.Blockstore, ds.Batching) {
	raw := ds.NewMapDatastore()
	safe := dsync.MutexWrap(raw)
	bs := bstore.NewBlockstore(safe)
	return bs, safe
}

func NewStack(ctx context.Context, h host.Host) (*Stack, error) {
	dht, err := myhost.NewDHT(ctx, h, myhost.DHTConfig{Mode: myhost.DHTModeServer})
	if err != nil {
		return nil, err
	}
	stack, err := NewStackWithRouter(ctx, h, dht)
	if err != nil {
		_ = dht.Close()
		return nil, err
	}
	stack.DHT = dht
	stack.Router = dht
	return stack, nil
}

// NewStackWithRouter is like NewStack but allows supplying a ContentRouting implementation.
func NewStackWithRouter(ctx context.Context, h host.Host, router routing.ContentRouting) (*Stack, error) {
	bs, safe := NewEphemeralBlockstore()

	// Bitswap network over our libp2p host
	network := bsnet.NewFromIpfsHost(h)

	engine := bitswap.New(ctx, network, router, bs)
	bsvc := bserv.New(bs, engine)

	return &Stack{
		Datastore:  safe,
		Blockstore: bs,
		Bitswap:    engine,
		BlockSvc:   &bsvc,
		Router:     router,
	}, nil
}

// Close closes Bitswap and DHT (if owned by this stack).
func (s *Stack) Close() {
	_ = s.Bitswap.Close()
	if s.DHT != nil {
		_ = s.DHT.Close()
	}
}

// NewStackFromBlockstore builds a stack from a provided blockstore and datastore.
func NewStackFromBlockstore(ctx context.Context, h host.Host, bs bstore.Blockstore, d ds.Batching, router routing.ContentRouting) (*Stack, error) {
	network := bsnet.NewFromIpfsHost(h)
	engine := bitswap.New(ctx, network, router, bs)
	bsvc := bserv.New(bs, engine)
	return &Stack{Datastore: d, Blockstore: bs, Bitswap: engine, BlockSvc: &bsvc, Router: router}, nil
}

const manifestIndexNS = "/manifest/index/"

func PutRawBlock(ctx context.Context, bsvc *bserv.BlockService, data []byte) (cid.Cid, error) {
	blk := blocks.NewBlock(data) // <- compute a proper CID
	err := (*bsvc).AddBlock(ctx, blk)
	if err != nil {
		err = (*bsvc).AddBlock(ctx, blk)
	}
	if err != nil {
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

// IndexCID records the presence of a CID in the local manifest index.
func IndexCID(ctx context.Context, d ds.Batching, c cid.Cid) error {
	if d == nil || !c.Defined() {
		return nil
	}
	key := ds.NewKey(manifestIndexNS + c.String())
	return d.Put(ctx, key, []byte{1})
}

// AnnounceProvider announces the CID to the DHT so other nodes can discover this peer as a provider.
// When AnnounceQueue is set and partitioned, queues for post-heal instead of announcing.
func (s *Stack) AnnounceProvider(ctx context.Context, c cid.Cid) {
	if s.ProviderRecords != nil {
		s.ProviderRecords.Add(c)
	}
	if s.AnnounceQueue != nil && s.AnnounceQueue.IsPartitioned() {
		s.AnnounceQueue.Add(c)
		_ = RecordPartitionLocalOp(ctx, s.Datastore, "put", c)
		return
	}
	Announce(ctx, s.Router, c)
	if s.OnAnnounce != nil {
		s.OnAnnounce()
	}
}

// FlushQueuedAnnouncements drains the announce queue and announces each CID to the DHT.
// Call after network heal. Uses Stack's Router; no-op if AnnounceQueue is nil.
func (s *Stack) FlushQueuedAnnouncements(ctx context.Context) {
	if s.AnnounceQueue == nil {
		return
	}
	s.AnnounceQueue.Flush(ctx, func(ctx context.Context, c cid.Cid) {
		Announce(ctx, s.Router, c)
		if s.OnAnnounce != nil {
			s.OnAnnounce()
		}
	})
}

// AnnounceProviderAsync runs AnnounceProvider in a goroutine so the caller can return
// immediately. Ensures local Put completes before any DHT work; under partition,
// local operations continue and announcement is best-effort.
func (s *Stack) AnnounceProviderAsync(ctx context.Context, c cid.Cid) {
	go s.AnnounceProvider(ctx, c)
}

// PutRawBlockIndexed stores a block and indexes its CID. Local only; no network.
func PutRawBlockIndexed(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, data []byte) (cid.Cid, error) {
	c, err := PutRawBlock(ctx, bsvc, data)
	if err != nil {
		return cid.Cid{}, err
	}
	_ = IndexCID(ctx, d, c)
	return c, nil
}

// GetBlockIndexed fetches a block and indexes its CID upon success.
func GetBlockIndexed(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, c cid.Cid) ([]byte, error) {
	b, err := GetBlock(ctx, bsvc, c)
	if err != nil {
		return nil, err
	}
	_ = IndexCID(ctx, d, c)
	return b, nil
}

// ListIndexedCIDs enumerates indexed CIDs. If startAfter is non-empty, results strictly greater than it (lexicographically).
func ListIndexedCIDs(ctx context.Context, d ds.Batching, limit int, startAfter string) ([]string, error) {
	if d == nil {
		return nil, nil
	}
	q := query.Query{Prefix: manifestIndexNS}
	res, err := d.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	out := make([]string, 0, limit)
	for r := range res.Next() {
		if r.Error != nil {
			continue
		}
		key := r.Key // like /manifest/index/<cid>
		if len(key) <= len(manifestIndexNS) {
			continue
		}
		cidStr := key[len(manifestIndexNS):]
		if startAfter != "" && !(cidStr > startAfter) {
			continue
		}
		out = append(out, cidStr)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
