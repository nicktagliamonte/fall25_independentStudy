package node

const kademliaOwnerCandidateQuorum = 16

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
