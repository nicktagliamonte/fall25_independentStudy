package storage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/names"
)

func PublishSignedProviderClaim(ctx context.Context, stack *Stack, h host.Host, key Key) error {
	if stack == nil {
		return errors.New("provider claim requires storage stack")
	}
	raw, err := BuildSignedProviderClaim(h, key)
	if err != nil {
		return err
	}
	if stack.DHT == nil {
		return nil
	}
	var claim names.ProviderClaim
	if err := names.UnmarshalCanonical(raw, &claim); err != nil {
		return err
	}
	claimKey := fmt.Sprintf("/providers/%x/%x", key[:], claim.Provider)
	if err := stack.DHT.PutValue(ctx, claimKey, raw); err != nil {
		return err
	}
	stack.CacheProviderClaim(claimKey, raw)
	return nil
}

// BuildSignedProviderClaim creates the durable-storage attestation returned by
// a repair receiver. Building and signing are local; a coordinator may publish
// the resulting canonical record so a receiver with a sparse DHT routing table
// is not placed on the publication critical path.
func BuildSignedProviderClaim(h host.Host, key Key) ([]byte, error) {
	if h == nil || key.IsZero() {
		return nil, errors.New("provider claim requires host and content key")
	}
	private := h.Peerstore().PrivKey(h.ID())
	if private == nil {
		return nil, errors.New("provider identity private key unavailable")
	}
	privateRaw, err := private.Raw()
	if err != nil {
		return nil, err
	}
	if len(privateRaw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("provider identity is not Ed25519: private key is %d bytes", len(privateRaw))
	}
	publicRaw, err := private.GetPublic().Raw()
	if err != nil || len(publicRaw) != ed25519.PublicKeySize {
		return nil, errors.New("provider Ed25519 public key unavailable")
	}
	address := "local"
	if addrs := h.Addrs(); len(addrs) > 0 {
		address = addrs[0].String()
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	claim := &names.ProviderClaim{Version: names.FormatVersion, ContentKey: key[:], Provider: publicRaw, Address: address, Timestamp: time.Now().UnixNano(), Expires: time.Now().Add(48 * time.Hour).UnixNano(), Nonce: nonce}
	if err := claim.Sign(ed25519.PrivateKey(privateRaw)); err != nil {
		return nil, err
	}
	raw, err := names.MarshalCanonical(claim)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// PublishReceivedProviderClaim validates that raw is a canonical claim signed
// by expectedProvider for key, then publishes it through the coordinator's DHT
// connection. The receiver cannot be impersonated: its libp2p Ed25519 key must
// match the signing key embedded in the claim.
func PublishReceivedProviderClaim(ctx context.Context, stack *Stack, expectedProvider peer.ID, key Key, raw []byte) error {
	if stack == nil || key.IsZero() || expectedProvider == "" {
		return errors.New("received provider claim requires stack, provider, and content key")
	}
	public, err := expectedProvider.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("extract provider public key: %w", err)
	}
	publicRaw, err := public.Raw()
	if err != nil {
		return fmt.Errorf("marshal provider public key: %w", err)
	}
	var claim names.ProviderClaim
	if err := names.UnmarshalCanonical(raw, &claim); err != nil {
		return fmt.Errorf("decode provider claim: %w", err)
	}
	if !bytes.Equal(claim.ContentKey, key[:]) || !bytes.Equal(claim.Provider, publicRaw) {
		return errors.New("provider claim does not match acknowledged transfer")
	}
	claimKey := fmt.Sprintf("/providers/%x/%x", key[:], publicRaw)
	if err := (&names.ProviderClaimValidator{}).Validate(claimKey, raw); err != nil {
		return fmt.Errorf("validate provider claim: %w", err)
	}
	if stack.DHT == nil {
		stack.CacheProviderClaim(claimKey, raw)
		return nil
	}
	if err := stack.DHT.PutValue(ctx, claimKey, raw); err != nil {
		return err
	}
	stack.CacheProviderClaim(claimKey, raw)
	return nil
}
