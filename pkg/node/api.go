// Purpose: Programmatic API for embedding the node as a library.

package node

import (
	"context"
	"time"
)

// Options configures the embedded node service created by Start. Zero values
// generally mean "use the built-in default" (see Start for the specific
// defaulting rules applied to ListenMultiaddrs, ClusterNodeCount,
// PerIPDialLimit, and DialTimeout).
type Options struct {
	// KeyPath is an optional filesystem path to a persistent libp2p private
	// key (PEM). If set, the node's identity is loaded from (or created and
	// saved to) this path. Mutually exclusive in practice with EphemeralSeed.
	KeyPath string
	// EphemeralSeed, if non-empty and KeyPath is empty, deterministically
	// derives an ed25519 identity from SHA-256(EphemeralSeed). Useful for
	// reproducible test/dev node identities without persisting a key file.
	EphemeralSeed string

	// ListenMultiaddrs are the multiaddrs the libp2p host listens on. If
	// empty, Start defaults to TCP/2893 and QUIC/2894 on all interfaces.
	ListenMultiaddrs []string
	// BootstrapPeers are additional seed multiaddrs (beyond the built-in
	// myhost.DefaultDHTBootstrapAddrs) used to populate the peerstore and DHT
	// bootstrap list before first connection.
	BootstrapPeers []string
	// DHTClientMode, if true, runs the DHT in client (query-only) mode;
	// default false runs it as a full DHT server participating in routing.
	DHTClientMode bool
	// MinOutbound is the target minimum number of outbound peer connections
	// the dial-maintenance loop tries to reach; <= 0 uses DefaultMinOutbound.
	MinOutbound int
	// MaxConnections is the high watermark for live libp2p connections.
	// Crossing it trims the overlay back to the effective MinOutbound target.
	// Peers protected by Kademlia's nearest routing buckets are exempt from
	// pruning. <= 0 uses DefaultMaxConnections.
	MaxConnections int
	// ClusterNodeCount, if > 0, caps MinOutbound at N-1 (network too small for the default target).
	ClusterNodeCount int
	// PerIPDialLimit caps how many outbound dials the dial-maintenance loop
	// will make to distinct peers sharing the same IP address, per
	// maintenance pass. <= 0 defaults to 3.
	PerIPDialLimit int
	// DialTimeout bounds each individual outbound connection attempt.
	// <= 0 defaults to 10 seconds.
	DialTimeout time.Duration

	// StorePath, if set, is the filesystem path for a persistent blockstore
	// and datastore. If empty, the node uses an ephemeral in-memory store.
	StorePath string

	// RequireToken, if true, forces the handshake policy to require a
	// credential (RequireCredential) even if CAPubKeysB64/Token are also set
	// independently.
	RequireToken bool
	// Token is the credential presented (and, together with CAPubKeysB64,
	// required from peers) under the "token-ed25519-v1" auth scheme.
	Token string
	// CAPubKeysB64 lists base64-encoded 32-byte ed25519 public keys of
	// certificate authorities trusted to sign peer credentials. A non-empty
	// list combined with a non-empty Token also implies RequireCredential.
	CAPubKeysB64 []string

	// TSHAddr is the "host:port" of an optional TSH (tuple-space) daemon,
	// e.g. "127.0.0.1:7000". When set, the Gateway routes regex/admin queries
	// through a P2PTupleSpace in addition to the DHT-backed tuple space, per
	// newReqs.txt.
	TSHAddr string

	// IndexShardCount controls independent PHT mutation owners and query
	// fanout. Values <= 0 use pht.DefaultShardCount.
	IndexShardCount int
	// DisableBloomPruning traverses all PHT branches for substring queries.
	// It is intended for controlled ablation experiments.
	DisableBloomPruning bool

	// OnHandshake, if set, is invoked after each inbound or outbound
	// handshake completes, with the remote peer ID and a small info map
	// describing the handshake (e.g. direction, remote height).
	OnHandshake func(peerID string, info map[string]any)
	// OnAck, if set, is invoked after a successful outbound handshake with
	// the remote peer ID and a status string.
	OnAck func(peerID string, status string)
}

// Service is the programmatic handle to a running embedded node, as returned
// by Start. All methods that talk to the node do so over its local HTTP
// control server (see start.go's *service implementation), not via direct
// in-process calls.
type Service interface {
	// Close shuts down the control server, stops all background loops
	// (dialer, gossip, pruning, security-check, IBLT exchange), and closes
	// the DHT, storage stack, and libp2p host, in that order.
	Close(ctx context.Context) error
	// Status returns basic node info and counters.
	Status(ctx context.Context) (Status, error)
	// PutRaw stores data as a new block and returns its CID and byte size.
	PutRaw(ctx context.Context, data []byte) (cid string, size int, err error)
	// GetRawFrom fetches the block identified by cidStr from the given
	// provider (address + peer ID), bounded by timeout, and returns its bytes.
	GetRawFrom(ctx context.Context, providerAddr string, providerPeer string, cidStr string, timeout time.Duration) ([]byte, error)
	// ListImmediatePeerIDs returns currently connected peer IDs (immediate neighbors).
	ListImmediatePeerIDs(ctx context.Context) ([]string, error)
	// RestoreFromManifest fetches blocks by CID (manifest); Key-based API preferred for new code.
	RestoreFromManifest(ctx context.Context, cids []string, concurrency int, timeout time.Duration, byteBudget int64) (RestoreStats, error)
}

// Status summarizes a node's identity, chain state, and running counters at
// the moment Service.Status was called.
type Status struct {
	// PeerID is the string-encoded libp2p peer ID of this node.
	PeerID string
	// Addrs lists the multiaddrs this node's host is currently listening on
	// or otherwise advertises.
	Addrs []string
	// Head is the string-encoded CID of the node's current state head
	// (empty if undefined).
	Head string
	// Height is the node's current state height (chain length).
	Height int64
	// Metrics holds point-in-time counters snapshotted from the node's
	// internal ctrl.NodeMetrics.
	Metrics struct {
		// DialsAttempted counts outbound connection attempts made so far.
		DialsAttempted int64
		// DialsSucceeded counts outbound connection attempts that succeeded.
		DialsSucceeded int64
		// DialsFailed counts outbound connection attempts that failed.
		DialsFailed int64
		// PeersPruned counts peers removed from the peerstore by the
		// periodic pruning loop for being stale or over the failure threshold.
		PeersPruned int64
		// GossipLearned counts peer records learned via periodic gossip
		// handshakes with connected peers.
		GossipLearned int64
	}
}

// RestoreStats summarizes a restore job outcome: how many of the requested
// CIDs were fetched successfully, how many failed, and the total bytes
// retrieved.
type RestoreStats struct {
	// OK is the count of CIDs successfully fetched and stored.
	OK int
	// Failed is the count of CIDs that could not be fetched within budget.
	Failed int
	// Bytes is the total number of bytes fetched across all successful CIDs.
	Bytes int64
}
