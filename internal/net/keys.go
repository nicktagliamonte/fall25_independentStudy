// Purpose: Load or create a persistent libp2p private key on disk.

package net

import (
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io/fs"
	"os"

	"github.com/libp2p/go-libp2p/core/crypto"
)

const keyFileMode fs.FileMode = 0600

func LoadOrCreatePrivateKey(path string) (crypto.PrivKey, error) {
	if path == "" {
		// Fallback to ephemeral
		priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
		return priv, err
	}
	if b, err := os.ReadFile(path); err == nil {
		// Try PEM first
		if p, _ := pem.Decode(b); p != nil {
			return crypto.UnmarshalPrivateKey(p.Bytes)
		}
		// Or raw protobuf marshaled key
		return crypto.UnmarshalPrivateKey(b)
	}
	// Create new
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}
	raw, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	// Store as PEM for readability
	blk := &pem.Block{Type: "LIBP2P PRIVATE KEY", Bytes: raw}
	if err := os.WriteFile(path, pem.EncodeToMemory(blk), keyFileMode); err != nil {
		return nil, err
	}
	return priv, nil
}

var ErrNoKey = errors.New("no key")
