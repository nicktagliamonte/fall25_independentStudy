// Purpose: Tests for QueryOptimizer and gateway query optimization.

package gateway

import (
	"context"
	"testing"
)

func TestQueryOptimizer_ParseQuery(t *testing.T) {
	o := NewQueryOptimizer()

	if q := o.ParseQuery("exact_key"); q.Type != QueryExact || q.Pattern != "exact_key" {
		t.Errorf("ParseQuery(exact_key): got Type=%d Pattern=%q", q.Type, q.Pattern)
	}
	if q := o.ParseQuery("prefix_*"); q.Type != QueryPrefix || q.Pattern != "prefix_*" {
		t.Errorf("ParseQuery(prefix_*): got Type=%d", q.Type)
	}
	if q := o.ParseQuery("a.b"); q.Type != QueryRegex {
		t.Errorf("ParseQuery(a.b): got Type=%d, want QueryRegex", q.Type)
	}
	if q := o.ParseQuery("a|b"); q.Type != QueryMultiPartition || q.Pattern != "a|b" {
		t.Errorf("ParseQuery(a|b): got Type=%d Pattern=%q", q.Type, q.Pattern)
	}
	if q := o.ParseQuery(""); q.Pattern != "" {
		t.Errorf("ParseQuery(empty): got Pattern=%q", q.Pattern)
	}
}

func TestQueryOptimizer_BreakDownQuery(t *testing.T) {
	o := NewQueryOptimizer()

	subs := o.BreakDownQuery(Query{Pattern: "a|b", Type: QueryMultiPartition})
	if len(subs) != 2 {
		t.Fatalf("BreakDownQuery(a|b): got %d sub-queries", len(subs))
	}

	subs = o.BreakDownQuery(Query{Pattern: "x", Type: QueryExact})
	if len(subs) != 1 || subs[0].Pattern != "x" {
		t.Errorf("BreakDownQuery(single): got %v", subs)
	}

	subs = o.BreakDownQuery(Query{Pattern: "a|a|b", Type: QueryMultiPartition})
	if len(subs) != 2 {
		t.Errorf("BreakDownQuery(dedup): got %d", len(subs))
	}
}

func TestQueryOptimizer_RouteForQuery(t *testing.T) {
	o := NewQueryOptimizer()

	if r := o.RouteForQuery(Query{Pattern: "x", Type: QueryExact}); r != "DHT" {
		t.Errorf("QueryExact: got %q", r)
	}
	if r := o.RouteForQuery(Query{Pattern: "p*", Type: QueryPrefix}); r != "PHT+DHT" {
		t.Errorf("QueryPrefix: got %q", r)
	}
	if r := o.RouteForQuery(Query{Pattern: "a.b", Type: QueryRegex}); r != "P2P" {
		t.Errorf("QueryRegex: got %q", r)
	}
	if r := o.RouteForQuery(Query{Pattern: "a|b", Type: QueryMultiPartition}); r != "multi-partition" {
		t.Errorf("QueryMultiPartition: got %q", r)
	}
}

func TestQueryOptimizer_OptimizeQuery(t *testing.T) {
	o := NewQueryOptimizer()

	q := o.OptimizeQuery(Query{Pattern: "  x  ", Type: QueryExact})
	if q.Pattern != "x" {
		t.Errorf("OptimizeQuery(trim): got %q", q.Pattern)
	}

	q = o.OptimizeQuery(Query{Pattern: "a|b|a", Type: QueryMultiPartition})
	if q.Pattern != "a|b" {
		t.Errorf("OptimizeQuery(dedup): got %q", q.Pattern)
	}

	q = o.OptimizeQuery(Query{Pattern: "single", Type: QueryMultiPartition})
	subs := o.BreakDownQuery(q)
	if len(subs) != 1 || subs[0].Pattern != "single" {
		t.Errorf("OptimizeQuery(single OR): got %v", subs)
	}
}

func TestGateway_QueryOptimization(t *testing.T) {
	o := NewQueryOptimizer()

	// Optimization reduces duplicate OR parts: a|a|b|a|b → a|b
	q := o.ParseQuery("a|a|b|a|b")
	if q.Type != QueryMultiPartition {
		t.Fatalf("ParseQuery: got Type=%d, want QueryMultiPartition", q.Type)
	}
	opt := o.OptimizeQuery(q)
	if opt.Pattern != "a|b" {
		t.Errorf("OptimizeQuery dedup: got %q, want a|b", opt.Pattern)
	}
	subs := o.BreakDownQuery(opt)
	if len(subs) != 2 {
		t.Errorf("BreakDownQuery: got %d subs, want 2", len(subs))
	}

	// Optimization trims whitespace in OR parts
	q = o.ParseQuery("  x  |  y  |  z  ")
	opt = o.OptimizeQuery(q)
	if opt.Pattern != "x|y|z" {
		t.Errorf("OptimizeQuery trim: got %q", opt.Pattern)
	}

	// Optimization collapses single OR to exact
	q = o.ParseQuery("  only  ")
	opt = o.OptimizeQuery(q)
	if opt.Type != QueryExact {
		t.Errorf("single pattern: got Type=%d, want QueryExact", opt.Type)
	}
}

func TestGateway_QueryMultiPartition_OptimizationDedup(t *testing.T) {
	ts := &mockTupleSpace{
		readFunc: func(p string) ([]byte, error) {
			switch p {
			case "k1", "k2", "k3":
				return []byte("v-" + p), nil
			}
			return nil, nil
		},
	}
	g := NewGateway(nil, ts)
	optimizer := NewQueryOptimizer()
	ctx := context.Background()

	// Duplicate patterns: optimizer dedups before execution
	results, err := g.QueryMultiPartition(ctx, "k1|k2|k1|k3|k2", optimizer)
	if err != nil {
		t.Fatalf("QueryMultiPartition: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("optimization should dedup to 3 unique results, got %d", len(results))
	}
	seen := make(map[string]bool)
	for _, r := range results {
		seen[r.Key] = true
	}
	for _, k := range []string{"k1", "k2", "k3"} {
		if !seen[k] {
			t.Errorf("missing result for %s", k)
		}
	}
}
