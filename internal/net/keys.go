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

// LoadOrCreatePrivateKey loads a libp2p Ed25519 private key from path, creating
// and persisting a new one if the file does not exist. Keys are stored as
// PEM-encoded protobuf-marshaled bytes (type "LIBP2P PRIVATE KEY") with file
// mode 0600. If path is empty, an ephemeral key is generated and not persisted.
// The file is also tolerant of raw (non-PEM) protobuf-marshaled key bytes.
//
// Parameters:
//   - path (string): filesystem path to read/write the key; "" for an ephemeral, unpersisted key.
//
// Returns:
//   - crypto.PrivKey: the loaded or newly generated private key.
//   - error: non-nil if key generation, decoding, or file I/O fails.
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

// ErrNoKey indicates no private key was available where one was required.
var ErrNoKey = errors.New("no key")
