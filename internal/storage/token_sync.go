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
// Skips /ip4/0.0.0.0 (listen-all, not reachable). Otherwise returns addresses
// in the order given, so any preference among routable addresses (e.g. private
// vs loopback ranges) must come from the ordering of addrs.
//
// Parameters:
//   - addrs ([]multiaddr.Multiaddr): candidate addresses, typically from host.Addrs().
//
// Returns:
//   - multiaddr.Multiaddr: the first non-"0.0.0.0" address found, or nil if addrs
//     is empty or every address is a 0.0.0.0 listen-all address.
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
//
// Parameters:
//   - err (error): the error returned from a token lookup (e.g. GetToken).
//
// Returns:
//   - bool: true if err is routing.ErrNotFound or its message contains
//     "token not found"; false if err is nil or looks like some other failure.
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
// Creates a token with the current peer as a location, or updates an existing
// token to include this peer if not already present. The token read is retried
// up to 5 times with increasing backoff (100ms * 2^attempt) to tolerate transient
// DHT propagation races in large clusters; a definitive "token absent" result
// short-circuits the retry loop.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the overall sync;
//     each token read additionally uses its own 20s sub-timeout.
//   - dht (routing.ValueStore): the DHT value store backing token storage; must be non-nil.
//   - h (host.Host): the local libp2p host, used for peer ID and addresses; must be non-nil.
//   - key (Key): the content key being stored; must be non-zero.
//   - c (cid.Cid): the CID corresponding to key (currently unused in the body but
//     accepted for future use / caller symmetry with the routing table).
//   - sink (MessageMetricsSink): optional; when non-nil, records one lookup-out and
//     one lookup-in for the read, and one put-out for the resulting write.
//
// Returns:
//   - error: non-nil if dht/h are nil, key is zero, the host has no addresses,
//     the token read fails with a non-"absent" error, or the subsequent
//     put/update fails.
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
// Removes this peer from the token's Locations. If this was the last location,
// the token is written back with an empty Locations slice rather than deleted
// outright; the DHT's own TTL handles eventual cleanup. If no token exists at
// all, this is treated as a successful no-op (UpdateTokenWithConflictResolution
// itself detects the absent token and returns nil; there's nothing to delete).
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the update.
//   - dht (routing.ValueStore): the DHT value store backing token storage; must be non-nil.
//   - h (host.Host): the local libp2p host, used to identify this peer; must be non-nil.
//   - key (Key): the content key being deleted; must be non-zero.
//
// Returns:
//   - error: nil if dht/h/key are valid and either the update succeeds or the
//     token does not exist; otherwise the wrapped update error.
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
		// UpdateTokenWithConflictResolution already treats an absent token as a
		// successful no-op ("nothing to delete"), so any error reaching here is
		// a genuine failure.
		return fmt.Errorf("sync token on delete: %w", err)
	}

	return nil
}

// SyncTokenOnReplication updates a token with new replica locations when replication occurs.
// Per newReqs.txt: "the only function of the token is to sync with the data".
// Adds the new replica peer to the token's Locations. Does not require the routing
// table—the node that replicates (e.g. a worker that fetched from bootstrap) may not
// have the key locally. If no token exists yet, creates one with the new replica as
// the sole location.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the update.
//   - dht (routing.ValueStore): the DHT value store backing token storage; must be non-nil.
//   - routingTable (*RoutingTable): accepted for interface symmetry/future use; not
//     consulted by this function.
//   - key (Key): the content key being replicated; must be non-zero.
//   - newReplicaPeerID (peer.ID): the peer ID of the new replica holder.
//   - newReplicaAddr (multiaddr.Multiaddr): the dialable address of the new replica;
//     must be non-nil.
//
// Returns:
//   - error: non-nil if dht is nil, key is zero, newReplicaAddr is nil, or both the
//     conflict-resolved update and the fallback token creation fail.
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

	// Check for token absence up front: UpdateTokenWithConflictResolution
	// treats a missing token as "nothing to update" and returns nil (see
	// SyncTokenOnDelete, which relies on that no-op), so its return value
	// alone can no longer be used to detect "no token yet, must create one."
	if _, err := GetToken(ctx, dht, key); err != nil && isTokenAbsent(err) {
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

	// Token exists (or existence is uncertain due to a transient error, in
	// which case UpdateTokenWithConflictResolution's own retries apply): use
	// conflict resolution to add the new replica location. This handles
	// concurrent updates from multiple peers.
	err := UpdateTokenWithConflictResolution(ctx, dht, key, func(currentToken Token) Token {
		// Check if new replica is already present
		for _, loc := range currentToken.Locations {
			if loc.ProviderID == newReplicaPeerID {
				// Already present, return unchanged
				return currentToken
			}
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
		return fmt.Errorf("update token on replication: %w", err)
	}

	return nil
}
