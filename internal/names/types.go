// Package names implements the signed mutable-name and policy plane.
package names

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	FormatVersion       = 1
	NameRecordDomain    = "tarsus-name-record-v1\x00"
	CapabilityDomain    = "tarsus-capability-v1\x00"
	ProviderClaimDomain = "tarsus-provider-claim-v1\x00"
	LeaseDomain         = "tarsus-lease-v1\x00"
	ManifestDomain      = "tarsus-object-manifest-v1\x00"
	nameIDDomain        = "tarsus-name-id-v1\x00"
	maxPathBytes        = 4096
)

type NamespaceID [32]byte
type NameID [32]byte
type ContentKey [32]byte

func NewNamespaceID() (NamespaceID, error) {
	var id NamespaceID
	_, err := rand.Read(id[:])
	return id, err
}

func ParseNamespaceID(value string) (NamespaceID, error) {
	var id NamespaceID
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != len(id) {
		return id, errors.New("namespace id must be 64 lower-case hexadecimal characters")
	}
	if value != strings.ToLower(value) {
		return id, errors.New("namespace id must use lower-case hexadecimal")
	}
	copy(id[:], b)
	return id, nil
}

func ParseNameID(value string) (NameID, error) {
	var id NameID
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != len(id) {
		return id, errors.New("name id must be 64 lower-case hexadecimal characters")
	}
	if value != strings.ToLower(value) {
		return id, errors.New("name id must use lower-case hexadecimal")
	}
	copy(id[:], b)
	return id, nil
}

func (id NamespaceID) String() string { return hex.EncodeToString(id[:]) }
func (id NameID) String() string      { return hex.EncodeToString(id[:]) }
func (key ContentKey) String() string { return hex.EncodeToString(key[:]) }

func NormalizePath(value string) (string, error) {
	if value == "" || !strings.HasPrefix(value, "/") {
		return "", errors.New("name path must be absolute")
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return "", errors.New("name path must be valid UTF-8 without NUL")
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return "", errors.New("name path may not contain '..'")
		}
	}
	normalized := path.Clean(value)
	if len(normalized) > maxPathBytes {
		return "", fmt.Errorf("name path exceeds %d bytes", maxPathBytes)
	}
	return normalized, nil
}

func DeriveNameID(namespace NamespaceID, normalizedPath string) NameID {
	h := sha256.New()
	_, _ = h.Write([]byte(nameIDDomain))
	_, _ = h.Write(namespace[:])
	_, _ = h.Write([]byte(normalizedPath))
	var id NameID
	copy(id[:], h.Sum(nil))
	return id
}

func DHTNameKey(id NameID) string { return "/names/" + id.String() }

func ParseTarsusURI(value string) (NamespaceID, string, NameID, error) {
	var zeroNS NamespaceID
	var zeroName NameID
	const prefix = "tarsus://"
	if !strings.HasPrefix(value, prefix) {
		return zeroNS, "", zeroName, errors.New("URI must begin with tarsus://")
	}
	rest := strings.TrimPrefix(value, prefix)
	cut := strings.IndexByte(rest, '/')
	if cut < 0 {
		return zeroNS, "", zeroName, errors.New("URI must include an absolute path")
	}
	ns, err := ParseNamespaceID(rest[:cut])
	if err != nil {
		return zeroNS, "", zeroName, err
	}
	p, err := NormalizePath(rest[cut:])
	if err != nil {
		return zeroNS, "", zeroName, err
	}
	return ns, p, DeriveNameID(ns, p), nil
}

type PlacementPolicy struct {
	Near   uint16 `json:"near" cbor:"near"`
	Middle uint16 `json:"middle" cbor:"middle"`
	Far    uint16 `json:"far" cbor:"far"`
}

type ObjectPolicy struct {
	Replicas        uint16          `json:"replicas" cbor:"replicas"`
	Placement       PlacementPolicy `json:"placement" cbor:"placement"`
	StrictPublish   bool            `json:"strict_publish" cbor:"strict_publish"`
	Encryption      string          `json:"encryption" cbor:"encryption"`
	KeyEpoch        uint64          `json:"key_epoch" cbor:"key_epoch"`
	RetainVersions  uint16          `json:"retain_versions" cbor:"retain_versions"`
	CollectionGrace int64           `json:"collection_grace_ns" cbor:"collection_grace_ns"`
	Searchable      bool            `json:"searchable" cbor:"searchable"`
}

func DefaultPolicy() ObjectPolicy {
	return ObjectPolicy{
		Replicas: 7, Placement: PlacementPolicy{Near: 3, Middle: 2, Far: 2},
		StrictPublish: true, Encryption: "private", RetainVersions: 3,
		CollectionGrace: int64(24 * 60 * 60 * 1e9), Searchable: true,
	}
}

func (p ObjectPolicy) Validate() error {
	if p.Replicas < 1 || p.Replicas > 32 {
		return errors.New("replicas must be between 1 and 32")
	}
	if uint32(p.Placement.Near)+uint32(p.Placement.Middle)+uint32(p.Placement.Far) != uint32(p.Replicas) {
		return errors.New("placement counts must sum to replicas")
	}
	if p.Encryption != "private" && p.Encryption != "public" {
		return errors.New("encryption must be private or public")
	}
	if p.RetainVersions < 1 || p.RetainVersions > 64 {
		return errors.New("retain_versions must be between 1 and 64")
	}
	if p.CollectionGrace < 0 || p.CollectionGrace > int64(30*24*60*60*1e9) {
		return errors.New("collection grace must be between zero and 30 days")
	}
	return nil
}
