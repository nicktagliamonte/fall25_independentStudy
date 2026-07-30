// Purpose: Replication vector definitions for N/M/F (Near/Midrange/Far-flung) distribution.

package storage

import (
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// ReplicationVector defines the distribution of replicas across distance categories
// from the provider node. Percentages must sum to 1.0 (100%).
//
// Per architecture.txt:
//   - Near: Replicas placed close to the provider node (default 40%)
//   - Midrange: Replicas placed at medium distance from provider (default 30%)
//   - Far-flung: Replicas placed far from the provider node (default 30%)
//
// The default vector is (Near: 40%, Midrange: 30%, Far-flung: 30%).
// For high-performance scenarios with reliable networks, (Near: 90%, Midrange: 10%, Far-flung: 0%)
// can be practical.
type ReplicationVector struct {
	// Near is the percentage of replicas to place near the provider node (0.0 to 1.0).
	Near float64

	// Midrange is the percentage of replicas to place at midrange distance from provider (0.0 to 1.0).
	Midrange float64

	// FarFlung is the percentage of replicas to place far from the provider node (0.0 to 1.0).
	FarFlung float64
}

// DefaultReplicationVector returns the default replication vector:
// Near: 40%, Midrange: 30%, Far-flung: 30%
//
// Returns:
//   - ReplicationVector: {Near: 0.4, Midrange: 0.3, FarFlung: 0.3}.
func DefaultReplicationVector() ReplicationVector {
	return ReplicationVector{
		Near:     0.4,
		Midrange: 0.3,
		FarFlung: 0.3,
	}
}

// DistanceCategory represents the distance classification of a peer from the provider node.
type DistanceCategory int

const (
	// DistanceUnknown indicates that no usable RTT measurement is available.
	// It is deliberately distinct from DistanceNear: treating a failed or
	// absent measurement as zero RTT creates fictitious regional diversity.
	DistanceUnknown DistanceCategory = -1
	// DistanceNear indicates a peer is near the provider (low RTT, typically local/regional).
	DistanceNear DistanceCategory = iota
	// DistanceMidrange indicates a peer is at midrange distance (medium RTT, typically cross-region).
	DistanceMidrange
	// DistanceFarFlung indicates a peer is far from the provider (high RTT, typically intercontinental).
	DistanceFarFlung
)

// String returns the string representation of the distance category.
//
// Returns:
//   - string: "Near", "Midrange", "Far-flung", or "Unknown" for any other value.
func (d DistanceCategory) String() string {
	switch d {
	case DistanceNear:
		return "Near"
	case DistanceMidrange:
		return "Midrange"
	case DistanceFarFlung:
		return "Far-flung"
	default:
		return "Unknown"
	}
}

// RTTThresholds defines the RTT thresholds for distance classification.
// Peers with RTT < NearThreshold are classified as Near.
// Peers with NearThreshold <= RTT < FarThreshold are classified as Midrange.
// Peers with RTT >= FarThreshold are classified as Far-flung.
type RTTThresholds struct {
	// NearThreshold is the maximum RTT for Near classification (default: 50ms).
	NearThreshold time.Duration
	// FarThreshold is the minimum RTT for Far-flung classification (default: 200ms).
	FarThreshold time.Duration
}

// DefaultRTTThresholds returns default RTT thresholds for distance classification:
// Near: < 50ms, Midrange: 50-200ms, Far-flung: >= 200ms
//
// Returns:
//   - RTTThresholds: {NearThreshold: 50ms, FarThreshold: 200ms}.
func DefaultRTTThresholds() RTTThresholds {
	return RTTThresholds{
		NearThreshold: 50 * time.Millisecond,
		FarThreshold:  200 * time.Millisecond,
	}
}

// ClassifyDistanceByRTT classifies a peer's distance category based on its RTT from the provider node.
// Uses the provided thresholds to determine Near, Midrange, or Far-flung classification.
// If thresholds is nil, uses DefaultRTTThresholds().
//
// Parameters:
//   - rtt (time.Duration): the measured round-trip time to the peer.
//   - thresholds (*RTTThresholds): the classification thresholds; nil uses DefaultRTTThresholds().
//
// Returns:
//   - DistanceCategory: DistanceNear, DistanceMidrange, or DistanceFarFlung
//     depending on where rtt falls relative to the thresholds.
func ClassifyDistanceByRTT(rtt time.Duration, thresholds *RTTThresholds) DistanceCategory {
	if rtt <= 0 {
		return DistanceUnknown
	}
	if thresholds == nil {
		defaults := DefaultRTTThresholds()
		thresholds = &defaults
	}
	if rtt < thresholds.NearThreshold {
		return DistanceNear
	}
	if rtt < thresholds.FarThreshold {
		return DistanceMidrange
	}
	return DistanceFarFlung
}

// ReplicationTargets converts a fractional replication vector into exact
// integer category targets whose sum is replicationFactor. It uses the largest
// remainder method with stable Near, Midrange, Far-flung tie-breaking.
func ReplicationTargets(vector ReplicationVector, replicationFactor int) (near, midrange, farFlung int) {
	if replicationFactor <= 0 {
		return 0, 0, 0
	}
	weights := []float64{vector.Near, vector.Midrange, vector.FarFlung}
	totalWeight := 0.0
	for i := range weights {
		if weights[i] < 0 {
			weights[i] = 0
		}
		totalWeight += weights[i]
	}
	if totalWeight <= 0 {
		weights = []float64{1, 0, 0}
		totalWeight = 1
	}
	type allocation struct {
		index     int
		remainder float64
	}
	counts := make([]int, 3)
	remainders := make([]allocation, 3)
	assigned := 0
	for i, weight := range weights {
		exact := float64(replicationFactor) * weight / totalWeight
		counts[i] = int(exact)
		assigned += counts[i]
		remainders[i] = allocation{index: i, remainder: exact - float64(counts[i])}
	}
	sort.SliceStable(remainders, func(i, j int) bool {
		return remainders[i].remainder > remainders[j].remainder
	})
	unassigned := replicationFactor - assigned
	for i := 0; i < unassigned; i++ {
		counts[remainders[i%len(remainders)].index]++
	}
	return counts[0], counts[1], counts[2]
}

// PeerCandidate represents a candidate peer for replica selection.
// Contains all factors used in the selection algorithm.
type PeerCandidate struct {
	// PeerID is the peer identifier.
	PeerID peer.ID
	// RTT is the round-trip time from the provider node.
	RTT time.Duration
	// DistanceCategory is the classified distance (Near/Midrange/Far-flung).
	DistanceCategory DistanceCategory
	// CommittedStake is the amount of tokens staked (0 if not tokenized).
	// Higher stake indicates more commitment and economic security.
	CommittedStake uint64
	// StorageAvailability is the available storage capacity (bytes).
	// Higher availability means more capacity to store replicas.
	StorageAvailability uint64
	// ReputationScore is the peer's reputation score (0.0 to 1.0).
	// Based on uptime, retrieval speeds, proof validations, etc.
	ReputationScore float64
}

// SelectionCriteria defines weights for each factor in replica selection.
// Weights are normalized internally; higher weights indicate higher importance.
type SelectionCriteria struct {
	// StakeWeight is the weight for committed stakes (default: 0.3 if tokenized, 0.0 otherwise).
	StakeWeight float64
	// RTTWeight is the weight for RTT (lower RTT is better, default: 0.2).
	RTTWeight float64
	// StorageWeight is the weight for storage availability (default: 0.2).
	StorageWeight float64
	// ReputationWeight is the weight for reputation score (default: 0.3).
	ReputationWeight float64
	// Tokenized indicates if the network uses token-based selection (affects stake weight).
	Tokenized bool
}

// DefaultSelectionCriteria returns default selection criteria weights.
// For tokenized networks: Stake 30%, RTT 20%, Storage 20%, Reputation 30%.
// For non-tokenized networks: Stake 0%, RTT 25%, Storage 25%, Reputation 50%.
//
// Parameters:
//   - tokenized (bool): whether the network uses token-based (staked) selection.
//
// Returns:
//   - SelectionCriteria: the corresponding default weight set, with Tokenized
//     set to match the input.
func DefaultSelectionCriteria(tokenized bool) SelectionCriteria {
	if tokenized {
		return SelectionCriteria{
			StakeWeight:      0.3,
			RTTWeight:        0.2,
			StorageWeight:    0.2,
			ReputationWeight: 0.3,
			Tokenized:        true,
		}
	}
	return SelectionCriteria{
		StakeWeight:      0.0,
		RTTWeight:        0.25,
		StorageWeight:    0.25,
		ReputationWeight: 0.5,
		Tokenized:        false,
	}
}

// normalizeWeights ensures weights sum to 1.0, adjusting proportionally if needed.
// If the current total is <= 0, falls back to equal weights (0.25 each) rather
// than dividing by zero. Mutates sc in place.
func (sc *SelectionCriteria) normalizeWeights() {
	total := sc.StakeWeight + sc.RTTWeight + sc.StorageWeight + sc.ReputationWeight
	if total <= 0 {
		// Fallback: equal weights
		sc.StakeWeight = 0.25
		sc.RTTWeight = 0.25
		sc.StorageWeight = 0.25
		sc.ReputationWeight = 0.25
		return
	}
	if total != 1.0 {
		sc.StakeWeight /= total
		sc.RTTWeight /= total
		sc.StorageWeight /= total
		sc.ReputationWeight /= total
	}
}

// ScoreCandidate computes a selection score for a peer candidate.
// Higher scores indicate better candidates for replica placement.
// Each factor is normalized to [0.0, 1.0] against the supplied maxima (RTT is
// inverted, since lower RTT is better), then combined via criteria's
// (normalized) weights into a single weighted sum.
//
// Parameters:
//   - candidate (PeerCandidate): the peer being scored.
//   - criteria (SelectionCriteria): factor weights; normalized internally
//     (criteria is passed by value, so the caller's copy is unaffected).
//   - maxStake (uint64): the maximum CommittedStake among the candidate pool,
//     used to normalize the stake component; ignored if criteria.Tokenized is false.
//   - maxRTT (time.Duration): the maximum RTT among the candidate pool, used to
//     normalize/invert the RTT component.
//   - maxStorage (uint64): the maximum StorageAvailability among the candidate
//     pool, used to normalize the storage component.
//
// Returns:
//   - float64: the weighted score, generally in [0.0, 1.0] (stake/RTT/storage
//     components are clamped to that range; the sum is not re-clamped).
func ScoreCandidate(candidate PeerCandidate, criteria SelectionCriteria, maxStake uint64, maxRTT time.Duration, maxStorage uint64) float64 {
	criteria.normalizeWeights()

	var score float64

	// Stake component (higher is better, normalized by maxStake)
	if criteria.Tokenized && maxStake > 0 {
		stakeNorm := float64(candidate.CommittedStake) / float64(maxStake)
		if stakeNorm > 1.0 {
			stakeNorm = 1.0
		}
		score += criteria.StakeWeight * stakeNorm
	}

	// RTT component (lower is better, inverted and normalized)
	if maxRTT > 0 && candidate.RTT > 0 {
		// Invert: lower RTT = higher score
		rttNorm := 1.0 - (float64(candidate.RTT) / float64(maxRTT))
		if rttNorm < 0 {
			rttNorm = 0
		}
		score += criteria.RTTWeight * rttNorm
	} else if candidate.RTT == 0 {
		// Zero RTT (local) gets maximum score
		score += criteria.RTTWeight * 1.0
	}

	// Storage availability component (higher is better, normalized)
	if maxStorage > 0 {
		storageNorm := float64(candidate.StorageAvailability) / float64(maxStorage)
		if storageNorm > 1.0 {
			storageNorm = 1.0
		}
		score += criteria.StorageWeight * storageNorm
	}

	// Reputation component (higher is better, already normalized 0.0-1.0)
	repNorm := candidate.ReputationScore
	if repNorm < 0 {
		repNorm = 0
	}
	if repNorm > 1.0 {
		repNorm = 1.0
	}
	score += criteria.ReputationWeight * repNorm

	return score
}

// SelectReplicaCandidates selects the best candidates for replica placement
// based on the replication vector and selection criteria.
//
// Filters candidates by the desired distance category, computes per-pool maxima
// for stake/storage/RTT among the filtered set, scores each with ScoreCandidate,
// then sorts by score descending (ties broken by higher ReputationScore, then
// lower RTT), and returns up to count candidates.
//
// Parameters:
//   - candidates ([]PeerCandidate): List of peer candidates to evaluate
//   - desiredCategory (DistanceCategory): The distance category needed (Near/Midrange/Far-flung)
//   - criteria (SelectionCriteria): Selection criteria with weights
//   - count (int): Maximum number of candidates to return
//
// Returns:
//   - []PeerCandidate: Sorted list of candidates (best first), or nil if
//     candidates is empty, count <= 0, or no candidate matches desiredCategory.
func SelectReplicaCandidates(candidates []PeerCandidate, desiredCategory DistanceCategory, criteria SelectionCriteria, count int) []PeerCandidate {
	if len(candidates) == 0 || count <= 0 {
		return nil
	}

	// Filter by distance category
	filtered := make([]PeerCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.DistanceCategory == desiredCategory {
			filtered = append(filtered, c)
		}
	}

	if len(filtered) == 0 {
		return nil
	}

	// Compute max values for normalization
	var maxStake, maxStorage uint64
	var maxRTT time.Duration
	for _, c := range filtered {
		if c.CommittedStake > maxStake {
			maxStake = c.CommittedStake
		}
		if c.StorageAvailability > maxStorage {
			maxStorage = c.StorageAvailability
		}
		if c.RTT > maxRTT {
			maxRTT = c.RTT
		}
	}

	// Score all candidates
	type scoredCandidate struct {
		candidate PeerCandidate
		score     float64
	}
	scored := make([]scoredCandidate, len(filtered))
	for i, c := range filtered {
		scored[i] = scoredCandidate{
			candidate: c,
			score:     ScoreCandidate(c, criteria, maxStake, maxRTT, maxStorage),
		}
	}

	// Sort by score (descending)
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		// Tie-breaker: prefer higher reputation, then lower RTT
		if scored[i].candidate.ReputationScore != scored[j].candidate.ReputationScore {
			return scored[i].candidate.ReputationScore > scored[j].candidate.ReputationScore
		}
		if scored[i].candidate.RTT != scored[j].candidate.RTT {
			return scored[i].candidate.RTT < scored[j].candidate.RTT
		}
		return scored[i].candidate.PeerID.String() < scored[j].candidate.PeerID.String()
	})

	// Return top 'count' candidates
	if count > len(scored) {
		count = len(scored)
	}
	result := make([]PeerCandidate, count)
	for i := 0; i < count; i++ {
		result[i] = scored[i].candidate
	}
	return result
}
