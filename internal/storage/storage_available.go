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

// NewStorageAvailableProtocol creates a new StorageAvailableProtocol backed by tuple space ts,
// with RTTThresholds left nil (so ClassifyDistanceByRTT will use its default thresholds).
// Callers typically set PeerIDsToCheck and/or RTTMeasurer on the returned value afterward.
//
// Parameters:
//   - ts (tuplespace.TupleSpace): the tuple space used to store/read storage-available offers.
//
// Returns:
//   - *StorageAvailableProtocol: the constructed protocol handler.
func NewStorageAvailableProtocol(ts tuplespace.TupleSpace) *StorageAvailableProtocol {
	return &StorageAvailableProtocol{
		ts:            ts,
		RTTThresholds: nil, // Will use defaults
	}
}

// AdvertiseStorageAvailable advertises this peer's storage availability by writing a
// StorageAvailableOffer (JSON-encoded) into the tuple space under the name
// StorageAvailableTuplePrefix+peerID, timestamped with the current Unix time, so other peers
// can discover it (via FindStorageAvailableCandidates).
//
// Parameters:
//   - peerID (peer.ID): the advertising peer's ID.
//   - committedStake (uint64): the amount of stake committed (0 if not tokenized).
//   - storageAvailability (uint64): available storage capacity, in bytes.
//   - reputationScore (float64): the peer's reputation score (0.0 to 1.0).
//   - availabilityDuration (time.Duration): how long the storage will remain available.
//
// Returns:
//   - error: non-nil if sap.ts is nil, JSON marshaling fails, or the tuple space write fails.
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
	if replacer, ok := sap.ts.(tuplespace.NamedTupleReplacer); ok {
		_, err = replacer.TsReplace(tupleName, data)
	} else {
		_, err = sap.ts.TsPut(tupleName, data)
	}
	if err != nil {
		return fmt.Errorf("tuple space put failed: %w", err)
	}

	return nil
}

// WithdrawStorageAvailable removes this peer's storage-available advertisement by consuming
// (via TsGet, which removes the tuple as it reads it) the tuple named
// StorageAvailableTuplePrefix+peerID.
//
// Parameters:
//   - peerID (peer.ID): the peer whose advertisement should be withdrawn.
//
// Returns:
//   - error: non-nil if sap.ts is nil or the tuple space get/consume fails (e.g. no such tuple).
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

// FindStorageAvailableCandidates finds storage-available peers matching desiredCategory. When
// sap.PeerIDsToCheck is set (used for a DHT-backed tuple space, which has no pattern-matching
// support), it iterates each returned peer ID, does a direct TsRead of
// "storage-available:<peer_id>", and returns an error if zero candidates matched after
// checking every peer. When sap.PeerIDsToCheck is nil (P2P tuple space, which supports
// wildcard reads), it instead repeatedly TsReads the pattern "storage-available:*", stopping
// after 2*maxCandidates iterations or once maxCandidates distinct matches are collected. In
// both modes, offers are decoded from JSON, de-duplicated by peer ID, converted to a
// PeerCandidate via offerToCandidate (which classifies distance using RTTMeasurer/RTTThresholds),
// and only candidates whose DistanceCategory equals desiredCategory are kept.
//
// Parameters:
//   - providerID (peer.ID): the provider node's peer ID (passed through for RTT measurement context).
//   - desiredCategory (DistanceCategory): the distance category needed (Near/Midrange/Far-flung).
//   - maxCandidates (int): maximum number of candidates to return.
//
// Returns:
//   - []PeerCandidate: matching candidates (not explicitly sorted by this function).
//   - error: non-nil if sap.ts is nil, or no matching offers are found in either mode.
func (sap *StorageAvailableProtocol) FindStorageAvailableCandidates(
	providerID peer.ID,
	desiredCategory DistanceCategory,
	maxCandidates int,
) ([]PeerCandidate, error) {
	return sap.findStorageAvailableCandidates(providerID, &desiredCategory, maxCandidates)
}

// FindAnyStorageAvailableCandidates returns unexpired offers without requiring
// one RTT category. Repair uses it only after preferred category placement
// cannot fill the fixed replica-count shortfall.
func (sap *StorageAvailableProtocol) FindAnyStorageAvailableCandidates(
	providerID peer.ID,
	maxCandidates int,
) ([]PeerCandidate, error) {
	return sap.findStorageAvailableCandidates(providerID, nil, maxCandidates)
}

func (sap *StorageAvailableProtocol) findStorageAvailableCandidates(
	providerID peer.ID,
	desiredCategory *DistanceCategory,
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
			if maxCandidates > 0 && len(candidates) >= maxCandidates {
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
			if offerExpired(offer, time.Now()) {
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
			if desiredCategory == nil || candidate.DistanceCategory == *desiredCategory {
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
	if maxCandidates <= 0 {
		maxIterations = 1024
	}
	for i := 0; i < maxIterations &&
		(maxCandidates <= 0 || len(candidates) < maxCandidates); i++ {
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
		if offerExpired(offer, time.Now()) {
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
		if desiredCategory == nil || candidate.DistanceCategory == *desiredCategory {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func offerExpired(offer StorageAvailableOffer, now time.Time) bool {
	if offer.Timestamp <= 0 || offer.AvailabilityDuration <= 0 {
		return true
	}
	expires := time.Unix(offer.Timestamp, 0).Add(time.Duration(offer.AvailabilityDuration) * time.Second)
	return !now.Before(expires)
}

// FindAndSelectReplicas finds storage-available candidates for desiredCategory (requesting up
// to count*3 candidates via FindStorageAvailableCandidates to give the selection algorithm a
// pool to choose from) and then narrows them to count using SelectReplicaCandidates and criteria.
//
// Parameters:
//   - providerID (peer.ID): the provider node's peer ID.
//   - desiredCategory (DistanceCategory): the distance category needed.
//   - criteria (SelectionCriteria): selection weights used to rank candidates.
//   - count (int): number of replicas needed.
//
// Returns:
//   - []PeerCandidate: selected peer candidates, sorted by score (best first) per
//     SelectReplicaCandidates.
//   - error: non-nil if candidate discovery fails or returns zero candidates.
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

// offerToCandidate converts a decoded StorageAvailableOffer into a PeerCandidate: it decodes
// the offer's peer ID string, measures RTT via sap.RTTMeasurer if set (0/unknown if unset or
// measurement fails), classifies the resulting distance category via ClassifyDistanceByRTT
// using sap.RTTThresholds, and copies over stake/availability/reputation fields.
//
// Parameters:
//   - offer (StorageAvailableOffer): the decoded offer to convert.
//   - _ (peer.ID): unused (reserved for future RTT-measurement context relative to a requesting
//     provider).
//
// Returns:
//   - PeerCandidate: the resulting candidate with RTT and distance classification populated.
//   - error: non-nil if offer.PeerID fails to decode as a peer.ID.
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

// UpdateOffer replaces an existing storage-available advertisement with new values, useful
// when storage availability or reputation changes. It first calls WithdrawStorageAvailable
// (ignoring any error, e.g. if no prior offer existed) and then re-advertises via
// AdvertiseStorageAvailable with the new parameters.
//
// Parameters:
//   - peerID (peer.ID): the advertising peer's ID.
//   - committedStake (uint64): the amount of stake committed (0 if not tokenized).
//   - storageAvailability (uint64): available storage capacity, in bytes.
//   - reputationScore (float64): the peer's reputation score (0.0 to 1.0).
//   - availabilityDuration (time.Duration): how long the storage will remain available.
//
// Returns:
//   - error: non-nil if the re-advertisement (AdvertiseStorageAvailable) fails.
func (sap *StorageAvailableProtocol) UpdateOffer(
	peerID peer.ID,
	committedStake uint64,
	storageAvailability uint64,
	reputationScore float64,
	availabilityDuration time.Duration,
) error {
	// AdvertiseStorageAvailable uses atomic replacement when the configured
	// tuple space supports the optional singleton-record extension.
	return sap.AdvertiseStorageAvailable(
		peerID,
		committedStake,
		storageAvailability,
		reputationScore,
		availabilityDuration,
	)
}
