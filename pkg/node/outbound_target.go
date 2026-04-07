// Purpose: Computes the dial-maintenance outbound target: default 20, capped when the cluster or peerstore cannot support more.

package node

// DefaultMinOutbound is the default target for minimum outbound libp2p connections.
const DefaultMinOutbound = 20

// normalizeMinOutbound maps zero or negative to the default; callers use explicit positive values for non-default behavior.
func normalizeMinOutbound(want int) int {
	if want <= 0 {
		return DefaultMinOutbound
	}
	return want
}

// effectiveOutboundTarget returns min(want, clusterCap, peerstoreCap) semantics:
// - want is normalized from MinOutbound (default 20 when <= 0).
// - If clusterSize > 0, caps at clusterSize-1 (not enough nodes in the network).
// - Else if knownOthers > 0, caps at knownOthers (cannot dial more distinct peers than we know with addresses).
// - When clusterSize == 0 and knownOthers == 0, returns want (e.g. early boot before any peer is in the store).
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
