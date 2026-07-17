package node

import (
	"context"
	"testing"
	"time"

	blocks "github.com/ipfs/go-block-format"
)

// TestRestoreFromManifest_PartialSuccessAndTimeout starts an embedded
// Service on ephemeral loopback ports (MinOutbound: 0, so the dial loop
// stays idle), stores two blocks locally via PutRaw ("alpha" and "beta") to
// get real, fetchable CIDs, then calls RestoreFromManifest with a manifest
// of four entries designed to exercise every outcome path in one call:
//   - cid1, cid2: valid CIDs already present locally -> expected to succeed
//     (fetched via the local blockstore short-circuit, no network needed).
//   - missingCID: a well-formed CID (computed locally from bytes that were
//     never stored) that is NOT present anywhere reachable -> expected to
//     fail once its 500ms per-item timeout elapses, exercising the
//     fetch-failure/timeout path.
//   - invalidCID: the literal string "not-a-cid", which fails cid.Decode
//     -> expected to fail immediately via the decode-error path (no
//     network I/O, no timeout wait).
//
// It asserts stats.OK >= 2 (the two local blocks), stats.Failed >= 2 (the
// missing + invalid entries), and stats.Bytes > 0 (some bytes were actually
// counted from the successful fetches). The call is made with concurrency 2
// and a 500ms per-item timeout, and the returned error is deliberately
// ignored (RestoreFromManifest's error return only reflects ctx.Err(), not
// individual item failures, which are asserted via stats instead).
func TestRestoreFromManifest_PartialSuccessAndTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Start embedded service on ephemeral ports
	svc, err := Start(ctx, Options{
		ListenMultiaddrs: []string{
			"/ip4/127.0.0.1/tcp/0",
			"/ip4/127.0.0.1/udp/0/quic-v1",
		},
		MinOutbound:    0,
		PerIPDialLimit: 1,
		DialTimeout:    2 * time.Second,
	})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_ = svc.Close(cctx)
	})

	// Put two local blocks; capture their CIDs
	cid1, _, err := svc.PutRaw(ctx, []byte("alpha"))
	if err != nil {
		t.Fatalf("put alpha: %v", err)
	}
	cid2, _, err := svc.PutRaw(ctx, []byte("beta"))
	if err != nil {
		t.Fatalf("put beta: %v", err)
	}

	// Craft a valid-but-missing CID to exercise timeout/failure.
	// Build a block locally but do NOT store it; use its CID.
	missingBlock := blocks.NewBlock([]byte("this-is-missing"))
	missingCID := missingBlock.Cid().String()

	// Also include an invalid CID string to exercise decode failure path
	invalidCID := "not-a-cid"

	cids := []string{cid1, cid2, missingCID, invalidCID}

	stats, _ := svc.RestoreFromManifest(ctx, cids, 2, 500*time.Millisecond, 0)
	if stats.OK < 2 {
		t.Fatalf("expected at least 2 OK (locals), got %d", stats.OK)
	}
	if stats.Failed < 2 {
		t.Fatalf("expected at least 2 failures (missing+invalid), got %d", stats.Failed)
	}
	if stats.Bytes <= 0 {
		t.Fatalf("expected positive restored bytes, got %d", stats.Bytes)
	}
}
