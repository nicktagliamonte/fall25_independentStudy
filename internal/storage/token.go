// Purpose: Token structure for DHT routing. Tokens contain physical locations of data.
// Per newReqs.txt: DHT routes tokens (containing physical locations) instead of directly routing data/provider announcements.
// Token is stateless (per newReqs.txt: "the key, the dht hash key has no state").

package storage

import (
	"encoding/json"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// Location represents a physical location where data is stored.
type Location struct {
	// ProviderID is the peer ID of the provider storing the data.
	ProviderID peer.ID
	// Address is the multiaddr address of the provider.
	Address multiaddr.Multiaddr
	// RTT is the round-trip time to this location (optional, for selection).
	RTT time.Duration
}

// Token represents a token that is routed through the DHT.
// Tokens contain physical locations of data instead of routing data/provider announcements directly.
//
// IMPORTANT: Token is stateless - the DHT hash key has no state per newReqs.txt.
// This means:
//   - Token is a pure data structure with no internal mutable state
//   - All methods (Marshal, Unmarshal, Validate) are stateless operations
//   - Token does not maintain caches, counters, or any runtime state
//   - Token can be safely serialized/deserialized without losing state
//   - Multiple goroutines can read the same Token concurrently without synchronization
type Token struct {
	// Key is the hash of data (primary identifier).
	Key Key
	// Locations is the list of physical locations where the data is stored.
	Locations []Location
	// Timestamp is the creation/update timestamp (Unix timestamp in nanoseconds).
	Timestamp int64
	// Version is used for conflict resolution (increments on updates).
	Version int
}

// tokenJSON is a helper struct for JSON serialization of Token.
// Converts custom types (Key, peer.ID, multiaddr.Multiaddr, time.Duration) to JSON-compatible formats.
type tokenJSON struct {
	Key       string         `json:"key"`
	Locations []locationJSON `json:"locations"`
	Timestamp int64          `json:"timestamp"`
	Version   int            `json:"version"`
}

// locationJSON is a helper struct for JSON serialization of Location.
type locationJSON struct {
	ProviderID string `json:"provider_id"`
	Address    string `json:"address"`
	RTT        int64  `json:"rtt_ns"` // nanoseconds
}

// Marshal serializes the Token to JSON bytes.
//
// Returns:
//   - []byte: the JSON encoding of the token, or nil if t is nil.
//   - error: non-nil if JSON marshaling fails.
func (t *Token) Marshal() ([]byte, error) {
	if t == nil {
		return nil, nil
	}

	tj := tokenJSON{
		Key:       t.Key.String(),
		Locations: make([]locationJSON, len(t.Locations)),
		Timestamp: t.Timestamp,
		Version:   t.Version,
	}

	for i, loc := range t.Locations {
		tj.Locations[i] = locationJSON{
			ProviderID: loc.ProviderID.String(),
			Address:    loc.Address.String(),
			RTT:        int64(loc.RTT),
		}
	}

	return json.Marshal(tj)
}

// Unmarshal deserializes JSON bytes into the Token, overwriting its fields in place.
//
// Parameters:
//   - data ([]byte): the JSON-encoded token; a zero-length slice is treated as a no-op.
//
// Returns:
//   - error: non-nil if data cannot be unmarshaled, or if the embedded key,
//     provider ID, or address fail to parse.
func (t *Token) Unmarshal(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	var tj tokenJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return err
	}

	// Parse Key
	key, err := ParseKey(tj.Key)
	if err != nil {
		return err
	}
	t.Key = key

	// Parse Locations
	t.Locations = make([]Location, len(tj.Locations))
	for i, lj := range tj.Locations {
		providerID, err := peer.Decode(lj.ProviderID)
		if err != nil {
			return err
		}

		address, err := multiaddr.NewMultiaddr(lj.Address)
		if err != nil {
			return err
		}

		t.Locations[i] = Location{
			ProviderID: providerID,
			Address:    address,
			RTT:        time.Duration(lj.RTT),
		}
	}

	t.Timestamp = tj.Timestamp
	t.Version = tj.Version

	return nil
}

// Validate checks that the Token is valid.
//
// Returns:
//   - error: nil if t is nil (a nil token is considered valid) or if Key is
//     non-zero and Locations is non-empty; otherwise a *TokenValidationError
//     describing the offending field.
func (t *Token) Validate() error {
	if t == nil {
		return nil // nil token is considered valid (no-op)
	}

	// Check Key is not zero
	if t.Key.IsZero() {
		return &TokenValidationError{Field: "key", Reason: "key cannot be zero"}
	}

	// Check Locations is not empty
	if len(t.Locations) == 0 {
		return &TokenValidationError{Field: "locations", Reason: "locations cannot be empty"}
	}

	return nil
}

// TokenValidationError represents a token validation error.
type TokenValidationError struct {
	// Field is the name of the token field that failed validation.
	Field string
	// Reason is a human-readable description of why validation failed.
	Reason string
}

// Error returns the formatted validation error message.
//
// Returns:
//   - string: a message combining the invalid field and the reason.
func (e *TokenValidationError) Error() string {
	return "token validation error: " + e.Field + " - " + e.Reason
}
