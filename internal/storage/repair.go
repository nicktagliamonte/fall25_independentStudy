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

// RepairProtocol handles automatic repair of missing replicas based on replication vector
// mismatches: it discovers storage-available candidates, selects among them, and replicates
// block content to close the gap between a key's expected and actual replica distribution.
type RepairProtocol struct {
	// stack provides datastore/blockstore/routing-table/token-store access for repair.
	stack *Stack
	// host is the local libp2p host used to dial peers and open repair streams.
	host host.Host
	// storageAvailable discovers peers advertising spare storage capacity for replication targets.
	storageAvailable *StorageAvailableProtocol
	// criteria are the weights used when selecting among storage-available candidates.
	criteria SelectionCriteria
}

// NewRepairProtocol creates a new RepairProtocol bound to stack and host h. It builds an
// internal StorageAvailableProtocol over the given tuple space, configuring its
// PeerIDsToCheck to enumerate every peer in h's peerstore that has known addresses (excluding
// h itself) — used when tokenized/DHT-backed tuple space lookups require an explicit peer
// list rather than pattern matching. Selection criteria are derived from tokenized via
// DefaultSelectionCriteria.
//
// Parameters:
//   - stack (*Stack): the storage stack repair operations act against.
//   - h (host.Host): the local libp2p host.
//   - ts (tuplespace.TupleSpace): the tuple space used for storage-available advertisements.
//   - tokenized (bool): whether the network uses staking/tokenized selection criteria.
//
// Returns:
//   - *RepairProtocol: the constructed repair protocol handler.
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

// StartAdvertisingStorageAvailability advertises this peer's storage availability (a fixed
// placeholder capacity of 1GiB, full reputation 1.0, 24h validity, 0 committed stake) once
// immediately, then again every 30 seconds in a background goroutine until ctx is done. Call
// once at node startup. No-op if rp.host or rp.storageAvailable is nil.
//
// Parameters:
//   - ctx (context.Context): stops the periodic re-advertisement loop when done/canceled.
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

// getTokenStore returns the routing.ValueStore to use for token operations: rp.stack.TokenStore
// if set, otherwise rp.stack.DHT, otherwise nil.
//
// Returns:
//   - routing.ValueStore: the resolved token store, or nil if rp.stack is nil or neither
//     TokenStore nor DHT is configured.
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

// TriggerRepair performs automatic repair for Key k based on a prior VerifyKeyStateWithRepVector
// result. If verification.IsSynchronized is already true, it returns an empty (all-zero)
// RepairResult with no work done. Otherwise it computes the shortfall per distance category
// via calculateNeededReplicas, and for each category with a positive shortfall: finds
// storage-available candidates via rp.storageAvailable.FindAndSelectReplicas, skips any
// candidate already present in verification.Providers, replicates blockData to each remaining
// candidate via rp.replicateToPeer, and on success records the peer in
// RepairResult.ReplicatedPeers and adds it to rp.stack.RoutingTable as a provider for k at that
// distance category. A category is recorded as repaired if at least one replication succeeded,
// otherwise as failed. verification.CID is used if defined; otherwise the CID is resolved from
// rp.stack.Datastore via GetCIDFromKey.
//
// Parameters:
//   - ctx (context.Context): cancels candidate discovery and all peer replication attempts.
//   - k (Key): the key to repair (primary identifier); must be non-zero.
//   - verification (*ReplicaStateVerification): the verification result showing missing
//     categories and existing providers; must be non-nil.
//   - blockData ([]byte): the block content to replicate; must be non-empty and must already
//     be available locally.
//
// Returns:
//   - *RepairResult: per-category and per-peer success/failure details; nil on validation error.
//   - error: non-nil if k is zero, verification is nil, blockData is empty, or rp.stack/rp.host
//     is nil. Individual candidate/peer failures are recorded in the result, not returned here.
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

// calculateNeededReplicas computes, for each distance category (Near/Midrange/FarFlung), the
// positive shortfall between verification.ExpectedCounts and verification.ActualCounts.
// Categories with a zero or negative shortfall are omitted from the result entirely.
//
// Parameters:
//   - verification (*ReplicaStateVerification): the verification result to compute shortfalls from.
//
// Returns:
//   - map[DistanceCategory]int: the number of additional replicas needed per category that has
//     a shortfall.
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

// ReplicateToNPeers sends blockData to up to n other peers, used after a local Put to enforce
// the replication factor policy. Peer candidates come from peersForReplication (connected
// peers first, then peerstore entries with known addresses); if none are available yet, it
// retries every 500ms for up to 20 attempts (returning 0 early if ctx is canceled first) to
// give the node time to establish connections. For each candidate it calls replicateToPeer
// with a 60s per-peer timeout; on success it registers the peer in rp.stack.RoutingTable at
// DistanceMidrange and stops once n replicas have been created.
//
// Parameters:
//   - ctx (context.Context): bounds the connection-wait retry loop; canceling it aborts early.
//   - key (Key): the key being replicated, used for routing table bookkeeping.
//   - c (cid.Cid): the CID of the block, sent to peers over the repair protocol.
//   - blockData ([]byte): the block content to replicate; a no-op if empty.
//   - n (int): the target number of successful replications; a no-op if <= 0.
//
// Returns:
//   - int: the number of peers the block was successfully replicated to (<= n).
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

// peersForReplication returns up to max candidate peer IDs for replication, excluding rp.host's
// own ID and de-duplicating. It first collects currently-connected peers from
// rp.host.Network().Peers(), then, if more are needed, falls back to peers known in
// rp.host.Peerstore() that have at least one known address (not yet connected but dialable).
//
// Parameters:
//   - max (int): the maximum number of peer IDs to return.
//
// Returns:
//   - []peer.ID: up to max candidate peer IDs, connected peers first; nil if rp.host is nil.
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

// ReplicateToPeer is the exported entry point for replicating blockData to targetPeer via the
// repair protocol; it is a thin wrapper around the unexported replicateToPeer, exposed for use
// by repair callers and tests.
//
// Parameters:
//   - ctx (context.Context): cancels connection and stream operations.
//   - c (cid.Cid): the CID of the block being replicated.
//   - targetPeer (peer.ID): the peer to replicate the block to.
//   - blockData ([]byte): the block content to send.
//
// Returns:
//   - error: non-nil if replication fails at any step (see replicateToPeer).
func (rp *RepairProtocol) ReplicateToPeer(
	ctx context.Context,
	c cid.Cid,
	targetPeer peer.ID,
	blockData []byte,
) error {
	return rp.replicateToPeer(ctx, c, targetPeer, blockData)
}

// replicateToPeer is the core replication operation: it ensures blockData is stored in the
// local blockstore (storing it via PutRawBlock if not already present), connects to targetPeer
// if not already connected (dialing addresses from the peerstore, or as a fallback, addresses
// discovered by looking up blockData's key in the token store), and then hands off to
// replicateViaDirectStream to actually transfer the block over a direct libp2p stream.
//
// Parameters:
//   - ctx (context.Context): cancels the blockstore check, dial, and stream transfer.
//   - c (cid.Cid): the CID of the block being replicated.
//   - targetPeer (peer.ID): the peer to replicate the block to.
//   - blockData ([]byte): the block content to send.
//
// Returns:
//   - error: non-nil if the block service is unavailable, the local store check/write fails,
//     no address for targetPeer can be found, connecting fails, or the direct stream transfer fails.
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

// replicateViaDirectStream replicates content to targetPeer via a direct libp2p stream on
// RepairProtocolID (more reliable than relying on Bitswap discovery for repair operations). It
// writes the CID string, then the decimal block size, then the raw block data, and expects an
// "OK\n" acknowledgment in response (any other response is treated as failure). On success, it
// determines targetPeer's best-known address (preferring a routable address from the
// peerstore, falling back to the stream's remote multiaddr) and, if the token store is
// available, calls SyncTokenOnReplication to record the new replica location in the key's
// token.
//
// Parameters:
//   - ctx (context.Context): cancels stream I/O.
//   - c (cid.Cid): the CID sent to identify the block to the peer.
//   - targetPeer (peer.ID): the peer receiving the replica.
//   - blockData ([]byte): the block content to send.
//
// Returns:
//   - error: non-nil if opening the stream, writing any part of the payload, or reading the
//     acknowledgment fails, or if the acknowledgment is not "OK\n".
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

// HandleRepairStream is the server-side handler for incoming repair replication requests on
// RepairProtocolID; it should be registered as a stream handler on the host. Protocol: reads a
// CID line, a decimal size line, and exactly that many bytes of block data (rejecting sizes
// <= 0 or > 10MiB); stores the block via PutRawBlock and verifies the resulting CID matches
// the one sent by the client; indexes the block via PutRawBlockIndexed (acquiring a per-key
// lock when rp.stack.KeyLockManager and rp.host are set); updates rp.stack.RoutingTable to
// record this node as a provider for the key (reusing an existing replication vector if the
// key is already known, otherwise DefaultReplicationVector, and recording the stream's remote
// peer as the source); and, if a token store is configured, syncs the token via SyncTokenOnPut
// to record this node's own addresses as a new location. Writes "OK\n" on success or an
// "ERROR: ...\n" line on failure, and always closes the stream before returning.
//
// Parameters:
//   - stream (network.Stream): the inbound libp2p stream to read the request from and write
//     the acknowledgment to.
//
// Returns:
//   - error: non-nil if reading/parsing the request fails, the received data's CID doesn't
//     match what the client claimed, or storing/indexing the block fails.
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
