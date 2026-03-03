// Purpose: Tests for CymruASNResolver.

package net

import (
	"context"
	"net"
	"testing"
)

func TestCymruASNResolver_PrivateAndLoopback(t *testing.T) {
	ctx := context.Background()
	r := NewCymruASNResolver()

	for _, ip := range []net.IP{
		net.ParseIP("127.0.0.1"),
		net.ParseIP("::1"),
		net.ParseIP("10.0.0.1"),
		net.ParseIP("192.168.1.1"),
		net.ParseIP("172.16.0.1"),
		net.ParseIP("169.254.1.1"),
	} {
		asn, ok := r.ResolveASN(ctx, ip)
		if ok || asn != 0 {
			t.Errorf("private/loopback %s: expected (0, false), got (%d, %v)", ip, asn, ok)
		}
	}
}
