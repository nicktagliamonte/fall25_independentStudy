package node

import (
	"context"
	"testing"
	"time"
)

func TestStartClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := Options{
		ListenMultiaddrs: []string{
			"/ip4/127.0.0.1/tcp/0",
			"/ip4/127.0.0.1/udp/0/quic-v1",
		},
		MinOutbound:    0, // avoid autodialing in test
		PerIPDialLimit: 1,
		DialTimeout:    2 * time.Second,
	}
	svc, err := Start(ctx, opts)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		if cerr := svc.Close(cctx); cerr != nil {
			t.Fatalf("Close failed: %v", cerr)
		}
	})
}
