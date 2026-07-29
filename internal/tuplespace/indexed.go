package tuplespace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func (c *IndexCoordinator) mutate(ctx context.Context, mutation indexMutation) error {
	owner, err := c.resolver.ResolveTupleOwner(ctx, fmt.Sprintf("%s:%d", indexOwnershipKey, mutation.Shard))
	if err != nil {
		return fmt.Errorf("resolve index owner: %w", err)
	}
	if owner == c.host.ID() {
		return c.apply(ctx, mutation)
	}
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
	base        TupleSpace
	stores      []pht.ValueStore
	coordinator *IndexCoordinator
	timeout     time.Duration
}

func NewIndexedTupleSpace(base TupleSpace, stores []pht.ValueStore, coordinator *IndexCoordinator) (*IndexedTupleSpace, error) {
	if base == nil || len(stores) == 0 || coordinator == nil {
		return nil, errors.New("base tuple space, PHT shard stores, and index coordinator required")
	}
	return &IndexedTupleSpace{base: base, stores: stores, coordinator: coordinator, timeout: defaultTupleTimeout}, nil
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

func (i *IndexedTupleSpace) TsRead(expr string) ([]byte, error) {
	if !isSimpleWildcard(expr) {
		return i.base.TsRead(expr)
	}
	names, err := i.candidates(expr)
	if err != nil {
		return nil, err
	}
	return firstCandidate(names, i.base.TsRead)
}

func (i *IndexedTupleSpace) TsGet(expr string) ([]byte, error) {
	if !isSimpleWildcard(expr) {
		value, err := i.base.TsGet(expr)
		if err == nil {
			i.removeIfExhausted(expr)
		}
		return value, err
	}
	names, err := i.candidates(expr)
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
	query := pht.ParseQuery(expr)
	ctx, cancel := context.WithTimeout(context.Background(), i.timeout)
	defer cancel()
	type result struct {
		names []string
		err   error
	}
	results := make(chan result, len(i.stores))
	for _, store := range i.stores {
		go func(store pht.ValueStore) {
			var names []string
			var err error
			switch query.Kind {
			case pht.QueryPrefix:
				names, err = pht.ExecutePrefixQuery(ctx, store, query.Prefix)
			case pht.QuerySubstring:
				names, err = pht.ExecuteSubstringQuery(ctx, store, query.Substring, 0)
			default:
				err = ErrTupleNotFound
			}
			results <- result{names: names, err: err}
		}(store)
	}
	var parts [][]string
	var lastErr error
	for range i.stores {
		result := <-results
		if result.err != nil {
			lastErr = result.err
			continue
		}
		parts = append(parts, result.names)
	}
	names := pht.CombineResults(parts...)
	if len(names) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return names, nil
}

func (i *IndexedTupleSpace) removeIfExhausted(name string) {
	if _, err := i.base.TsRead(name); err == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), i.timeout)
	defer cancel()
	_ = i.coordinator.Delete(ctx, name)
}

func firstCandidate(names []string, operation func(string) ([]byte, error)) ([]byte, error) {
	for _, name := range names {
		value, err := operation(name)
		if err == nil {
			return value, nil
		}
	}
	return nil, ErrTupleNotFound
}
