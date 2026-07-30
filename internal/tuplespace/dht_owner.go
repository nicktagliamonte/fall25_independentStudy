package tuplespace

import (
	"context"
	"errors"
	"fmt"
	"sort"

	kbucket "github.com/libp2p/go-libp2p-kbucket"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ClosestPeerFinder is the subset of Kademlia used for tuple ownership.
type ClosestPeerFinder interface {
	GetClosestPeers(ctx context.Context, key string) ([]peer.ID, error)
}

type dhtPeerFinder interface {
	FindPeer(ctx context.Context, id peer.ID) (peer.AddrInfo, error)
}

type stablePeerFinder interface {
	StablePeerInfo(id peer.ID) (peer.AddrInfo, bool)
}

// DHTTupleOwnerResolver maps a tuple name into the Kademlia keyspace and
// selects the XOR-closest known peer. Including self is important: Kademlia
// lookups generally return remote peers and may omit the querying node even
// when it is the closest owner.
type DHTTupleOwnerResolver struct {
	self              peer.ID
	finder            ClosestPeerFinder
	stablePeers       stablePeerFinder
	minimumCandidates int
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

// SetMinimumCandidates prevents ownership decisions from an under-populated
// routing view. Production clusters use a quorum below Kademlia's full
// closest-peer result width so a healthy but not perfectly full routing table
// can still make progress. A zero value retains the single-node fallback used
// by standalone nodes and focused tests.
func (r *DHTTupleOwnerResolver) SetMinimumCandidates(minimum int) {
	if minimum < 0 {
		minimum = 0
	}
	r.minimumCandidates = minimum
}

// SetStablePeerFinder supplies authenticated listen addresses learned through
// Tarsus handshakes. These take precedence over observed inbound source ports
// returned by the DHT peerstore.
func (r *DHTTupleOwnerResolver) SetStablePeerFinder(finder stablePeerFinder) {
	r.stablePeers = finder
}

// FindPeer resolves a selected owner's current addresses through the same DHT
// used for ownership. Callers use it when an authority record names a peer that
// is not yet present in their local peerstore.
func (r *DHTTupleOwnerResolver) FindPeer(ctx context.Context, id peer.ID) (peer.AddrInfo, error) {
	if r.stablePeers != nil {
		if info, ok := r.stablePeers.StablePeerInfo(id); ok {
			return info, nil
		}
	}
	finder, ok := r.finder.(dhtPeerFinder)
	if !ok {
		return peer.AddrInfo{}, errors.New("closest-peer finder does not support peer lookup")
	}
	return finder.FindPeer(ctx, id)
}

func (r *DHTTupleOwnerResolver) ResolveTupleOwner(ctx context.Context, tupleName string) (peer.ID, error) {
	return r.resolveTupleOwnerExcluding(ctx, tupleName, "")
}

// ResolveTupleOwnerAfter selects the next XOR-closest candidate after a failed
// owner. Index-authority failover uses it to avoid immediately re-electing the
// same unreachable peer in a higher epoch.
func (r *DHTTupleOwnerResolver) ResolveTupleOwnerAfter(
	ctx context.Context,
	tupleName string,
	excluded string,
) (peer.ID, error) {
	return r.resolveTupleOwnerExcluding(ctx, tupleName, excluded)
}

func (r *DHTTupleOwnerResolver) resolveTupleOwnerExcluding(
	ctx context.Context,
	tupleName string,
	excluded string,
) (peer.ID, error) {
	if tupleName == "" {
		return "", errors.New("tuple name required")
	}
	peers, err := r.finder.GetClosestPeers(ctx, tupleName)
	if len(peers) < r.minimumCandidates {
		return "", fmt.Errorf(
			"ownership lookup returned %d candidates, need at least %d",
			len(peers),
			r.minimumCandidates,
		)
	}
	if err != nil && len(peers) == 0 {
		// A single-node network remains a valid tuple space.
		if r.self.String() == excluded {
			return "", errors.New("no tuple owner remains after exclusion")
		}
		return r.self, nil
	}
	candidates := make([]peer.ID, 0, len(peers)+1)
	seen := make(map[peer.ID]struct{}, len(peers)+1)
	if r.self.String() != excluded {
		candidates = append(candidates, r.self)
		seen[r.self] = struct{}{}
	}
	for _, candidate := range peers {
		if candidate == "" || candidate.String() == excluded {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return "", errors.New("no tuple owner remains after exclusion")
	}
	sort.Slice(candidates, func(i, j int) bool {
		return kbucket.Closer(candidates[i], candidates[j], tupleName)
	})
	return candidates[0], nil
}
