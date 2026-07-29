package tuplespace

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/pht"
)

type indexedTestStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *indexedTestStore) PutValue(_ context.Context, key string, value []byte, _ ...interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	s.data[key] = append([]byte(nil), value...)
	return nil
}

func (s *indexedTestStore) GetValue(_ context.Context, key string, _ ...interface{}) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte(nil), value...), nil
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
