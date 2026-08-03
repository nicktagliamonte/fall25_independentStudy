package net

import (
	"context"
	"testing"
)

func TestExplicitAdvertisedAddressesReplaceListeners(t *testing.T) {
	h, err := NewHostWithConnectionLimitsAndAdvertise(context.Background(), []string{"/ip4/127.0.0.1/tcp/0"}, []string{"/ip4/192.0.2.10/tcp/4001"}, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if len(h.Addrs()) != 1 || h.Addrs()[0].String() != "/ip4/192.0.2.10/tcp/4001" {
		t.Fatalf("advertised addresses = %v", h.Addrs())
	}
}
