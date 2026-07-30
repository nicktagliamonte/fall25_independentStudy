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

func TestConnectionWatermarks(t *testing.T) {
	for _, test := range []struct {
		name            string
		minOutbound     int
		maxConnections  int
		clusterSize     int
		wantLow, wantHi int
	}{
		{
			name:    "defaults",
			wantLow: DefaultMinOutbound, wantHi: DefaultMaxConnections,
		},
		{
			name: "campaign bounds", minOutbound: 3, maxConnections: 8,
			clusterSize: 100, wantLow: 3, wantHi: 8,
		},
		{
			name: "small cluster cap", minOutbound: 20, maxConnections: 32,
			clusterSize: 4, wantLow: 3, wantHi: 3,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			low, high, err := connectionWatermarks(
				test.minOutbound,
				test.maxConnections,
				test.clusterSize,
			)
			if err != nil {
				t.Fatal(err)
			}
			if low != test.wantLow || high != test.wantHi {
				t.Fatalf(
					"watermarks = %d/%d, want %d/%d",
					low,
					high,
					test.wantLow,
					test.wantHi,
				)
			}
		})
	}
}

func TestConnectionWatermarksRejectMaximumBelowMinimum(t *testing.T) {
	if _, _, err := connectionWatermarks(9, 8, 100); err == nil {
		t.Fatal("maximum below minimum accepted")
	}
}
