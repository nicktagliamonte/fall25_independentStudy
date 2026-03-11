// Purpose: Query optimizer for gateway query routing. Per planTwo Phase 5.2.
// p2pTS mimics the query optimizer and the general gateway concept.
//
// Query routing (when TupleSpace is tuplespace.Router):
//   - QueryExact         → DHT token lookup
//   - QueryPrefix        → PHT + DHT token lookup
//   - QueryRegex         → P2P tuple space
//   - QueryMultiPartition → Break down and route each part to DHT/PHT/P2P
package gateway

import (
	"strings"
)

// RouteTarget constants for query routing observability.
const (
	RouteDHT       = "DHT"
	RoutePHTDHT    = "PHT+DHT"
	RouteP2P       = "P2P"
	RoutePartition = "partition"
)

// QueryType identifies the query kind for routing.
type QueryType int

const (
	QueryExact          QueryType = iota // Exact key, DHT token lookup
	QueryPrefix                          // Prefix pattern, PHT + DHT token lookup
	QueryRegex                           // Complex regex, P2P tuple space
	QueryMultiPartition                  // Multi-partition, break down and route
)

// Query holds a query with pattern and type for routing.
type Query struct {
	Pattern string
	Type    QueryType
}

// SubQuery is a single sub-query from a broken-down query.
type SubQuery struct {
	Pattern string
	Type    QueryType
}

// QueryOptimizer parses, breaks down, and optimizes queries.
type QueryOptimizer struct{}

// NewQueryOptimizer creates a QueryOptimizer.
func NewQueryOptimizer() *QueryOptimizer {
	return &QueryOptimizer{}
}

// regexMetachars are regex metacharacters beyond simple *.
const regexMetachars = ".+?^$[]{}|()\\"

// ParseQuery classifies the pattern and returns a Query with Type set.
// Exact: no wildcards. Prefix: simple * (trailing or surrounding).
// Regex: contains .+?^$[]{}|()\  MultiPartition: contains | as OR separator.
func (o *QueryOptimizer) ParseQuery(query string) Query {
	q := Query{Pattern: strings.TrimSpace(query)}
	if q.Pattern == "" {
		return q
	}
	if strings.Contains(q.Pattern, "|") {
		q.Type = QueryMultiPartition
		return q
	}
	if strings.ContainsAny(q.Pattern, regexMetachars) {
		q.Type = QueryRegex
		return q
	}
	if strings.Contains(q.Pattern, "*") {
		q.Type = QueryPrefix
		return q
	}
	q.Type = QueryExact
	return q
}

// BreakDownQuery splits a query into sub-queries. For QueryMultiPartition,
// splits on |. Otherwise returns a single SubQuery.
func (o *QueryOptimizer) BreakDownQuery(query Query) []SubQuery {
	if query.Pattern == "" {
		return nil
	}
	if query.Type == QueryMultiPartition && strings.Contains(query.Pattern, "|") {
		parts := strings.Split(query.Pattern, "|")
		var out []SubQuery
		seen := make(map[string]bool)
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			sub := o.ParseQuery(p)
			out = append(out, SubQuery{Pattern: sub.Pattern, Type: sub.Type})
		}
		return out
	}
	return []SubQuery{{Pattern: query.Pattern, Type: query.Type}}
}

// RouteTarget returns the routing target for a query. Exact→DHT, Prefix→PHT+DHT,
// Regex→P2P, MultiPartition→partition. Used for observability; actual routing
// happens when TupleSpace.TsRead is called (tuplespace.Router routes by pattern).
func (o *QueryOptimizer) RouteTarget(query Query) string {
	switch query.Type {
	case QueryExact:
		return RouteDHT
	case QueryPrefix:
		return RoutePHTDHT
	case QueryRegex:
		return RouteP2P
	case QueryMultiPartition:
		return RoutePartition
	default:
		return RouteDHT
	}
}

// RouteForQuery returns the routing target for a query. Used for documentation
// and routing decisions. Exact→DHT token lookup; Prefix→PHT+DHT token lookup;
// Regex→P2P tuple space; MultiPartition→break down and route each part.
func (o *QueryOptimizer) RouteForQuery(query Query) string {
	switch query.Type {
	case QueryExact:
		return "DHT"
	case QueryPrefix:
		return "PHT+DHT"
	case QueryRegex:
		return "P2P"
	case QueryMultiPartition:
		return "multi-partition"
	default:
		return "unknown"
	}
}

// OptimizeQuery rewrites a query for efficiency. Trims whitespace, deduplicates
// OR parts, and normalizes the pattern.
func (o *QueryOptimizer) OptimizeQuery(query Query) Query {
	if query.Pattern == "" {
		return query
	}
	opt := Query{Pattern: strings.TrimSpace(query.Pattern), Type: query.Type}
	if opt.Type == QueryMultiPartition && strings.Contains(opt.Pattern, "|") {
		parts := strings.Split(opt.Pattern, "|")
		var kept []string
		seen := make(map[string]bool)
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			kept = append(kept, p)
		}
		if len(kept) == 1 {
			opt.Pattern = kept[0]
			opt.Type = o.ParseQuery(kept[0]).Type
		} else {
			opt.Pattern = strings.Join(kept, "|")
		}
	}
	return opt
}
