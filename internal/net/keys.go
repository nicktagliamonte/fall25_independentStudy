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

// keyFileMode is the file permission mode used when a new private key file is written
// (owner read/write only).
const keyFileMode fs.FileMode = 0600

// LoadOrCreatePrivateKey loads a libp2p Ed25519 private key from path, creating and
// persisting a new one if the file does not already exist. Behavior by path:
//
//   - path == "": no file is touched; a new ephemeral Ed25519 key is generated (via
//     crypto/rand) and returned. This key is not persisted, so the resulting PeerID
//     will differ on every call.
//   - path exists and is readable: the file contents are parsed first as a PEM block
//     (type "LIBP2P PRIVATE KEY") and, if that succeeds, the PEM payload is unmarshaled
//     as a libp2p private key; if PEM decoding fails (pem.Decode returns nil, e.g. the
//     file is raw bytes rather than PEM-wrapped), the raw file bytes are unmarshaled
//     directly as a libp2p-serialized (protobuf) private key instead. This keeps a
//     stable PeerID across restarts.
//   - path does not exist (or is unreadable — see note below): a new Ed25519 key is
//     generated, marshaled, PEM-encoded, and written to path with keyFileMode (0600)
//     permissions, then returned.
//
// Note: os.ReadFile errors other than "file does not exist" (e.g. permission denied)
// are not distinguished from "does not exist" — any read error falls through to the
// key-creation branch, which will then attempt to overwrite path.
//
// Returns the loaded or newly created crypto.PrivKey, or a non-nil error if key
// generation, unmarshaling, marshaling, or writing the key file fails.
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

// ErrNoKey is a sentinel error available for callers that need to signal "no key
// present/configured". It is currently unused within this package (LoadOrCreatePrivateKey
// never returns it) but is exported for use by other packages.
var ErrNoKey = errors.New("no key")
