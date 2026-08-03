package storage

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/host"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/names"
)

func PublishSignedProviderClaim(ctx context.Context, stack *Stack, h host.Host, key Key) error {
	if stack == nil || h == nil || key.IsZero() {
		return errors.New("provider claim requires host and content key")
	}
	if stack.DHT == nil {
		return nil
	}
	private := h.Peerstore().PrivKey(h.ID())
	if private == nil {
		return errors.New("provider identity private key unavailable")
	}
	privateRaw, err := private.Raw()
	if err != nil {
		return err
	}
	if len(privateRaw) != ed25519.PrivateKeySize {
		return fmt.Errorf("provider identity is not Ed25519: private key is %d bytes", len(privateRaw))
	}
	publicRaw, err := private.GetPublic().Raw()
	if err != nil || len(publicRaw) != ed25519.PublicKeySize {
		return errors.New("provider Ed25519 public key unavailable")
	}
	address := "local"
	if addrs := h.Addrs(); len(addrs) > 0 {
		address = addrs[0].String()
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	claim := &names.ProviderClaim{Version: names.FormatVersion, ContentKey: key[:], Provider: publicRaw, Address: address, Timestamp: time.Now().UnixNano(), Expires: time.Now().Add(48 * time.Hour).UnixNano(), Nonce: nonce}
	if err := claim.Sign(ed25519.PrivateKey(privateRaw)); err != nil {
		return err
	}
	raw, err := names.MarshalCanonical(claim)
	if err != nil {
		return err
	}
	return stack.DHT.PutValue(ctx, fmt.Sprintf("/providers/%x/%x", key[:], publicRaw), raw)
}
