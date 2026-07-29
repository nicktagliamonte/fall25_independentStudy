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
	index    *pht.MutableIndex
	timeout  time.Duration
}

func NewIndexCoordinator(h host.Host, resolver TupleOwnerResolver, store pht.ValueStore) (*IndexCoordinator, error) {
	if h == nil || resolver == nil || store == nil {
		return nil, errors.New("host, owner resolver, and PHT store required")
	}
	index, err := pht.NewMutableIndex(store)
	if err != nil {
		return nil, err
	}
	c := &IndexCoordinator{host: h, resolver: resolver, index: index, timeout: defaultTupleTimeout}
	h.SetStreamHandler(indexMutationProtocolID, c.handleStream)
	return c, nil
}

func (c *IndexCoordinator) Close() {
	if c != nil && c.host != nil {
		c.host.RemoveStreamHandler(indexMutationProtocolID)
	}
}

func (c *IndexCoordinator) Insert(ctx context.Context, key string) error {
	return c.mutate(ctx, indexMutation{Operation: "insert", Key: key})
}

func (c *IndexCoordinator) Delete(ctx context.Context, key string) error {
	return c.mutate(ctx, indexMutation{Operation: "delete", Key: key})
}

func (c *IndexCoordinator) mutate(ctx context.Context, mutation indexMutation) error {
	owner, err := c.resolver.ResolveTupleOwner(ctx, indexOwnershipKey)
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
	switch mutation.Operation {
	case "insert":
		return c.index.Insert(ctx, mutation.Key)
	case "delete":
		return c.index.Delete(ctx, mutation.Key)
	default:
		return fmt.Errorf("unsupported index mutation %q", mutation.Operation)
	}
}

// IndexedTupleSpace maintains a distributed tuple-name index around an
// authoritative tuple space. Index records are hints: every candidate is
// verified by an exact operation at its tuple owner.
type IndexedTupleSpace struct {
	base        TupleSpace
	store       pht.ValueStore
	coordinator *IndexCoordinator
	timeout     time.Duration
}

func NewIndexedTupleSpace(base TupleSpace, store pht.ValueStore, coordinator *IndexCoordinator) (*IndexedTupleSpace, error) {
	if base == nil || store == nil || coordinator == nil {
		return nil, errors.New("base tuple space, PHT store, and index coordinator required")
	}
	return &IndexedTupleSpace{base: base, store: store, coordinator: coordinator, timeout: defaultTupleTimeout}, nil
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
	switch query.Kind {
	case pht.QueryPrefix:
		return pht.ExecutePrefixQuery(ctx, i.store, query.Prefix)
	case pht.QuerySubstring:
		return pht.ExecuteSubstringQuery(ctx, i.store, query.Substring, 0)
	default:
		return nil, ErrTupleNotFound
	}
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
