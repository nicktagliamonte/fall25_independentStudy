// Purpose: Automatic repair protocol triggered on replication vector mismatch.
// Implements industry-standard repair: discover missing replicas, select candidates, replicate content.

package storage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multiaddr"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

// RepairProtocolID is the libp2p protocol ID for repair replication.
const RepairProtocolID = "/sng40/repair/1.0.0"

// RepairProtocol handles automatic repair of missing replicas based on replication vector mismatches.
type RepairProtocol struct {
	stack            *Stack
	host             host.Host
	storageAvailable *StorageAvailableProtocol
	criteria         SelectionCriteria
}

// NewRepairProtocol creates a new repair protocol handler.
func NewRepairProtocol(stack *Stack, h host.Host, ts tuplespace.TupleSpace, tokenized bool) *RepairProtocol {
	sap := NewStorageAvailableProtocol(ts)
	sap.PeerIDsToCheck = func() []peer.ID {
		var pids []peer.ID
		if h == nil {
			return pids
		}
		for _, p := range h.Peerstore().Peers() {
			if p == h.ID() {
				continue
			}
			if len(h.Peerstore().Addrs(p)) == 0 {
				continue
			}
			pids = append(pids, p)
		}
		return pids
	}
	return &RepairProtocol{
		stack:            stack,
		host:             h,
		storageAvailable: sap,
		criteria:         DefaultSelectionCriteria(tokenized),
	}
}

// StartAdvertisingStorageAvailability advertises this peer's storage availability
// immediately and periodically. Call once at node startup.
func (rp *RepairProtocol) StartAdvertisingStorageAvailability(ctx context.Context) {
	if rp.host == nil || rp.storageAvailable == nil {
		return
	}
	_ = rp.storageAvailable.AdvertiseStorageAvailable(rp.host.ID(), 0, 1<<30, 1.0, 24*time.Hour)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = rp.storageAvailable.AdvertiseStorageAvailable(rp.host.ID(), 0, 1<<30, 1.0, 24*time.Hour)
			}
		}
	}()
}

// getTokenStore returns the ValueStore for token operations (TokenStore or DHT).
func (rp *RepairProtocol) getTokenStore() routing.ValueStore {
	if rp.stack == nil {
		return nil
	}
	if rp.stack.TokenStore != nil {
		return rp.stack.TokenStore
	}
	if rp.stack.DHT != nil {
		return rp.stack.DHT
	}
	return nil
}

// RepairResult represents the outcome of a repair operation.
type RepairResult struct {
	// Key is the primary identifier being repaired.
	Key Key
	// CID is the content identifier (for IPFS compatibility).
	CID cid.Cid
	// RepairedCategories lists distance categories where replicas were successfully created.
	RepairedCategories []DistanceCategory
	// FailedCategories lists distance categories where repair failed.
	FailedCategories []DistanceCategory
	// ReplicatedPeers lists peer IDs where content was successfully replicated.
	ReplicatedPeers []peer.ID
	// FailedPeers lists peer IDs where replication failed.
	FailedPeers []peer.ID
	// TotalReplicasCreated is the number of new replicas successfully created.
	TotalReplicasCreated int
}

// TriggerRepair triggers automatic repair for a Key based on verification results.
// Discovers missing replicas, finds storage-available candidates, replicates content,
// updates token with new replica locations, and adds new providers to routing table.
//
// Parameters:
//   - ctx: Context for the repair operation
//   - k: The Key to repair (primary identifier)
//   - verification: Verification result showing missing categories
//   - blockData: The block data to replicate (must be available locally)
//
// Returns: Repair result with success/failure details.
func (rp *RepairProtocol) TriggerRepair(
	ctx context.Context,
	k Key,
	verification *ReplicaStateVerification,
	blockData []byte,
) (*RepairResult, error) {
	if k.IsZero() {
		return nil, errors.New("invalid key")
	}
	if verification == nil {
		return nil, errors.New("verification result required")
	}
	if len(blockData) == 0 {
		return nil, errors.New("block data required for replication")
	}
	if rp.stack == nil || rp.host == nil {
		return nil, errors.New("stack and host required")
	}

	c := verification.CID
	if !c.Defined() && rp.stack.Datastore != nil {
		if resolved, err := GetCIDFromKey(ctx, rp.stack.Datastore, k); err == nil && resolved.Defined() {
			c = resolved
		}
	}

	result := &RepairResult{
		Key:                  k,
		CID:                  c,
		RepairedCategories:   make([]DistanceCategory, 0),
		FailedCategories:     make([]DistanceCategory, 0),
		ReplicatedPeers:      make([]peer.ID, 0),
		FailedPeers:          make([]peer.ID, 0),
		TotalReplicasCreated: 0,
	}

	// If already synchronized, no repair needed
	if verification.IsSynchronized {
		return result, nil
	}

	// Calculate how many replicas are needed for each missing category
	needed := rp.calculateNeededReplicas(verification)

	// Build set of existing providers to exclude from replication targets
	existingProviders := make(map[peer.ID]bool)
	for _, p := range verification.Providers {
		existingProviders[p.ProviderID] = true
	}

	// Repair each missing category
	for category, count := range needed {
		if count <= 0 {
			continue
		}

		// Find storage-available candidates for this category
		candidates, err := rp.storageAvailable.FindAndSelectReplicas(
			rp.host.ID(),
			category,
			rp.criteria,
			count,
		)
		if err != nil || len(candidates) == 0 {
			result.FailedCategories = append(result.FailedCategories, category)
			continue
		}

		// Replicate to selected candidates (skip those that already have the block)
		replicated := 0
		for _, candidate := range candidates {
			if existingProviders[candidate.PeerID] {
				continue
			}
			if err := rp.replicateToPeer(ctx, c, candidate.PeerID, blockData); err != nil {
				result.FailedPeers = append(result.FailedPeers, candidate.PeerID)
				continue
			}

			result.ReplicatedPeers = append(result.ReplicatedPeers, candidate.PeerID)
			replicated++
			result.TotalReplicasCreated++

			// Add new provider to routing table with distance category
			if rp.stack.RoutingTable != nil {
				rp.stack.RoutingTable.AddProvider(k, candidate.PeerID, category)
			}
		}

		if replicated > 0 {
			result.RepairedCategories = append(result.RepairedCategories, category)
		} else {
			result.FailedCategories = append(result.FailedCategories, category)
		}
	}

	return result, nil
}

// calculateNeededReplicas calculates how many replicas are needed for each missing category.
func (rp *RepairProtocol) calculateNeededReplicas(verification *ReplicaStateVerification) map[DistanceCategory]int {
	needed := make(map[DistanceCategory]int)

	// Calculate shortfall for each category
	nearShortfall := verification.ExpectedCounts.Near - verification.ActualCounts.Near
	if nearShortfall > 0 {
		needed[DistanceNear] = nearShortfall
	}

	midrangeShortfall := verification.ExpectedCounts.Midrange - verification.ActualCounts.Midrange
	if midrangeShortfall > 0 {
		needed[DistanceMidrange] = midrangeShortfall
	}

	farFlungShortfall := verification.ExpectedCounts.FarFlung - verification.ActualCounts.FarFlung
	if farFlungShortfall > 0 {
		needed[DistanceFarFlung] = farFlungShortfall
	}

	return needed
}

// ReplicateToNPeers sends the block to n other nodes. Used after PUT to enforce replication.
// Picks peers from connected network, then peerstore. Retries if no peers (waits for connections).
func (rp *RepairProtocol) ReplicateToNPeers(ctx context.Context, key Key, c cid.Cid, blockData []byte, n int) int {
	if rp.host == nil || rp.stack == nil || n <= 0 || len(blockData) == 0 {
		return 0
	}
	var peers []peer.ID
	for attempt := 0; attempt < 20; attempt++ {
		peers = rp.peersForReplication(n)
		if len(peers) > 0 {
			break
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(500 * time.Millisecond):
		}
	}
	replicated := 0
	for _, pid := range peers {
		peerCtx, cancelPeer := context.WithTimeout(ctx, 60*time.Second)
		err := rp.replicateToPeer(peerCtx, c, pid, blockData)
		cancelPeer()
		if err != nil {
			continue
		}
		replicated++
		if rp.stack.RoutingTable != nil {
			rp.stack.RoutingTable.AddProvider(key, pid, DistanceMidrange)
		}
		if replicated >= n {
			break
		}
	}
	return replicated
}

func (rp *RepairProtocol) peersForReplication(max int) []peer.ID {
	if rp.host == nil {
		return nil
	}
	var out []peer.ID
	seen := make(map[peer.ID]bool)
	for _, pid := range rp.host.Network().Peers() {
		if pid == rp.host.ID() || seen[pid] {
			continue
		}
		if rp.host.Network().Connectedness(pid) != network.Connected {
			continue
		}
		seen[pid] = true
		out = append(out, pid)
		if len(out) >= max {
			return out
		}
	}
	for _, pid := range rp.host.Peerstore().Peers() {
		if pid == rp.host.ID() || seen[pid] {
			continue
		}
		if len(rp.host.Peerstore().Addrs(pid)) == 0 {
			continue
		}
		seen[pid] = true
		out = append(out, pid)
		if len(out) >= max {
			return out
		}
	}
	return out
}

// ReplicateToPeer replicates block content to a specific peer via the repair protocol.
// Updates token with new replica location on success. Used for repair and testing.
func (rp *RepairProtocol) ReplicateToPeer(
	ctx context.Context,
	c cid.Cid,
	targetPeer peer.ID,
	blockData []byte,
) error {
	return rp.replicateToPeer(ctx, c, targetPeer, blockData)
}

// replicateToPeer replicates content to a peer using Bitswap or direct transfer.
// This is the core replication operation.
func (rp *RepairProtocol) replicateToPeer(
	ctx context.Context,
	c cid.Cid,
	targetPeer peer.ID,
	blockData []byte,
) error {
	if rp.stack == nil || rp.stack.BlockSvc == nil {
		return errors.New("block service unavailable")
	}

	// Ensure we have the block locally first
	// (It should already be available since we're repairing)
	has, err := rp.stack.Blockstore.Has(ctx, c)
	if err != nil {
		return fmt.Errorf("check blockstore: %w", err)
	}
	if !has {
		// Store block locally if not present
		_, err = PutRawBlock(ctx, rp.stack.BlockSvc, blockData)
		if err != nil {
			return fmt.Errorf("store block locally: %w", err)
		}
	}

	// Connect to target peer if not already connected
	if rp.host.Network().Connectedness(targetPeer) != 2 { // 2 = Connected
		addrs := rp.host.Peerstore().Addrs(targetPeer)
		if len(addrs) == 0 {
			// Try to find peer address from token (key-based)
			tokenStore := rp.getTokenStore()
			if tokenStore != nil {
				key := KeyFromData(blockData)
				if !key.IsZero() {
					ctxFind, cancel := context.WithTimeout(ctx, 5*time.Second)
					token, err := GetToken(ctxFind, tokenStore, key)
					cancel()
					if err == nil {
						for _, loc := range token.Locations {
							if loc.ProviderID == targetPeer && loc.Address != nil {
								addrs = []multiaddr.Multiaddr{loc.Address}
								break
							}
						}
					}
				}
			}
		}

		if len(addrs) == 0 {
			return fmt.Errorf("no addresses found for peer %s", targetPeer)
		}

		// Dial peer
		info := peer.AddrInfo{ID: targetPeer, Addrs: addrs}
		ctxDial, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = rp.host.Connect(ctxDial, info)
		cancel()
		if err != nil {
			return fmt.Errorf("connect to peer %s: %w", targetPeer, err)
		}
	}

	// Replicate via direct stream. Token sync happens after target stores (SyncTokenOnPut).

	// Trigger Bitswap session: create a session and request the block from ourselves
	// This ensures Bitswap is aware of the block and can serve it
	// The target peer will fetch it via normal Bitswap protocol when needed

	// For immediate replication, we use a direct approach:
	// Open a stream to the peer and send the block directly
	// This is more reliable for repair operations
	return rp.replicateViaDirectStream(ctx, c, targetPeer, blockData)
}

// replicateViaDirectStream replicates content via a direct libp2p stream.
// This is more reliable than relying on Bitswap discovery for repair operations.
func (rp *RepairProtocol) replicateViaDirectStream(
	ctx context.Context,
	c cid.Cid,
	targetPeer peer.ID,
	blockData []byte,
) error {
	// Open stream to target peer
	stream, err := rp.host.NewStream(ctx, targetPeer, RepairProtocolID)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	// Send CID first (as string)
	cidStr := c.String()
	if _, err := stream.Write([]byte(cidStr + "\n")); err != nil {
		return fmt.Errorf("write identifier: %w", err)
	}

	// Send block size
	sizeBytes := fmt.Sprintf("%d\n", len(blockData))
	if _, err := stream.Write([]byte(sizeBytes)); err != nil {
		return fmt.Errorf("write size: %w", err)
	}

	// Send block data
	if _, err := stream.Write(blockData); err != nil {
		return fmt.Errorf("write block data: %w", err)
	}

	// Read acknowledgment (simple "OK\n" or error message)
	ack := make([]byte, 256)
	n, err := stream.Read(ack)
	if err != nil {
		return fmt.Errorf("read ack: %w", err)
	}

	ackStr := string(ack[:n])
	if ackStr != "OK\n" {
		return fmt.Errorf("replication failed: %s", ackStr)
	}

	// Auto-sync token: update token with new replica location (per newReqs: token syncs with data)
	if tokenStore := rp.getTokenStore(); rp.stack != nil && tokenStore != nil {
		key := KeyFromData(blockData)
		if !key.IsZero() {
			addrs := rp.host.Peerstore().Addrs(targetPeer)
			var targetAddr multiaddr.Multiaddr
			if len(addrs) > 0 {
				targetAddr = pickRoutableAddr(addrs)
				if targetAddr == nil {
					targetAddr = addrs[0]
				}
			}
			if targetAddr == nil {
				if sc := stream.Conn(); sc != nil {
					targetAddr = sc.RemoteMultiaddr()
				}
			}
			if targetAddr != nil {
				_ = SyncTokenOnReplication(ctx, tokenStore, rp.stack.RoutingTable, key, targetPeer, targetAddr)
			}
		}
	}

	return nil
}

// HandleRepairStream handles incoming repair replication requests.
// Should be registered as a stream handler on the host.
func (rp *RepairProtocol) HandleRepairStream(stream network.Stream) error {
	defer stream.Close()

	ctx := context.Background()
	r := bufio.NewReader(stream)

	// Read CID line
	cidLine, err := r.ReadString('\n')
	if err != nil {
		_, _ = stream.Write([]byte("ERROR: read identifier\n"))
		return fmt.Errorf("read identifier: %w", err)
	}
	cidStr := cidLine
	if len(cidStr) > 0 && cidStr[len(cidStr)-1] == '\n' {
		cidStr = cidStr[:len(cidStr)-1]
	}

	c, err := cid.Decode(cidStr)
	if err != nil {
		_, _ = stream.Write([]byte("ERROR: invalid identifier\n"))
		return fmt.Errorf("decode identifier: %w", err)
	}

	// Read block size line
	sizeLine, err := r.ReadString('\n')
	if err != nil {
		_, _ = stream.Write([]byte("ERROR: read size\n"))
		return fmt.Errorf("read size: %w", err)
	}
	sizeStr := sizeLine
	if len(sizeStr) > 0 && sizeStr[len(sizeStr)-1] == '\n' {
		sizeStr = sizeStr[:len(sizeStr)-1]
	}
	blockSize, err := strconv.Atoi(sizeStr)
	if err != nil {
		_, _ = stream.Write([]byte("ERROR: invalid size\n"))
		return fmt.Errorf("parse size: %w", err)
	}

	if blockSize <= 0 || blockSize > 10*1024*1024 { // 10MB limit
		_, _ = stream.Write([]byte("ERROR: invalid block size\n"))
		return fmt.Errorf("invalid block size: %d", blockSize)
	}

	// Read block data
	blockData := make([]byte, blockSize)
	if _, err := io.ReadFull(r, blockData); err != nil {
		_, _ = stream.Write([]byte("ERROR: read block data\n"))
		return fmt.Errorf("read block data: %w", err)
	}

	// Verify CID matches
	expectedCid, err := PutRawBlock(ctx, rp.stack.BlockSvc, blockData)
	if err != nil {
		_, _ = stream.Write([]byte("ERROR: store block\n"))
		return fmt.Errorf("store block: %w", err)
	}

	if !expectedCid.Equals(c) {
		_, _ = stream.Write([]byte("ERROR: key mismatch\n"))
		return fmt.Errorf("key mismatch: expected %s, got %s", c, expectedCid)
	}

	// Store block and update routing table
	lockOpts := (*PutLockOpts)(nil)
	if rp.stack.KeyLockManager != nil && rp.host != nil {
		lockOpts = &PutLockOpts{Manager: rp.stack.KeyLockManager, Holder: rp.host.ID()}
	}
	key, storedCID, err := PutRawBlockIndexed(ctx, rp.stack.Datastore, rp.stack.BlockSvc, blockData, lockOpts)
	if err != nil {
		_, _ = stream.Write([]byte("ERROR: index block\n"))
		return fmt.Errorf("index block: %w", err)
	}

	// Update routing table: we are now a replica provider
	// Get replication vector from source (or use default)
	if rp.stack.RoutingTable != nil {
		// Try to get repVector from routing table, or use default
		entry := rp.stack.RoutingTable.Get(key)
		var repVector ReplicationVector
		if entry != nil {
			repVector = entry.RepVector
		} else {
			repVector = DefaultReplicationVector()
		}
		// Update routing table: we are a replica provider
		// The original provider ID is the one who initiated repair
		sourcePeer := stream.Conn().RemotePeer()
		rp.stack.RoutingTable.Set(key, sourcePeer, repVector, storedCID)
	}

	// Auto-sync token: update token with our location (we received replicated content)
	if tokenStore := rp.getTokenStore(); tokenStore != nil && rp.host != nil {
		ourAddrs := rp.host.Addrs()
		if len(ourAddrs) > 0 {
			_ = SyncTokenOnPut(ctx, tokenStore, rp.host, key, storedCID, nil)
		}
	}

	// Send success acknowledgment
	_, err = stream.Write([]byte("OK\n"))
	return err
}
