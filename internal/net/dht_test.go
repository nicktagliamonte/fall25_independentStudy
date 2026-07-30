// Purpose: Unit tests for DHT initialization and bootstrap.

package net

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestVersionedJSONValidatorSelectsNewestRecord(t *testing.T) {
	validator := versionedJSONValidator{}
	values := make([][]byte, 0, 3)
	for _, version := range []uint64{2, 9, 4} {
		value, err := json.Marshal(map[string]any{"version": version, "prefix": "task"})
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	selected, err := validator.Select("/pht/key", values)
	if err != nil {
		t.Fatal(err)
	}
	if selected != 1 {
		t.Fatalf("selected index = %d, want 1", selected)
	}
}

func TestTokenRecordValidatorSelectsNewestVersion(t *testing.T) {
	validator := tokenRecordValidator{}
	values := [][]byte{
		[]byte(`{"version":2,"timestamp":900,"locations":[]}`),
		[]byte(`{"version":4,"timestamp":100,"locations":[]}`),
		[]byte(`{"version":3,"timestamp":999,"locations":[]}`),
	}
	selected, err := validator.Select("/tokens/key", values)
	if err != nil {
		t.Fatal(err)
	}
	if selected != 1 {
		t.Fatalf("selected index = %d, want highest version at 1", selected)
	}
}

func TestVersionedJSONValidatorOrdersFencesBeforeLocalVersion(t *testing.T) {
	validator := versionedJSONValidator{}
	values := [][]byte{
		[]byte(`{"epoch":4,"writer":"owner-z","version":999}`),
		[]byte(`{"epoch":5,"writer":"owner-a","version":1}`),
	}
	selected, err := validator.Select("/pht/key", values)
	if err != nil {
		t.Fatal(err)
	}
	if selected != 1 {
		t.Fatalf("selected index = %d, want newer epoch at 1", selected)
	}
}

func TestVersionedJSONValidatorDeterministicallyFencesSameEpochWriters(t *testing.T) {
	validator := versionedJSONValidator{}
	values := [][]byte{
		[]byte(`{"epoch":7,"writer":"owner-a","version":100}`),
		[]byte(`{"epoch":7,"writer":"owner-z","version":1}`),
	}
	selected, err := validator.Select("/pht/key", values)
	if err != nil {
		t.Fatal(err)
	}
	if selected != 1 {
		t.Fatalf("selected index = %d, want deterministic writer winner at 1", selected)
	}
	reversed, err := validator.Select("/pht/key", [][]byte{values[1], values[0]})
	if err != nil {
		t.Fatal(err)
	}
	if reversed != 0 {
		t.Fatalf("reversed selected index = %d, want same writer winner at 0", reversed)
	}
}

func TestVersionedJSONValidatorRejectsMalformedRecord(t *testing.T) {
	validator := versionedJSONValidator{}
	if err := validator.Validate("/pht/key", []byte("not-json")); err == nil {
		t.Fatal("expected malformed PHT record to be rejected")
	}
}

func TestTokenDHTRoundTripsVersionedPHTRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	h1, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Close()
	h2, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()

	d1, err := NewDHT(ctx, h1, DHTConfig{
		Mode:               DHTModeServer,
		UseTokenDHT:        true,
		BootstrapPeersFunc: func() []peer.AddrInfo { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d1.Close()
	d2, err := NewDHT(ctx, h2, DHTConfig{
		Mode:        DHTModeServer,
		UseTokenDHT: true,
		BootstrapPeers: []peer.AddrInfo{{
			ID:    h1.ID(),
			Addrs: h1.Addrs(),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if err := h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}); err != nil {
		t.Fatal(err)
	}
	if err := d2.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 40 && d2.RoutingTable().Size() == 0; attempt++ {
		time.Sleep(50 * time.Millisecond)
	}
	if d2.RoutingTable().Size() == 0 {
		t.Fatal("second DHT did not discover bootstrap peer")
	}

	const key = "/pht/3/versioned-node"
	for _, version := range []uint64{1, 2} {
		value, err := json.Marshal(map[string]any{
			"version": version,
			"kind":    0,
			"prefix":  "task:",
			"entries": []string{"task:image:001"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := d2.PutValue(ctx, key, value); err != nil {
			t.Fatalf("put PHT version %d: %v", version, err)
		}
	}

	var got []byte
	for attempt := 0; attempt < 20; attempt++ {
		got, err = d1.GetValue(ctx, key)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("get PHT record: %v", err)
	}
	var record struct {
		Version uint64 `json:"version"`
	}
	if err := json.Unmarshal(got, &record); err != nil {
		t.Fatal(err)
	}
	if record.Version != 2 {
		t.Fatalf("PHT version = %d, want 2", record.Version)
	}
}

func TestNewDHT_ServerMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	defer h.Close()

	cfg := DHTConfig{
		Mode: DHTModeServer,
		BootstrapPeersFunc: func() []peer.AddrInfo {
			return nil
		},
	}
	d, err := NewDHT(ctx, h, cfg)
	if err != nil {
		t.Fatalf("NewDHT server mode: %v", err)
	}
	defer d.Close()

	if d == nil {
		t.Fatal("NewDHT returned nil DHT")
	}
}

func TestNewDHT_ClientMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	defer h.Close()

	cfg := DHTConfig{
		Mode: DHTModeClient,
		BootstrapPeersFunc: func() []peer.AddrInfo {
			return nil
		},
	}
	d, err := NewDHT(ctx, h, cfg)
	if err != nil {
		t.Fatalf("NewDHT client mode: %v", err)
	}
	defer d.Close()

	if d == nil {
		t.Fatal("NewDHT returned nil DHT")
	}
}

func TestNewDHT_ConfiguresBucketSize(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	defer h.Close()

	const bucketSize = 8
	d, err := NewDHT(ctx, h, DHTConfig{
		Mode:        DHTModeServer,
		UseTokenDHT: true,
		BucketSize:  bucketSize,
		BootstrapPeersFunc: func() []peer.AddrInfo {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewDHT with bucket size: %v", err)
	}
	defer d.Close()
	if got := d.BucketSize(); got != bucketSize {
		t.Fatalf("DHT bucket size = %d, want %d", got, bucketSize)
	}
}

func TestNewDHT_BootstrapPeersFunc(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := DHTConfig{
		Mode: DHTModeServer,
		BootstrapPeersFunc: func() []peer.AddrInfo {
			return []peer.AddrInfo{}
		},
	}

	h, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	defer h.Close()

	d, err := NewDHT(ctx, h, cfg)
	if err != nil {
		t.Fatalf("NewDHT with BootstrapPeersFunc: %v", err)
	}
	defer d.Close()

	if d == nil {
		t.Fatal("NewDHT returned nil DHT")
	}
}

func TestDefaultBootstrapPeerInfos(t *testing.T) {
	infos := DefaultBootstrapPeerInfos()
	if len(infos) == 0 {
		t.Error("DefaultBootstrapPeerInfos returned empty slice")
	}
	seen := make(map[peer.ID]struct{})
	for _, info := range infos {
		if info.ID == "" {
			t.Errorf("parsed AddrInfo has empty ID")
		}
		if _, ok := seen[info.ID]; ok {
			t.Errorf("duplicate peer ID %s", info.ID)
		}
		seen[info.ID] = struct{}{}
	}
}

func TestParseBootstrapAddrs(t *testing.T) {
	addrs := []string{
		"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
		"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
		"",
		"/invalid",
	}
	out := parseBootstrapAddrs(addrs)
	if len(out) != 1 {
		t.Errorf("expected 1 valid AddrInfo (dedup + skip empty + skip invalid), got %d", len(out))
	}
}
