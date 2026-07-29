// Purpose: Integration tests for Phase 7.2 (Put→Token→Direct fetch, replication→multiple providers→token updated, write lock→concurrent writes, read without lock→multiple concurrent reads).
// C.1 verification: After Put, GetToken for same key; assert token.Locations non-empty.

package node

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	bstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dsync "github.com/ipfs/go-datastore/sync"
	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

func TestPutTokenCreatedGetTokenDirectFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	hA, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	infoA := peer.AddrInfo{ID: hA.ID(), Addrs: hA.Addrs()}
	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}

	buildStack := func(h host.Host, other peer.AddrInfo, bs bstore.Blockstore, datastore ds.Batching) (*mystore.Stack, *kaddht.IpfsDHT, error) {
		dhtCfg := myhost.DHTConfig{
			Mode:        myhost.DHTModeServer,
			UseTokenDHT: true, // enables /tokens/ namespace for token routing
			BootstrapPeersFunc: func() []peer.AddrInfo {
				if other.ID == h.ID() {
					return nil
				}
				return []peer.AddrInfo{other}
			},
		}
		d, err := myhost.NewDHT(ctx, h, dhtCfg)
		if err != nil {
			return nil, nil, err
		}
		dr := ctrl.NewDynamicRouter()
		router := ctrl.NewFallbackContentRouter(d, dr)
		stack, err := mystore.NewStackFromBlockstore(ctx, h, bs, datastore, router)
		if err != nil {
			_ = d.Close()
			return nil, nil, err
		}
		stack.DHT = d
		return stack, d, nil
	}

	bsA, dsA := mystore.NewEphemeralBlockstore()
	bsB, dsB := mystore.NewEphemeralBlockstore()

	stackA, dhtA, err := buildStack(hA, infoB, bsA, dsA)
	if err != nil {
		t.Fatalf("buildStack A: %v", err)
	}
	defer dhtA.Close()
	defer stackA.Close()

	stackB, dhtB, err := buildStack(hB, infoA, bsB, dsB)
	if err != nil {
		t.Fatalf("buildStack B: %v", err)
	}
	defer dhtB.Close()
	defer stackB.Close()

	stackA.ProviderRecords = mystore.NewLocalProviderRecords()

	connectAndAwaitTestDHT(t, ctx, hA, hB, dhtA, dhtB)

	// Register DirectFetch handler on A so B can fetch blocks from A
	hA.SetStreamHandler(mystore.DirectFetchProtocolID, func(stream network.Stream) {
		_ = mystore.HandleDirectFetchStream(stream, stackA)
	})

	// 1. Put on A → token created via UpdateRoutingTableOnPut → SyncTokenOnPut
	payload := []byte("token routing integration test payload")
	key, c, err := stackA.PutBlock(ctx, payload)
	if err != nil {
		t.Fatalf("PutBlock: %v", err)
	}
	stackA.UpdateRoutingTableOnPut(key, hA.ID(), nil, c)

	// C.1 verification: After Put, GetToken for same key; assert token.Locations non-empty
	tokenA, err := mystore.GetToken(ctx, stackA.DHT, key)
	if err != nil {
		t.Fatalf("GetToken on A after put: %v (token storage may require DHT record validator)", err)
	}
	if len(tokenA.Locations) == 0 {
		t.Fatal("C.1 verification failed: token.Locations must be non-empty after Put")
	}

	// 2. Wait for the observable DHT token condition on B.
	awaitTestToken(t, ctx, dhtB, key, 1)

	// 3. GetToken → Direct fetch on B (block not local, so GetBlock uses token + DirectFetch)
	got, _, err := stackB.GetBlock(ctx, key)
	if err != nil {
		t.Fatalf("GetBlock (token + DirectFetch): %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

func TestReplicationMultipleProvidersTokenUpdated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	hA, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	infoA := peer.AddrInfo{ID: hA.ID(), Addrs: hA.Addrs()}
	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}

	buildStack := func(h host.Host, other peer.AddrInfo, bs bstore.Blockstore, datastore ds.Batching) (*mystore.Stack, *kaddht.IpfsDHT, error) {
		dhtCfg := myhost.DHTConfig{
			Mode:        myhost.DHTModeServer,
			UseTokenDHT: true,
			BootstrapPeersFunc: func() []peer.AddrInfo {
				if other.ID == h.ID() {
					return nil
				}
				return []peer.AddrInfo{other}
			},
		}
		d, err := myhost.NewDHT(ctx, h, dhtCfg)
		if err != nil {
			return nil, nil, err
		}
		dr := ctrl.NewDynamicRouter()
		router := ctrl.NewFallbackContentRouter(d, dr)
		stack, err := mystore.NewStackFromBlockstore(ctx, h, bs, datastore, router)
		if err != nil {
			_ = d.Close()
			return nil, nil, err
		}
		stack.DHT = d
		return stack, d, nil
	}

	bsA, dsA := mystore.NewEphemeralBlockstore()
	bsB, dsB := mystore.NewEphemeralBlockstore()

	stackA, dhtA, err := buildStack(hA, infoB, bsA, dsA)
	if err != nil {
		t.Fatalf("buildStack A: %v", err)
	}
	defer dhtA.Close()
	defer stackA.Close()

	stackB, dhtB, err := buildStack(hB, infoA, bsB, dsB)
	if err != nil {
		t.Fatalf("buildStack B: %v", err)
	}
	defer dhtB.Close()
	defer stackB.Close()

	stackA.ProviderRecords = mystore.NewLocalProviderRecords()

	connectAndAwaitTestDHT(t, ctx, hA, hB, dhtA, dhtB)

	repairB := mystore.NewRepairProtocol(stackB, hB, nil, false)
	hB.SetStreamHandler(mystore.RepairProtocolID, func(stream network.Stream) {
		_ = repairB.HandleRepairStream(stream)
	})

	repairA := mystore.NewRepairProtocol(stackA, hA, nil, false)

	// 1. Put on A → token created with A as location
	payload := []byte("replication token integration test payload")
	key, c, err := stackA.PutBlock(ctx, payload)
	if err != nil {
		t.Fatalf("PutBlock: %v", err)
	}
	stackA.UpdateRoutingTableOnPut(key, hA.ID(), nil, c)

	// 2. Replicate from A to B via repair protocol
	blockData, _, err := stackA.GetBlock(ctx, key)
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if err := repairA.ReplicateToPeer(ctx, c, hB.ID(), blockData); err != nil {
		t.Fatalf("ReplicateToPeer A→B: %v", err)
	}

	// 3. Wait for both provider updates rather than sleeping for a fixed duration.
	token := awaitTestToken(t, ctx, dhtA, key, 2)

	// 4. Verify token has multiple providers (A and B)
	providerIDs := make(map[string]bool)
	for _, loc := range token.Locations {
		providerIDs[loc.ProviderID.String()] = true
	}
	if !providerIDs[hA.ID().String()] {
		t.Error("token missing provider A")
	}
	if !providerIDs[hB.ID().String()] {
		t.Error("token missing provider B")
	}
	if len(token.Locations) < 2 {
		t.Errorf("token has %d locations, want at least 2 (A and B)", len(token.Locations))
	}
}

func TestWriteLockConcurrentWritesOneSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lockDS := dsync.MutexWrap(ds.NewMapDatastore())
	lockMgr := mystore.NewKeyLockManagerFromDatastore(lockDS)

	hA, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	bsA, dsA := mystore.NewEphemeralBlockstore()
	bsB, dsB := mystore.NewEphemeralBlockstore()

	dhtCfg := myhost.DHTConfig{Mode: myhost.DHTModeServer}
	dhtA, _ := myhost.NewDHT(ctx, hA, dhtCfg)
	dhtB, _ := myhost.NewDHT(ctx, hB, dhtCfg)
	if dhtA != nil {
		defer dhtA.Close()
	}
	if dhtB != nil {
		defer dhtB.Close()
	}

	routerA := ctrl.NewFallbackContentRouter(dhtA, ctrl.NewDynamicRouter())
	routerB := ctrl.NewFallbackContentRouter(dhtB, ctrl.NewDynamicRouter())

	stackA, err := mystore.NewStackFromBlockstore(ctx, hA, bsA, dsA, routerA)
	if err != nil {
		t.Fatalf("buildStack A: %v", err)
	}
	defer stackA.Close()

	stackB, err := mystore.NewStackFromBlockstore(ctx, hB, bsB, dsB, routerB)
	if err != nil {
		t.Fatalf("buildStack B: %v", err)
	}
	defer stackB.Close()

	stackA.KeyLockManager = lockMgr
	stackB.KeyLockManager = lockMgr
	stackB.PutLockRetryConfig = &mystore.LockRetryConfig{
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		Timeout:        500 * time.Millisecond,
	}

	payload := []byte("concurrent write lock test payload")
	key := mystore.KeyFromData(payload)

	if err := lockMgr.AcquireLock(ctx, key, hA.ID(), 2*time.Second); err != nil {
		t.Fatalf("AcquireLock (holder): %v", err)
	}

	var wg sync.WaitGroup
	var putBErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, putBErr = stackB.PutBlock(ctx, payload)
	}()

	time.Sleep(600 * time.Millisecond)
	_ = lockMgr.ReleaseLock(ctx, key, hA.ID())

	wg.Wait()

	if putBErr == nil {
		t.Fatal("stackB.PutBlock should have failed (lock held by A)")
	}
	if !errors.Is(putBErr, mystore.ErrLockTimeout) && !errors.Is(putBErr, mystore.ErrLockHeldByAnother) {
		inner := errors.Unwrap(putBErr)
		if inner != nil && !errors.Is(inner, mystore.ErrLockTimeout) && !errors.Is(inner, mystore.ErrLockHeldByAnother) {
			t.Errorf("expected lock error, got %v", putBErr)
		}
	}

	_, _, err = stackA.PutBlock(ctx, payload)
	if err != nil {
		t.Fatalf("stackA.PutBlock after release: %v", err)
	}
}

func TestReadWithoutLockMultipleConcurrentReadsSucceed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	hA, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost A: %v", err)
	}
	defer hA.Close()

	hB, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost B: %v", err)
	}
	defer hB.Close()

	infoA := peer.AddrInfo{ID: hA.ID(), Addrs: hA.Addrs()}
	infoB := peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}

	buildStack := func(h host.Host, other peer.AddrInfo, bs bstore.Blockstore, datastore ds.Batching) (*mystore.Stack, *kaddht.IpfsDHT, error) {
		dhtCfg := myhost.DHTConfig{
			Mode:        myhost.DHTModeServer,
			UseTokenDHT: true,
			BootstrapPeersFunc: func() []peer.AddrInfo {
				if other.ID == h.ID() {
					return nil
				}
				return []peer.AddrInfo{other}
			},
		}
		d, err := myhost.NewDHT(ctx, h, dhtCfg)
		if err != nil {
			return nil, nil, err
		}
		dr := ctrl.NewDynamicRouter()
		router := ctrl.NewFallbackContentRouter(d, dr)
		stack, err := mystore.NewStackFromBlockstore(ctx, h, bs, datastore, router)
		if err != nil {
			_ = d.Close()
			return nil, nil, err
		}
		stack.DHT = d
		return stack, d, nil
	}

	bsA, dsA := mystore.NewEphemeralBlockstore()
	bsB, dsB := mystore.NewEphemeralBlockstore()

	stackA, dhtA, err := buildStack(hA, infoB, bsA, dsA)
	if err != nil {
		t.Fatalf("buildStack A: %v", err)
	}
	defer dhtA.Close()
	defer stackA.Close()

	stackB, dhtB, err := buildStack(hB, infoA, bsB, dsB)
	if err != nil {
		t.Fatalf("buildStack B: %v", err)
	}
	defer dhtB.Close()
	defer stackB.Close()

	stackA.ProviderRecords = mystore.NewLocalProviderRecords()

	connectAndAwaitTestDHT(t, ctx, hA, hB, dhtA, dhtB)

	hA.SetStreamHandler(mystore.DirectFetchProtocolID, func(stream network.Stream) {
		_ = mystore.HandleDirectFetchStream(stream, stackA)
	})

	payload := []byte("concurrent read without lock test payload")
	key, c, err := stackA.PutBlock(ctx, payload)
	if err != nil {
		t.Fatalf("PutBlock: %v", err)
	}
	stackA.UpdateRoutingTableOnPut(key, hA.ID(), nil, c)

	awaitTestToken(t, ctx, dhtB, key, 1)

	const numReaders = 8
	var wg sync.WaitGroup
	results := make([][]byte, numReaders)
	errs := make([]error, numReaders)
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _, errs[i] = stackB.GetBlock(ctx, key)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("reader %d: %v", i, err)
		}
	}
	for i, got := range results {
		if got != nil && !bytes.Equal(got, payload) {
			t.Errorf("reader %d: got %q, want %q", i, got, payload)
		}
	}
}
