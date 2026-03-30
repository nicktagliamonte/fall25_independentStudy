// Purpose: Token synchronization protocol. Tokens sync with data storage operations.
// Per newReqs.txt: "the only function of the token is to sync with the data"

package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multiaddr"
)

// pickRoutableAddr returns the first address that is routable by other peers.
// Skips /ip4/0.0.0.0 (listen-all, not reachable). Prefers /ip4/172.x, /ip4/10.x, /ip4/127.0.0.1.
func pickRoutableAddr(addrs []multiaddr.Multiaddr) multiaddr.Multiaddr {
	for _, a := range addrs {
		s := a.String()
		if strings.Contains(s, "/ip4/0.0.0.0/") {
			continue
		}
		return a
	}
	return nil
}

// isTokenAbsent reports whether err means no token record exists yet (vs transient DHT failure).
func isTokenAbsent(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, routing.ErrNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "token not found")
}

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

	// Get current peer's address - prefer routable (skip 0.0.0.0, not reachable by other peers)
	peerID := h.ID()
	addrs := h.Addrs()
	if len(addrs) == 0 {
		return fmt.Errorf("host has no addresses")
	}
	addr := pickRoutableAddr(addrs)
	if addr == nil {
		addr = addrs[0]
	}

	location := Location{
		ProviderID: peerID,
		Address:    addr,
		RTT:        0, // Unknown RTT for local storage
	}

	// Read existing token; retry transient DHT errors (large clusters: first read may race propagation).
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		ctxGet, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err = GetToken(ctxGet, dht, key)
		cancel()
		if err == nil {
			break
		}
		if isTokenAbsent(err) {
			break
		}
		if attempt < 4 {
			time.Sleep(time.Duration(100*(1<<attempt)) * time.Millisecond)
		}
	}
	if sink != nil {
		sink.AddLookupMessagesOut(1)
		sink.AddLookupMessagesIn(1)
	}
	if err != nil {
		if !isTokenAbsent(err) {
			return fmt.Errorf("sync token on put: get token: %w", err)
		}
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
// Per newReqs.txt: "the only function of the token is to sync with the data".
// Adds new replica peer to token Locations. Does not require routing table—the node
// that replicates (e.g. worker that fetched from bootstrap) may not have the key locally.
func SyncTokenOnReplication(ctx context.Context, dht routing.ValueStore, routingTable *RoutingTable, key Key, newReplicaPeerID peer.ID, newReplicaAddr multiaddr.Multiaddr) error {
	if dht == nil {
		return fmt.Errorf("DHT required for token sync")
	}
	if key.IsZero() {
		return fmt.Errorf("key cannot be zero")
	}
	if newReplicaAddr == nil {
		return fmt.Errorf("address required for new replica")
	}

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
