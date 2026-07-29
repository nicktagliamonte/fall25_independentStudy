package tuplespace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/pht"
)

const (
	indexMutationProtocolID protocol.ID = "/tarsus/pht-mutation/1.0.0"
	indexOwnershipKey                   = "__tarsus_global_tuple_name_index__"
)

type indexMutation struct {
	Operation string `json:"operation"`
	Key       string `json:"key"`
	Shard     int    `json:"shard"`
}

type indexMutationResponse struct {
	Error string `json:"error,omitempty"`
}

// IndexCoordinator serializes all PHT read-modify-write mutations at one
// deterministic overlay owner. Queries still read PHT nodes directly from the
// DHT and therefore do not pass through this coordinator.
type IndexCoordinator struct {
	host     host.Host
	resolver TupleOwnerResolver
	indexes  []*pht.MutableIndex
	timeout  time.Duration
	metrics  indexMutationMetrics
}

type indexMutationMetrics struct {
	total      atomic.Uint64
	local      atomic.Uint64
	remote     atomic.Uint64
	failures   atomic.Uint64
	durationNS atomic.Uint64
	perShard   []atomic.Uint64
}

// IndexMutationStats is a monotonic snapshot of coordinator activity.
type IndexMutationStats struct {
	Total      uint64   `json:"total"`
	Local      uint64   `json:"local"`
	Remote     uint64   `json:"remote"`
	Failures   uint64   `json:"failures"`
	DurationNS uint64   `json:"duration_ns"`
	PerShard   []uint64 `json:"per_shard"`
}

func NewIndexCoordinator(h host.Host, resolver TupleOwnerResolver, stores []pht.ValueStore) (*IndexCoordinator, error) {
	if h == nil || resolver == nil || len(stores) == 0 {
		return nil, errors.New("host, owner resolver, and PHT shard stores required")
	}
	indexes := make([]*pht.MutableIndex, len(stores))
	for shard, store := range stores {
		index, err := pht.NewMutableIndex(store)
		if err != nil {
			return nil, fmt.Errorf("PHT shard %d: %w", shard, err)
		}
		indexes[shard] = index
	}
	c := &IndexCoordinator{host: h, resolver: resolver, indexes: indexes, timeout: defaultTupleTimeout}
	c.metrics.perShard = make([]atomic.Uint64, len(indexes))
	h.SetStreamHandler(indexMutationProtocolID, c.handleStream)
	return c, nil
}

func (c *IndexCoordinator) Close() {
	if c != nil && c.host != nil {
		c.host.RemoveStreamHandler(indexMutationProtocolID)
	}
}

func (c *IndexCoordinator) Insert(ctx context.Context, key string) error {
	shard := pht.ShardForKey(key, len(c.indexes))
	return c.mutate(ctx, indexMutation{Operation: "insert", Key: key, Shard: shard})
}

func (c *IndexCoordinator) Delete(ctx context.Context, key string) error {
	shard := pht.ShardForKey(key, len(c.indexes))
	return c.mutate(ctx, indexMutation{Operation: "delete", Key: key, Shard: shard})
}

func (c *IndexCoordinator) mutate(ctx context.Context, mutation indexMutation) (err error) {
	started := time.Now()
	c.metrics.total.Add(1)
	if mutation.Shard >= 0 && mutation.Shard < len(c.metrics.perShard) {
		c.metrics.perShard[mutation.Shard].Add(1)
	}
	defer func() {
		c.metrics.durationNS.Add(uint64(time.Since(started).Nanoseconds()))
		if err != nil {
			c.metrics.failures.Add(1)
		}
	}()
	owner, err := c.resolver.ResolveTupleOwner(ctx, fmt.Sprintf("%s:%d", indexOwnershipKey, mutation.Shard))
	if err != nil {
		return fmt.Errorf("resolve index owner: %w", err)
	}
	if owner == c.host.ID() {
		c.metrics.local.Add(1)
		return c.apply(ctx, mutation)
	}
	c.metrics.remote.Add(1)
	stream, err := c.host.NewStream(ctx, owner, indexMutationProtocolID)
	if err != nil {
		return fmt.Errorf("open index-owner stream: %w", err)
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	if err := json.NewEncoder(stream).Encode(mutation); err != nil {
		return fmt.Errorf("write index mutation: %w", err)
	}
	var response indexMutationResponse
	if err := json.NewDecoder(io.LimitReader(stream, maxTupleRequestBytes)).Decode(&response); err != nil {
		return fmt.Errorf("read index response: %w", err)
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}

// Snapshot returns monotonic mutation counters without resetting them.
func (c *IndexCoordinator) Snapshot() IndexMutationStats {
	stats := IndexMutationStats{
		Total:      c.metrics.total.Load(),
		Local:      c.metrics.local.Load(),
		Remote:     c.metrics.remote.Load(),
		Failures:   c.metrics.failures.Load(),
		DurationNS: c.metrics.durationNS.Load(),
		PerShard:   make([]uint64, len(c.metrics.perShard)),
	}
	for shard := range c.metrics.perShard {
		stats.PerShard[shard] = c.metrics.perShard[shard].Load()
	}
	return stats
}

func (c *IndexCoordinator) handleStream(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(c.timeout))
	var mutation indexMutation
	if err := json.NewDecoder(io.LimitReader(stream, maxTupleRequestBytes)).Decode(&mutation); err != nil {
		_ = json.NewEncoder(stream).Encode(indexMutationResponse{Error: err.Error()})
		return
	}
	err := c.apply(context.Background(), mutation)
	response := indexMutationResponse{}
	if err != nil {
		response.Error = err.Error()
	}
	_ = json.NewEncoder(stream).Encode(response)
}

func (c *IndexCoordinator) apply(ctx context.Context, mutation indexMutation) error {
	if mutation.Shard < 0 || mutation.Shard >= len(c.indexes) {
		return fmt.Errorf("invalid index shard %d", mutation.Shard)
	}
	switch mutation.Operation {
	case "insert":
		return c.indexes[mutation.Shard].Insert(ctx, mutation.Key)
	case "delete":
		return c.indexes[mutation.Shard].Delete(ctx, mutation.Key)
	default:
		return fmt.Errorf("unsupported index mutation %q", mutation.Operation)
	}
}

// IndexedTupleSpace maintains a distributed tuple-name index around an
// authoritative tuple space. Index records are hints: every candidate is
// verified by an exact operation at its tuple owner.
type IndexedTupleSpace struct {
	base         TupleSpace
	stores       []pht.ValueStore
	coordinator  *IndexCoordinator
	timeout      time.Duration
	bloomPruning bool
}

// IndexedQueryStats aggregates direct index and owner-verification work across
// all PHT shards for one tuple read.
type IndexedQueryStats struct {
	QueryKind          string `json:"query_kind"`
	ShardsContacted    int    `json:"shards_contacted"`
	ShardsSucceeded    int    `json:"shards_succeeded"`
	ShardsFailed       int    `json:"shards_failed"`
	NodesFetched       int    `json:"nodes_fetched"`
	BranchesConsidered int    `json:"branches_considered"`
	BranchesPruned     int    `json:"branches_pruned"`
	IndexCandidates    int    `json:"index_candidates"`
	IndexMatches       int    `json:"index_matches"`
	OwnerAttempts      int    `json:"owner_attempts"`
	VerifiedMatches    int    `json:"verified_matches"`
	DurationNS         int64  `json:"duration_ns"`
}

func NewIndexedTupleSpace(base TupleSpace, stores []pht.ValueStore, coordinator *IndexCoordinator) (*IndexedTupleSpace, error) {
	if base == nil || len(stores) == 0 || coordinator == nil {
		return nil, errors.New("base tuple space, PHT shard stores, and index coordinator required")
	}
	return &IndexedTupleSpace{base: base, stores: stores, coordinator: coordinator, timeout: defaultTupleTimeout, bloomPruning: true}, nil
}

// SetBloomPruning enables or disables Bloom-based branch pruning. Disabling it
// is intended for controlled experiments; exact verification remains enabled.
func (i *IndexedTupleSpace) SetBloomPruning(enabled bool) {
	i.bloomPruning = enabled
}

func (i *IndexedTupleSpace) TsPut(name string, value []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), i.timeout)
	defer cancel()
	// Index first: a stale hint is safe, while an unindexed live tuple would be
	// invisible to associative queries.
	if err := i.coordinator.Insert(ctx, name); err != nil {
		return TSPUT_ER, err
	}
	return i.base.TsPut(name, value)
}

// MutationSnapshot returns the coordinator counters visible from this node.
func (i *IndexedTupleSpace) MutationSnapshot() IndexMutationStats {
	return i.coordinator.Snapshot()
}

func (i *IndexedTupleSpace) TsRead(expr string) ([]byte, error) {
	value, _, err := i.TsReadWithStats(expr)
	return value, err
}

// TsReadWithStats performs a read and returns direct query-cost metrics.
func (i *IndexedTupleSpace) TsReadWithStats(expr string) ([]byte, IndexedQueryStats, error) {
	started := time.Now()
	stats := IndexedQueryStats{}
	if !isSimpleWildcard(expr) {
		if isTuplePattern(expr) {
			stats.QueryKind = "regex"
		} else {
			stats.QueryKind = "exact"
		}
		value, err := i.base.TsRead(expr)
		stats.OwnerAttempts = 1
		if err == nil {
			stats.VerifiedMatches = 1
		}
		stats.DurationNS = time.Since(started).Nanoseconds()
		return value, stats, err
	}
	names, stats, err := i.candidatesWithStats(expr)
	if err != nil {
		stats.DurationNS = time.Since(started).Nanoseconds()
		return nil, stats, err
	}
	for _, name := range names {
		stats.OwnerAttempts++
		value, readErr := i.base.TsRead(name)
		if readErr == nil {
			stats.VerifiedMatches = 1
			stats.DurationNS = time.Since(started).Nanoseconds()
			return value, stats, nil
		}
	}
	stats.DurationNS = time.Since(started).Nanoseconds()
	return nil, stats, ErrTupleNotFound
}

func (i *IndexedTupleSpace) TsGet(expr string) ([]byte, error) {
	if !isSimpleWildcard(expr) {
		value, err := i.base.TsGet(expr)
		if err == nil {
			i.removeIfExhausted(expr)
		}
		return value, err
	}
	names, _, err := i.candidatesWithStats(expr)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		value, getErr := i.base.TsGet(name)
		if getErr != nil {
			continue
		}
		i.removeIfExhausted(name)
		return value, nil
	}
	return nil, ErrTupleNotFound
}

func (i *IndexedTupleSpace) candidates(expr string) ([]string, error) {
	names, _, err := i.candidatesWithStats(expr)
	return names, err
}

func (i *IndexedTupleSpace) candidatesWithStats(expr string) ([]string, IndexedQueryStats, error) {
	query := pht.ParseQuery(expr)
	stats := IndexedQueryStats{ShardsContacted: len(i.stores)}
	switch query.Kind {
	case pht.QueryPrefix:
		stats.QueryKind = "prefix"
	case pht.QuerySubstring:
		stats.QueryKind = "substring"
	}
	ctx, cancel := context.WithTimeout(context.Background(), i.timeout)
	defer cancel()
	type result struct {
		names []string
		stats pht.QueryStats
		err   error
	}
	results := make(chan result, len(i.stores))
	for _, store := range i.stores {
		go func(store pht.ValueStore) {
			var names []string
			var queryStats pht.QueryStats
			var err error
			switch query.Kind {
			case pht.QueryPrefix:
				names, queryStats, err = pht.PrefixQueryDHTWithStats(ctx, store, query.Prefix)
			case pht.QuerySubstring:
				names, queryStats, err = pht.ExecuteSubstringQueryWithStatsAndPruning(ctx, store, query.Substring, 0, i.bloomPruning)
			default:
				err = ErrTupleNotFound
			}
			results <- result{names: names, stats: queryStats, err: err}
		}(store)
	}
	var parts [][]string
	var lastErr error
	for range i.stores {
		result := <-results
		if result.err != nil {
			stats.ShardsFailed++
			lastErr = result.err
			continue
		}
		stats.ShardsSucceeded++
		stats.NodesFetched += result.stats.NodesFetched
		stats.BranchesConsidered += result.stats.BranchesConsidered
		stats.BranchesPruned += result.stats.BranchesPruned
		stats.IndexCandidates += result.stats.Candidates
		stats.IndexMatches += result.stats.Matches
		parts = append(parts, result.names)
	}
	names := pht.CombineResults(parts...)
	if len(names) == 0 && lastErr != nil {
		return nil, stats, lastErr
	}
	return names, stats, nil
}

func (i *IndexedTupleSpace) removeIfExhausted(name string) {
	if _, err := i.base.TsRead(name); err == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), i.timeout)
	defer cancel()
	_ = i.coordinator.Delete(ctx, name)
}
