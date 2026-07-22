// Purpose: DHT token routing. Stores and retrieves tokens containing physical locations of data.
// Per newReqs.txt: DHT routes tokens (containing physical locations) instead of directly routing data/provider announcements.

package storage

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p/core/routing"
)

// TokenNamespace is the DHT key prefix for token records.
const TokenNamespace = "/tokens/"

// tokenDHTKey returns the DHT key for a token.
// Format: "/tokens/" + hex(key)
//
// Parameters:
//   - k (Key): the token's content key.
//
// Returns:
//   - string: the fully-qualified DHT key ("/tokens/" + hex(k)).
func tokenDHTKey(k Key) string {
	return TokenNamespace + k.String()
}

// PutToken stores a token in the DHT using key as the DHT key.
// Token contains locations, not data. The token is validated, serialized to JSON,
// and stored in the DHT. Token TTL is 48 hours, handled automatically by the
// underlying libp2p DHT (not enforced by this function).
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the DHT write.
//   - dht (routing.ValueStore): the DHT value store to write to.
//   - key (Key): the token's key; must be non-zero.
//   - token (Token): the token to store; must pass Token.Validate.
//
// Returns:
//   - error: non-nil if dht is nil, key is zero, token fails validation, JSON
//     marshaling fails, or the underlying DHT PutValue call fails.
func PutToken(ctx context.Context, dht routing.ValueStore, key Key, token Token) error {
	if dht == nil {
		return fmt.Errorf("DHT ValueStore required")
	}
	if key.IsZero() {
		return fmt.Errorf("key cannot be zero")
	}

	// Validate token before storing
	if err := token.Validate(); err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}

	// Marshal token to JSON bytes
	tokenData, err := token.Marshal()
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}

	// Construct DHT key: "/tokens/" + hex(key)
	dhtKey := tokenDHTKey(key)

	// Store token in DHT
	// Token TTL: 48 hours (per libp2p DHT standard - handled automatically by DHT)
	if err := dht.PutValue(ctx, dhtKey, tokenData); err != nil {
		return fmt.Errorf("DHT put token failed: %w", err)
	}

	return nil
}

// GetToken retrieves a token from the DHT using key.
// Returns the token with locations for direct device-to-device fetch. Token
// contains physical locations (peer.ID + multiaddr) where data is stored.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the DHT read.
//   - dht (routing.ValueStore): the DHT value store to read from.
//   - key (Key): the token's key; must be non-zero.
//
// Returns:
//   - Token: the retrieved and validated token (zero value on error).
//   - error: non-nil if dht is nil, key is zero, the DHT GetValue call fails,
//     no data is found, unmarshaling fails, or the unmarshaled token fails validation.
func GetToken(ctx context.Context, dht routing.ValueStore, key Key) (Token, error) {
	var token Token

	if dht == nil {
		return token, fmt.Errorf("DHT ValueStore required")
	}
	if key.IsZero() {
		return token, fmt.Errorf("key cannot be zero")
	}

	// Construct DHT key: "/tokens/" + hex(key)
	dhtKey := tokenDHTKey(key)

	// Retrieve token data from DHT
	tokenData, err := dht.GetValue(ctx, dhtKey)
	if err != nil {
		return token, fmt.Errorf("DHT get token failed: %w", err)
	}

	if len(tokenData) == 0 {
		return token, fmt.Errorf("token not found")
	}

	// Unmarshal JSON bytes into Token
	if err := token.Unmarshal(tokenData); err != nil {
		return token, fmt.Errorf("unmarshal token: %w", err)
	}

	// Validate unmarshaled token
	if err := token.Validate(); err != nil {
		return token, fmt.Errorf("retrieved token validation failed: %w", err)
	}

	return token, nil
}

// UpdateTokenLocations updates a token with new locations (for replication).
// Uses conflict resolution to handle concurrent updates; the update function
// replaces the token's Locations wholesale with the provided slice.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the update.
//   - dht (routing.ValueStore): the DHT value store backing token storage.
//   - key (Key): the token's key; must be non-zero.
//   - locations ([]Location): the new location set to store; must be non-empty.
//
// Returns:
//   - error: non-nil if dht is nil, key is zero, locations is empty, or the
//     underlying conflict-resolved update fails after its retries.
func UpdateTokenLocations(ctx context.Context, dht routing.ValueStore, key Key, locations []Location) error {
	if dht == nil {
		return fmt.Errorf("DHT ValueStore required")
	}
	if key.IsZero() {
		return fmt.Errorf("key cannot be zero")
	}
	if len(locations) == 0 {
		return fmt.Errorf("locations cannot be empty")
	}

	// Use conflict resolution to update token
	err := UpdateTokenWithConflictResolution(ctx, dht, key, func(currentToken Token) Token {
		updatedToken := currentToken
		updatedToken.Locations = locations
		return updatedToken
	}, 3)

	if err != nil {
		return fmt.Errorf("update token locations: %w", err)
	}

	return nil
}
