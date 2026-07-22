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
	// Timestamp is when this version was produced, in Unix nanoseconds.
	Timestamp int64
	// NodeID is the peer that produced this version.
	NodeID peer.ID
	// Hash is the content hash (CID) associated with this version.
	Hash cid.Cid
}

// CompareVersionsLastWriterWins compares two versions under a last-writer-wins
// policy: the later Timestamp wins; on a timestamp tie, the lexicographically
// greater NodeID string wins.
//
// Parameters:
//   - a (Version): the first version to compare.
//   - b (Version): the second version to compare.
//
// Returns:
//   - int: 1 if a wins, -1 if b wins, 0 if they are considered equal
//     (identical timestamp and NodeID string).
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
// For content-addressed immutable objects, an identical hash means there is
// nothing to reconcile, regardless of differing timestamps or node IDs.
//
// Parameters:
//   - a (Version): the first version to compare.
//   - b (Version): the second version to compare.
//
// Returns:
//   - bool: true if both a.Hash and b.Hash are defined and equal.
func NoConflictForImmutable(a, b Version) bool {
	return a.Hash.Defined() && b.Hash.Defined() && a.Hash.Equals(b.Hash)
}

// ResolveMutableMetadata applies version resolution for mutable metadata.
// Delegates to CompareVersionsLastWriterWins; on a tie (comparison result 0),
// a is returned.
//
// Parameters:
//   - a (Version): the first candidate version.
//   - b (Version): the second candidate version.
//
// Returns:
//   - Version: the winning version per last-writer-wins (a on a tie).
func ResolveMutableMetadata(a, b Version) Version {
	if CompareVersionsLastWriterWins(a, b) >= 0 {
		return a
	}
	return b
}
