// Purpose: DHT-backed tuple space implementation for O(log N) exact-match operations.
// Per planTwo 6.2: DHT tuple space is the open storage layer and does NOT require permission.

package tuplespace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/libp2p/go-libp2p/core/routing"
)

// DHTNamespace is the key prefix for tuple space records in the DHT.
const DHTNamespace = "/tuplespace/"

// tombstoneMarker is a special value that marks a tuple as consumed/deleted.
// Per DHT standards, we use tombstone markers for immediate consumption semantics,
// and rely on TTL expiration (48h default per libp2p spec) for cleanup.
var tombstoneMarker = []byte{0x00}

// isTombstone checks if a value is a tombstone marker.
func isTombstone(value []byte) bool {
	return len(value) == 1 && value[0] == 0x00
}

// ValueStore provides Put/Get for tuple space records. Implemented by routing.ValueStore (e.g. IpfsDHT).
type ValueStore interface {
	PutValue(ctx context.Context, key string, value []byte, opts ...interface{}) error
	GetValue(ctx context.Context, key string, opts ...interface{}) ([]byte, error)
}

// dhtValueStoreAdapter adapts routing.ValueStore (with routing.Option) to tuplespace.ValueStore (with interface{}).
type dhtValueStoreAdapter struct {
	store routing.ValueStore
}

// NewDHTValueStoreAdapter creates an adapter from routing.ValueStore to tuplespace.ValueStore.
func NewDHTValueStoreAdapter(store routing.ValueStore) ValueStore {
	return &dhtValueStoreAdapter{store: store}
}

func (a *dhtValueStoreAdapter) PutValue(ctx context.Context, key string, value []byte, opts ...interface{}) error {
	routingOpts := make([]routing.Option, 0, len(opts))
	for _, opt := range opts {
		if ro, ok := opt.(routing.Option); ok {
			routingOpts = append(routingOpts, ro)
		}
	}
	return a.store.PutValue(ctx, key, value, routingOpts...)
}

func (a *dhtValueStoreAdapter) GetValue(ctx context.Context, key string, opts ...interface{}) ([]byte, error) {
	routingOpts := make([]routing.Option, 0, len(opts))
	for _, opt := range opts {
		if ro, ok := opt.(routing.Option); ok {
			routingOpts = append(routingOpts, ro)
		}
	}
	return a.store.GetValue(ctx, key, routingOpts...)
}

// dhtKey returns the DHT key for a tuple name.
func dhtKey(tpname string) string {
	h := sha256.Sum256([]byte(tpname))
	return DHTNamespace + hex.EncodeToString(h[:])
}

// DHTTupleSpace implements TupleSpace using Kademlia DHT for the storage layer.
// Storage layer is open: usable by permissioned and non-permissioned callers; no permission checks.
// Uses tombstone markers for consuming operations and TTL-based expiration (48h default per libp2p spec).
type DHTTupleSpace struct {
	store ValueStore
}

// NewDHTTupleSpace creates a DHT-backed tuple space.
func NewDHTTupleSpace(store ValueStore) *DHTTupleSpace {
	return &DHTTupleSpace{store: store}
}

// TsPut stores a tuple in the DHT.
// Returns status/error code (0 for success, non-zero for error).
// Values expire after 48 hours per libp2p Kademlia DHT standard (RFM17).
func (d *DHTTupleSpace) TsPut(tpname string, tpvalue []byte) (int, error) {
	if d.store == nil {
		return TSPUT_ER, errors.New("store required")
	}
	if tpname == "" {
		return TSPUT_ER, errors.New("tuple name required")
	}
	if len(tpvalue) == 0 {
		return TSPUT_ER, errors.New("tuple value cannot be empty (use tombstone for deletion)")
	}
	key := dhtKey(tpname)
	if err := d.store.PutValue(context.Background(), key, tpvalue); err != nil {
		return TSPUT_ER, fmt.Errorf("DHT put failed: %w", err)
	}
	return 0, nil
}

// TsGet retrieves and removes (consumes) a tuple from the DHT.
// Returns the tuple data. After retrieval, stores a tombstone marker to signal consumption.
// Tombstones are cleaned up by DHT TTL expiration (48h default per libp2p spec).
func (d *DHTTupleSpace) TsGet(tpname string) ([]byte, error) {
	if d.store == nil {
		return nil, errors.New("store required")
	}
	if tpname == "" {
		return nil, errors.New("tuple name required")
	}
	key := dhtKey(tpname)
	data, err := d.store.GetValue(context.Background(), key)
	if err != nil {
		return nil, fmt.Errorf("DHT get failed: %w", err)
	}
	// Check for tombstone (already consumed)
	if isTombstone(data) {
		return nil, errors.New("tuple already consumed")
	}
	// Consume: store tombstone marker to signal deletion
	// This follows standard DHT practice: use tombstone for immediate consumption,
	// TTL (48h default) handles cleanup per libp2p Kademlia DHT standard (RFM17).
	if err := d.store.PutValue(context.Background(), key, tombstoneMarker); err != nil {
		// If tombstone storage fails, return data anyway (best-effort consumption)
		return data, fmt.Errorf("consumed tuple but tombstone storage failed: %w", err)
	}
	return data, nil
}

// TsRead retrieves a tuple from the DHT without removing it (non-consuming).
// Returns the tuple data. Returns error if tuple is tombstoned (consumed).
func (d *DHTTupleSpace) TsRead(tpname string) ([]byte, error) {
	if d.store == nil {
		return nil, errors.New("store required")
	}
	if tpname == "" {
		return nil, errors.New("tuple name required")
	}
	key := dhtKey(tpname)
	data, err := d.store.GetValue(context.Background(), key)
	if err != nil {
		return nil, fmt.Errorf("DHT read failed: %w", err)
	}
	// Check for tombstone (consumed)
	if isTombstone(data) {
		return nil, errors.New("tuple consumed")
	}
	return data, nil
}
