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
	// IsSynchronized indicates if actual distribution matches expected (within tolerance).
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

// VerifyKeyStateWithRepVector verifies key state against its replication vector.
// Key is the primary identifier. Discovers providers via GetToken (key-based) when
// tokenStore is set, merges routing table providers (multiple per key), and compares
// actual vs expected distribution.
//
// Parameters:
//   - k: The Key to verify (primary identifier)
//   - rt: The routing table containing expected replication vector and providers
//   - tokenStore: ValueStore for GetToken (key-based provider discovery); nil skips DHT lookup
//   - providerID: The local provider ID (for RTT measurement reference)
//   - rttMeasurer: Optional function to measure RTT to providers (nil uses 0)
//   - replicationFactor: The total replication factor R (default 7 if <= 0)
//   - thresholds: RTT thresholds for distance classification (nil uses defaults)
//
// Returns: Verification result with actual vs expected distribution.
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
		Key:               k,
		MissingCategories: make([]DistanceCategory, 0),
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
	verification.ExpectedCounts.Near = int(float64(replicationFactor) * expectedRepVector.Near)
	verification.ExpectedCounts.Midrange = int(float64(replicationFactor) * expectedRepVector.Midrange)
	verification.ExpectedCounts.FarFlung = int(float64(replicationFactor) * expectedRepVector.FarFlung)

	// Discover providers via GetToken (key-based) when tokenStore is available
	if tokenStore != nil {
		token, err := GetToken(ctx, tokenStore, k)
		if err == nil && len(token.Locations) > 0 {
			verification.Providers = make([]ProviderDistanceInfo, 0, len(token.Locations))
			for _, loc := range token.Locations {
				var rtt time.Duration = loc.RTT
				if rtt == 0 && rttMeasurer != nil {
					measuredRTT, err := rttMeasurer(loc.ProviderID)
					if err == nil {
						rtt = measuredRTT
					}
				}
				distanceCategory := ClassifyDistanceByRTT(rtt, thresholds)
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
			if seenProviderIDs[p.ProviderID] {
				continue
			}
			seenProviderIDs[p.ProviderID] = true
			info := ProviderDistanceInfo{
				ProviderID:       p.ProviderID,
				DistanceCategory: p.DistanceCategory,
				RTT:              0,
			}
			verification.Providers = append(verification.Providers, info)
			switch p.DistanceCategory {
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

	// Check synchronization: actual vs expected (with tolerance)
	// Allow some tolerance (e.g., ±1 replica per category)
	tolerance := 1
	nearOK := abs(verification.ActualCounts.Near-verification.ExpectedCounts.Near) <= tolerance
	midrangeOK := abs(verification.ActualCounts.Midrange-verification.ExpectedCounts.Midrange) <= tolerance
	farFlungOK := abs(verification.ActualCounts.FarFlung-verification.ExpectedCounts.FarFlung) <= tolerance

	verification.IsSynchronized = nearOK && midrangeOK && farFlungOK

	// Identify missing categories
	if !nearOK && verification.ActualCounts.Near < verification.ExpectedCounts.Near {
		verification.MissingCategories = append(verification.MissingCategories, DistanceNear)
	}
	if !midrangeOK && verification.ActualCounts.Midrange < verification.ExpectedCounts.Midrange {
		verification.MissingCategories = append(verification.MissingCategories, DistanceMidrange)
	}
	if !farFlungOK && verification.ActualCounts.FarFlung < verification.ExpectedCounts.FarFlung {
		verification.MissingCategories = append(verification.MissingCategories, DistanceFarFlung)
	}

	return verification, nil
}

// abs returns the absolute value of an integer.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
