// Purpose: Computes the dial-maintenance outbound target: default 20, capped when the cluster or peerstore cannot support more.

package node

// DefaultMinOutbound is the default target for minimum outbound libp2p connections.
const DefaultMinOutbound = 20

// normalizeMinOutbound returns want if it is a positive value, otherwise it
// substitutes DefaultMinOutbound. Callers use explicit positive values to
// override the default target; zero or negative values mean "use the
// default".
//
// Parameters:
//   - want (int): the caller-requested minimum outbound target; <= 0 means unset.
//
// Returns:
//   - int: want unchanged if positive, otherwise DefaultMinOutbound.
func normalizeMinOutbound(want int) int {
	if want <= 0 {
		return DefaultMinOutbound
	}
	return want
}

// effectiveOutboundTarget returns the number of outbound peer connections this
// node should maintain, capped by cluster size or known-peer count so the
// dialer loop never chases a target it cannot possibly satisfy.
//
// want is first normalized via normalizeMinOutbound (default 20 when <= 0).
// Then:
//   - If clusterSize > 0, the target is capped at clusterSize-1 (there are
//     not enough nodes in the network to reach a higher target).
//   - Else if knownOthers > 0, the target is capped at knownOthers (the node
//     cannot dial more distinct peers than it knows with addresses).
//   - When clusterSize == 0 and knownOthers == 0 (e.g. early boot before any
//     peer is in the store), the normalized want is returned unmodified.
//
// Parameters:
//   - want (int): the configured minimum outbound connection target; <= 0 uses DefaultMinOutbound.
//   - clusterSize (int): total nodes in the cluster if known; 0 if unknown.
//   - knownOthers (int): peers in the peerstore that have known addresses (excluding self).
//
// Returns:
//   - int: the effective outbound connection target to dial toward.
func effectiveOutboundTarget(want int, clusterSize int, knownOthers int) int {
	want = normalizeMinOutbound(want)
	if clusterSize > 0 {
		cap := clusterSize - 1
		if cap < 0 {
			cap = 0
		}
		if cap < want {
			want = cap
		}
		return want
	}
	if knownOthers > 0 && knownOthers < want {
		return knownOthers
	}
	return want
}
