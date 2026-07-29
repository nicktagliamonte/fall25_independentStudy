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

func TestVersionedJSONValidatorRejectsMalformedRecord(t *testing.T) {
	validator := versionedJSONValidator{}
	if err := validator.Validate("/pht/key", []byte("not-json")); err == nil {
		t.Fatal("expected malformed PHT record to be rejected")
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
