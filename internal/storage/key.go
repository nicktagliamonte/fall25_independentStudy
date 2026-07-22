// Purpose: Key type representing hash(data) for key-based storage.
// Key is SHA256 hash of data only (no provider ID in hash).

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Key represents a hash of data (SHA256). Used as primary identifier for storage.
// Per newReqs.txt: key = hash(data) only, with provider ID attached separately.
type Key [32]byte

// KeyFromData generates a Key from raw data by computing SHA256 hash.
// This is the primary way to create keys. Provider ID is NOT included in hash.
//
// IMPORTANT: This function ONLY hashes the data. ProviderID must be stored separately
// (e.g., in routing table or Key→ProviderID mapping). ProviderID is never included
// in the key hash calculation per newReqs.txt requirement.
//
// Parameters:
//   - data ([]byte): the raw payload bytes to hash.
//
// Returns:
//   - Key: the SHA256 digest of data, as a 32-byte Key.
func KeyFromData(data []byte) Key {
	// Hash only the data - ProviderID is NOT included
	return sha256.Sum256(data)
}

// String returns the hexadecimal representation of the key.
//
// Returns:
//   - string: the 64-character lowercase hex encoding of the key bytes.
func (k Key) String() string {
	return hex.EncodeToString(k[:])
}

// Bytes returns the key as a byte slice.
//
// Returns:
//   - []byte: a slice view of the underlying 32-byte array.
func (k Key) Bytes() []byte {
	return k[:]
}

// IsZero returns true if the key is the zero value (all zeros).
//
// Returns:
//   - bool: true if every byte of the key is zero.
func (k Key) IsZero() bool {
	return k == Key{}
}

// Equal returns true if two keys are equal.
//
// Parameters:
//   - other (Key): the key to compare against.
//
// Returns:
//   - bool: true if k and other have identical bytes.
func (k Key) Equal(other Key) bool {
	return k == other
}

// ParseKey parses a hexadecimal string into a Key.
// Returns error if the string is not a valid 64-character hex string.
//
// Parameters:
//   - s (string): the candidate hex string; must be exactly 64 characters.
//
// Returns:
//   - Key: the parsed key (zero value on error).
//   - error: non-nil if s is not 64 hex characters or fails to decode to 32 bytes.
func ParseKey(s string) (Key, error) {
	if len(s) != 64 {
		return Key{}, fmt.Errorf("invalid key length: want 64 hex chars, got %d", len(s))
	}
	bytes, err := hex.DecodeString(s)
	if err != nil {
		return Key{}, fmt.Errorf("invalid hex string: %w", err)
	}
	if len(bytes) != 32 {
		return Key{}, fmt.Errorf("invalid key length: want 32 bytes, got %d", len(bytes))
	}
	var k Key
	copy(k[:], bytes)
	return k, nil
}
