package tuplespace

import (
	"context"
	"errors"

	kbucket "github.com/libp2p/go-libp2p-kbucket"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ClosestPeerFinder is the subset of Kademlia used for tuple ownership.
type ClosestPeerFinder interface {
	GetClosestPeers(ctx context.Context, key string) ([]peer.ID, error)
}

// DHTTupleOwnerResolver maps a tuple name into the Kademlia keyspace and
// selects the XOR-closest known peer. Including self is important: Kademlia
// lookups generally return remote peers and may omit the querying node even
// when it is the closest owner.
type DHTTupleOwnerResolver struct {
	self   peer.ID
	finder ClosestPeerFinder
}

func NewDHTTupleOwnerResolver(self peer.ID, finder ClosestPeerFinder) (*DHTTupleOwnerResolver, error) {
	if self == "" {
		return nil, errors.New("local peer ID required")
	}
	if finder == nil {
		return nil, errors.New("closest-peer finder required")
	}
	return &DHTTupleOwnerResolver{self: self, finder: finder}, nil
}

func (r *DHTTupleOwnerResolver) ResolveTupleOwner(ctx context.Context, tupleName string) (peer.ID, error) {
	if tupleName == "" {
		return "", errors.New("tuple name required")
	}
	peers, err := r.finder.GetClosestPeers(ctx, tupleName)
	if err != nil && len(peers) == 0 {
		// A single-node network remains a valid tuple space.
		return r.self, nil
	}
	owner := r.self
	for _, candidate := range peers {
		if candidate == "" {
			continue
		}
		if kbucket.Closer(candidate, owner, tupleName) {
			owner = candidate
		}
	}
	return owner, nil
}
