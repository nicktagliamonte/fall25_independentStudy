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

// tokenNamespace is the ValueStore key prefix under which token records are
// stored/read via TokenStore, e.g. "/tokens/<hex key>".
const tokenNamespace = "/tokens/"

// Gateway is stateless: no storage, no mutable state, no cached data.
// Router and TupleSpace are references to external components; Gateway does not
// persist or mutate any state. Per planTwo: "gateway is stateless. the key, the
// dht hash key has no state." Router for token routing (DHT); TupleSpace for query.
type Gateway struct {
	// Router is the underlying libp2p content router used for token routing (DHT).
	Router routing.ContentRouting
	// TupleSpace resolves query patterns to tuple/token data. When this is a
	// tuplespace.Router, patterns are further dispatched by shape (exact →
	// DHT, prefix/substring → PHT+DHT, regex → P2P).
	TupleSpace tuplespace.TupleSpace
}

// NewGateway creates a stateless gateway with the given router and tuple space.
//
// Parameters:
//   - router (routing.ContentRouting): the content router used for token routing.
//   - ts (tuplespace.TupleSpace): the tuple space used to resolve query patterns.
//
// Returns:
//   - *Gateway: the constructed gateway.
func NewGateway(router routing.ContentRouting, ts tuplespace.TupleSpace) *Gateway {
	return &Gateway{
		Router:     router,
		TupleSpace: ts,
	}
}

// Result holds a single query result. Value is token/metadata (e.g. locations);
// Gateway does not return block data.
type Result struct {
	// Key is the matched pattern/sub-pattern (currently always the exact
	// pattern string that was looked up, not a resolved content key).
	Key string
	// Value is the raw tuple/token data (e.g. serialized storage.Token JSON) for Key.
	Value []byte
}

// Query executes the query via the tuple space. Accepts key pattern, regex, etc.
// Returns tokens/metadata, not block data. Breaks down query if needed (e.g.
// OR-separated patterns), aggregates results. Query routing (when TupleSpace is
// tuplespace.Router): exact key→DHT token lookup; prefix→PHT+DHT token lookup;
// regex→P2P tuple space; multi-partition→break down and route each part.
//
// Note: sub-patterns produced by breakDownQuery are looked up sequentially
// (via TsRead) and deduplicated by the trimmed pattern string, not run in
// parallel — QueryMultiPartition/ExecuteSubQueriesParallel is the parallel,
// optimizer-driven counterpart to this method.
//
// Parameters:
//   - ctx (context.Context): cancels the query; checked before starting and before each sub-pattern.
//   - query (Query): the pattern (and, unused here, Type) to execute. query.Pattern
//     may contain "|"-separated sub-patterns.
//
// Returns:
//   - []Result: matched key/token-JSON pairs, one per distinct non-empty
//     sub-pattern that resolved to data; nil if the pattern is empty.
//   - error: non-nil if ctx is already done or TupleSpace is unset. Individual
//     sub-pattern TsRead errors are swallowed (that sub-pattern is simply skipped).
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
//
// Parameters:
//   - pattern (string): the raw query pattern, possibly containing "|"-separated parts.
//
// Returns:
//   - []string: the individual (untrimmed, not deduplicated) sub-patterns; a
//     single-element slice containing pattern unchanged if it has no "|".
func breakDownQuery(pattern string) []string {
	if strings.Contains(pattern, "|") {
		return strings.Split(pattern, "|")
	}
	return []string{pattern}
}

// QueryMultiPartition breaks down a query involving multiple partitions, executes
// sub-queries in parallel, and aggregates results. Multi-partition: break down on
// |, route each part per its type (exact→DHT, prefix→PHT+DHT, regex→P2P).
//
// Parameters:
//   - ctx (context.Context): cancels the query.
//   - queryStr (string): the raw query string, e.g. "key1|key2|key3".
//   - optimizer (*QueryOptimizer): used to parse/optimize/break down queryStr;
//     if nil, a new QueryOptimizer is created.
//
// Returns:
//   - []Result: aggregated, deduplicated results from all sub-queries (see ExecuteSubQueriesParallel).
//   - error: non-nil if ctx is already done or TupleSpace is unset.
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
//
// Returns:
//   - routing.ValueStore: a store restricted to "/tokens/" keys, backed by
//     g.TupleSpace; nil if g.TupleSpace is unset.
func (g *Gateway) TokenStore() routing.ValueStore {
	if g.TupleSpace == nil {
		return nil
	}
	return &gatewayTokenStore{ts: g.TupleSpace}
}

// gatewayTokenStore implements routing.ValueStore by delegating to TupleSpace for /tokens/ keys.
type gatewayTokenStore struct {
	// ts is the tuple space that actually stores/serves token data.
	ts tuplespace.TupleSpace
}

// PutValue stores value under key by stripping the "/tokens/" prefix and
// delegating to the tuple space's TsPut.
//
// Parameters:
//   - ctx (context.Context): unused (present to satisfy routing.ValueStore).
//   - key (string): must start with "/tokens/"; the remainder is used as the tuple name.
//   - value ([]byte): the token payload.
//   - opts (...routing.Option): unused (present to satisfy routing.ValueStore).
//
// Returns:
//   - error: non-nil if key does not have the "/tokens/" prefix, or if the underlying TsPut failed.
func (s *gatewayTokenStore) PutValue(ctx context.Context, key string, value []byte, opts ...routing.Option) error {
	if !strings.HasPrefix(key, tokenNamespace) {
		return fmt.Errorf("gateway token store only handles %s keys, got %q", tokenNamespace, key)
	}
	hexKey := strings.TrimPrefix(key, tokenNamespace)
	_, err := s.ts.TsPut(hexKey, value)
	return err
}

// GetValue retrieves the value under key by stripping the "/tokens/" prefix
// and delegating to the tuple space's TsRead.
//
// Parameters:
//   - ctx (context.Context): unused (present to satisfy routing.ValueStore).
//   - key (string): must start with "/tokens/"; the remainder is used as the tuple name.
//   - opts (...routing.Option): unused (present to satisfy routing.ValueStore).
//
// Returns:
//   - []byte: the token payload.
//   - error: non-nil if key does not have the "/tokens/" prefix, or if the underlying TsRead failed.
func (s *gatewayTokenStore) GetValue(ctx context.Context, key string, opts ...routing.Option) ([]byte, error) {
	if !strings.HasPrefix(key, tokenNamespace) {
		return nil, fmt.Errorf("gateway token store only handles %s keys, got %q", tokenNamespace, key)
	}
	hexKey := strings.TrimPrefix(key, tokenNamespace)
	return s.ts.TsRead(hexKey)
}

// SearchValue looks up key asynchronously, returning a channel that receives
// at most one value (if GetValue succeeded and returned non-empty data)
// before being closed. Implements the streaming-search shape of routing.ValueStore.
//
// Parameters:
//   - ctx (context.Context): passed through to GetValue.
//   - key (string): must start with "/tokens/"; see GetValue.
//   - opts (...routing.Option): passed through to GetValue.
//
// Returns:
//   - <-chan []byte: a buffered (capacity 1) channel that receives the found
//     value (if any) and is then closed; errors are not surfaced on the
//     channel, only via the absence of a value.
//   - error: always nil.
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
//
// Parameters:
//   - ctx (context.Context): cancels the query; checked before starting and
//     after all goroutines complete (not actively enforced inside each
//     goroutine's TsRead call).
//   - subs ([]SubQuery): the sub-queries to execute. A single sub-query is
//     executed synchronously (no goroutine); two or more are run concurrently,
//     one goroutine per sub-query.
//
// Returns:
//   - []Result: aggregated, deduplicated-by-pattern results.
//   - error: non-nil if ctx is already/subsequently done, TupleSpace is
//     unset, or (single-sub-query case only) the TsRead call itself failed.
//     For the multi-sub-query case, individual TsRead errors are swallowed
//     (that sub-query contributes no result).
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
