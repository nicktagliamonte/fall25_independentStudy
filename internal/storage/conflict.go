// Purpose: Conflict resolution version structure (Phase 5.3).
// Defines (timestamp, node_id, hash) for cryptographic versioning on heal.
// No Phase 2 dependencies.

package storage

import (
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Version is the version structure for conflict resolution: (timestamp, node_id, hash).
type Version struct {
	Timestamp int64   // Unix nanoseconds
	NodeID    peer.ID // node that produced this version
	Hash      cid.Cid // content hash
}

// CompareVersionsLastWriterWins returns: 1 if a wins, -1 if b wins, 0 if tie.
// Last-writer-wins: later timestamp wins; on tie, NodeID lexicographic breaks the tie.
func CompareVersionsLastWriterWins(a, b Version) int {
	if a.Timestamp > b.Timestamp {
		return 1
	}
	if a.Timestamp < b.Timestamp {
		return -1
	}
	sa, sb := a.NodeID.String(), b.NodeID.String()
	if sa > sb {
		return 1
	}
	if sa < sb {
		return -1
	}
	return 0
}

// NoConflictForImmutable returns true when both versions reference the same content (same hash).
// For content-addressed immutable objects, same hash means no conflict to resolve.
func NoConflictForImmutable(a, b Version) bool {
	return a.Hash.Defined() && b.Hash.Defined() && a.Hash.Equals(b.Hash)
}

// ResolveMutableMetadata applies version resolution for mutable metadata. Returns the winning
// version using last-writer-wins. On tie, returns a.
func ResolveMutableMetadata(a, b Version) Version {
	if CompareVersionsLastWriterWins(a, b) >= 0 {
		return a
	}
	return b
}
