// Datastore/blockstore/bitswap/blockservice wiring

package storage

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

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
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multiaddr"

	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
)

// remoteOnlyGetKey is the context key used to flag "remote-only" GetBlock calls.
type remoteOnlyGetKey struct{}

// WithRemoteOnlyGet marks ctx so GetBlock skips the local blockstore and uses GetToken + DirectFetch.
// This is used for measurement purposes, to force a Get to exercise the full DHT/network path
// even when a copy of the block is available in the local store.
//
// Parameters:
//   - ctx (context.Context): the parent context to annotate.
//
// Returns:
//   - context.Context: a derived context that Stack.GetBlock checks via remoteOnlyGetFromContext.
func WithRemoteOnlyGet(ctx context.Context) context.Context {
	return context.WithValue(ctx, remoteOnlyGetKey{}, true)
}

// remoteOnlyGetFromContext reports whether ctx was previously annotated by WithRemoteOnlyGet.
//
// Parameters:
//   - ctx (context.Context): the context to inspect.
//
// Returns:
//   - bool: true if the context requests remote-only (bypass local store) Get behavior.
func remoteOnlyGetFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(remoteOnlyGetKey{}).(bool)
	return v
}

// gatherAddrsForPeer returns addresses for connecting to the provider.
// Prefers loc.Address if it is routable (not 0.0.0.0); falls back to the host's peerstore
// (e.g. addresses learned from DHT bootstrap) when loc.Address is absent or unroutable, and
// falls back to loc.Address anyway as a last resort if the peerstore has nothing.
//
// Parameters:
//   - h (host.Host): the local libp2p host, used to consult the peerstore.
//   - loc (Location): the token location describing the provider and its advertised address.
//
// Returns:
//   - []multiaddr.Multiaddr: candidate addresses to dial, in preference order; may be empty.
func gatherAddrsForPeer(h host.Host, loc Location) []multiaddr.Multiaddr {
	var addrs []multiaddr.Multiaddr
	if loc.Address != nil {
		s := loc.Address.String()
		if !strings.Contains(s, "/ip4/0.0.0.0/") {
			addrs = []multiaddr.Multiaddr{loc.Address}
		}
	}
	if len(addrs) == 0 {
		addrs = h.Peerstore().Addrs(loc.ProviderID)
	}
	if len(addrs) == 0 && loc.Address != nil {
		addrs = []multiaddr.Multiaddr{loc.Address}
	}
	return addrs
}

// KeyedBlock represents a block with its key, data, provider ID, and CID.
// Per newReqs.txt: Key is hash(data) only; ProviderID is attached separately, not in hash.
type KeyedBlock struct {
	// Key is the primary identifier (SHA256 hash of data).
	Key Key
	// Data is the actual block content.
	Data []byte
	// ProviderID is the peer ID of the provider (attached separately, not in hash).
	ProviderID peer.ID
	// CID is the content identifier for IPFS blockstore compatibility.
	CID cid.Cid
}

// Stack bundles together the datastore, blockstore, Bitswap/BlockService, DHT/router, host,
// and auxiliary components (provider records, announce queue, routing table, lock manager,
// token store, metrics sinks) needed to serve Put/Get operations for this node. Most storage
// package functions that need network or persistence context take a *Stack (or its fields)
// as a receiver or parameter.
type Stack struct {
	// Datastore is the underlying key/value store backing the blockstore and manifest indexes.
	Datastore ds.Batching
	// Blockstore is the IPFS-compatible block store (wraps Datastore) used by Bitswap/BlockSvc.
	Blockstore bstore.Blockstore
	// Bitswap is the Bitswap exchange engine used for block transfer/discovery.
	Bitswap *bitswap.Bitswap
	// BlockSvc is the BlockService built on top of Blockstore and Bitswap.
	BlockSvc *bserv.BlockService
	// DHT is the Kademlia DHT instance, used both as a routing.ContentRouting and as the
	// default routing.ValueStore for token storage when TokenStore is nil.
	DHT *kaddht.IpfsDHT
	// Router is the content routing implementation (typically the DHT) used by Bitswap.
	Router routing.ContentRouting
	// Host is this node's libp2p host, used for stream protocols (direct fetch, repair) and
	// as the source of the local peer ID.
	Host host.Host
	// ProviderRecords tracks locally-announced CIDs (legacy provider-record bookkeeping).
	ProviderRecords *LocalProviderRecords
	// OnAnnounce, when set, is invoked after a successful (non-partitioned) provider announce.
	OnAnnounce func()
	// AnnounceQueue, when set, buffers CID announcements made while partitioned for later flush.
	AnnounceQueue *AnnounceQueue
	// RoutingTable is the local {Key, CID, Providers[], RepVector} table used to answer
	// Put/Delete bookkeeping and repair/verification queries without a DHT round trip.
	RoutingTable *RoutingTable
	// KeyLockManager, when set, causes PutBlock/DeleteBlock to acquire a per-key write lock
	// (holder = Host.ID()) before mutating storage.
	KeyLockManager *KeyLockManager
	// PutLockRetryConfig, when set, configures the exponential-backoff retry behavior PutBlock
	// uses while attempting to acquire the KeyLockManager lock; nil uses library defaults.
	PutLockRetryConfig *LockRetryConfig
	// TokenStore, when set, is used for token reads/writes/syncs instead of the DHT directly
	// (e.g. a Gateway-backed token routing implementation).
	TokenStore routing.ValueStore
	// MessageSink, when set, receives counts of P2P protocol messages (put/get/lookup) for metrics.
	MessageSink MessageMetricsSink
	// HopSink, when set, receives DHT lookup hop counts for metrics.
	HopSink NetworkHopsSink
}

// tokenValueStore returns the configured token store without constructing an
// interface containing a nil *IpfsDHT. Such an interface compares non-nil and
// panics when its methods are called.
func (s *Stack) tokenValueStore() routing.ValueStore {
	if s == nil {
		return nil
	}
	if s.TokenStore != nil {
		return s.TokenStore
	}
	if s.DHT != nil {
		return s.DHT
	}
	return nil
}

// NewEphemeralBlockstore creates an in-memory blockstore and datastore, suitable for tests
// or ephemeral nodes that do not need persistence across restarts.
//
// Returns:
//   - bstore.Blockstore: an in-memory, mutex-synchronized blockstore.
//   - ds.Batching: the underlying in-memory datastore backing the blockstore.
func NewEphemeralBlockstore() (bstore.Blockstore, ds.Batching) {
	raw := ds.NewMapDatastore()
	safe := dsync.MutexWrap(raw)
	bs := bstore.NewBlockstore(safe)
	return bs, safe
}

// NewStack builds a fully-wired Stack for host h, creating and owning a new server-mode DHT
// and a datastore-backed KeyLockManager. This is the standard constructor for production nodes;
// use NewStackWithRouter or NewStackFromBlockstore when a DHT/router/blockstore is supplied
// externally instead.
//
// Parameters:
//   - ctx (context.Context): used to start the DHT and underlying bitswap engine.
//   - h (host.Host): the libp2p host the stack will operate over.
//
// Returns:
//   - *Stack: the constructed stack, with DHT, Router, Host, and KeyLockManager populated.
//   - error: non-nil if DHT creation or stack construction fails.
func NewStack(ctx context.Context, h host.Host) (*Stack, error) {
	dht, err := myhost.NewDHT(ctx, h, myhost.DHTConfig{
		Mode:        myhost.DHTModeServer,
		UseTokenDHT: true,
	})
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
	stack.Host = h
	stack.KeyLockManager = NewKeyLockManagerFromDatastore(stack.Datastore)
	return stack, nil
}

// NewStackWithRouter is like NewStack but allows supplying a ContentRouting implementation
// instead of creating a new DHT. It builds a fresh ephemeral blockstore/datastore, wires up
// Bitswap and a BlockService over router, and initializes a new RoutingTable. Note that unlike
// NewStack, this does not populate Stack.DHT, Stack.Host, or Stack.KeyLockManager.
//
// Parameters:
//   - ctx (context.Context): used to start the bitswap engine.
//   - h (host.Host): the libp2p host used for the Bitswap network layer.
//   - router (routing.ContentRouting): the content routing implementation Bitswap will use.
//
// Returns:
//   - *Stack: the constructed stack (Datastore, Blockstore, Bitswap, BlockSvc, Router, Host,
//     RoutingTable populated).
//   - error: always nil in the current implementation, reserved for future validation.
func NewStackWithRouter(ctx context.Context, h host.Host, router routing.ContentRouting) (*Stack, error) {
	bs, safe := NewEphemeralBlockstore()

	// Bitswap network over our libp2p host
	network := bsnet.NewFromIpfsHost(h)

	engine := bitswap.New(ctx, network, router, bs)
	bsvc := bserv.New(bs, engine)

	return &Stack{
		Datastore:    safe,
		Blockstore:   bs,
		Bitswap:      engine,
		BlockSvc:     &bsvc,
		Router:       router,
		Host:         h,
		RoutingTable: NewRoutingTable(),
	}, nil
}

// Close shuts down the Stack's Bitswap engine and, if present, its DHT. Errors from either
// close operation are discarded; callers that need shutdown errors should close DHT/Bitswap
// directly instead.
func (s *Stack) Close() {
	_ = s.Bitswap.Close()
	if s.DHT != nil {
		_ = s.DHT.Close()
	}
}

// NewStackFromBlockstore builds a Stack from a caller-provided blockstore and datastore
// (rather than a fresh ephemeral one), wiring up Bitswap and a BlockService over router and
// initializing a new RoutingTable. Useful when the caller manages blockstore/datastore
// lifetime independently (e.g. persistent on-disk storage).
//
// Parameters:
//   - ctx (context.Context): used to start the bitswap engine.
//   - h (host.Host): the libp2p host used for the Bitswap network layer.
//   - bs (bstore.Blockstore): the blockstore to use.
//   - d (ds.Batching): the datastore backing bs and manifest indexes.
//   - router (routing.ContentRouting): the content routing implementation Bitswap will use.
//
// Returns:
//   - *Stack: the constructed stack (Datastore, Blockstore, Bitswap, BlockSvc, Router, Host,
//     RoutingTable populated).
//   - error: always nil in the current implementation, reserved for future validation.
func NewStackFromBlockstore(ctx context.Context, h host.Host, bs bstore.Blockstore, d ds.Batching, router routing.ContentRouting) (*Stack, error) {
	network := bsnet.NewFromIpfsHost(h)
	engine := bitswap.New(ctx, network, router, bs)
	bsvc := bserv.New(bs, engine)
	return &Stack{
		Datastore:    d,
		Blockstore:   bs,
		Bitswap:      engine,
		BlockSvc:     &bsvc,
		Router:       router,
		Host:         h,
		RoutingTable: NewRoutingTable(),
	}, nil
}

// manifestIndexNS is the datastore key prefix for the CID presence index (IPFS compatibility).
const manifestIndexNS = "/manifest/index/"

// keyIndexNS is the datastore key prefix for the Key presence index (primary identifier).
const keyIndexNS = "/manifest/key/"

// keyToCIDNS is the datastore key prefix for the Key -> CID mapping.
const keyToCIDNS = "/manifest/key-to-cid/"

// keyToProviderIDNS is the datastore key prefix for the Key -> ProviderID mapping.
const keyToProviderIDNS = "/manifest/key-to-provider/"

// DirectFetchProtocolID is the libp2p protocol ID for direct data fetch by key.
const DirectFetchProtocolID = "/sng40/direct-fetch/1.0.0"

// MaxTransferBlockSize bounds one logical payload transferred by the direct
// fetch and repair protocols. Keeping one shared limit prevents the HTTP put
// path from accepting an object that peers will later refuse to fetch or
// replicate.
const MaxTransferBlockSize = 64 << 20

// PutRawBlock stores raw data in the blockstore via bsvc, letting the block-format library
// compute the block's CID. On the first AddBlock error, it retries once before giving up.
//
// Parameters:
//   - ctx (context.Context): cancels the underlying blockstore write.
//   - bsvc (*bserv.BlockService): the block service to write through.
//   - data ([]byte): the raw block bytes to store.
//
// Returns:
//   - cid.Cid: the CID computed for data.
//   - error: non-nil if both write attempts fail.
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

// GetBlockByKey retrieves a block by Key from local storage only. This is the primary method
// for Key-based block retrieval: it looks up the Key -> CID mapping in the datastore, then
// fetches the block from the blockstore using that CID. Key is the primary identifier; the
// CID lookup exists only for IPFS blockstore compatibility. Returns (nil, nil), not an error,
// when k is zero or when no CID mapping exists for k.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore/blockstore lookups.
//   - d (ds.Batching): the datastore holding the Key -> CID mapping.
//   - bsvc (*bserv.BlockService): the block service to fetch the block from.
//   - k (Key): the content key to retrieve.
//
// Returns:
//   - []byte: the raw block bytes, or nil if k is zero, unmapped, or the fetch fails.
//   - error: non-nil if the CID lookup or blockstore fetch fails (excluding "not found" for k).
func GetBlockByKey(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, k Key) ([]byte, error) {
	if k.IsZero() {
		return nil, nil
	}
	// Get CID from Key mapping
	c, err := GetCIDFromKey(ctx, d, k)
	if err != nil {
		return nil, err
	}
	if !c.Defined() {
		return nil, nil // Key not found in mapping
	}
	// Fetch block using CID
	blk, err := (*bsvc).GetBlock(ctx, c)
	if err != nil {
		return nil, err
	}
	return blk.RawData(), nil
}

// GetBlock retrieves a block by Key, implementing the full Tarsus Get flow: unless the
// context requests remote-only behavior (WithRemoteOnlyGet), it first checks the local store
// via GetBlockByKey. On a local miss (or when bypassed), it looks up a Token for k via
// GetToken against s.TokenStore (falling back to s.DHT), counting DHT query hops as it goes,
// then races DirectFetch against every location listed in the token's Locations and returns
// the first successful result. DirectFetch itself validates KeyFromData(blockData) == k, so a
// successful return is guaranteed to match the requested key. MessageSink/HopSink, when set,
// are updated with lookup/get message and hop counts for metrics.
//
// Parameters:
//   - ctx (context.Context): cancels the token lookup and all in-flight direct fetches.
//   - k (Key): the content key to retrieve.
//
// Returns:
//   - []byte: the raw block bytes, or nil on failure.
//   - int: the number of DHT query hops used to resolve the token (0 if served locally or if
//     hop counting produced no events).
//   - error: non-nil if k is zero, no token store is configured, the token has no locations,
//     or direct fetch failed against every location.
func (s *Stack) GetBlock(ctx context.Context, k Key) ([]byte, int, error) {
	if k.IsZero() {
		return nil, 0, fmt.Errorf("key cannot be zero")
	}

	if !remoteOnlyGetFromContext(ctx) {
		if localData, err := GetBlockByKey(ctx, s.Datastore, s.BlockSvc, k); err == nil && localData != nil {
			return localData, 0, nil
		}
	}

	tokenStore := s.tokenValueStore()
	if tokenStore == nil {
		return nil, 0, fmt.Errorf("token store or DHT required for token-based routing")
	}

	// RegisterForQueryEvents ties channel lifetime to its ctx; cancel that ctx after GetToken
	// so the hop-count goroutine exits before DirectFetch (otherwise channel closes only when ctx ends).
	ctxToken, cancelToken := context.WithCancel(ctx)
	defer cancelToken()
	evCtx, evCh := routing.RegisterForQueryEvents(ctxToken)
	evCtx, cancel := context.WithCancel(evCtx)
	defer cancel()
	var hops int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range evCh {
			if ev != nil && ev.Type == routing.SendingQuery {
				hops++
			}
		}
	}()
	token, err := GetToken(evCtx, tokenStore, k)
	cancel()
	cancelToken()
	<-done
	if s.MessageSink != nil {
		s.MessageSink.AddLookupMessagesOut(1)
		s.MessageSink.AddLookupMessagesIn(1)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("get token failed: %w", err)
	}
	if s.HopSink != nil && hops > 0 {
		s.HopSink.AddLookupHops(int(hops))
	}

	if len(token.Locations) == 0 {
		return nil, 0, fmt.Errorf("token has no locations")
	}

	// Try direct fetch from each location in parallel
	var wg sync.WaitGroup
	var mu sync.Mutex
	var result []byte
	var fetchErrors []error
	success := false

	for _, location := range token.Locations {
		wg.Add(1)
		go func(loc Location) {
			defer wg.Done()

			data, err := DirectFetch(ctx, s, loc, k)
			if err == nil && s.MessageSink != nil {
				s.MessageSink.AddGetMessagesOut(1)
				s.MessageSink.AddGetMessagesIn(1)
			}
			if err != nil {
				// Record error and try next location
				mu.Lock()
				fetchErrors = append(fetchErrors, fmt.Errorf("peer %s: %w", loc.ProviderID, err))
				mu.Unlock()
				return
			}

			// Success - capture first successful result
			mu.Lock()
			if !success && data != nil {
				result = data
				success = true
			}
			mu.Unlock()
		}(location)
	}

	wg.Wait()

	if !success {
		return nil, int(hops), fmt.Errorf("direct fetch failed from all %d locations: %v", len(token.Locations), fetchErrors)
	}

	return result, int(hops), nil
}

// DirectFetch opens a direct libp2p stream to location.ProviderID (connecting first if not
// already connected, using addresses from gatherAddrsForPeer) and requests the block for key
// using the DirectFetchProtocolID protocol. It writes the hex key followed by a newline, then
// reads a status line ("OK" or "ERROR: ..."), a decimal size line, and exactly that many bytes
// of block data (rejecting sizes <= 0 or > 10MiB). It then verifies KeyFromData(blockData)
// equals key before returning, guarding against a misbehaving or corrupted peer.
//
// Parameters:
//   - ctx (context.Context): bounds dialing (15s) and the stream lifetime (45s) as well as
//     cancellation of reads/writes.
//   - stack (*Stack): supplies the local Host used to dial and open the stream.
//   - location (Location): identifies the peer to fetch from and its known address.
//   - key (Key): the content key being requested.
//
// Returns:
//   - []byte: the fetched and key-verified block data.
//   - error: non-nil if stack/host is missing, no address can be found, connecting/streaming
//     fails, the peer reports an error, the reported size is invalid, or the returned data's
//     key does not match key.
func DirectFetch(ctx context.Context, stack *Stack, location Location, key Key) ([]byte, error) {
	if stack == nil {
		return nil, fmt.Errorf("stack required")
	}
	if stack.Host == nil {
		return nil, fmt.Errorf("host required for direct fetch")
	}

	// Connect to peer if not already connected
	if stack.Host.Network().Connectedness(location.ProviderID) != network.Connected {
		addrs := gatherAddrsForPeer(stack.Host, location)
		if len(addrs) == 0 {
			return nil, fmt.Errorf("no addresses found for peer %s", location.ProviderID)
		}
		info := peer.AddrInfo{ID: location.ProviderID, Addrs: addrs}
		stack.Host.Peerstore().AddAddrs(info.ID, info.Addrs, 30*time.Minute)
		ctxDial, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := stack.Host.Connect(ctxDial, info)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("connect to peer %s: %w", location.ProviderID, err)
		}
	}

	streamCtx, streamCancel := context.WithTimeout(ctx, 45*time.Second)
	defer streamCancel()
	stream, err := stack.Host.NewStream(streamCtx, location.ProviderID, DirectFetchProtocolID)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	keyStr := key.String()
	if _, err := stream.Write([]byte(keyStr + "\n")); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}

	r := bufio.NewReader(stream)
	statusLine, err := r.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read status: %w", err)
	}
	if len(statusLine) >= 5 && statusLine[:5] == "ERROR" {
		return nil, fmt.Errorf("fetch failed: %s", strings.TrimSpace(statusLine))
	}

	sizeLine, err := r.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read size: %w", err)
	}
	sizeStr := strings.TrimSpace(sizeLine)
	blockSize, err := strconv.Atoi(sizeStr)
	if err != nil {
		return nil, fmt.Errorf("parse size: %w", err)
	}
	if blockSize <= 0 || blockSize > MaxTransferBlockSize {
		return nil, fmt.Errorf("invalid block size: %d", blockSize)
	}

	blockData := make([]byte, blockSize)
	if _, err := io.ReadFull(r, blockData); err != nil {
		return nil, fmt.Errorf("read block data: %w", err)
	}

	// Verify key matches
	expectedKey := KeyFromData(blockData)
	if !expectedKey.Equal(key) {
		return nil, fmt.Errorf("key mismatch: expected %s, got %s", key.String(), expectedKey.String())
	}

	return blockData, nil
}

// HandleDirectFetchStream is the server-side handler for the DirectFetchProtocolID protocol,
// implementing the counterpart to DirectFetch. Protocol: the client sends a key (hex string +
// "\n"); the server parses it, resolves the payload locally via ResolvePayloadByKeyLocal
// (handling both single-block and chunk-indexed storage), verifies KeyFromData(blockData)
// matches the requested key, then responds with:
//   - Status: "OK\n" or "ERROR: ...\n"
//   - If OK: block size (int + "\n"), then the raw block data.
//
// The stream is always closed before returning. Any protocol-level failure is both written to
// the stream as an ERROR line and returned as a Go error for local logging.
//
// Parameters:
//   - stream (network.Stream): the inbound libp2p stream to read the request from and write
//     the response to.
//   - stack (*Stack): supplies the datastore and block service used to resolve the payload.
//
// Returns:
//   - error: non-nil if reading/parsing the key fails, the block cannot be resolved or is
//     empty, the resolved data's key doesn't match the request, or a write to the stream fails.
func HandleDirectFetchStream(stream network.Stream, stack *Stack) error {
	defer stream.Close()

	ctx := context.Background()
	r := bufio.NewReader(stream)

	keyLine, err := r.ReadString('\n')
	if err != nil {
		_, _ = stream.Write([]byte("ERROR: read key\n"))
		return fmt.Errorf("read key: %w", err)
	}
	keyStr := strings.TrimSpace(keyLine)

	// Parse key
	key, err := ParseKey(keyStr)
	if err != nil {
		_, _ = stream.Write([]byte(fmt.Sprintf("ERROR: invalid key: %v\n", err)))
		return fmt.Errorf("parse key: %w", err)
	}

	// Resolve payload by key from local storage (single-block or chunk-indexed).
	blockData, err := ResolvePayloadByKeyLocal(ctx, stack.Datastore, stack.BlockSvc, key)
	if err != nil {
		_, _ = stream.Write([]byte(fmt.Sprintf("ERROR: get block: %v\n", err)))
		return fmt.Errorf("get block: %w", err)
	}

	if len(blockData) == 0 {
		_, _ = stream.Write([]byte("ERROR: block not found\n"))
		return fmt.Errorf("block not found for key %s", key.String())
	}

	// Verify key matches data
	expectedKey := KeyFromData(blockData)
	if !expectedKey.Equal(key) {
		_, _ = stream.Write([]byte("ERROR: key mismatch\n"))
		return fmt.Errorf("key mismatch: expected %s, got %s", key.String(), expectedKey.String())
	}

	// Send success status
	if _, err := stream.Write([]byte("OK\n")); err != nil {
		return fmt.Errorf("write status: %w", err)
	}

	// Send block size
	sizeStr := fmt.Sprintf("%d\n", len(blockData))
	if _, err := stream.Write([]byte(sizeStr)); err != nil {
		return fmt.Errorf("write size: %w", err)
	}

	// Send block data
	if _, err := stream.Write(blockData); err != nil {
		return fmt.Errorf("write block data: %w", err)
	}

	return nil
}

// GetBlockByCID retrieves a block by CID directly from the block service, bypassing the
// Key -> CID mapping. Retained for IPFS blockstore compatibility; prefer GetBlockByKey(Key)
// for new code since Key is the primary identifier.
//
// Parameters:
//   - ctx (context.Context): cancels the blockstore fetch.
//   - bsvc (*bserv.BlockService): the block service to fetch the block from.
//   - c (cid.Cid): the CID to retrieve.
//
// Returns:
//   - []byte: the raw block bytes.
//   - error: non-nil if the fetch fails (e.g. block not found).
func GetBlockByCID(ctx context.Context, bsvc *bserv.BlockService, c cid.Cid) ([]byte, error) {
	blk, err := (*bsvc).GetBlock(ctx, c)
	if err != nil {
		return nil, err
	}
	return blk.RawData(), nil
}

// IndexCID records the presence of a CID in the local manifest index (manifestIndexNS).
// Kept for IPFS blockstore compatibility. No-op if d is nil or c is undefined.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore write.
//   - d (ds.Batching): the datastore to write the index entry to.
//   - c (cid.Cid): the CID to index.
//
// Returns:
//   - error: non-nil if the datastore write fails.
func IndexCID(ctx context.Context, d ds.Batching, c cid.Cid) error {
	if d == nil || !c.Defined() {
		return nil
	}
	key := ds.NewKey(manifestIndexNS + c.String())
	return d.Put(ctx, key, []byte{1})
}

// IndexKey records the presence of a Key in the local manifest index (keyIndexNS). Key is the
// primary identifier for storage. No-op if d is nil or k is zero.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore write.
//   - d (ds.Batching): the datastore to write the index entry to.
//   - k (Key): the key to index.
//
// Returns:
//   - error: non-nil if the datastore write fails.
func IndexKey(ctx context.Context, d ds.Batching, k Key) error {
	if d == nil || k.IsZero() {
		return nil
	}
	key := ds.NewKey(keyIndexNS + k.String())
	return d.Put(ctx, key, []byte{1})
}

// StoreKeyToCIDMapping persists the mapping from Key to CID (keyToCIDNS), allowing later
// lookup of the CID for a Key via GetCIDFromKey, needed for IPFS blockstore operations since
// the blockstore itself is addressed by CID. No-op if d is nil, k is zero, or c is undefined.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore write.
//   - d (ds.Batching): the datastore to write the mapping to.
//   - k (Key): the key half of the mapping.
//   - c (cid.Cid): the CID half of the mapping.
//
// Returns:
//   - error: non-nil if the datastore write fails.
func StoreKeyToCIDMapping(ctx context.Context, d ds.Batching, k Key, c cid.Cid) error {
	if d == nil || k.IsZero() || !c.Defined() {
		return nil
	}
	key := ds.NewKey(keyToCIDNS + k.String())
	return d.Put(ctx, key, []byte(c.String()))
}

// GetCIDFromKey retrieves the CID associated with a Key via the keyToCIDNS mapping.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore read.
//   - d (ds.Batching): the datastore holding the mapping.
//   - k (Key): the key to look up.
//
// Returns:
//   - cid.Cid: the mapped CID, or the zero CID if d is nil, k is zero, no mapping exists, or
//     the stored value fails to decode as a CID.
//   - error: currently always nil; decode/lookup failures are reported via a zero CID instead.
func GetCIDFromKey(ctx context.Context, d ds.Batching, k Key) (cid.Cid, error) {
	if d == nil || k.IsZero() {
		return cid.Cid{}, nil
	}
	key := ds.NewKey(keyToCIDNS + k.String())
	val, err := d.Get(ctx, key)
	if err != nil {
		return cid.Cid{}, nil // Not found, return zero CID
	}
	return cid.Decode(string(val))
}

// StoreKeyToProviderIDMapping persists the mapping from Key to ProviderID (keyToProviderIDNS),
// separately from the block data itself. ProviderID is attached separately and is never part
// of the content hash (Key). No-op if d is nil or k is zero.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore write.
//   - d (ds.Batching): the datastore to write the mapping to.
//   - k (Key): the key half of the mapping.
//   - providerID (peer.ID): the provider peer ID to associate with k.
//
// Returns:
//   - error: non-nil if the datastore write fails.
func StoreKeyToProviderIDMapping(ctx context.Context, d ds.Batching, k Key, providerID peer.ID) error {
	if d == nil || k.IsZero() {
		return nil
	}
	key := ds.NewKey(keyToProviderIDNS + k.String())
	return d.Put(ctx, key, []byte(providerID.String()))
}

// GetProviderIDFromKey retrieves the ProviderID associated with a Key via the
// keyToProviderIDNS mapping stored separately from the block data.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore read.
//   - d (ds.Batching): the datastore holding the mapping.
//   - k (Key): the key to look up.
//
// Returns:
//   - peer.ID: the mapped provider ID, or "" if d is nil, k is zero, no mapping exists, or the
//     stored value fails to decode as a peer.ID.
//   - error: currently always nil; decode/lookup failures are reported via an empty peer.ID.
func GetProviderIDFromKey(ctx context.Context, d ds.Batching, k Key) (peer.ID, error) {
	if d == nil || k.IsZero() {
		return "", nil
	}
	key := ds.NewKey(keyToProviderIDNS + k.String())
	val, err := d.Get(ctx, key)
	if err != nil {
		return "", nil // Not found, return empty peer.ID
	}
	return peer.Decode(string(val))
}

// RemoveKeyToProviderIDMapping deletes the Key -> ProviderID mapping (keyToProviderIDNS) for k.
// No-op if d is nil or k is zero.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore delete.
//   - d (ds.Batching): the datastore holding the mapping.
//   - k (Key): the key whose mapping should be removed.
//
// Returns:
//   - error: non-nil if the datastore delete fails.
func RemoveKeyToProviderIDMapping(ctx context.Context, d ds.Batching, k Key) error {
	if d == nil || k.IsZero() {
		return nil
	}
	key := ds.NewKey(keyToProviderIDNS + k.String())
	return d.Delete(ctx, key)
}

// UnindexCID removes a CID from the local manifest index (manifestIndexNS). No-op if d is nil
// or c is undefined.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore delete.
//   - d (ds.Batching): the datastore holding the index.
//   - c (cid.Cid): the CID to remove from the index.
//
// Returns:
//   - error: non-nil if the datastore delete fails.
func UnindexCID(ctx context.Context, d ds.Batching, c cid.Cid) error {
	if d == nil || !c.Defined() {
		return nil
	}
	key := ds.NewKey(manifestIndexNS + c.String())
	return d.Delete(ctx, key)
}

// DeleteLockOpts supplies an optional lock manager, holder, and key for DeleteBlockIndexed.
// When Manager, Holder, and Key are all set (Manager non-nil, Holder non-empty, Key non-zero),
// DeleteBlockIndexed acquires the per-key write lock for Key before deleting and releases it
// after a successful delete.
type DeleteLockOpts struct {
	// Manager is the KeyLockManager to acquire/release the lock through.
	Manager *KeyLockManager
	// Holder is the peer ID recorded as the lock holder (typically the local Host.ID()).
	Holder peer.ID
	// Key is the content key to lock for the duration of the delete.
	Key Key
	// RetryConfig configures exponential backoff on lock contention; nil uses library defaults.
	RetryConfig *LockRetryConfig
}

// DeleteBlockIndexed removes a block from the blockstore and unindexes its CID (via
// UnindexCID), also removing it from providerRecords if non-nil. When lockOpts is supplied
// with Manager, Holder, and a non-zero Key, it first acquires the write lock for that key
// (retrying per lockOpts.RetryConfig), performs the delete, and always releases the lock
// afterward (deferred release covers the acquired-but-failed-delete case; an explicit release
// also runs on success so the lock isn't held for the rest of the deferred call chain).
//
// Parameters:
//   - ctx (context.Context): cancels the lock acquisition and delete operations.
//   - d (ds.Batching): the datastore used to unindex the CID.
//   - bs (bstore.Blockstore): the blockstore to delete the block from.
//   - c (cid.Cid): the CID of the block to delete; a no-op if undefined.
//   - providerRecords (*LocalProviderRecords): optional provider-record tracker to update.
//   - lockOpts (*DeleteLockOpts): optional locking configuration; nil skips locking entirely.
//
// Returns:
//   - error: non-nil if lock acquisition fails, or if the blockstore delete/unindex fails.
func DeleteBlockIndexed(ctx context.Context, d ds.Batching, bs bstore.Blockstore, c cid.Cid, providerRecords *LocalProviderRecords, lockOpts *DeleteLockOpts) error {
	if !c.Defined() {
		return nil
	}

	if lockOpts != nil && lockOpts.Manager != nil && lockOpts.Holder != "" && !lockOpts.Key.IsZero() {
		if err := lockOpts.Manager.AcquireLockWithRetry(ctx, lockOpts.Key, lockOpts.Holder, 0, lockOpts.RetryConfig); err != nil {
			return fmt.Errorf("acquire lock: %w", err)
		}
		released := false
		defer func() {
			if !released {
				_ = lockOpts.Manager.ReleaseLock(context.Background(), lockOpts.Key, lockOpts.Holder)
			}
		}()
		err := deleteBlockIndexedInner(ctx, d, bs, c, providerRecords)
		if err != nil {
			return err
		}
		released = true
		_ = lockOpts.Manager.ReleaseLock(ctx, lockOpts.Key, lockOpts.Holder)
		return nil
	}

	return deleteBlockIndexedInner(ctx, d, bs, c, providerRecords)
}

// deleteBlockIndexedInner performs the unlocked body of DeleteBlockIndexed: delete the block
// from bs, unindex its CID in d, and remove it from providerRecords if provided.
//
// Parameters:
//   - ctx (context.Context): cancels the blockstore and datastore operations.
//   - d (ds.Batching): the datastore used to unindex the CID.
//   - bs (bstore.Blockstore): the blockstore to delete the block from (may be nil).
//   - c (cid.Cid): the CID of the block to delete.
//   - providerRecords (*LocalProviderRecords): optional provider-record tracker to update.
//
// Returns:
//   - error: non-nil if the blockstore delete or CID unindex fails.
func deleteBlockIndexedInner(ctx context.Context, d ds.Batching, bs bstore.Blockstore, c cid.Cid, providerRecords *LocalProviderRecords) error {
	// Delete from blockstore
	if bs != nil {
		if err := bs.DeleteBlock(ctx, c); err != nil {
			return err
		}
	}
	// Unindex CID
	if err := UnindexCID(ctx, d, c); err != nil {
		return err
	}
	// Remove from provider records
	if providerRecords != nil {
		providerRecords.Remove(c)
	}
	return nil
}

// AnnounceProvider is deprecated: provider discovery now uses token routing (SyncTokenOnPut),
// not CID-based DHT provider announcements. It still records c in ProviderRecords if set. If
// AnnounceQueue is set and currently reports a network partition, the CID is queued (via
// AnnounceQueue.Add) for a post-heal flush and the local op is recorded via
// RecordPartitionLocalOp instead of announcing immediately. Otherwise, OnAnnounce is invoked
// if set (no actual DHT announce is performed).
//
// Parameters:
//   - ctx (context.Context): passed through to RecordPartitionLocalOp when partitioned.
//   - c (cid.Cid): the CID being "announced" (recorded/queued).
func (s *Stack) AnnounceProvider(ctx context.Context, c cid.Cid) {
	if s.ProviderRecords != nil {
		s.ProviderRecords.Add(c)
	}
	if s.AnnounceQueue != nil && s.AnnounceQueue.IsPartitioned() {
		s.AnnounceQueue.Add(c)
		_ = RecordPartitionLocalOp(ctx, s.Datastore, "put", c)
		return
	}
	if s.OnAnnounce != nil {
		s.OnAnnounce()
	}
}

// FlushQueuedAnnouncements drains any CIDs buffered in s.AnnounceQueue (e.g. queued while
// partitioned by AnnounceProvider), invoking OnAnnounce for each. This is a no-op with respect
// to actual CID-based DHT announcements (token routing is used instead) and exists purely to
// clear the queue and preserve OnAnnounce callback semantics. No-op if AnnounceQueue is nil.
//
// Parameters:
//   - ctx (context.Context): passed through to AnnounceQueue.Flush.
func (s *Stack) FlushQueuedAnnouncements(ctx context.Context) {
	if s.AnnounceQueue == nil {
		return
	}
	s.AnnounceQueue.Flush(ctx, func(ctx context.Context, c cid.Cid) {
		if s.OnAnnounce != nil {
			s.OnAnnounce()
		}
	})
}

// PutBlock stores data locally via PutRawBlockIndexed, computing its Key and CID and indexing
// both. When s.KeyLockManager and s.Host are set, the write is protected by a per-key lock
// (holder = s.Host.ID(), retry policy from s.PutLockRetryConfig); otherwise no locking occurs.
// This method only performs the local store step of the Put flow; routing table updates and
// token sync/replication are handled separately (see UpdateRoutingTableOnPut).
//
// Parameters:
//   - ctx (context.Context): cancels lock acquisition and the underlying block writes.
//   - data ([]byte): the raw block bytes to store.
//
// Returns:
//   - Key: KeyFromData(data), the primary identifier for the stored block.
//   - cid.Cid: the CID computed for the block (IPFS blockstore compatibility).
//   - error: non-nil if lock acquisition or the underlying store fails.
func (s *Stack) PutBlock(ctx context.Context, data []byte) (Key, cid.Cid, error) {
	opts := (*PutLockOpts)(nil)
	if s.KeyLockManager != nil && s.Host != nil {
		opts = &PutLockOpts{Manager: s.KeyLockManager, Holder: s.Host.ID(), RetryConfig: s.PutLockRetryConfig}
	}
	return PutRawBlockIndexed(ctx, s.Datastore, s.BlockSvc, data, opts)
}

// PutPayload stores one logical payload as one content-addressed block. The
// network replication protocol transfers logical payloads atomically, so the
// local representation must use the same key/CID pair. A prior HTTP-only
// chunking path returned the first chunk's CID for the whole-payload key,
// making replication reject every payload larger than the chunk threshold.
func (s *Stack) PutPayload(ctx context.Context, data []byte) (Key, cid.Cid, error) {
	return s.PutBlock(ctx, data)
}

// DeleteBlock removes the block identified by CID c: it resolves c to a Key via
// s.RoutingTable (if set), deletes the block via DeleteBlockIndexed (protected by a per-key
// lock when s.KeyLockManager, s.Host, and a resolved key are all available), and then updates
// the routing table via UpdateRoutingTableOnDelete (which also removes the Key -> ProviderID
// mapping and syncs the deletion to the token store).
//
// Parameters:
//   - ctx (context.Context): cancels lock acquisition and the underlying delete.
//   - c (cid.Cid): the CID of the block to delete; a no-op if undefined.
//
// Returns:
//   - error: non-nil if lock acquisition or the underlying blockstore delete fails.
func (s *Stack) DeleteBlock(ctx context.Context, c cid.Cid) error {
	if !c.Defined() {
		return nil
	}
	var key Key
	if s.RoutingTable != nil {
		entry := s.RoutingTable.GetByCID(c)
		if entry != nil {
			key = entry.Key
		}
	}
	lockOpts := (*DeleteLockOpts)(nil)
	if s.KeyLockManager != nil && s.Host != nil && !key.IsZero() {
		lockOpts = &DeleteLockOpts{Manager: s.KeyLockManager, Holder: s.Host.ID(), Key: key}
	}
	err := DeleteBlockIndexed(ctx, s.Datastore, s.Blockstore, c, s.ProviderRecords, lockOpts)
	if err != nil {
		return err
	}
	s.UpdateRoutingTableOnDelete(c)
	return nil
}

// PutLockOpts supplies an optional lock manager and holder for PutRawBlockIndexed. When both
// Manager and Holder are set, the write acquires the per-key lock before writing and releases
// it after a successful write.
type PutLockOpts struct {
	// Manager is the KeyLockManager to acquire/release the lock through.
	Manager *KeyLockManager
	// Holder is the peer ID recorded as the lock holder (typically the local Host.ID()).
	Holder peer.ID
	// RetryConfig configures exponential backoff on lock contention; nil uses library defaults.
	RetryConfig *LockRetryConfig
}

// PutRawBlockIndexed stores data locally (blockstore + manifest/key/key-to-CID indexes),
// computing key = KeyFromData(data) as the primary identifier and deriving a CID via
// PutRawBlock for IPFS blockstore compatibility. This is local-only; no network I/O occurs
// (provider-ID association and token sync happen elsewhere, e.g. via
// Stack.UpdateRoutingTableOnPut). When lockOpts is supplied with both Manager and Holder set,
// the write is preceded by an AcquireLockWithRetry call for key and followed by a
// ReleaseLock call on success; the lock is also released via a deferred fallback if the
// write path returns early.
//
// Parameters:
//   - ctx (context.Context): cancels lock acquisition and the underlying block writes.
//   - d (ds.Batching): the datastore to index into.
//   - bsvc (*bserv.BlockService): the block service to write the block through.
//   - data ([]byte): the raw block bytes to store.
//   - lockOpts (*PutLockOpts): optional locking configuration; nil skips locking entirely.
//
// Returns:
//   - Key: KeyFromData(data), the primary identifier for the stored block.
//   - cid.Cid: the CID computed for the block.
//   - error: non-nil if lock acquisition fails or the underlying store fails.
func PutRawBlockIndexed(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, data []byte, lockOpts *PutLockOpts) (Key, cid.Cid, error) {
	// Generate Key from data (primary identifier)
	// NOTE: KeyFromData only hashes the data - ProviderID is NOT included in hash.
	// ProviderID will be stored separately via UpdateRoutingTableOnPut().
	key := KeyFromData(data)

	if lockOpts != nil && lockOpts.Manager != nil && lockOpts.Holder != "" {
		if err := lockOpts.Manager.AcquireLockWithRetry(ctx, key, lockOpts.Holder, 0, lockOpts.RetryConfig); err != nil {
			return Key{}, cid.Cid{}, fmt.Errorf("acquire lock: %w", err)
		}
		released := false
		defer func() {
			if !released {
				_ = lockOpts.Manager.ReleaseLock(context.Background(), key, lockOpts.Holder)
			}
		}()
		c, err := putRawBlockIndexedInner(ctx, d, bsvc, data, key)
		if err != nil {
			return Key{}, cid.Cid{}, err
		}
		released = true
		_ = lockOpts.Manager.ReleaseLock(ctx, key, lockOpts.Holder)
		return key, c, nil
	}

	c, err := putRawBlockIndexedInner(ctx, d, bsvc, data, key)
	if err != nil {
		return Key{}, cid.Cid{}, err
	}
	return key, c, nil
}

// putBlockIndexBatch stages the manifest-index (CID), key-index (Key), and Key -> CID mapping
// writes onto batch, so they commit atomically together as part of one batch commit. No-op
// (returns nil without staging anything) if batch is nil, k is zero, or c is undefined.
//
// Parameters:
//   - ctx (context.Context): cancels the batch.Put calls.
//   - batch (ds.Batch): the datastore batch to stage writes onto.
//   - k (Key): the key to index.
//   - c (cid.Cid): the CID to index and map k to.
//
// Returns:
//   - error: non-nil if any of the staged batch.Put calls fails.
func putBlockIndexBatch(ctx context.Context, batch ds.Batch, k Key, c cid.Cid) error {
	if batch == nil || k.IsZero() || !c.Defined() {
		return nil
	}
	if err := batch.Put(ctx, ds.NewKey(manifestIndexNS+c.String()), []byte{1}); err != nil {
		return err
	}
	if err := batch.Put(ctx, ds.NewKey(keyIndexNS+k.String()), []byte{1}); err != nil {
		return err
	}
	return batch.Put(ctx, ds.NewKey(keyToCIDNS+k.String()), []byte(c.String()))
}

// putRawBlockIndexedInner performs the unlocked body of PutRawBlockIndexed: writes the block
// to the blockstore via PutRawBlock, then indexes it. It prefers a single atomic batch commit
// (via putBlockIndexBatch) and falls back to three separate, best-effort IndexCID/IndexKey/
// StoreKeyToCIDMapping calls (errors ignored) if batching is unavailable or the batch commit
// fails, so indexing is attempted even when the datastore doesn't support batching well.
//
// Parameters:
//   - ctx (context.Context): cancels the block write and index writes.
//   - d (ds.Batching): the datastore to index into.
//   - bsvc (*bserv.BlockService): the block service to write the block through.
//   - data ([]byte): the raw block bytes to store.
//   - key (Key): the precomputed key for data (KeyFromData(data)).
//
// Returns:
//   - cid.Cid: the CID computed for the block.
//   - error: non-nil only if the initial PutRawBlock call fails; indexing failures are
//     swallowed by the batch-then-fallback strategy.
func putRawBlockIndexedInner(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, data []byte, key Key) (cid.Cid, error) {
	// Store block in IPFS blockstore (generates CID)
	c, err := PutRawBlock(ctx, bsvc, data)
	if err != nil {
		return cid.Cid{}, err
	}

	if batch, berr := d.Batch(ctx); berr == nil {
		if err := putBlockIndexBatch(ctx, batch, key, c); err == nil {
			if err := batch.Commit(ctx); err == nil {
				return c, nil
			}
		}
	}

	_ = IndexCID(ctx, d, c)
	_ = IndexKey(ctx, d, key)
	_ = StoreKeyToCIDMapping(ctx, d, key, c)

	return c, nil
}

// UpdateRoutingTableOnPut updates (synchronously) the routing table entry for Key after a Put
// operation, using providerID and repVector (DefaultReplicationVector() if repVector is nil).
// It also stores the Key -> ProviderID mapping in the datastore and synchronously syncs the
// token for k on the configured token store (TokenStore, or the DHT if TokenStore is nil),
// creating/updating the token with the current peer's location. No-op if s.RoutingTable is
// nil or k is zero.
//
// Parameters:
//   - k (Key): the key whose routing table entry is being updated.
//   - providerID (peer.ID): the provider to record for k.
//   - repVector (*ReplicationVector): the replication vector to store; nil uses the default.
//   - c (cid.Cid): the CID to associate with k in the routing table.
func (s *Stack) UpdateRoutingTableOnPut(k Key, providerID peer.ID, repVector *ReplicationVector, c cid.Cid) {
	s.updateRoutingTableOnPut(k, providerID, repVector, c, false)
}

// UpdateRoutingTableOnPutAsync is like UpdateRoutingTableOnPut but performs the token sync
// (SyncTokenOnPut) and the Key -> ProviderID mapping write in a background goroutine instead
// of blocking the caller. Use this for HTTP PUT handlers that want Swarm-like semantics:
// return to the client after the local store completes, while the DHT/token announce happens
// asynchronously.
//
// Parameters:
//   - k (Key): the key whose routing table entry is being updated.
//   - providerID (peer.ID): the provider to record for k.
//   - repVector (*ReplicationVector): the replication vector to store; nil uses the default.
//   - c (cid.Cid): the CID to associate with k in the routing table.
func (s *Stack) UpdateRoutingTableOnPutAsync(
	k Key,
	providerID peer.ID,
	repVector *ReplicationVector,
	c cid.Cid,
) <-chan error {
	ready := make(chan error, 1)
	if s.RoutingTable == nil || k.IsZero() {
		ready <- nil
		close(ready)
		return ready
	}
	rv := DefaultReplicationVector()
	if repVector != nil {
		rv = *repVector
	}
	s.RoutingTable.Set(k, providerID, rv, c)
	go func() {
		defer close(ready)
		if err := StoreKeyToProviderIDMapping(
			context.Background(),
			s.Datastore,
			k,
			providerID,
		); err != nil {
			log.Printf("store provider mapping for key %s: %v", k.String(), err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err := s.SyncLocalTokenLocation(ctx, k, c)
		ready <- err
	}()
	return ready
}

// SyncLocalTokenLocation publishes this stack's host as a provider for k and
// returns only after the token update has completed. Callers use it to order
// source publication before replica-location updates.
func (s *Stack) SyncLocalTokenLocation(ctx context.Context, k Key, c cid.Cid) error {
	if s == nil || s.Host == nil {
		return fmt.Errorf("storage host unavailable")
	}
	store := s.tokenValueStore()
	if store == nil {
		return fmt.Errorf("token store unavailable")
	}
	return SyncTokenOnPut(ctx, store, s.Host, k, c, s.MessageSink)
}

// updateRoutingTableOnPut is the shared implementation behind UpdateRoutingTableOnPut and
// UpdateRoutingTableOnPutAsync. It sets the routing table entry for k, persists the
// Key -> ProviderID mapping (in a goroutine if asyncTokenSync, otherwise inline), and syncs the
// token for k via SyncTokenOnPut against s.TokenStore (falling back to s.DHT) when s.Host is
// set, again either in a goroutine or inline depending on asyncTokenSync. Sync errors are
// logged, not returned. No-op if s.RoutingTable is nil or k is zero.
//
// Parameters:
//   - k (Key): the key whose routing table entry is being updated.
//   - providerID (peer.ID): the provider to record for k.
//   - repVector (*ReplicationVector): the replication vector to store; nil uses the default.
//   - c (cid.Cid): the CID to associate with k in the routing table.
//   - asyncTokenSync (bool): when true, run the datastore write and token sync in background
//     goroutines instead of blocking the caller.
func (s *Stack) updateRoutingTableOnPut(k Key, providerID peer.ID, repVector *ReplicationVector, c cid.Cid, asyncTokenSync bool) {
	if s.RoutingTable == nil || k.IsZero() {
		return
	}
	rv := DefaultReplicationVector()
	if repVector != nil {
		rv = *repVector
	}
	s.RoutingTable.Set(k, providerID, rv, c)
	if asyncTokenSync {
		d := s.Datastore
		pid := providerID
		kk := k
		go func() {
			_ = StoreKeyToProviderIDMapping(context.Background(), d, kk, pid)
		}()
	} else {
		_ = StoreKeyToProviderIDMapping(context.Background(), s.Datastore, k, providerID)
	}

	// Auto-sync token on Put operations (use TokenStore when set, else DHT)
	if s.Host != nil {
		store := s.tokenValueStore()
		if store != nil {
			doSync := func() {
				ctx := context.Background()
				if syncErr := SyncTokenOnPut(ctx, store, s.Host, k, c, s.MessageSink); syncErr != nil {
					log.Printf("SyncTokenOnPut failed for key %s: %v (host.Addrs=%d)", k.String(), syncErr, len(s.Host.Addrs()))
				}
			}
			if asyncTokenSync {
				go doSync()
			} else {
				doSync()
			}
		}
	}
}

// UpdateRoutingTableOnDelete removes the routing table entry corresponding to CID c after a
// Delete operation. It looks up the entry by CID (for IPFS compatibility), extracts its Key,
// and then removes by Key (RoutingTable.Remove) for efficiency. It also removes the
// Key -> ProviderID mapping from the datastore and syncs the deletion to the token store
// (TokenStore, or the DHT if TokenStore is nil) via SyncTokenOnDelete, removing the current
// peer from that key's token locations. No-op if s.RoutingTable is nil or no entry is found
// for c (or its Key is zero).
//
// Parameters:
//   - c (cid.Cid): the CID whose routing table entry should be removed.
func (s *Stack) UpdateRoutingTableOnDelete(c cid.Cid) {
	if s.RoutingTable == nil {
		return
	}
	// Get entry by CID to extract Key, then use Key-based removal
	entry := s.RoutingTable.GetByCID(c)
	if entry != nil && !entry.Key.IsZero() {
		s.RoutingTable.Remove(entry.Key)
		// Remove Key → ProviderID mapping separately from data
		_ = RemoveKeyToProviderIDMapping(context.Background(), s.Datastore, entry.Key)

		// Auto-sync token on Delete operations
		store := s.tokenValueStore()
		if store != nil && s.Host != nil {
			ctx := context.Background()
			_ = SyncTokenOnDelete(ctx, store, s.Host, entry.Key)
		}
	}
}

// UpdateRoutingTableOnDeleteByKey removes the routing table entry for Key k directly (Key is
// the primary identifier, so this avoids the CID lookup step UpdateRoutingTableOnDelete needs).
// It also removes the Key -> ProviderID mapping from the datastore and syncs the deletion to
// the token store (TokenStore, or the DHT if TokenStore is nil) via SyncTokenOnDelete, removing
// the current peer from k's token locations. No-op if s.RoutingTable is nil or k is zero.
//
// Parameters:
//   - k (Key): the key whose routing table entry should be removed.
func (s *Stack) UpdateRoutingTableOnDeleteByKey(k Key) {
	if s.RoutingTable == nil || k.IsZero() {
		return
	}
	s.RoutingTable.Remove(k)
	// Remove Key → ProviderID mapping separately from data
	_ = RemoveKeyToProviderIDMapping(context.Background(), s.Datastore, k)

	// Auto-sync token on Delete operations
	store := s.tokenValueStore()
	if store != nil && s.Host != nil {
		ctx := context.Background()
		_ = SyncTokenOnDelete(ctx, store, s.Host, k)
	}
}

// TriggerRepairForAllCIDsOnRecovery iterates a snapshot of every entry in s.RoutingTable and,
// for each non-zero key, calls VerifyKeyStateWithRepVector (5s timeout per key, replication
// factor 7, no RTT measurer) to check whether the key's replica distribution matches its
// replication vector. For any key found not synchronized, it fetches the block data locally
// via s.GetBlock and, if available, launches a background goroutine (30s timeout) calling
// repairProtocol.TriggerRepair to restore missing replicas. Intended to be called once after a
// network partition heals. No-op if s.RoutingTable or repairProtocol is nil, or the routing
// table is empty. Keys whose block data isn't available locally are silently skipped (repair
// cannot proceed without the data to replicate).
//
// Parameters:
//   - ctx (context.Context): cancels the per-key verification and the initial GetBlock lookup;
//     each spawned repair goroutine uses its own independent timeout.
//   - h (host.Host): supplies the local peer ID passed to VerifyKeyStateWithRepVector.
//   - repairProtocol (*RepairProtocol): performs the actual repair; if nil, the method returns
//     immediately without doing anything.
func (s *Stack) TriggerRepairForAllCIDsOnRecovery(ctx context.Context, h host.Host, repairProtocol *RepairProtocol) {
	if s.RoutingTable == nil || repairProtocol == nil {
		return
	}
	for _, entry := range s.RoutingTable.Snapshot() {
		if entry.Key.IsZero() {
			continue
		}
		go func(key Key) {
			auditCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			_, _, _ = repairProtocol.AuditAndRepair(auditCtx, key, 7)
		}(entry.Key)
	}
}

// GetBlockIndexed fetches a block by CID via GetBlockByCID and, on success, (re-)indexes the
// CID via IndexCID (best-effort; indexing errors are ignored). Retained for IPFS blockstore
// compatibility; prefer Stack.GetBlock(Key) for the Key-based flow.
//
// Parameters:
//   - ctx (context.Context): cancels the blockstore fetch and index write.
//   - d (ds.Batching): the datastore to write the CID index entry to.
//   - bsvc (*bserv.BlockService): the block service to fetch the block from.
//   - c (cid.Cid): the CID to fetch and index.
//
// Returns:
//   - []byte: the raw block bytes.
//   - error: non-nil if the underlying GetBlockByCID fetch fails.
func GetBlockIndexed(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, c cid.Cid) ([]byte, error) {
	b, err := GetBlockByCID(ctx, bsvc, c)
	if err != nil {
		return nil, err
	}
	_ = IndexCID(ctx, d, c)
	return b, nil
}

// ListIndexedCIDs enumerates CID strings recorded in the manifest index (manifestIndexNS),
// i.e. every CID that has been indexed via IndexCID (a superset that includes CIDs with a
// corresponding Key -> CID mapping). Results are paginated lexicographically: only CID strings
// strictly greater than startAfter are returned, up to limit entries (limit <= 0 means
// unlimited). Query result rows with errors are silently skipped. Returns (nil, nil) if d is
// nil.
//
// Parameters:
//   - ctx (context.Context): cancels the underlying datastore query.
//   - d (ds.Batching): the datastore to query.
//   - limit (int): maximum number of CID strings to return; <= 0 means no limit.
//   - startAfter (string): pagination cursor; only CIDs sorting strictly after this string are
//     included. Empty string means start from the beginning.
//
// Returns:
//   - []string: the matching CID strings, in the order returned by the datastore query.
//   - error: non-nil if the underlying datastore query fails.
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
