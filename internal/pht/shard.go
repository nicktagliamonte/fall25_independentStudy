package pht

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

const DefaultShardCount = 16

// ShardForKey assigns tuple names uniformly by SHA-256 rather than by their
// human-readable prefix, avoiding hot shards for workloads dominated by names
// such as "task:*".
func ShardForKey(key string, shardCount int) int {
	if shardCount <= 1 {
		return 0
	}
	digest := sha256.Sum256([]byte(key))
	value := uint64(digest[0])<<56 |
		uint64(digest[1])<<48 |
		uint64(digest[2])<<40 |
		uint64(digest[3])<<32 |
		uint64(digest[4])<<24 |
		uint64(digest[5])<<16 |
		uint64(digest[6])<<8 |
		uint64(digest[7])
	return int(value % uint64(shardCount))
}

// ShardStore gives one logical PHT shard an independent DHT keyspace while
// retaining the /pht/ namespace used by the Kademlia validator.
type ShardStore struct {
	base  ValueStore
	shard int
}

func NewShardStores(base ValueStore, shardCount int) ([]ValueStore, error) {
	if base == nil {
		return nil, errors.New("base PHT store required")
	}
	if shardCount <= 0 {
		return nil, errors.New("positive PHT shard count required")
	}
	stores := make([]ValueStore, shardCount)
	for shard := 0; shard < shardCount; shard++ {
		stores[shard] = &ShardStore{base: base, shard: shard}
	}
	return stores, nil
}

func (s *ShardStore) PutValue(ctx context.Context, key string, value []byte, opts ...interface{}) error {
	return s.base.PutValue(ctx, s.key(key), value, opts...)
}

func (s *ShardStore) GetValue(ctx context.Context, key string, opts ...interface{}) ([]byte, error) {
	return s.base.GetValue(ctx, s.key(key), opts...)
}

func (s *ShardStore) key(key string) string {
	suffix := strings.TrimPrefix(key, DHTNamespace)
	return fmt.Sprintf("%s%d/%s", DHTNamespace, s.shard, suffix)
}
