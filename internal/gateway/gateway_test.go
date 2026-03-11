// Purpose: Tests for Gateway.Query.

package gateway

import (
	"context"
	"testing"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

var _ tuplespace.TupleSpace = (*mockTupleSpace)(nil)

type mockTupleSpace struct {
	readFunc func(string) ([]byte, error)
}

func (m *mockTupleSpace) TsPut(tpname string, tpvalue []byte) (int, error) { return 0, nil }
func (m *mockTupleSpace) TsGet(tpname string) ([]byte, error)               { return nil, nil }
func (m *mockTupleSpace) TsRead(tpname string) ([]byte, error) {
	if m.readFunc != nil {
		return m.readFunc(tpname)
	}
	return nil, nil
}

func TestGateway_Query_NilTupleSpace(t *testing.T) {
	g := NewGateway(nil, nil)
	ctx := context.Background()
	_, err := g.Query(ctx, Query{Pattern: "x"})
	if err == nil {
		t.Fatal("expected error when tuple space is nil")
	}
}

func TestGateway_Query_EmptyPattern(t *testing.T) {
	ts := &mockTupleSpace{}
	g := NewGateway(nil, ts)
	ctx := context.Background()
	results, err := g.Query(ctx, Query{Pattern: ""})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestGateway_Query_SinglePattern(t *testing.T) {
	ts := &mockTupleSpace{
		readFunc: func(p string) ([]byte, error) {
			if p == "key1" {
				return []byte("val1"), nil
			}
			return nil, nil
		},
	}
	g := NewGateway(nil, ts)
	ctx := context.Background()
	results, err := g.Query(ctx, Query{Pattern: "key1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Key != "key1" || string(results[0].Value) != "val1" {
		t.Errorf("got Key=%q Value=%q", results[0].Key, results[0].Value)
	}
}

func TestGateway_Query_BreakDownOrPatterns(t *testing.T) {
	ts := &mockTupleSpace{
		readFunc: func(p string) ([]byte, error) {
			if p == "a" {
				return []byte("vA"), nil
			}
			if p == "b" {
				return []byte("vB"), nil
			}
			return nil, nil
		},
	}
	g := NewGateway(nil, ts)
	ctx := context.Background()
	results, err := g.Query(ctx, Query{Pattern: "a|b"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestGateway_Query_ContextCanceled(t *testing.T) {
	ts := &mockTupleSpace{}
	g := NewGateway(nil, ts)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := g.Query(ctx, Query{Pattern: "x"})
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestGateway_QueryMultiPartition_Parallel(t *testing.T) {
	ts := &mockTupleSpace{
		readFunc: func(p string) ([]byte, error) {
			switch p {
			case "a", "b", "c":
				return []byte("v-" + p), nil
			}
			return nil, nil
		},
	}
	g := NewGateway(nil, ts)
	optimizer := NewQueryOptimizer()
	ctx := context.Background()
	results, err := g.QueryMultiPartition(ctx, "a|b|c", optimizer)
	if err != nil {
		t.Fatalf("QueryMultiPartition: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	seen := make(map[string]bool)
	for _, r := range results {
		seen[r.Key] = true
		if string(r.Value) != "v-"+r.Key {
			t.Errorf("Key %s: got Value %q", r.Key, r.Value)
		}
	}
	for _, k := range []string{"a", "b", "c"} {
		if !seen[k] {
			t.Errorf("missing result for %s", k)
		}
	}
}

func TestGateway_ExecuteSubQueriesParallel(t *testing.T) {
	ts := &mockTupleSpace{
		readFunc: func(p string) ([]byte, error) {
			if p == "x" {
				return []byte("vx"), nil
			}
			if p == "y" {
				return []byte("vy"), nil
			}
			return nil, nil
		},
	}
	g := NewGateway(nil, ts)
	ctx := context.Background()
	subs := []SubQuery{{Pattern: "x", Type: QueryExact}, {Pattern: "y", Type: QueryExact}}
	results, err := g.ExecuteSubQueriesParallel(ctx, subs)
	if err != nil {
		t.Fatalf("ExecuteSubQueriesParallel: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}
