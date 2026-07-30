package tuplespace

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/pht"
)

type indexedTestStore struct {
	mu   sync.Mutex
	data map[string][]byte
	puts atomic.Int64
	gets atomic.Int64
}

type slowIndexedTestTupleSpace struct {
	live string
}

func (s *slowIndexedTestTupleSpace) TsPut(string, []byte) (int, error) {
	return 0, nil
}

func (s *slowIndexedTestTupleSpace) TsGet(string) ([]byte, error) {
	return nil, ErrTupleNotFound
}

func (s *slowIndexedTestTupleSpace) TsRead(name string) ([]byte, error) {
	time.Sleep(50 * time.Millisecond)
	if name == s.live {
		return []byte(name), nil
	}
	return nil, ErrTupleNotFound
}

func (s *indexedTestStore) PutValue(_ context.Context, key string, value []byte, _ ...interface{}) error {
	s.puts.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	s.data[key] = append([]byte(nil), value...)
	return nil
}

func (s *indexedTestStore) GetValue(_ context.Context, key string, _ ...interface{}) ([]byte, error) {
	s.gets.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte(nil), value...), nil
}

func TestIndexCoordinatorDeduplicatesRetriedMutationAtOwner(t *testing.T) {
	h, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	store := &indexedTestStore{}
	stores, err := pht.NewShardStores(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewIndexCoordinator(
		h,
		fixedOwnerResolver{owner: h.ID()},
		stores,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	coordinator.SetAuthorityTiming(0, time.Minute, 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	fence, err := coordinator.authority.resolve(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	mutation := indexMutation{
		Operation: "insert",
		Key:       "task:deduplicated",
		Shard:     0,
		Fence:     fence,
		RequestID: "retry-id",
	}
	if err := coordinator.applyAuthorizedOnce(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	putsAfterFirst := store.puts.Load()
	getsAfterFirst := store.gets.Load()
	if err := coordinator.applyAuthorizedOnce(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	if store.puts.Load() != putsAfterFirst || store.gets.Load() != getsAfterFirst {
		t.Fatalf(
			"duplicate mutation touched DHT: puts %d->%d, gets %d->%d",
			putsAfterFirst,
			store.puts.Load(),
			getsAfterFirst,
			store.gets.Load(),
		)
	}
}

func TestIndexCoordinatorFailsOverUnreachableAuthorityAndRetries(t *testing.T) {
	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer clientHost.Close()
	failedHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer failedHost.Close()
	successorHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer successorHost.Close()
	connectTupleHosts(t, clientHost, successorHost)

	store := &indexedTestStore{}
	stores, err := pht.NewShardStores(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	resolver := failoverOwnerResolver{
		primary:   failedHost.ID(),
		successor: successorHost.ID(),
	}
	client, err := NewIndexCoordinator(clientHost, resolver, stores)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	successor, err := NewIndexCoordinator(successorHost, resolver, stores)
	if err != nil {
		t.Fatal(err)
	}
	defer successor.Close()
	client.SetAuthorityTiming(0, time.Minute, 0)
	successor.SetAuthorityTiming(0, time.Minute, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const key = "task:authority-failover"
	if err := client.Insert(ctx, key); err != nil {
		t.Fatal(err)
	}
	record, err := client.authority.read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if record.Epoch != 2 || record.Writer != successorHost.ID().String() {
		t.Fatalf("authority after retry = %+v", record)
	}
	root, err := pht.GetNode(ctx, stores[0], "")
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Entries) != 1 || root.Entries[0] != key {
		t.Fatalf("PHT root entries = %v", root.Entries)
	}
}

func TestIndexedTupleSpaceMultiPeerMutationAndQuery(t *testing.T) {
	ownerHost, _ := libp2p.New()
	defer ownerHost.Close()
	clientAHost, _ := libp2p.New()
	defer clientAHost.Close()
	clientBHost, _ := libp2p.New()
	defer clientBHost.Close()
	connectTupleHosts(t, clientAHost, ownerHost)
	connectTupleHosts(t, clientBHost, ownerHost)

	store := &indexedTestStore{}
	stores, err := pht.NewShardStores(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	resolver := fixedOwnerResolver{owner: ownerHost.ID()}
	ownerBase, _ := NewDistributedTupleSpace(ownerHost, resolver)
	defer ownerBase.Close()
	clientABase, _ := NewDistributedTupleSpace(clientAHost, resolver)
	defer clientABase.Close()
	clientBBase, _ := NewDistributedTupleSpace(clientBHost, resolver)
	defer clientBBase.Close()
	ownerCoordinator, _ := NewIndexCoordinator(ownerHost, resolver, stores)
	defer ownerCoordinator.Close()
	clientACoordinator, _ := NewIndexCoordinator(clientAHost, resolver, stores)
	defer clientACoordinator.Close()
	clientBCoordinator, _ := NewIndexCoordinator(clientBHost, resolver, stores)
	defer clientBCoordinator.Close()
	for _, coordinator := range []*IndexCoordinator{
		ownerCoordinator,
		clientACoordinator,
		clientBCoordinator,
	} {
		coordinator.SetAuthorityTiming(0, time.Minute, 0)
	}
	clientA, _ := NewIndexedTupleSpace(clientABase, stores, clientACoordinator)
	clientB, _ := NewIndexedTupleSpace(clientBBase, stores, clientBCoordinator)

	const tuplesPerClient = 24
	var wg sync.WaitGroup
	for i := 0; i < tuplesPerClient; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("task:image:dataset-a:%03d", i)
			if _, err := clientA.TsPut(name, []byte(name)); err != nil {
				t.Errorf("client A put: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("task:text:dataset-b:%03d", i)
			if _, err := clientB.TsPut(name, []byte(name)); err != nil {
				t.Errorf("client B put: %v", err)
			}
		}()
	}
	wg.Wait()
	for label, snapshot := range map[string]IndexMutationStats{
		"client-a": clientACoordinator.Snapshot(),
		"client-b": clientBCoordinator.Snapshot(),
	} {
		if snapshot.Total != tuplesPerClient || snapshot.Remote != tuplesPerClient ||
			snapshot.Failures != 0 || snapshot.DurationNS == 0 {
			t.Fatalf("%s mutation stats = %+v", label, snapshot)
		}
		usedShards := 0
		for _, count := range snapshot.PerShard {
			if count > 0 {
				usedShards++
			}
		}
		if usedShards < 2 {
			t.Fatalf("%s used only %d shards: %+v", label, usedShards, snapshot.PerShard)
		}
	}

	got, queryStats, err := clientA.TsReadWithStats("task:image:dataset-a:*")
	if err != nil || string(got[:21]) != "task:image:dataset-a:" {
		t.Fatalf("indexed prefix read = %q, %v", got, err)
	}
	if queryStats.QueryKind != "prefix" || queryStats.ShardsContacted != len(stores) ||
		queryStats.ShardsSucceeded != len(stores) || queryStats.NodesFetched == 0 ||
		queryStats.IndexMatches != tuplesPerClient || queryStats.VerifiedMatches != 1 ||
		queryStats.DurationNS <= 0 {
		t.Fatalf("indexed prefix stats = %+v", queryStats)
	}
	got, err = clientB.TsRead("*dataset-b:01*")
	if err != nil || string(got[:20]) != "task:text:dataset-b:" {
		t.Fatalf("indexed substring read = %q, %v", got, err)
	}

	name := "task:image:dataset-a:000"
	got, err = clientB.TsGet(name)
	if err != nil || string(got) != name {
		t.Fatalf("exact indexed get = %q, %v", got, err)
	}
	if _, err := clientA.TsRead("task:image:dataset-a:000"); !errors.Is(err, ErrTupleNotFound) {
		t.Fatalf("consumed tuple read = %v", err)
	}
	if _, err := clientA.TsRead("*dataset-a:000*"); !errors.Is(err, ErrTupleNotFound) {
		t.Fatalf("deleted index candidate read = %v", err)
	}
}

func TestIndexedTupleSpaceRoutesMutationAndExactTupleOverOverlay(t *testing.T) {
	clientHost, _ := libp2p.New()
	defer clientHost.Close()
	relayHost, _ := libp2p.New()
	defer relayHost.Close()
	ownerHost, _ := libp2p.New()
	defer ownerHost.Close()
	connectTupleHosts(t, clientHost, relayHost)
	connectTupleHosts(t, relayHost, ownerHost)

	resolver := fixedOwnerResolver{owner: ownerHost.ID()}
	store := &indexedTestStore{}
	stores, err := pht.NewShardStores(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	clientBase, _ := NewDistributedTupleSpace(clientHost, resolver)
	defer clientBase.Close()
	relayBase, _ := NewDistributedTupleSpace(relayHost, resolver)
	defer relayBase.Close()
	ownerBase, _ := NewDistributedTupleSpace(ownerHost, resolver)
	defer ownerBase.Close()
	clientCoordinator, _ := NewIndexCoordinator(clientHost, resolver, stores)
	defer clientCoordinator.Close()
	relayCoordinator, _ := NewIndexCoordinator(relayHost, resolver, stores)
	defer relayCoordinator.Close()
	ownerCoordinator, _ := NewIndexCoordinator(ownerHost, resolver, stores)
	defer ownerCoordinator.Close()
	for _, coordinator := range []*IndexCoordinator{
		clientCoordinator,
		relayCoordinator,
		ownerCoordinator,
	} {
		coordinator.SetAuthorityTiming(0, time.Minute, 0)
	}
	client, err := NewIndexedTupleSpace(clientBase, stores, clientCoordinator)
	if err != nil {
		t.Fatal(err)
	}
	if clientHost.Network().Connectedness(ownerHost.ID()) == network.Connected {
		t.Fatal("test requires client and owner to have no direct connection")
	}

	if _, err := client.TsPut("task:overlay:indexed", []byte("payload")); err != nil {
		t.Fatalf("overlay indexed put: %v", err)
	}
	got, err := client.TsRead("task:overlay:*")
	if err != nil || string(got) != "payload" {
		t.Fatalf("overlay indexed read = %q, %v", got, err)
	}
	if clientHost.Network().Connectedness(ownerHost.ID()) == network.Connected {
		t.Fatal("indexed overlay request unexpectedly created a direct connection")
	}
}

func TestIndexedTupleSpaceVerifiesStaleCandidatesConcurrently(t *testing.T) {
	h, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	store := &indexedTestStore{}
	stores, err := pht.NewShardStores(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	resolver := fixedOwnerResolver{owner: h.ID()}
	coordinator, err := NewIndexCoordinator(h, resolver, stores)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	coordinator.SetAuthorityTiming(0, time.Minute, 0)

	const live = "task:stale:040"
	base := &slowIndexedTestTupleSpace{live: live}
	indexed, err := NewIndexedTupleSpace(base, stores, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	for candidate := 0; candidate <= 40; candidate++ {
		name := fmt.Sprintf("task:stale:%03d", candidate)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := coordinator.Insert(ctx, name)
		cancel()
		if err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
	}

	started := time.Now()
	value, stats, err := indexed.TsReadWithStats("task:stale:*")
	elapsed := time.Since(started)
	if err != nil || string(value) != live {
		t.Fatalf("read = %q, %v", value, err)
	}
	if stats.OwnerAttempts <= 1 || stats.VerifiedMatches != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("verification took %v; candidates appear to be serialized", elapsed)
	}
}

func TestIndexedTupleSpaceReplaceReassertsIndexMembership(t *testing.T) {
	h, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	store := &indexedTestStore{}
	stores, err := pht.NewShardStores(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	resolver := fixedOwnerResolver{owner: h.ID()}
	coordinator, err := NewIndexCoordinator(h, resolver, stores)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	coordinator.SetAuthorityTiming(0, time.Minute, 0)
	indexed, err := NewIndexedTupleSpace(NewNativeTupleSpace(), stores, coordinator)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := indexed.TsReplace("storage-available:peer", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := indexed.TsReplace("storage-available:peer", []byte("second")); err != nil {
		t.Fatal(err)
	}
	snapshot := coordinator.Snapshot()
	if snapshot.Total != 2 || snapshot.Failures != 0 {
		t.Fatalf("replacement mutation stats = %+v", snapshot)
	}
	got, err := indexed.TsRead("storage-available:peer")
	if err != nil || string(got) != "second" {
		t.Fatalf("replacement read = %q, %v", got, err)
	}
}
