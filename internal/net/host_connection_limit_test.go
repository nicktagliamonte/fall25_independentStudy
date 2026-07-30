package net

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestHostConnectionLimitsTrimExcessPeers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	limited, err := NewHostWithConnectionLimits(
		ctx,
		[]string{"/ip4/127.0.0.1/tcp/0"},
		2,
		4,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()

	targets := make([]peer.AddrInfo, 0, 8)
	for index := 0; index < 8; index++ {
		target, err := NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
		if err != nil {
			t.Fatal(err)
		}
		defer target.Close()
		targets = append(targets, peer.AddrInfo{
			ID:    target.ID(),
			Addrs: target.Addrs(),
		})
	}
	for _, target := range targets {
		if err := limited.Connect(ctx, target); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if peers := len(limited.Network().Peers()); peers <= 2 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf(
		"limited host retained %d peers, want at most low watermark 2",
		len(limited.Network().Peers()),
	)
}

func TestHostConnectionLimitsRejectInvalidWatermarks(t *testing.T) {
	ctx := context.Background()
	if _, err := NewHostWithConnectionLimits(
		ctx,
		[]string{"/ip4/127.0.0.1/tcp/0"},
		0,
		4,
	); err == nil {
		t.Fatal("zero low watermark accepted")
	}
	if _, err := NewHostWithConnectionLimits(
		ctx,
		[]string{"/ip4/127.0.0.1/tcp/0"},
		5,
		4,
	); err == nil {
		t.Fatal("high watermark below low watermark accepted")
	}
}
