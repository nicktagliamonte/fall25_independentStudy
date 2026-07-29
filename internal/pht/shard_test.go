package pht

import (
	"context"
	"testing"
)

func TestShardStoresUseIndependentNamespaces(t *testing.T) {
	base := &mockStore{}
	stores, err := NewShardStores(base, 4)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := stores[0].PutValue(ctx, "/pht/root", []byte("zero")); err != nil {
		t.Fatal(err)
	}
	if err := stores[1].PutValue(ctx, "/pht/root", []byte("one")); err != nil {
		t.Fatal(err)
	}
	zero, _ := stores[0].GetValue(ctx, "/pht/root")
	one, _ := stores[1].GetValue(ctx, "/pht/root")
	if string(zero) != "zero" || string(one) != "one" {
		t.Fatalf("shard values = %q, %q", zero, one)
	}
}

func TestShardForKeyIsStableAndDistributed(t *testing.T) {
	const shards = 16
	seen := make(map[int]bool)
	for i := 0; i < 256; i++ {
		key := string(rune(i)) + ":task"
		first := ShardForKey(key, shards)
		second := ShardForKey(key, shards)
		if first != second || first < 0 || first >= shards {
			t.Fatalf("unstable/out-of-range shard %d, %d", first, second)
		}
		seen[first] = true
	}
	if len(seen) < 12 {
		t.Fatalf("only %d/%d shards used", len(seen), shards)
	}
}
