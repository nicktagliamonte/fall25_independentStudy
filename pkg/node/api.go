// Purpose: Programmatic API for embedding the node as a library.

package node

import (
	"context"
	"time"
)

// Options configures the embedded node service.
type Options struct {
	// Identity
	KeyPath       string
	EphemeralSeed string

	// Network
	ListenMultiaddrs []string
	BootstrapPeers   []string
	MinOutbound      int
	PerIPDialLimit   int
	DialTimeout      time.Duration

	// Storage
	StorePath string

	// Admission (token-gated)
	RequireToken bool
	Token        string
	CAPubKeysB64 []string

	// Control-plane hooks
	OnHandshake func(peerID string, info map[string]any)
	OnAck       func(peerID string, status string)
}

// Service is the running embedded node.
type Service interface {
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

// Status summarizes node state and counters.
type Status struct {
	PeerID  string
	Addrs   []string
	Head    string
	Height  int64
	Metrics struct {
		DialsAttempted int64
		DialsSucceeded int64
		DialsFailed    int64
		PeersPruned    int64
		GossipLearned  int64
	}
}

// RestoreStats summarizes a restore job outcome.
type RestoreStats struct {
	OK     int
	Failed int
	Bytes  int64
}
