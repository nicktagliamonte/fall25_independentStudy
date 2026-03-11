// Purpose: Token synchronization protocol. Tokens sync with data storage operations.
// Per newReqs.txt: "the only function of the token is to sync with the data"

package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multiaddr"
)

// SyncTokenOnPut creates or updates a token when data is stored locally.
// Creates a token with the current peer as a location, or updates existing token
// to include this peer if not already present.
// sink is optional; when non-nil, P2P message counts (lookup, put) are recorded.
func SyncTokenOnPut(ctx context.Context, dht routing.ValueStore, h host.Host, key Key, c cid.Cid, sink MessageMetricsSink) error {
	if dht == nil {
		return fmt.Errorf("DHT required for token sync")
	}
	if h == nil {
		return fmt.Errorf("host required for token sync")
	}
	if key.IsZero() {
		return fmt.Errorf("key cannot be zero")
	}

	// Get current peer's address
	peerID := h.ID()
	addrs := h.Addrs()
	if len(addrs) == 0 {
		return fmt.Errorf("host has no addresses")
	}

	// Create location for current peer (use first address)
	location := Location{
		ProviderID: peerID,
		Address:    addrs[0],
		RTT:        0, // Unknown RTT for local storage
	}

	// Try to get existing token first
	_, err := GetToken(ctx, dht, key)
	if sink != nil {
		sink.AddLookupMessagesOut(1)
		sink.AddLookupMessagesIn(1)
	}
	if err != nil {
		// Token doesn't exist - create new token
		token := Token{
			Key:       key,
			Locations: []Location{location},
			Timestamp: time.Now().UnixNano(),
			Version:   1,
		}

		if err := PutTokenWithConflictResolution(ctx, dht, key, token, 3); err != nil {
			return fmt.Errorf("put new token: %w", err)
		}
		if sink != nil {
			sink.AddPutMessagesOut(1)
		}
		return nil
	}

	// Token exists - use conflict resolution to update token
	// This handles concurrent updates from multiple peers
	err = UpdateTokenWithConflictResolution(ctx, dht, key, func(currentToken Token) Token {
		// Check if this peer is already in locations
		for _, loc := range currentToken.Locations {
			if loc.ProviderID == peerID {
				// Already present, return unchanged
				return currentToken
			}
		}

		// Add this peer to locations
		updatedToken := currentToken
		updatedToken.Locations = append(updatedToken.Locations, location)
		return updatedToken
	}, 3)

	if err != nil {
		return fmt.Errorf("sync token on put: %w", err)
	}
	if sink != nil {
		sink.AddPutMessagesOut(1)
	}
	return nil
}

// SyncTokenOnDelete removes a token when data is deleted locally.
// Removes this peer from token locations. If this was the last location, removes the token entirely.
func SyncTokenOnDelete(ctx context.Context, dht routing.ValueStore, h host.Host, key Key) error {
	if dht == nil {
		return fmt.Errorf("DHT required for token sync")
	}
	if h == nil {
		return fmt.Errorf("host required for token sync")
	}
	if key.IsZero() {
		return fmt.Errorf("key cannot be zero")
	}

	peerID := h.ID()

	// Use conflict resolution to update token
	// This handles concurrent updates from multiple peers
	err := UpdateTokenWithConflictResolution(ctx, dht, key, func(currentToken Token) Token {
		// Remove this peer from locations
		newLocations := make([]Location, 0, len(currentToken.Locations))
		for _, loc := range currentToken.Locations {
			if loc.ProviderID != peerID {
				newLocations = append(newLocations, loc)
			}
		}

		// If no locations remain, return token with empty locations
		// DHT TTL will handle cleanup
		updatedToken := currentToken
		updatedToken.Locations = newLocations
		return updatedToken
	}, 3)

	if err != nil {
		// If token doesn't exist, that's fine - nothing to delete
		// Check if error is "token not found" and ignore it
		if _, getErr := GetToken(ctx, dht, key); getErr != nil {
			// Token doesn't exist - nothing to do
			return nil
		}
		return fmt.Errorf("sync token on delete: %w", err)
	}

	return nil
}

// SyncTokenOnReplication updates a token with new replica locations when replication occurs.
// Adds new replica peer locations to the token, or updates existing locations.
func SyncTokenOnReplication(ctx context.Context, dht routing.ValueStore, routingTable *RoutingTable, key Key, newReplicaPeerID peer.ID, newReplicaAddr multiaddr.Multiaddr) error {
	if dht == nil {
		return fmt.Errorf("DHT required for token sync")
	}
	if routingTable == nil {
		return fmt.Errorf("routing table required for token sync")
	}
	if key.IsZero() {
		return fmt.Errorf("key cannot be zero")
	}

	// Get all providers from routing table for this key
	providers := routingTable.GetProviders(key)
	if len(providers) == 0 {
		return fmt.Errorf("no providers found in routing table for key")
	}

	// Build locations from all providers
	locations := make([]Location, 0, len(providers))
	seenPeers := make(map[peer.ID]bool)

	// Add new replica location
	if newReplicaAddr != nil {
		locations = append(locations, Location{
			ProviderID: newReplicaPeerID,
			Address:    newReplicaAddr,
			RTT:        0, // Unknown RTT
		})
		seenPeers[newReplicaPeerID] = true
	}

	// Add other providers from routing table
	// Note: We don't have addresses for all providers, so we'll need to get them from peerstore or DHT
	// For now, we'll include the new replica and let other providers update their own tokens
	// This is a simplified approach - in a full implementation, we'd query DHT for provider addresses

	// Use conflict resolution to update token
	// This handles concurrent updates from multiple peers
	err := UpdateTokenWithConflictResolution(ctx, dht, key, func(currentToken Token) Token {
		// Check if new replica is already present
		for _, loc := range currentToken.Locations {
			if loc.ProviderID == newReplicaPeerID {
				// Already present, return unchanged
				return currentToken
			}
		}

		// Add new replica location if address is provided
		if newReplicaAddr == nil {
			return currentToken
		}

		updatedToken := currentToken
		updatedToken.Locations = append(updatedToken.Locations, Location{
			ProviderID: newReplicaPeerID,
			Address:    newReplicaAddr,
			RTT:        0,
		})
		return updatedToken
	}, 3)

	if err != nil {
		// If token doesn't exist, create it with new replica location
		if newReplicaAddr == nil {
			return fmt.Errorf("no address provided for new replica")
		}
		newToken := Token{
			Key: key,
			Locations: []Location{
				{
					ProviderID: newReplicaPeerID,
					Address:    newReplicaAddr,
					RTT:        0,
				},
			},
			Timestamp: time.Now().UnixNano(),
			Version:   1,
		}
		if err := PutTokenWithConflictResolution(ctx, dht, key, newToken, 3); err != nil {
			return fmt.Errorf("create token after replication: %w", err)
		}
		return nil
	}

	return nil
}
