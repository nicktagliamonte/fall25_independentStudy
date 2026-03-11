// Purpose: Stateless gateway for token routing and query operations.
// Per planTwo Phase 5.1: gateway has no state; routes tokens, not data.
// Gateway routes tokens (location metadata, tuple values) and never fetches or
// transfers block content; data retrieval is done by callers via direct fetch.

package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

const tokenNamespace = "/tokens/"

// Gateway is stateless: no storage, no mutable state, no cached data.
// Router and TupleSpace are references to external components; Gateway does not
// persist or mutate any state. Per planTwo: "gateway is stateless. the key, the
// dht hash key has no state." Router for token routing (DHT); TupleSpace for query.
type Gateway struct {
	Router     routing.ContentRouting
	TupleSpace tuplespace.TupleSpace
}

// NewGateway creates a stateless gateway with the given router and tuple space.
func NewGateway(router routing.ContentRouting, ts tuplespace.TupleSpace) *Gateway {
	return &Gateway{
		Router:     router,
		TupleSpace: ts,
	}
}

// Result holds a single query result. Value is token/metadata (e.g. locations);
// Gateway does not return block data.
type Result struct {
	Key   string
	Value []byte
}

// Query executes the query via the tuple space. Accepts key pattern, regex, etc.
// Returns tokens/metadata, not block data. Breaks down query if needed (e.g.
// OR-separated patterns), aggregates results. Query routing (when TupleSpace is
// tuplespace.Router): exact key→DHT token lookup; prefix→PHT+DHT token lookup;
// regex→P2P tuple space; multi-partition→break down and route each part.
func (g *Gateway) Query(ctx context.Context, query Query) ([]Result, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if g.TupleSpace == nil {
		return nil, fmt.Errorf("tuple space required for query")
	}
	if query.Pattern == "" {
		return nil, nil
	}

	patterns := breakDownQuery(query.Pattern)
	var results []Result
	seen := make(map[string]bool)

	for _, p := range patterns {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		data, err := g.TupleSpace.TsRead(p)
		if err != nil {
			continue
		}
		if data == nil {
			continue
		}
		key := p
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, Result{Key: key, Value: data})
	}

	return results, nil
}

// breakDownQuery splits a query into sub-queries when needed.
// Supports | as OR separator for multiple patterns.
func breakDownQuery(pattern string) []string {
	if strings.Contains(pattern, "|") {
		return strings.Split(pattern, "|")
	}
	return []string{pattern}
}

// QueryMultiPartition breaks down a query involving multiple partitions, executes
// sub-queries in parallel, and aggregates results. Multi-partition: break down on
// |, route each part per its type (exact→DHT, prefix→PHT+DHT, regex→P2P).
func (g *Gateway) QueryMultiPartition(ctx context.Context, queryStr string, optimizer *QueryOptimizer) ([]Result, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if g.TupleSpace == nil {
		return nil, fmt.Errorf("tuple space required for query")
	}
	if optimizer == nil {
		optimizer = NewQueryOptimizer()
	}
	query := optimizer.ParseQuery(queryStr)
	query = optimizer.OptimizeQuery(query)
	subs := optimizer.BreakDownQuery(query)
	return g.ExecuteSubQueriesParallel(ctx, subs)
}

// TokenStore returns a routing.ValueStore that delegates token Put/Get to the Gateway's TupleSpace.
// Used by storage.SyncTokenOnPut when Gateway handles token routing (Phase 5.3).
func (g *Gateway) TokenStore() routing.ValueStore {
	if g.TupleSpace == nil {
		return nil
	}
	return &gatewayTokenStore{ts: g.TupleSpace}
}

// gatewayTokenStore implements routing.ValueStore by delegating to TupleSpace for /tokens/ keys.
type gatewayTokenStore struct {
	ts tuplespace.TupleSpace
}

func (s *gatewayTokenStore) PutValue(ctx context.Context, key string, value []byte, opts ...routing.Option) error {
	if !strings.HasPrefix(key, tokenNamespace) {
		return fmt.Errorf("gateway token store only handles %s keys, got %q", tokenNamespace, key)
	}
	hexKey := strings.TrimPrefix(key, tokenNamespace)
	_, err := s.ts.TsPut(hexKey, value)
	return err
}

func (s *gatewayTokenStore) GetValue(ctx context.Context, key string, opts ...routing.Option) ([]byte, error) {
	if !strings.HasPrefix(key, tokenNamespace) {
		return nil, fmt.Errorf("gateway token store only handles %s keys, got %q", tokenNamespace, key)
	}
	hexKey := strings.TrimPrefix(key, tokenNamespace)
	return s.ts.TsRead(hexKey)
}

func (s *gatewayTokenStore) SearchValue(ctx context.Context, key string, opts ...routing.Option) (<-chan []byte, error) {
	ch := make(chan []byte, 1)
	go func() {
		defer close(ch)
		val, err := s.GetValue(ctx, key, opts...)
		if err == nil && len(val) > 0 {
			ch <- val
		}
	}()
	return ch, nil
}

// ExecuteSubQueriesParallel runs sub-queries in parallel and aggregates results.
// Each SubQuery.Pattern is passed to TupleSpace.TsRead; Router routes exact→DHT,
// prefix→PHT+DHT, regex→P2P per pattern.
func (g *Gateway) ExecuteSubQueriesParallel(ctx context.Context, subs []SubQuery) ([]Result, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if g.TupleSpace == nil {
		return nil, fmt.Errorf("tuple space required for query")
	}
	if len(subs) == 0 {
		return nil, nil
	}
	if len(subs) == 1 {
		data, err := g.TupleSpace.TsRead(subs[0].Pattern)
		if err != nil {
			return nil, err
		}
		if data == nil {
			return nil, nil
		}
		return []Result{{Key: subs[0].Pattern, Value: data}}, nil
	}

	var mu sync.Mutex
	seen := make(map[string]bool)
	var results []Result
	var wg sync.WaitGroup

	for _, sub := range subs {
		sub := sub
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ctx.Err() != nil {
				return
			}
			p := strings.TrimSpace(sub.Pattern)
			if p == "" {
				return
			}
			data, err := g.TupleSpace.TsRead(p)
			if err != nil || data == nil {
				return
			}
			mu.Lock()
			if !seen[p] {
				seen[p] = true
				results = append(results, Result{Key: p, Value: data})
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return results, nil
}
