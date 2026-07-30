// Purpose: Key state verification with replication vectors for read protocol.

package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
)

// ReplicaStateVerification represents the result of verifying key state against replication vector.
type ReplicaStateVerification struct {
	// Key is the primary identifier being verified.
	Key Key
	// CID is the content identifier (from routing table) for IPFS compatibility.
	CID cid.Cid
	// ExpectedRepVector is the expected replication vector from routing table.
	ExpectedRepVector ReplicationVector
	// ActualCounts shows actual replica distribution by distance category.
	ActualCounts struct {
		Near     int
		Midrange int
		FarFlung int
		Total    int
		Unknown  int // providers with unknown/unclassified distance
	}
	// ExpectedCounts shows expected replica distribution (based on replication factor R).
	ExpectedCounts struct {
		Near     int
		Midrange int
		FarFlung int
		Total    int
	}
	// Providers lists all discovered providers with their distance classifications.
	Providers []ProviderDistanceInfo
	// UnreachableProviders lists token or routing-table providers that failed an
	// active RTT/liveness probe. They are not counted toward durability.
	UnreachableProviders []peer.ID
	// IsSynchronized indicates that the reachable replica-count target is met.
	// MissingCategories separately reports best-effort placement deficits.
	IsSynchronized bool
	// MissingCategories lists distance categories that are missing replicas.
	MissingCategories []DistanceCategory
}

// ProviderDistanceInfo contains provider information with distance classification.
type ProviderDistanceInfo struct {
	// ProviderID is the peer ID of the provider.
	ProviderID peer.ID
	// DistanceCategory is the classified distance (Near/Midrange/Far-flung).
	DistanceCategory DistanceCategory
	// RTT is the measured or estimated RTT (0 if unknown).
	RTT time.Duration
}

// VerifyKeyStateWithRepVector verifies the actual replica distribution for Key k against its
// expected replication vector. It first resolves the expected ReplicationVector from rt.Get(k)
// (falling back to DefaultReplicationVector if rt is nil or has no entry for k) and computes
// expected per-category counts by scaling replicationFactor (default 7 if <= 0) by each vector
// component. It then discovers actual providers from two sources and merges them, de-duplicating
// by provider ID (token-discovered entries take precedence; routing-table-only entries are
// added afterward): (1) if tokenStore is non-nil, via GetToken(k), classifying each location's
// distance using its stored RTT (or rttMeasurer if the stored RTT is 0) and thresholds; (2) any
// providers in rt's entry for k not already seen via the token. Finally it
// requires the total reachable replica count to meet the durability target.
// Category shortfalls remain visible in MissingCategories so placement and
// repair can prefer them, but a topology with no midrange or far-flung
// candidates does not sacrifice the fixed replica count.
//
// Parameters:
//   - ctx (context.Context): passed through to GetToken for cancellation.
//   - k (Key): the key to verify (primary identifier); must be non-zero.
//   - rt (*RoutingTable): the routing table containing the expected replication vector and any
//     locally-known providers; may be nil.
//   - tokenStore (routing.ValueStore): used for GetToken-based provider discovery; nil skips
//     this source entirely (only routing-table providers are considered).
//   - providerID (peer.ID): the local provider ID (currently unused in the body; reserved for
//     RTT measurement reference).
//   - rttMeasurer (func(peer.ID) (time.Duration, error)): optional function to measure RTT to a
//     token location when its stored RTT is 0; nil leaves RTT as 0 (unknown).
//   - replicationFactor (int): the total replication factor R; values <= 0 default to 7.
//   - thresholds (*RTTThresholds): RTT thresholds for distance classification; nil uses
//     ClassifyDistanceByRTT's defaults.
//
// Returns:
//   - *ReplicaStateVerification: the verification result with actual vs expected distribution,
//     merged provider list, synchronization status, and missing categories.
//   - error: non-nil only if k is zero.
func VerifyKeyStateWithRepVector(
	ctx context.Context,
	k Key,
	rt *RoutingTable,
	tokenStore routing.ValueStore,
	providerID peer.ID,
	rttMeasurer func(peer.ID) (time.Duration, error),
	replicationFactor int,
	thresholds *RTTThresholds,
) (*ReplicaStateVerification, error) {
	if k.IsZero() {
		return nil, fmt.Errorf("invalid key")
	}

	verification := &ReplicaStateVerification{
		Key:                  k,
		MissingCategories:    make([]DistanceCategory, 0),
		UnreachableProviders: make([]peer.ID, 0),
	}

	// Get expected replication vector and routing table providers by Key
	var expectedRepVector ReplicationVector
	var routingEntry *RoutingTableEntry
	if rt != nil {
		routingEntry = rt.Get(k)
		if routingEntry != nil {
			expectedRepVector = routingEntry.RepVector
			if routingEntry.CID.Defined() {
				verification.CID = routingEntry.CID
			}
		} else {
			expectedRepVector = DefaultReplicationVector()
		}
	} else {
		expectedRepVector = DefaultReplicationVector()
	}
	verification.ExpectedRepVector = expectedRepVector

	// Set replication factor
	if replicationFactor <= 0 {
		replicationFactor = 7 // default per architecture
	}

	// Calculate expected counts based on replication factor
	verification.ExpectedCounts.Total = replicationFactor
	verification.ExpectedCounts.Near,
		verification.ExpectedCounts.Midrange,
		verification.ExpectedCounts.FarFlung = ReplicationTargets(expectedRepVector, replicationFactor)
	unreachableIDs := make(map[peer.ID]bool)

	// Discover providers via GetToken (key-based) when tokenStore is available
	if tokenStore != nil {
		token, err := GetToken(ctx, tokenStore, k)
		if err == nil && len(token.Locations) > 0 {
			verification.Providers = make([]ProviderDistanceInfo, 0, len(token.Locations))
			for _, loc := range token.Locations {
				var rtt time.Duration = loc.RTT
				distanceCategory := DistanceUnknown
				if loc.ProviderID == providerID {
					distanceCategory = DistanceNear
				} else if rttMeasurer != nil {
					measuredRTT, err := rttMeasurer(loc.ProviderID)
					if err != nil || measuredRTT <= 0 {
						if !unreachableIDs[loc.ProviderID] {
							unreachableIDs[loc.ProviderID] = true
							verification.UnreachableProviders = append(verification.UnreachableProviders, loc.ProviderID)
						}
						continue
					}
					rtt = measuredRTT
					distanceCategory = ClassifyDistanceByRTT(rtt, thresholds)
				} else {
					distanceCategory = ClassifyDistanceByRTT(rtt, thresholds)
				}
				info := ProviderDistanceInfo{
					ProviderID:       loc.ProviderID,
					DistanceCategory: distanceCategory,
					RTT:              rtt,
				}
				verification.Providers = append(verification.Providers, info)
				switch distanceCategory {
				case DistanceNear:
					verification.ActualCounts.Near++
				case DistanceMidrange:
					verification.ActualCounts.Midrange++
				case DistanceFarFlung:
					verification.ActualCounts.FarFlung++
				default:
					verification.ActualCounts.Unknown++
				}
				verification.ActualCounts.Total++
			}
		}
	}

	// Merge routing table providers (multiple providers per key) into verification.
	// Add any from routing table not already discovered via token.
	seenProviderIDs := make(map[peer.ID]bool)
	for _, p := range verification.Providers {
		seenProviderIDs[p.ProviderID] = true
	}
	if routingEntry != nil && len(routingEntry.Providers) > 0 {
		for _, p := range routingEntry.Providers {
			if seenProviderIDs[p.ProviderID] || unreachableIDs[p.ProviderID] {
				continue
			}
			category := p.DistanceCategory
			var rtt time.Duration
			if p.ProviderID == providerID {
				category = DistanceNear
			} else if rttMeasurer != nil {
				measuredRTT, err := rttMeasurer(p.ProviderID)
				if err != nil || measuredRTT <= 0 {
					unreachableIDs[p.ProviderID] = true
					verification.UnreachableProviders = append(verification.UnreachableProviders, p.ProviderID)
					continue
				}
				rtt = measuredRTT
				category = ClassifyDistanceByRTT(rtt, thresholds)
			}
			seenProviderIDs[p.ProviderID] = true
			info := ProviderDistanceInfo{
				ProviderID:       p.ProviderID,
				DistanceCategory: category,
				RTT:              rtt,
			}
			verification.Providers = append(verification.Providers, info)
			switch category {
			case DistanceNear:
				verification.ActualCounts.Near++
			case DistanceMidrange:
				verification.ActualCounts.Midrange++
			case DistanceFarFlung:
				verification.ActualCounts.FarFlung++
			default:
				verification.ActualCounts.Unknown++
			}
			verification.ActualCounts.Total++
		}
	}

	nearOK := verification.ActualCounts.Near >= verification.ExpectedCounts.Near
	midrangeOK := verification.ActualCounts.Midrange >= verification.ExpectedCounts.Midrange
	farFlungOK := verification.ActualCounts.FarFlung >= verification.ExpectedCounts.FarFlung

	verification.IsSynchronized =
		verification.ActualCounts.Total >= verification.ExpectedCounts.Total

	// Identify missing categories
	if !nearOK {
		verification.MissingCategories = append(verification.MissingCategories, DistanceNear)
	}
	if !midrangeOK {
		verification.MissingCategories = append(verification.MissingCategories, DistanceMidrange)
	}
	if !farFlungOK {
		verification.MissingCategories = append(verification.MissingCategories, DistanceFarFlung)
	}

	return verification, nil
}
