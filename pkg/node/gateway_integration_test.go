// Purpose: Integration test for gateway query breakdown → multi-partition query (Phase 7.2).

package node

import (
	"context"
	"errors"
	"sync"
	"testing"

	mygateway "github.com/nicktagliamonte/fall25_independentStudy/internal/gateway"
	mytuplespace "github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

var errNotFound = errors.New("not found")

// mapValueStore implements tuplespace.ValueStore for integration tests.
// DHTTupleSpace uses it; keys are internal (e.g. /tuplespace/ + hex(sha256(tpname))).
type mapValueStore struct {
	mu sync.RWMutex
	m  map[string][]byte
}

func (s *mapValueStore) PutValue(ctx context.Context, key string, value []byte, opts ...interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[string][]byte)
	}
	s.m[key] = value
	return nil
}

func (s *mapValueStore) GetValue(ctx context.Context, key string, opts ...interface{}) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[key]
	if !ok {
		return nil, errNotFound
	}
	return v, nil
}

var _ mytuplespace.ValueStore = (*mapValueStore)(nil)

func TestGatewayQueryBreakdownMultiPartitionQuery(t *testing.T) {
	ctx := context.Background()

	store := &mapValueStore{m: make(map[string][]byte)}
	dhtTS := mytuplespace.NewDHTTupleSpace(store)

	_, _ = dhtTS.TsPut("part-a", []byte("val-a"))
	_, _ = dhtTS.TsPut("part-b", []byte("val-b"))
	_, _ = dhtTS.TsPut("part-c", []byte("val-c"))

	gateway := mygateway.NewGateway(nil, dhtTS)
	optimizer := mygateway.NewQueryOptimizer()

	results, err := gateway.QueryMultiPartition(ctx, "part-a|part-b|part-c", optimizer)
	if err != nil {
		t.Fatalf("QueryMultiPartition: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	seen := make(map[string]string)
	for _, r := range results {
		seen[r.Key] = string(r.Value)
	}
	for k, want := range map[string]string{"part-a": "val-a", "part-b": "val-b", "part-c": "val-c"} {
		if got := seen[k]; got != want {
			t.Errorf("key %s: got %q, want %q", k, got, want)
		}
	}
}
