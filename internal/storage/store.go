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

	routinghelpers "github.com/libp2p/go-libp2p-routing-helpers"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/routing"
)

// Stack bundles the P2P data-plane components a node needs to store and
// exchange content-addressed blocks: a key/value datastore, a blockstore
// layered on top of it, a Bitswap exchange engine, and a BlockService that
// ties the blockstore and exchange together for callers (e.g. PutRawBlock,
// GetBlock).
type Stack struct {
	// Datastore is the underlying (concurrency-safe) key/value store
	// backing Blockstore. It is also the same kind of ds.Batching used
	// for the head/height keys in state.go and the manifest index keys
	// (manifestIndexNS) in this file, so the same Datastore can be shared
	// across those subsystems.
	Datastore ds.Batching
	// Blockstore is the content-addressed block store (get/put by CID)
	// built on top of Datastore.
	Blockstore bstore.Blockstore
	// Bitswap is the block-exchange engine used to fetch blocks this node
	// doesn't have locally from connected peers, and to serve blocks it
	// does have to peers that request them. Constructed with a no-op
	// content router in NewStack (see below) — this repo deliberately
	// does not perform DHT/global routing; peer/content discovery is
	// expected to come from the separate SNG tuple-space system.
	Bitswap *bitswap.Bitswap
	// BlockSvc is a pointer to the BlockService (Blockstore + Bitswap
	// wired together) that most storage helpers in this package
	// (PutRawBlock, GetBlock, etc.) operate on. It is stored as a pointer
	// because bserv.BlockService is an interface value and callers pass
	// *bserv.BlockService around this codebase for a stable reference.
	BlockSvc *bserv.BlockService
}

// NewStack constructs a Stack for proof-of-concept use: an in-memory
// (non-persistent) datastore wrapped for concurrency safety, a blockstore
// on top of it, and a Bitswap engine bound to h's libp2p network with a
// no-op content router (routinghelpers.Null{}), meaning Bitswap will not
// attempt any DHT/IPNI-based content discovery — peers must already be
// known/connected via some other mechanism (e.g. the SNG ring overlay in
// the companion repo) for block exchange to find them.
//
// Note the returned datastore is in-memory (ds.NewMapDatastore()), not the
// disk-backed LevelDB store from persist.go's NewPersistentBlockstore —
// callers that need durable storage should use NewStackFromBlockstore with
// a blockstore/datastore obtained from NewPersistentBlockstore instead.
//
// Parameters:
//   - ctx: context passed through to bitswap.New for the engine's internal
//     lifecycle/background operations.
//   - h: the libp2p host used to construct the Bitswap network layer.
//
// Returns:
//   - *Stack: the constructed stack.
//   - error: always nil in the current implementation (reserved for future
//     fallibility / signature consistency with NewStackWithRouter and
//     NewStackFromBlockstore).
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

// NewStackWithRouter is like NewStack but allows supplying a
// routing.ContentRouting implementation instead of the hardcoded no-op
// router, for cases where some form of content discovery is wanted (still
// in-memory storage, same as NewStack).
//
// Parameters:
//   - ctx: context passed through to bitswap.New.
//   - h: the libp2p host used to construct the Bitswap network layer.
//   - router: the content routing implementation Bitswap should use for
//     discovering providers of content it doesn't have.
//
// Returns:
//   - *Stack: the constructed stack (in-memory datastore/blockstore, as in
//     NewStack).
//   - error: always nil in the current implementation.
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

// NewStackFromBlockstore builds a Stack from a caller-provided blockstore
// and datastore instead of constructing an in-memory one — this is the
// constructor to use for durable storage, typically pairing it with
// NewPersistentBlockstore's return values as bs and d, so blocks and the
// manifest index survive process restarts (subject to the LevelDB
// syncWrites durability described in persist.go).
//
// Parameters:
//   - ctx: context passed through to bitswap.New.
//   - h: the libp2p host used to construct the Bitswap network layer.
//   - bs: the blockstore Bitswap/BlockService should read from and write
//     to.
//   - d: the datastore recorded on the returned Stack.Datastore (not
//     otherwise used to construct bs here, since bs is supplied directly
//     rather than derived from d — callers are responsible for ensuring bs
//     and d are consistent, e.g. by passing the pair returned together
//     from NewPersistentBlockstore).
//   - router: content routing implementation for Bitswap to use; pass a
//     no-op implementation (e.g. routinghelpers.Null{}) to disable
//     DHT/IPNI-based discovery as in NewStack.
//
// Returns:
//   - *Stack: the constructed stack wrapping the provided bs/d.
//   - error: always nil in the current implementation.
func NewStackFromBlockstore(ctx context.Context, h host.Host, bs bstore.Blockstore, d ds.Batching, router routing.ContentRouting) (*Stack, error) {
	network := bsnet.NewFromIpfsHost(h)
	engine := bitswap.New(ctx, network, router, bs)
	bsvc := bserv.New(bs, engine)
	return &Stack{Datastore: d, Blockstore: bs, Bitswap: engine, BlockSvc: &bsvc}, nil
}

// manifestIndexNS is the datastore key prefix under which the manifest
// index records locally-stored CIDs (see docs/FOR_NEXT_WEEK.txt's
// "Lightweight manifest index under /manifest/index/<cid> for snapshots").
// This index is separate from the blockstore's own key space and from the
// event-log head/height keys in state.go — it exists purely so a node can
// cheaply enumerate ("snapshot") which CIDs it holds, e.g. to feed
// node-repair/restore on a peer with the same stable PeerID, without
// having to scan the entire underlying blockstore.
const manifestIndexNS = "/manifest/index/"

// PutRawBlock computes a content-derived CID for data (via
// blocks.NewBlock, using boxo's default multihash function) and stores it
// through the BlockService. This is plain block storage — it does NOT
// touch the manifest index (see PutRawBlockIndexed for the
// indexing variant, which is what should be used if the block needs to
// show up in snapshots/manifests).
//
// Because the CID is derived purely from the content of data, calling
// PutRawBlock again with identical data yields the same CID and is
// idempotent at the storage layer: BlockService.AddBlock checks
// Blockstore.Has (when checkFirst is enabled) and Blockstore.Put itself
// re-checks Has before writing (unless writeThrough is set), so re-storing
// existing content is a cheap no-op rather than a duplicate write.
//
// Parameters:
//   - ctx: context for the underlying AddBlock call.
//   - bsvc: pointer to the BlockService to store into. Must be non-nil (a
//     nil bsvc will panic on dereference here — there is no nil check).
//   - data: the raw bytes to store as a block.
//
// Returns:
//   - cid.Cid: the CID of the stored block (zero value on error).
//   - error: non-nil if AddBlock fails after one retry (see below).
//
// Reliability note: AddBlock is retried exactly once on failure with no
// backoff (mirrors the same blind-retry pattern in state.go's
// AppendPeerAdded) — worth consolidating into one shared retry helper.
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

// GetBlock fetches the block identified by c through the BlockService
// (which may serve it from the local blockstore or fetch it from a peer
// via Bitswap) and returns its raw bytes.
//
// Parameters:
//   - ctx: context for the underlying GetBlock call (e.g. governs how long
//     to wait/retry if the block must be fetched from a remote peer).
//   - bsvc: pointer to the BlockService to fetch from. Must be non-nil.
//   - c: the CID of the block to fetch.
//
// Returns:
//   - []byte: the raw block bytes on success, nil on error.
//   - error: non-nil if the block could not be retrieved (not found
//     locally and not resolvable via the exchange, or any other
//     underlying blockservice error).
func GetBlock(ctx context.Context, bsvc *bserv.BlockService, c cid.Cid) ([]byte, error) {
	blk, err := (*bsvc).GetBlock(ctx, c)
	if err != nil {
		return nil, err
	}
	return blk.RawData(), nil
}

// IndexCID records c's presence in the local manifest index by writing a
// single sentinel byte ([]byte{1}) under manifestIndexNS+c.String(). The
// value itself is never read back (ListIndexedCIDs only cares about key
// presence), so it functions purely as a set-membership marker. Writing
// the same CID twice overwrites the same key with the same value, so this
// is idempotent.
//
// Parameters:
//   - ctx: context for the datastore write.
//   - d: the batching datastore to index into.
//   - c: the CID to record.
//
// Returns:
//   - error: nil (not an error) if d is nil or c is undefined — this
//     function fails silently/no-ops in those cases rather than surfacing
//     a problem, which is why both call sites below (PutRawBlockIndexed,
//     GetBlockIndexed) also discard its return value with `_ =`. Otherwise
//     returns whatever error the underlying d.Put call produces.
func IndexCID(ctx context.Context, d ds.Batching, c cid.Cid) error {
	if d == nil || !c.Defined() {
		return nil
	}
	key := ds.NewKey(manifestIndexNS + c.String())
	return d.Put(ctx, key, []byte{1})
}

// PutRawBlockIndexed stores data as a block (via PutRawBlock) and then
// records its CID in the manifest index (via IndexCID), so it will show up
// in ListIndexedCIDs / node snapshots. This is the block-storing entry
// point that should be used when content needs to be recoverable via the
// manifest-based restore flow described in docs/FOR_NEXT_WEEK.txt.
//
// Parameters:
//   - ctx: context for both the block store and index write.
//   - d: batching datastore for the manifest index.
//   - bsvc: pointer to the BlockService to store into.
//   - data: raw bytes to store.
//
// Returns:
//   - cid.Cid: the CID of the stored block (zero value on error).
//   - error: non-nil only if the underlying PutRawBlock call fails. Note
//     the IndexCID call's error is explicitly discarded (`_ =`) — if
//     block storage succeeds but indexing fails (e.g. datastore write
//     error), PutRawBlockIndexed still returns success with no
//     indication that the block is now stored but absent from the
//     manifest index, which would make it invisible to snapshot-based
//     node repair despite being retrievable by direct GetBlock/CID.
func PutRawBlockIndexed(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, data []byte) (cid.Cid, error) {
	c, err := PutRawBlock(ctx, bsvc, data)
	if err != nil {
		return cid.Cid{}, err
	}
	_ = IndexCID(ctx, d, c)
	return c, nil
}

// GetBlockIndexed fetches a block (via GetBlock) and, on success, also
// records its CID in the manifest index (via IndexCID). This means fetching
// a block a node didn't already have — e.g. one pulled in from a peer via
// Bitswap — opportunistically adds it to this node's own manifest, so a
// later snapshot of this node will include content it has cached, not just
// content it originally authored/stored.
//
// Parameters:
//   - ctx: context for both the fetch and the index write.
//   - d: batching datastore for the manifest index.
//   - bsvc: pointer to the BlockService to fetch from.
//   - c: the CID to fetch.
//
// Returns:
//   - []byte: the raw block bytes on success, nil on error.
//   - error: non-nil only if the underlying GetBlock call fails. As with
//     PutRawBlockIndexed, the IndexCID error is discarded, so an indexing
//     failure after a successful fetch is silent.
func GetBlockIndexed(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, c cid.Cid) ([]byte, error) {
	b, err := GetBlock(ctx, bsvc, c)
	if err != nil {
		return nil, err
	}
	_ = IndexCID(ctx, d, c)
	return b, nil
}

// ListIndexedCIDs enumerates CID strings recorded in the manifest index
// (i.e. builds the snapshot list referenced in docs/FOR_NEXT_WEEK.txt),
// optionally paginated.
//
// Parameters:
//   - ctx: context for the datastore query.
//   - d: batching datastore holding the manifest index. If nil, returns
//     (nil, nil) rather than an error.
//   - limit: maximum number of CID strings to return. If <= 0, no limit is
//     applied and all matching indexed CIDs are returned (result set
//     bounded only by however many keys exist under manifestIndexNS).
//   - startAfter: if non-empty, only CIDs that sort strictly greater than
//     startAfter (Go string/byte-lexicographic comparison, not numeric or
//     multibase-aware comparison) are included — this provides simple
//     keyset-style pagination across repeated calls, assuming the
//     underlying query iterates keys in lexicographic order.
//
// Returns:
//   - []string: the matching CID strings (the raw c.String() form,
//     without the manifestIndexNS prefix), in the order the underlying
//     datastore query yields them, truncated to limit if limit > 0.
//   - error: non-nil only if the underlying d.Query call fails to start;
//     individual result errors encountered while iterating (r.Error) are
//     silently skipped via `continue` rather than aborting the whole call
//     or being reported to the caller.
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
