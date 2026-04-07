// Purpose: Unit tests for effective outbound dial target.

package node

import "testing"

func TestEffectiveOutboundTarget_clusterCapsAtNMinusOne(t *testing.T) {
	if g := effectiveOutboundTarget(20, 10, 100); g != 9 {
		t.Fatalf("got %d want 9", g)
	}
	if g := effectiveOutboundTarget(20, 2, 0); g != 1 {
		t.Fatalf("got %d want 1", g)
	}
}

func TestEffectiveOutboundTarget_peerstoreCapWhenClusterUnset(t *testing.T) {
	if g := effectiveOutboundTarget(20, 0, 5); g != 5 {
		t.Fatalf("got %d want 5", g)
	}
	if g := effectiveOutboundTarget(20, 0, 100); g != 20 {
		t.Fatalf("got %d want 20", g)
	}
}

func TestEffectiveOutboundTarget_noPeerstoreCapWhenUnknown(t *testing.T) {
	if g := effectiveOutboundTarget(20, 0, 0); g != 20 {
		t.Fatalf("got %d want 20", g)
	}
}

func TestNormalizeMinOutbound(t *testing.T) {
	if g := normalizeMinOutbound(0); g != DefaultMinOutbound {
		t.Fatalf("got %d", g)
	}
	if g := normalizeMinOutbound(7); g != 7 {
		t.Fatalf("got %d", g)
	}
}
