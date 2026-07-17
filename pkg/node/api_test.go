package node

import (
	"context"
	"testing"
	"time"
)

// TestStartClose is a smoke test verifying the basic embed-API lifecycle:
// Start succeeds with ephemeral loopback listen addresses and MinOutbound
// disabled (0, to avoid the dial-maintenance loop trying to reach real
// peers during the test), and the returned Service's Close (registered via
// t.Cleanup, given its own 5-second timeout) completes without error. It
// does not exercise any data-plane methods (PutRaw, GetRawFrom, etc.) or
// peer connectivity — see TestRestoreFromManifest_PartialSuccessAndTimeout
// in restore_test.go for a test that exercises PutRaw/RestoreFromManifest.
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
