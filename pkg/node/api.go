// Purpose: Programmatic API for embedding the node as a library.

package node

import (
	"context"
	"time"
)

// Options configures the embedded node service. It is passed to Start, which
// applies defaults for any zero-valued fields that require one (see Start in
// start.go for the exact defaulting rules). Options is read once at Start
// time; mutating it afterward has no effect on the running Service.
type Options struct {
	// Identity

	// KeyPath is an optional filesystem path to a persistent libp2p private
	// key (PEM). If set, Start loads the key from this path, creating and
	// writing one via myhost.LoadOrCreatePrivateKey if it does not already
	// exist, so the node keeps a stable PeerID across restarts. If empty,
	// KeyPath is not required; identity falls back to EphemeralSeed (or a
	// fully random key if that is also empty). Takes precedence over
	// EphemeralSeed when both are set.
	KeyPath string
	// EphemeralSeed, when KeyPath is empty and this is non-empty, is hashed
	// with SHA-256 to deterministically derive an in-memory Ed25519 private
	// key (useful for reproducible test/dev PeerIDs without touching disk).
	// If both KeyPath and EphemeralSeed are empty, Start generates a fresh
	// random key each time the node starts (PeerID changes every run).
	EphemeralSeed string

	// Network

	// ListenMultiaddrs is the set of multiaddrs the libp2p host listens on.
	// If empty, Start defaults it to
	// {"/ip4/0.0.0.0/tcp/2893", "/ip4/0.0.0.0/udp/2894/quic-v1"}.
	ListenMultiaddrs []string
	// BootstrapPeers is a list of peer multiaddrs (in the standard
	// "/.../p2p/<peerID>" form) used to seed the PeerStore on startup. Peers
	// that fail to parse, or whose ID equals the local node's ID, are
	// skipped. Duplicate addresses in the slice are deduplicated before
	// insertion. Optional; may be empty.
	BootstrapPeers []string
	// MinOutbound is the target minimum number of outbound peer connections
	// the dial-maintenance loop tries to keep alive. If <= 0, Start defaults
	// it to 4 (see start.go), so a caller cannot fully disable the dial loop
	// via this field alone; the loop simply does nothing useful when there
	// are no dialable candidates in the PeerStore (e.g. no BootstrapPeers
	// and nothing learned via gossip/inbound handshakes yet).
	MinOutbound int
	// PerIPDialLimit caps the number of outbound dial attempts the
	// dial-maintenance loop will make to distinct peers sharing the same
	// IPv4/IPv6 address, per maintenance pass. If <= 0, Start defaults it to
	// 3. Helps avoid hammering a single host that advertises many peer IDs.
	PerIPDialLimit int
	// DialTimeout bounds each individual outbound connection attempt made by
	// the dial-maintenance loop (and is also used as the handshake timeout
	// budget for the initial post-connect handshake). If <= 0, Start
	// defaults it to 10 seconds.
	DialTimeout time.Duration

	// Storage

	// StorePath is an optional filesystem directory for a persistent
	// blockstore/datastore. If set, Start opens (creating if necessary) a
	// persistent store there via mystore.NewPersistentBlockstore, so blocks
	// and node state (chain head, peer-added log, etc.) survive restarts. If
	// empty, Start uses an in-memory store (mystore.NewStack) that is
	// discarded on Close.
	StorePath string

	// Admission (token-gated)

	// RequireToken, when true, forces the handshake admission policy to
	// require a credential (AuthScheme "token-ed25519-v1") from inbound
	// peers regardless of whether CAPubKeysB64/Token are also set. See
	// Start's admission-policy construction in start.go.
	RequireToken bool
	// Token is the shared credential string presented to peers (and
	// expected from peers, once credential requirement is enabled) under
	// the token-ed25519-v1 auth scheme. Required for admission to be
	// enforced whenever CAPubKeysB64 is non-empty (RequireCredential is also
	// enabled automatically in that case even if RequireToken is false).
	Token string
	// CAPubKeysB64 is a list of base64-standard-encoded 32-byte Ed25519
	// public keys accepted as certificate authorities for verifying peer
	// credentials. Each entry must decode to exactly 32 bytes; Start returns
	// an error ("invalid CAPubKeysB64 entry") and aborts startup if any
	// entry is malformed. If non-empty together with a non-empty Token,
	// credential requirement is enabled automatically even when
	// RequireToken is false.
	CAPubKeysB64 []string

	// Control-plane hooks

	// OnHandshake, if non-null, is invoked (best-effort, from background
	// goroutines) whenever a handshake completes, whether inbound
	// (direction "inbound"), outbound after a fresh dial (direction
	// "outbound"), or as part of periodic gossip (direction "gossip"). info
	// carries a "direction" key and, for outbound/gossip, a "remote_height"
	// key with the peer's reported chain height. Callers must not assume a
	// particular goroutine or make it block for long, as it runs inline in
	// the dial/gossip/inbound-handshake loops.
	OnHandshake func(peerID string, info map[string]any)
	// OnAck, if non-null, is invoked when a handshake succeeds during
	// GetRawFrom's ephemeral fetch path (status is always "ok" in the
	// current implementation). Not invoked from the Start-time dial/gossip
	// loops (those use OnHandshake only).
	OnAck func(peerID string, status string)
}

// Service is the running embedded node returned by Start. All methods are
// safe to call concurrently from multiple goroutines unless individually
// documented otherwise. A Service must be closed via Close when no longer
// needed to release the libp2p host, blockstore, and control server.
type Service interface {
	// Close stops all background loops (pruning, dial maintenance, gossip),
	// shuts down the control server, closes the Bitswap/blockstore stack and
	// the libp2p host, and releases associated resources. ctx bounds how
	// long Close waits for background goroutines to finish before proceeding
	// with a best-effort shutdown anyway; Close currently always returns a
	// nil error (individual teardown errors are swallowed). Close is
	// idempotent-unsafe: calling it more than once is not supported by the
	// concrete implementation (see service.go).
	Close(ctx context.Context) error
	// Status returns basic node info and counters
	Status(ctx context.Context) (Status, error)
	// Data-plane helpers for simple publish/fetch flows.
	PutRaw(ctx context.Context, data []byte) (cid string, size int, err error)
	GetRawFrom(ctx context.Context, providerAddr string, providerPeer string, cidStr string, timeout time.Duration) ([]byte, error)
	// ListImmediatePeerIDs returns currently connected peer IDs (immediate neighbors).
	ListImmediatePeerIDs(ctx context.Context) ([]string, error)
	// RestoreFromManifest fetches the provided CIDs with bounded concurrency and budgets.
	RestoreFromManifest(ctx context.Context, cids []string, concurrency int, timeout time.Duration, byteBudget int64) (RestoreStats, error)
}

// Status summarizes node state and counters, as returned by Service.Status.
type Status struct {
	// PeerID is the string encoding of the local libp2p host's peer ID.
	PeerID string
	// Addrs is the list of string-encoded multiaddrs the local host is
	// currently listening on/advertising (via host.Host.Addrs()).
	Addrs []string
	// Head is the string encoding of the current chain-state head CID, or
	// the empty string if no state has been recorded yet (i.e. the
	// underlying cid.Cid is not "defined").
	Head string
	// Height is the current chain-state height (block/entry count),
	// as tracked by the node's append-only state log.
	Height int64
	// Metrics holds point-in-time counters snapshotted from the node's
	// internal control.NodeMetrics at the time Status was called.
	Metrics struct {
		// DialsAttempted is the cumulative number of outbound connection
		// attempts made by the dial-maintenance loop.
		DialsAttempted int64
		// DialsSucceeded is the cumulative number of outbound connection
		// attempts that succeeded.
		DialsSucceeded int64
		// DialsFailed is the cumulative number of outbound connection
		// attempts that failed.
		DialsFailed int64
		// PeersPruned is the cumulative number of peer-store entries removed
		// by the periodic pruning loop (stale or over the failure
		// threshold).
		PeersPruned int64
		// GossipLearned is the cumulative number of peer addresses learned
		// via periodic gossip handshakes with connected peers.
		GossipLearned int64
	}
}

// RestoreStats summarizes the outcome of a Service.RestoreFromManifest call.
type RestoreStats struct {
	// OK is the number of CIDs that were successfully decoded and fetched.
	OK int
	// Failed is the number of CIDs that failed to decode, or whose fetch
	// errored or timed out, or that were skipped because the byte budget
	// was already exhausted when their worker picked them up.
	Failed int
	// Bytes is the total number of bytes successfully fetched across all OK
	// items.
	Bytes int64
}
