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

type remoteOnlyGetKey struct{}

// WithRemoteOnlyGet marks ctx so GetBlock skips the local blockstore and uses GetToken + DirectFetch.
func WithRemoteOnlyGet(ctx context.Context) context.Context {
	return context.WithValue(ctx, remoteOnlyGetKey{}, true)
}

func remoteOnlyGetFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(remoteOnlyGetKey{}).(bool)
	return v
}

// gatherAddrsForPeer returns addresses for connecting to the provider.
// Prefers location.Address if routable (not 0.0.0.0); falls back to peerstore (e.g. from DHT bootstrap).
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

type Stack struct {
	Datastore          ds.Batching
	Blockstore         bstore.Blockstore
	Bitswap            *bitswap.Bitswap
	BlockSvc           *bserv.BlockService
	DHT                *kaddht.IpfsDHT
	Router             routing.ContentRouting
	Host               host.Host
	ProviderRecords    *LocalProviderRecords
	OnAnnounce         func()
	AnnounceQueue      *AnnounceQueue
	RoutingTable       *RoutingTable
	KeyLockManager     *KeyLockManager  // when set, PutBlock/DeleteBlock acquire locks
	PutLockRetryConfig *LockRetryConfig // optional; when set, PutBlock uses this for lock retry
	// TokenStore: optional; when set, token sync uses this instead of DHT (Gateway for token routing).
	TokenStore routing.ValueStore
	// MessageSink: optional; when set, P2P message counts are reported (put/get/lookup).
	MessageSink MessageMetricsSink
	// HopSink: optional; when set, DHT lookup hop counts are reported.
	HopSink NetworkHopsSink
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
	stack.Host = h
	stack.KeyLockManager = NewKeyLockManagerFromDatastore(stack.Datastore)
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
		Datastore:    safe,
		Blockstore:   bs,
		Bitswap:      engine,
		BlockSvc:     &bsvc,
		Router:       router,
		Host:         h,
		RoutingTable: NewRoutingTable(),
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

const manifestIndexNS = "/manifest/index/"
const keyIndexNS = "/manifest/key/"
const keyToCIDNS = "/manifest/key-to-cid/"
const keyToProviderIDNS = "/manifest/key-to-provider/"

// DirectFetchProtocolID is the libp2p protocol ID for direct data fetch by key.
const DirectFetchProtocolID = "/sng40/direct-fetch/1.0.0"

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

// GetBlockByKey retrieves a block by Key. This is the primary method for Key-based block retrieval.
// Converts Key to CID internally using the Key→CID mapping, then fetches from blockstore.
// Key is the primary identifier; CID lookup is for IPFS blockstore compatibility.
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

// GetBlock retrieves a block by Key using token-based routing.
// Calls GetToken(key) to get locations, then tries direct fetch from each location in parallel.
// Returns (data, lookupHops, error). lookupHops is 0 when served from local store, or the
// DHT query hop count when resolved via token routing. Requires Stack with DHT and Host.
func (s *Stack) GetBlock(ctx context.Context, k Key) ([]byte, int, error) {
	if k.IsZero() {
		return nil, 0, fmt.Errorf("key cannot be zero")
	}

	if !remoteOnlyGetFromContext(ctx) {
		if localData, err := GetBlockByKey(ctx, s.Datastore, s.BlockSvc, k); err == nil && localData != nil {
			return localData, 0, nil
		}
	}

	tokenStore := s.TokenStore
	if tokenStore == nil && s.DHT != nil {
		tokenStore = routing.ValueStore(s.DHT)
	}
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

// DirectFetch opens a direct stream to the provider and requests data by key.
// Returns data or error. Uses DirectFetchProtocolID for the stream protocol.
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
	if blockSize <= 0 || blockSize > 10*1024*1024 {
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

// HandleDirectFetchStream handles incoming direct fetch requests by key.
// Protocol: client sends key (hex string + "\n"), server responds with:
//   - Status: "OK\n" or "ERROR: ...\n"
//   - If OK: block size (int + "\n"), then block data
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

// GetBlockByCID retrieves a block by CID (for IPFS compatibility).
// This is a compatibility method. Prefer GetBlockByKey(Key) for new code.
func GetBlockByCID(ctx context.Context, bsvc *bserv.BlockService, c cid.Cid) ([]byte, error) {
	blk, err := (*bsvc).GetBlock(ctx, c)
	if err != nil {
		return nil, err
	}
	return blk.RawData(), nil
}

// IndexCID records the presence of a CID in the local manifest index.
// Kept for IPFS blockstore compatibility.
func IndexCID(ctx context.Context, d ds.Batching, c cid.Cid) error {
	if d == nil || !c.Defined() {
		return nil
	}
	key := ds.NewKey(manifestIndexNS + c.String())
	return d.Put(ctx, key, []byte{1})
}

// IndexKey records the presence of a Key in the local manifest index.
// Key is the primary identifier for storage.
func IndexKey(ctx context.Context, d ds.Batching, k Key) error {
	if d == nil || k.IsZero() {
		return nil
	}
	key := ds.NewKey(keyIndexNS + k.String())
	return d.Put(ctx, key, []byte{1})
}

// StoreKeyToCIDMapping stores the mapping from Key to CID.
// Allows lookup of CID from Key for IPFS blockstore operations.
func StoreKeyToCIDMapping(ctx context.Context, d ds.Batching, k Key, c cid.Cid) error {
	if d == nil || k.IsZero() || !c.Defined() {
		return nil
	}
	key := ds.NewKey(keyToCIDNS + k.String())
	return d.Put(ctx, key, []byte(c.String()))
}

// GetCIDFromKey retrieves the CID associated with a Key.
// Returns empty CID if not found.
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

// StoreKeyToProviderIDMapping stores the mapping from Key to ProviderID separately from data.
// Per newReqs.txt: ProviderID is attached separately, not in hash, and stored separately from data.
// This creates a persistent mapping in the datastore, independent of the block data itself.
func StoreKeyToProviderIDMapping(ctx context.Context, d ds.Batching, k Key, providerID peer.ID) error {
	if d == nil || k.IsZero() {
		return nil
	}
	key := ds.NewKey(keyToProviderIDNS + k.String())
	return d.Put(ctx, key, []byte(providerID.String()))
}

// GetProviderIDFromKey retrieves the ProviderID associated with a Key.
// Returns empty peer.ID if not found.
// This retrieves the ProviderID mapping stored separately from data.
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

// RemoveKeyToProviderIDMapping removes the Key → ProviderID mapping.
func RemoveKeyToProviderIDMapping(ctx context.Context, d ds.Batching, k Key) error {
	if d == nil || k.IsZero() {
		return nil
	}
	key := ds.NewKey(keyToProviderIDNS + k.String())
	return d.Delete(ctx, key)
}

// UnindexCID removes a CID from the local manifest index.
func UnindexCID(ctx context.Context, d ds.Batching, c cid.Cid) error {
	if d == nil || !c.Defined() {
		return nil
	}
	key := ds.NewKey(manifestIndexNS + c.String())
	return d.Delete(ctx, key)
}

// DeleteLockOpts supplies optional lock manager, holder, and key for DeleteBlockIndexed.
// When Manager, Holder, and Key are set, acquires lock for key before delete and releases after successful delete.
// RetryConfig configures exponential backoff on lock contention; nil uses defaults.
type DeleteLockOpts struct {
	Manager     *KeyLockManager
	Holder      peer.ID
	Key         Key
	RetryConfig *LockRetryConfig
}

// DeleteBlockIndexed removes a block from the blockstore and unindexes its CID.
// Also removes from provider records if provided. When lockOpts is set with Manager, Holder, and non-zero Key,
// acquires lock for key before delete and releases after successful delete.
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

// AnnounceProvider is deprecated. Provider discovery uses token routing (SyncTokenOnPut).
// When AnnounceQueue is set and partitioned, queues for post-heal. No CID-based DHT announce.
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

// FlushQueuedAnnouncements drains the announce queue. No-op for CID announcements
// (token routing is used instead). Retained for API compatibility.
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

// PutBlock stores a block with optional lock. When KeyLockManager is set, PutRawBlockIndexed acquires lock before write.
func (s *Stack) PutBlock(ctx context.Context, data []byte) (Key, cid.Cid, error) {
	opts := (*PutLockOpts)(nil)
	if s.KeyLockManager != nil && s.Host != nil {
		opts = &PutLockOpts{Manager: s.KeyLockManager, Holder: s.Host.ID(), RetryConfig: s.PutLockRetryConfig}
	}
	return PutRawBlockIndexed(ctx, s.Datastore, s.BlockSvc, data, opts)
}

// DeleteBlock removes a block with optional lock. When KeyLockManager and RoutingTable are set,
// DeleteBlockIndexed acquires lock for key before delete and releases after.
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

// PutLockOpts supplies optional lock manager and holder for PutRawBlockIndexed.
// When both are set, acquires lock for key before write and releases after successful write.
// RetryConfig configures exponential backoff on lock contention; nil uses defaults.
type PutLockOpts struct {
	Manager     *KeyLockManager
	Holder      peer.ID
	RetryConfig *LockRetryConfig
}

// PutRawBlockIndexed stores a block and indexes both its Key (primary) and CID (for IPFS compatibility).
// Local only; no network. When lockOpts is set with Manager and Holder, acquires lock for key before write,
// releases after successful write, and returns error if lock cannot be acquired.
// Returns (Key, CID) tuple plus error. Key is the primary identifier; CID is derived for IPFS blockstore compatibility.
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

// putBlockIndexBatch writes manifest (CID), key index, and Key→CID mapping for one batch commit.
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

// UpdateRoutingTableOnPut updates the routing table entry for a Key after a Put operation.
// Uses the provided provider ID and replication vector. If repVector is nil, uses default.
// Key is the primary identifier; CID is kept for IPFS blockstore compatibility.
// Also stores Key → ProviderID mapping separately from data in the datastore.
// Auto-syncs token on Put operations (creates/updates token with current peer location).
// No-op if RoutingTable is nil.
func (s *Stack) UpdateRoutingTableOnPut(k Key, providerID peer.ID, repVector *ReplicationVector, c cid.Cid) {
	s.updateRoutingTableOnPut(k, providerID, repVector, c, false)
}

// UpdateRoutingTableOnPutAsync is like UpdateRoutingTableOnPut but runs SyncTokenOnPut in background.
// Use for HTTP PUT when matching Swarm semantics (return after local store; DHT announce async).
func (s *Stack) UpdateRoutingTableOnPutAsync(k Key, providerID peer.ID, repVector *ReplicationVector, c cid.Cid) {
	s.updateRoutingTableOnPut(k, providerID, repVector, c, true)
}

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
		var store routing.ValueStore = s.DHT
		if s.TokenStore != nil {
			store = s.TokenStore
		}
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

// UpdateRoutingTableOnDelete removes the routing table entry after a Delete (looks up by CID for compatibility).
// Gets Key from routing table entry and uses Key-based removal for efficiency.
// Also removes Key → ProviderID mapping from datastore.
// Auto-syncs token on Delete operations (removes current peer from token locations).
// No-op if RoutingTable is nil or entry not found.
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
		var store routing.ValueStore = s.DHT
		if s.TokenStore != nil {
			store = s.TokenStore
		}
		if store != nil && s.Host != nil {
			ctx := context.Background()
			_ = SyncTokenOnDelete(ctx, store, s.Host, entry.Key)
		}
	}
}

// UpdateRoutingTableOnDeleteByKey removes the routing table entry for a Key after a Delete operation.
// Key is the primary identifier. Also removes Key → ProviderID mapping from datastore.
// Auto-syncs token on Delete operations (removes current peer from token locations).
// No-op if RoutingTable is nil.
func (s *Stack) UpdateRoutingTableOnDeleteByKey(k Key) {
	if s.RoutingTable == nil || k.IsZero() {
		return
	}
	s.RoutingTable.Remove(k)
	// Remove Key → ProviderID mapping separately from data
	_ = RemoveKeyToProviderIDMapping(context.Background(), s.Datastore, k)

	// Auto-sync token on Delete operations
	var store routing.ValueStore = s.DHT
	if s.TokenStore != nil {
		store = s.TokenStore
	}
	if store != nil && s.Host != nil {
		ctx := context.Background()
		_ = SyncTokenOnDelete(ctx, store, s.Host, k)
	}
}

// TriggerRepairForAllCIDsOnRecovery verifies all keys in the routing table and triggers repair
// for any that have missing replicas. Called after network partition recovery.
// repairProtocol may be nil (repair will be skipped if nil).
func (s *Stack) TriggerRepairForAllCIDsOnRecovery(ctx context.Context, h host.Host, repairProtocol *RepairProtocol) {
	if s.RoutingTable == nil || repairProtocol == nil {
		return
	}
	// Get snapshot of all routing table entries
	entries := s.RoutingTable.Snapshot()
	if len(entries) == 0 {
		return
	}
	// Verify each key and trigger repair if needed
	for _, entry := range entries {
		if entry.Key.IsZero() {
			continue
		}
		// Verify key state with replication vector
		ctxVerify, cancelVerify := context.WithTimeout(ctx, 5*time.Second)
		tokenStore := s.TokenStore
		if tokenStore == nil {
			tokenStore = routing.ValueStore(s.DHT)
		}
		verification, verifyErr := VerifyKeyStateWithRepVector(
			ctxVerify,
			entry.Key,
			s.RoutingTable,
			tokenStore,
			h.ID(),
			nil, // RTT measurer (nil = use 0, unknown distance)
			7,   // replication factor (default)
			nil, // RTT thresholds (nil = use defaults)
		)
		cancelVerify()
		// If verification succeeded and shows not synchronized, trigger repair
		if verifyErr == nil && verification != nil && !verification.IsSynchronized {
			// Get block data locally using Key (primary identifier)
			blockData, _, err := s.GetBlock(ctx, entry.Key)
			if err != nil || len(blockData) == 0 {
				// Block not available locally, skip repair for this key
				continue
			}
			// Trigger repair asynchronously (don't block recovery callback)
			go func(k Key, v *ReplicaStateVerification, data []byte) {
				ctxRepair, cancelRepair := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancelRepair()
				_, _ = repairProtocol.TriggerRepair(ctxRepair, k, v, data)
			}(entry.Key, verification, blockData)
		}
	}
}

// GetBlockIndexed fetches a block by CID and indexes upon success. Prefer GetBlock(Key) for key-based flow.
// Uses GetBlockByCID for IPFS compatibility. For Key-based operations, use GetBlock(Key) directly.
func GetBlockIndexed(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, c cid.Cid) ([]byte, error) {
	b, err := GetBlockByCID(ctx, bsvc, c)
	if err != nil {
		return nil, err
	}
	_ = IndexCID(ctx, d, c)
	return b, nil
}

// ListIndexedCIDs enumerates indexed CIDs (Key→CID mapping exists for each). Lexicographic pagination via startAfter.
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
