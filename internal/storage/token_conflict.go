// Purpose: Token versioning and conflict resolution for concurrent updates.
// Implements optimistic concurrency control and conflict resolution for token updates.

package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/routing"
)

// TokenConflictError represents a conflict detected during token update.
type TokenConflictError struct {
	// ExpectedVersion is the version the caller believed was current.
	ExpectedVersion int
	// ActualVersion is the version actually found in the store.
	ActualVersion int
	// Message is a human-readable description of the conflict.
	Message string
}

// Error returns the formatted conflict error message, including expected and actual versions.
//
// Returns:
//   - string: a message describing the version mismatch.
func (e *TokenConflictError) Error() string {
	return fmt.Sprintf("token conflict: %s (expected version %d, got %d)", e.Message, e.ExpectedVersion, e.ActualVersion)
}

// ResolveTokenConflict resolves conflicts between two token versions.
// Strategy: Merge locations from both versions, use higher version number, use later timestamp.
// If local and remote have different Keys, local is returned unchanged as a defensive
// fallback (this should not happen in practice, since conflicts only arise between two
// versions of the token for the same key).
//
// Parameters:
//   - local (Token): the caller's in-memory/candidate token version.
//   - remote (Token): the token version read from the DHT.
//
// Returns:
//   - Token: a merged token with combined Locations (deduplicated by ProviderID,
//     preferring the remote entry on collision), the higher of the two Version
//     values incremented by one, and the later of the two Timestamp values.
func ResolveTokenConflict(local Token, remote Token) Token {
	if local.Key != remote.Key {
		// Different keys - shouldn't happen, but return local as fallback
		return local
	}

	// Use higher version number
	mergedVersion := local.Version
	if remote.Version > mergedVersion {
		mergedVersion = remote.Version
	}

	// Use later timestamp
	mergedTimestamp := local.Timestamp
	if remote.Timestamp > mergedTimestamp {
		mergedTimestamp = remote.Timestamp
	}

	// Merge locations: deduplicate by ProviderID, prefer more recent address if same peer
	locationMap := make(map[string]Location) // key: ProviderID.String()

	// Add all locations from local token
	for _, loc := range local.Locations {
		locationMap[loc.ProviderID.String()] = loc
	}

	// Add locations from remote token (overwrites if same ProviderID, which is fine)
	for _, loc := range remote.Locations {
		// If same ProviderID exists, prefer the one with later timestamp
		if _, exists := locationMap[loc.ProviderID.String()]; exists {
			// Compare timestamps if available, otherwise keep existing
			// For simplicity, prefer remote if it exists (it's more recent)
			locationMap[loc.ProviderID.String()] = loc
		} else {
			locationMap[loc.ProviderID.String()] = loc
		}
	}

	// Convert map back to slice
	mergedLocations := make([]Location, 0, len(locationMap))
	for _, loc := range locationMap {
		mergedLocations = append(mergedLocations, loc)
	}

	return Token{
		Key:       local.Key,
		Locations: mergedLocations,
		Timestamp: mergedTimestamp,
		Version:   mergedVersion + 1, // Increment version for merged result
	}
}

// PutTokenWithConflictResolution stores a token in DHT with conflict resolution.
// Implements optimistic concurrency control: reads the current token, checks its
// version against newToken's version, resolves conflicts if the versions differ
// (via ResolveTokenConflict), then writes back. If no token currently exists for
// key, newToken is written directly via PutToken. Retries on write failure with
// a linear backoff of 50ms * attempt number.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of DHT reads and writes.
//   - dht (routing.ValueStore): the DHT value store backing token storage.
//   - key (Key): the token's key; must be non-zero.
//   - newToken (Token): the candidate token to store or merge in.
//   - maxRetries (int): maximum write attempts on failure; values < 1 default to 3.
//
// Returns:
//   - error: non-nil if dht is nil, key is zero, or the put fails after
//     maxRetries attempts (wrapping the underlying error).
func PutTokenWithConflictResolution(ctx context.Context, dht routing.ValueStore, key Key, newToken Token, maxRetries int) error {
	if dht == nil {
		return fmt.Errorf("DHT ValueStore required")
	}
	if key.IsZero() {
		return fmt.Errorf("key cannot be zero")
	}
	if maxRetries < 1 {
		maxRetries = 3 // Default to 3 retries
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Read current token from DHT
		currentToken, err := GetToken(ctx, dht, key)
		if err != nil {
			// Token doesn't exist - safe to write new token
			return PutToken(ctx, dht, key, newToken)
		}

		// Check for conflict: if versions differ, we have a conflict
		if currentToken.Version != newToken.Version {
			// Conflict detected - resolve it
			resolvedToken := ResolveTokenConflict(newToken, currentToken)

			// Try to write resolved token
			// Note: DHT PutValue is eventually consistent, so we may still have conflicts
			// But we've merged the locations, so the result is still valid
			if err := PutToken(ctx, dht, key, resolvedToken); err != nil {
				if attempt < maxRetries-1 {
					// Retry after a short delay
					time.Sleep(time.Millisecond * time.Duration(50*(attempt+1)))
					continue
				}
				return fmt.Errorf("put resolved token failed after %d attempts: %w", maxRetries, err)
			}
			return nil
		}

		// No conflict - versions match, safe to update
		// Increment version for this update
		newToken.Version = currentToken.Version + 1
		newToken.Timestamp = time.Now().UnixNano()

		if err := PutToken(ctx, dht, key, newToken); err != nil {
			if attempt < maxRetries-1 {
				// Retry after a short delay
				time.Sleep(time.Millisecond * time.Duration(50*(attempt+1)))
				continue
			}
			return fmt.Errorf("put token failed after %d attempts: %w", maxRetries, err)
		}

		return nil
	}

	return fmt.Errorf("put token failed after %d attempts", maxRetries)
}

// UpdateTokenWithConflictResolution updates a token with conflict resolution.
// Reads the current token, applies updateFunc to produce a new version (bumping
// Version and Timestamp), then writes it back through PutTokenWithConflictResolution
// with a single internal attempt. The outer retry loop re-reads and re-applies
// updateFunc on failure, so updateFunc should be idempotent/pure with respect to
// its input token.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of DHT reads and writes.
//   - dht (routing.ValueStore): the DHT value store backing token storage.
//   - key (Key): the token's key; must be non-zero.
//   - updateFunc (func(Token) Token): transforms the current token into the desired
//     next state; must be non-nil.
//   - maxRetries (int): maximum read-modify-write attempts; values < 1 default to 3.
//
// Returns:
//   - error: non-nil if dht is nil, key is zero, updateFunc is nil, the initial
//     GetToken fails, or the update fails after maxRetries attempts.
func UpdateTokenWithConflictResolution(
	ctx context.Context,
	dht routing.ValueStore,
	key Key,
	updateFunc func(Token) Token,
	maxRetries int,
) error {
	if dht == nil {
		return fmt.Errorf("DHT ValueStore required")
	}
	if key.IsZero() {
		return fmt.Errorf("key cannot be zero")
	}
	if updateFunc == nil {
		return fmt.Errorf("update function required")
	}
	if maxRetries < 1 {
		maxRetries = 3
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Read current token
		currentToken, err := GetToken(ctx, dht, key)
		if err != nil {
			return fmt.Errorf("get token: %w", err)
		}

		// Apply update function
		updatedToken := updateFunc(currentToken)
		updatedToken.Version = currentToken.Version + 1
		updatedToken.Timestamp = time.Now().UnixNano()

		// Write back with conflict resolution
		if err := PutTokenWithConflictResolution(ctx, dht, key, updatedToken, 1); err != nil {
			if attempt < maxRetries-1 {
				time.Sleep(time.Millisecond * time.Duration(50*(attempt+1)))
				continue
			}
			return fmt.Errorf("update token failed after %d attempts: %w", maxRetries, err)
		}

		return nil
	}

	return fmt.Errorf("update token failed after %d attempts", maxRetries)
}
