// Purpose: "storage-available" protocol integration for open networks.
// Allows stakers to advertise storage availability and providers to find replica hosts.

package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

const (
	// StorageAvailableTuplePrefix is the tuple name prefix for storage-available offers.
	// Format: "storage-available:<peer_id>"
	StorageAvailableTuplePrefix = "storage-available:"
)

// StorageAvailableOffer represents a storage-available advertisement from a staker.
// This is serialized as JSON and stored in the tuple space.
type StorageAvailableOffer struct {
	// PeerID is the peer identifier of the staker.
	PeerID string `json:"peer_id"`
	// CommittedStake is the amount of tokens staked (0 if not tokenized).
	CommittedStake uint64 `json:"committed_stake"`
	// StorageAvailability is the available storage capacity (bytes).
	StorageAvailability uint64 `json:"storage_availability"`
	// ReputationScore is the peer's reputation score (0.0 to 1.0).
	ReputationScore float64 `json:"reputation_score"`
	// AvailabilityDuration is how long the storage will be available (seconds).
	AvailabilityDuration int64 `json:"availability_duration"`
	// Timestamp is when this offer was created (Unix timestamp).
	Timestamp int64 `json:"timestamp"`
}

// StorageAvailableProtocol handles storage-available protocol operations.
// Integrates with tuple space for O(log N) discovery and replica selection algorithm.
type StorageAvailableProtocol struct {
	ts tuplespace.TupleSpace
	// PeerIDsToCheck returns peer IDs to query for storage-available offers.
	// When set, used for DHT-backed tuple space (no pattern matching); each peer
	// is looked up as "storage-available:<peer_id>". When nil, uses pattern match.
	PeerIDsToCheck func() []peer.ID
	// RTTMeasurer is an optional function to measure RTT to a peer.
	// If nil, RTT will be set to 0 (unknown) in candidates.
	RTTMeasurer func(peerID peer.ID) (time.Duration, error)
	// RTTThresholds for distance classification (nil uses defaults).
	RTTThresholds *RTTThresholds
}

// NewStorageAvailableProtocol creates a new storage-available protocol handler.
func NewStorageAvailableProtocol(ts tuplespace.TupleSpace) *StorageAvailableProtocol {
	return &StorageAvailableProtocol{
		ts:            ts,
		RTTThresholds: nil, // Will use defaults
	}
}

// AdvertiseStorageAvailable advertises this peer's storage availability.
// Creates a tuple in the tuple space that other peers can discover.
// Returns error if tuple space operation fails.
func (sap *StorageAvailableProtocol) AdvertiseStorageAvailable(
	peerID peer.ID,
	committedStake uint64,
	storageAvailability uint64,
	reputationScore float64,
	availabilityDuration time.Duration,
) error {
	if sap.ts == nil {
		return errors.New("tuple space required")
	}

	offer := StorageAvailableOffer{
		PeerID:               peerID.String(),
		CommittedStake:       committedStake,
		StorageAvailability:  storageAvailability,
		ReputationScore:      reputationScore,
		AvailabilityDuration: int64(availabilityDuration.Seconds()),
		Timestamp:            time.Now().Unix(),
	}

	data, err := json.Marshal(offer)
	if err != nil {
		return fmt.Errorf("marshal offer: %w", err)
	}

	tupleName := StorageAvailableTuplePrefix + peerID.String()
	_, err = sap.ts.TsPut(tupleName, data)
	if err != nil {
		return fmt.Errorf("tuple space put failed: %w", err)
	}

	return nil
}

// WithdrawStorageAvailable removes this peer's storage-available advertisement.
// Uses TsGet (consuming) to remove the tuple.
func (sap *StorageAvailableProtocol) WithdrawStorageAvailable(peerID peer.ID) error {
	if sap.ts == nil {
		return errors.New("tuple space required")
	}

	tupleName := StorageAvailableTuplePrefix + peerID.String()
	_, err := sap.ts.TsGet(tupleName)
	if err != nil {
		return fmt.Errorf("tuple space get failed: %w", err)
	}

	return nil
}

// FindStorageAvailableCandidates finds storage-available peers matching the desired distance category.
// When PeerIDsToCheck is set (DHT tuple space): iterates over those peers and TsRead each
// "storage-available:<peer_id>". When nil: uses pattern matching (P2P tuple space supports regex).
//
// Parameters:
//   - providerID: The provider node's peer ID (for RTT measurement)
//   - desiredCategory: The distance category needed (Near/Midrange/Far-flung)
//   - maxCandidates: Maximum number of candidates to return
//
// Returns: List of PeerCandidate structs, sorted by selection score.
func (sap *StorageAvailableProtocol) FindStorageAvailableCandidates(
	providerID peer.ID,
	desiredCategory DistanceCategory,
	maxCandidates int,
) ([]PeerCandidate, error) {
	if sap.ts == nil {
		return nil, errors.New("tuple space required")
	}

	var candidates []PeerCandidate
	seenPeers := make(map[string]bool)

	if sap.PeerIDsToCheck != nil {
		// DHT tuple space: no pattern matching; iterate over known peers
		for _, pid := range sap.PeerIDsToCheck() {
			if len(candidates) >= maxCandidates {
				break
			}
			tupleName := StorageAvailableTuplePrefix + pid.String()
			offerData, err := sap.ts.TsRead(tupleName)
			if err != nil {
				continue
			}
			var offer StorageAvailableOffer
			if err := json.Unmarshal(offerData, &offer); err != nil {
				continue
			}
			if seenPeers[offer.PeerID] {
				continue
			}
			seenPeers[offer.PeerID] = true
			candidate, err := sap.offerToCandidate(offer, providerID)
			if err != nil {
				continue
			}
			if candidate.DistanceCategory == desiredCategory {
				candidates = append(candidates, candidate)
			}
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("no storage-available offers found (checked %d peers)", len(sap.PeerIDsToCheck()))
		}
		return candidates, nil
	}

	// P2P tuple space: pattern matching
	pattern := StorageAvailableTuplePrefix + "*"
	maxIterations := maxCandidates * 2
	for i := 0; i < maxIterations && len(candidates) < maxCandidates; i++ {
		offerData, err := sap.ts.TsRead(pattern)
		if err != nil {
			if len(candidates) == 0 {
				return nil, fmt.Errorf("no storage-available offers found: %w", err)
			}
			break
		}
		var offer StorageAvailableOffer
		if err := json.Unmarshal(offerData, &offer); err != nil {
			continue
		}
		if seenPeers[offer.PeerID] {
			continue
		}
		seenPeers[offer.PeerID] = true
		candidate, err := sap.offerToCandidate(offer, providerID)
		if err != nil {
			continue
		}
		if candidate.DistanceCategory == desiredCategory {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

// FindAndSelectReplicas finds storage-available candidates and selects the best ones
// using the replica selection algorithm.
//
// Parameters:
//   - providerID: The provider node's peer ID
//   - desiredCategory: The distance category needed
//   - repVector: The replication vector for the key
//   - criteria: Selection criteria with weights
//   - count: Number of replicas needed
//
// Returns: Selected peer candidates, sorted by score (best first).
func (sap *StorageAvailableProtocol) FindAndSelectReplicas(
	providerID peer.ID,
	desiredCategory DistanceCategory,
	criteria SelectionCriteria,
	count int,
) ([]PeerCandidate, error) {
	// Find all candidates (this would be expanded to read multiple offers)
	candidates, err := sap.FindStorageAvailableCandidates(providerID, desiredCategory, count*3)
	if err != nil {
		return nil, err
	}

	if len(candidates) == 0 {
		return nil, errors.New("no storage-available candidates found")
	}

	// Use the replica selection algorithm
	selected := SelectReplicaCandidates(candidates, desiredCategory, criteria, count)
	return selected, nil
}

// offerToCandidate converts a StorageAvailableOffer to a PeerCandidate.
// Measures RTT if RTTMeasurer is set, otherwise uses 0.
func (sap *StorageAvailableProtocol) offerToCandidate(
	offer StorageAvailableOffer,
	_ peer.ID,
) (PeerCandidate, error) {
	peerID, err := peer.Decode(offer.PeerID)
	if err != nil {
		return PeerCandidate{}, fmt.Errorf("invalid peer ID: %w", err)
	}

	var rtt time.Duration
	if sap.RTTMeasurer != nil {
		measuredRTT, err := sap.RTTMeasurer(peerID)
		if err == nil {
			rtt = measuredRTT
		}
		// If measurement fails, rtt remains 0
	}

	distanceCategory := ClassifyDistanceByRTT(rtt, sap.RTTThresholds)

	return PeerCandidate{
		PeerID:              peerID,
		RTT:                 rtt,
		DistanceCategory:    distanceCategory,
		CommittedStake:      offer.CommittedStake,
		StorageAvailability: offer.StorageAvailability,
		ReputationScore:     offer.ReputationScore,
	}, nil
}

// UpdateOffer updates an existing storage-available advertisement.
// Useful when storage availability or reputation changes.
func (sap *StorageAvailableProtocol) UpdateOffer(
	peerID peer.ID,
	committedStake uint64,
	storageAvailability uint64,
	reputationScore float64,
	availabilityDuration time.Duration,
) error {
	// Withdraw old offer
	if err := sap.WithdrawStorageAvailable(peerID); err != nil {
		// Ignore error if offer doesn't exist
	}

	// Advertise new offer
	return sap.AdvertiseStorageAvailable(
		peerID,
		committedStake,
		storageAvailability,
		reputationScore,
		availabilityDuration,
	)
}
