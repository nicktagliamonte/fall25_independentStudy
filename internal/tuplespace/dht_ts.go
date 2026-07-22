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
//
// Parameters:
//   - value ([]byte): the raw value read back from the DHT.
//
// Returns:
//   - bool: true if value is the single-byte tombstone marker (0x00),
//     indicating the tuple has been consumed.
func isTombstone(value []byte) bool {
	return len(value) == 1 && value[0] == 0x00
}

// ValueStore provides Put/Get for tuple space records. Implemented by routing.ValueStore (e.g. IpfsDHT).
type ValueStore interface {
	// PutValue stores value under key.
	//
	// Parameters:
	//   - ctx (context.Context): cancels/deadlines the underlying store call.
	//   - key (string): the namespaced record key.
	//   - value ([]byte): the record payload to store.
	//   - opts (...interface{}): implementation-specific options (e.g. routing.Option for a DHT-backed store).
	//
	// Returns:
	//   - error: non-nil if the store operation failed.
	PutValue(ctx context.Context, key string, value []byte, opts ...interface{}) error

	// GetValue retrieves the value stored under key.
	//
	// Parameters:
	//   - ctx (context.Context): cancels/deadlines the underlying store call.
	//   - key (string): the namespaced record key.
	//   - opts (...interface{}): implementation-specific options (e.g. routing.Option for a DHT-backed store).
	//
	// Returns:
	//   - []byte: the stored value.
	//   - error: non-nil if the key is not found or the retrieval failed.
	GetValue(ctx context.Context, key string, opts ...interface{}) ([]byte, error)
}

// dhtValueStoreAdapter adapts routing.ValueStore (with routing.Option) to tuplespace.ValueStore (with interface{}).
type dhtValueStoreAdapter struct {
	store routing.ValueStore
}

// NewDHTValueStoreAdapter creates an adapter from routing.ValueStore to tuplespace.ValueStore.
//
// Parameters:
//   - store (routing.ValueStore): the underlying libp2p routing value store (e.g. an IpfsDHT instance).
//
// Returns:
//   - ValueStore: an adapter exposing the tuplespace.ValueStore interface backed by store.
func NewDHTValueStoreAdapter(store routing.ValueStore) ValueStore {
	return &dhtValueStoreAdapter{store: store}
}

// PutValue stores value under key via the wrapped routing.ValueStore, filtering
// opts down to the routing.Option values the underlying store expects and
// discarding any options of other types.
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the DHT put.
//   - key (string): the DHT record key.
//   - value ([]byte): the record payload.
//   - opts (...interface{}): options, of which only routing.Option values are forwarded.
//
// Returns:
//   - error: non-nil if the underlying DHT put failed.
func (a *dhtValueStoreAdapter) PutValue(ctx context.Context, key string, value []byte, opts ...interface{}) error {
	routingOpts := make([]routing.Option, 0, len(opts))
	for _, opt := range opts {
		if ro, ok := opt.(routing.Option); ok {
			routingOpts = append(routingOpts, ro)
		}
	}
	return a.store.PutValue(ctx, key, value, routingOpts...)
}

// GetValue retrieves the value stored under key via the wrapped
// routing.ValueStore, filtering opts down to routing.Option values.
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the DHT get.
//   - key (string): the DHT record key.
//   - opts (...interface{}): options, of which only routing.Option values are forwarded.
//
// Returns:
//   - []byte: the retrieved value.
//   - error: non-nil if the underlying DHT get failed.
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
//
// Parameters:
//   - tpname (string): the tuple name to derive a key for.
//
// Returns:
//   - string: DHTNamespace concatenated with the hex-encoded SHA-256 hash of tpname.
func dhtKey(tpname string) string {
	h := sha256.Sum256([]byte(tpname))
	return DHTNamespace + hex.EncodeToString(h[:])
}

// DHTTupleSpace implements TupleSpace using Kademlia DHT for the storage layer.
// Storage layer is open: usable by permissioned and non-permissioned callers; no permission checks.
// Uses tombstone markers for consuming operations and TTL-based expiration (48h default per libp2p spec).
type DHTTupleSpace struct {
	// store is the underlying key/value backend (typically the Kademlia DHT).
	store ValueStore
}

// NewDHTTupleSpace creates a DHT-backed tuple space.
//
// Parameters:
//   - store (ValueStore): the backing key/value store (e.g. a DHT adapter).
//
// Returns:
//   - *DHTTupleSpace: the constructed tuple space.
func NewDHTTupleSpace(store ValueStore) *DHTTupleSpace {
	return &DHTTupleSpace{store: store}
}

// TsPut stores a tuple in the DHT.
// Returns status/error code (0 for success, non-zero for error).
// Values expire after 48 hours per libp2p Kademlia DHT standard (RFM17).
//
// Parameters:
//   - tpname (string): the tuple name; hashed via dhtKey to form the DHT record key.
//   - tpvalue ([]byte): the tuple payload; must be non-empty (empty values are
//     reserved to signal deletion via tombstone).
//
// Returns:
//   - int: 0 on success; TSPUT_ER if store is nil, tpname/tpvalue are empty, or the DHT put failed.
//   - error: non-nil describing the failure when the int result is TSPUT_ER.
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
//
// Note: consumption is implemented as a separate read-then-write-tombstone
// sequence (not atomic), so concurrent TsGet calls for the same tpname can
// both observe the data before either writes the tombstone.
//
// Parameters:
//   - tpname (string): the tuple name to consume.
//
// Returns:
//   - []byte: the tuple data, unless it was already tombstoned.
//   - error: non-nil if store is nil, tpname is empty, the DHT get failed,
//     the tuple was already consumed (tombstoned), or the tombstone write
//     failed after data was already retrieved (in which case data is still
//     returned alongside the error, best-effort).
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
//
// Parameters:
//   - tpname (string): the tuple name to read.
//
// Returns:
//   - []byte: the tuple data.
//   - error: non-nil if store is nil, tpname is empty, the DHT get failed, or the tuple is tombstoned (consumed).
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
