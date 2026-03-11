// Purpose: Phase 7.3 benchmarks for gateway query optimization improvement.

package gateway

import (
	"context"
	"testing"
	"time"
)

// slowMockTupleSpace adds per-read latency to simulate DHT/network cost.
type slowMockTupleSpace struct {
	mockTupleSpace
	latencyPerRead time.Duration
}

func (s *slowMockTupleSpace) TsRead(tpname string) ([]byte, error) {
	time.Sleep(s.latencyPerRead)
	return s.mockTupleSpace.TsRead(tpname)
}

// BenchmarkQuery_Unoptimized measures Query with duplicate-heavy pattern (no optimizer).
// Pattern "a|a|b|a|b|a|b|a" = 8 TsRead calls, sequential. Simulates costly reads.
func BenchmarkQuery_Unoptimized(b *testing.B) {
	ts := &slowMockTupleSpace{
		mockTupleSpace: mockTupleSpace{
			readFunc: func(p string) ([]byte, error) {
				switch p {
				case "a", "b", "c", "d", "e", "f", "g", "h":
					return []byte("v-" + p), nil
				}
				return nil, nil
			},
		},
		latencyPerRead: 10 * time.Microsecond,
	}
	g := NewGateway(nil, ts)
	ctx := context.Background()
	pattern := "a|a|b|a|b|a|b|a"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := g.Query(ctx, Query{Pattern: pattern})
		if err != nil {
			b.Fatalf("Query: %v", err)
		}
	}
}

// BenchmarkQueryMultiPartition_Optimized measures QueryMultiPartition with optimizer.
// Same pattern "a|a|b|a|b|a|b|a" → optimized to "a|b" → 2 TsRead calls, parallel.
func BenchmarkQueryMultiPartition_Optimized(b *testing.B) {
	ts := &slowMockTupleSpace{
		mockTupleSpace: mockTupleSpace{
			readFunc: func(p string) ([]byte, error) {
				switch p {
				case "a", "b", "c", "d", "e", "f", "g", "h":
					return []byte("v-" + p), nil
				}
				return nil, nil
			},
		},
		latencyPerRead: 10 * time.Microsecond,
	}
	g := NewGateway(nil, ts)
	optimizer := NewQueryOptimizer()
	ctx := context.Background()
	pattern := "a|a|b|a|b|a|b|a"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := g.QueryMultiPartition(ctx, pattern, optimizer)
		if err != nil {
			b.Fatalf("QueryMultiPartition: %v", err)
		}
	}
}
