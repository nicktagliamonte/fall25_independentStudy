package node

// Match the private DHT's closest-peer width. This is a convergence readiness
// guard, not the single-writer safety mechanism; leased authority records and
// PHT write fences provide that boundary.
const kademliaOwnerCandidateQuorum = DefaultTarsusDHTBucketSize

// ownerElectionCandidateMinimum returns the routing-view precondition for a
// configured cluster. Unknown and single-node deployments retain the
// resolver's standalone behavior.
func ownerElectionCandidateMinimum(clusterNodes int) int {
	if clusterNodes <= 1 {
		return 0
	}
	minimum := clusterNodes - 1
	if minimum > kademliaOwnerCandidateQuorum {
		minimum = kademliaOwnerCandidateQuorum
	}
	return minimum
}
