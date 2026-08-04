package storage

import (
	"context"
	"testing"

	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
)

func TestReceivedProviderClaimBindsTransferPeerAndContent(t *testing.T) {
	ctx := context.Background()
	provider, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	other, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	key := KeyFromData([]byte("durable replica"))
	raw, err := BuildSignedProviderClaim(provider, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := PublishReceivedProviderClaim(ctx, &Stack{}, provider.ID(), key, raw); err != nil {
		t.Fatalf("valid acknowledged claim: %v", err)
	}
	if err := PublishReceivedProviderClaim(ctx, &Stack{}, other.ID(), key, raw); err == nil {
		t.Fatal("claim signed by a different transfer peer was accepted")
	}
	if err := PublishReceivedProviderClaim(ctx, &Stack{}, provider.ID(), KeyFromData([]byte("other content")), raw); err == nil {
		t.Fatal("claim for a different content key was accepted")
	}

	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-1] ^= 1
	if err := PublishReceivedProviderClaim(ctx, &Stack{}, provider.ID(), key, tampered); err == nil {
		t.Fatal("tampered provider claim was accepted")
	}
}
