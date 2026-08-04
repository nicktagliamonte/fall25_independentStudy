package names

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxDirectoryChildren = 4096

type Permissions uint8

const (
	PermissionWrite Permissions = 1 << iota
	PermissionDelete
	PermissionAdmin
)

type Capability struct {
	Version     uint64      `json:"version" cbor:"version"`
	Namespace   []byte      `json:"namespace" cbor:"namespace"`
	PathPrefix  string      `json:"path_prefix" cbor:"path_prefix"`
	Delegate    []byte      `json:"delegate" cbor:"delegate"`
	Permissions Permissions `json:"permissions" cbor:"permissions"`
	NotBefore   int64       `json:"not_before_ns" cbor:"not_before_ns"`
	Expires     int64       `json:"expires_ns" cbor:"expires_ns"`
	Nonce       []byte      `json:"nonce" cbor:"nonce"`
	Signature   []byte      `json:"signature,omitempty" cbor:"signature,omitempty"`
}

type NameRecord struct {
	Version           uint64       `json:"version" cbor:"version"`
	Namespace         []byte       `json:"namespace" cbor:"namespace"`
	NameID            []byte       `json:"name_id" cbor:"name_id"`
	Path              string       `json:"path" cbor:"path"`
	Kind              string       `json:"kind" cbor:"kind"`
	Generation        uint64       `json:"generation" cbor:"generation"`
	PreviousHash      []byte       `json:"previous_hash,omitempty" cbor:"previous_hash,omitempty"`
	ManifestKey       []byte       `json:"manifest_key,omitempty" cbor:"manifest_key,omitempty"`
	Tombstone         bool         `json:"tombstone" cbor:"tombstone"`
	Owner             []byte       `json:"owner" cbor:"owner"`
	Capability        *Capability  `json:"capability,omitempty" cbor:"capability,omitempty"`
	Policy            ObjectPolicy `json:"policy" cbor:"policy"`
	DirectoryChildren [][]byte     `json:"directory_children,omitempty" cbor:"directory_children,omitempty"`
	Signer            []byte       `json:"signer" cbor:"signer"`
	Timestamp         int64        `json:"timestamp_ns" cbor:"timestamp_ns"`
	Nonce             []byte       `json:"nonce" cbor:"nonce"`
	Signature         []byte       `json:"signature,omitempty" cbor:"signature,omitempty"`
}

func signingBytes(domain string, value any) ([]byte, error) {
	raw, err := MarshalCanonical(value)
	if err != nil {
		return nil, err
	}
	return append([]byte(domain), raw...), nil
}

func (c *Capability) unsigned() Capability {
	copyValue := *c
	copyValue.Signature = nil
	return copyValue
}

func (c *Capability) Sign(owner ed25519.PrivateKey) error {
	if len(owner) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 owner private key")
	}
	payload, err := signingBytes(CapabilityDomain, c.unsigned())
	if err != nil {
		return err
	}
	c.Signature = ed25519.Sign(owner, payload)
	return nil
}

func (c *Capability) Validate(owner ed25519.PublicKey, now time.Time) error {
	if c == nil || c.Version != FormatVersion || len(c.Namespace) != 32 || len(c.Delegate) != ed25519.PublicKeySize || len(c.Nonce) < 16 {
		return errors.New("invalid capability schema")
	}
	prefix, err := NormalizePath(c.PathPrefix)
	if err != nil || prefix != c.PathPrefix {
		return errors.New("invalid capability path prefix")
	}
	if c.Permissions == 0 || c.Permissions&^(PermissionWrite|PermissionDelete|PermissionAdmin) != 0 {
		return errors.New("invalid capability permissions")
	}
	if c.NotBefore > now.UnixNano() || c.Expires <= now.UnixNano() || c.Expires <= c.NotBefore {
		return errors.New("capability is not currently valid")
	}
	payload, err := signingBytes(CapabilityDomain, c.unsigned())
	if err != nil {
		return err
	}
	if len(c.Signature) != ed25519.SignatureSize || !ed25519.Verify(owner, payload, c.Signature) {
		return errors.New("invalid capability signature")
	}
	return nil
}

func (r *NameRecord) unsigned() NameRecord {
	copyValue := *r
	copyValue.Signature = nil
	return copyValue
}

func (r *NameRecord) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 private key")
	}
	r.Signer = append(r.Signer[:0], privateKey.Public().(ed25519.PublicKey)...)
	payload, err := signingBytes(NameRecordDomain, r.unsigned())
	if err != nil {
		return err
	}
	r.Signature = ed25519.Sign(privateKey, payload)
	return nil
}

func (r *NameRecord) RequiredPermission(previous *NameRecord) Permissions {
	if r.Tombstone {
		return PermissionDelete
	}
	if previous != nil && r.Policy != previous.Policy {
		return PermissionAdmin
	}
	return PermissionWrite
}

func (r *NameRecord) Validate(now time.Time, previous *NameRecord) error {
	if err := validateRecordEnvelope(r, now); err != nil {
		return err
	}
	if previous == nil {
		if r.Generation != 0 || len(r.PreviousHash) != 0 {
			return errors.New("generation zero must not have a predecessor")
		}
	} else {
		if r.Generation != previous.Generation+1 || len(r.PreviousHash) != 32 {
			return errors.New("generation must increment once and name a predecessor")
		}
		previousHash, err := previous.Hash()
		if err != nil || !bytes.Equal(previousHash[:], r.PreviousHash) {
			return errors.New("previous record hash mismatch")
		}
		if !bytes.Equal(previous.Namespace, r.Namespace) || !bytes.Equal(previous.NameID, r.NameID) || previous.Path != r.Path || !bytes.Equal(previous.Owner, r.Owner) {
			return errors.New("immutable name identity fields changed")
		}
	}
	if !bytes.Equal(r.Signer, r.Owner) {
		required := r.RequiredPermission(previous)
		if r.Capability.Permissions&required != required {
			return errors.New("delegated capability lacks required permission")
		}
	}
	return nil
}

// ValidateEnvelope verifies the self-contained, signed portion of a name
// record without requiring its predecessor. DHT validators and publication
// preflight use this; expected-generation CAS still performs full Validate.
func (r *NameRecord) ValidateEnvelope(now time.Time) error {
	return validateRecordEnvelope(r, now)
}

// validateRecordEnvelope verifies the self-contained portions of a record.
// It is used by DHT validators, which deliberately cannot decide CAS or
// predecessor relationships; those checks remain at the fenced exact owner.
func validateRecordEnvelope(r *NameRecord, now time.Time) error {
	if r == nil || r.Version != FormatVersion {
		return errors.New("unsupported name-record version")
	}
	if len(r.Namespace) != 32 || len(r.NameID) != 32 || len(r.Owner) != ed25519.PublicKeySize || len(r.Signer) != ed25519.PublicKeySize {
		return errors.New("invalid name-record identifier or key length")
	}
	normalized, err := NormalizePath(r.Path)
	if err != nil || normalized != r.Path {
		return errors.New("name-record path is not normalized")
	}
	var ns NamespaceID
	copy(ns[:], r.Namespace)
	expectedID := DeriveNameID(ns, r.Path)
	if !bytes.Equal(expectedID[:], r.NameID) {
		return errors.New("name_id does not match namespace and path")
	}
	if r.Kind != "file" && r.Kind != "directory" {
		return errors.New("kind must be file or directory")
	}
	if r.Tombstone {
		if len(r.ManifestKey) != 0 || len(r.DirectoryChildren) != 0 {
			return errors.New("tombstone may not reference content")
		}
	} else if r.Kind == "file" && len(r.ManifestKey) != 32 {
		return errors.New("live file requires a 32-byte manifest key")
	} else if r.Kind == "directory" && len(r.ManifestKey) != 0 {
		return errors.New("directory membership is stored as child NameIDs, not a content manifest")
	}
	if r.Kind != "directory" && len(r.DirectoryChildren) != 0 {
		return errors.New("only directories may list child NameIDs")
	}
	if len(r.DirectoryChildren) > maxDirectoryChildren {
		return fmt.Errorf("directory exceeds %d direct children", maxDirectoryChildren)
	}
	for index, child := range r.DirectoryChildren {
		if len(child) != 32 {
			return errors.New("directory child must be a NameID")
		}
		if bytes.Equal(child, r.NameID) {
			return errors.New("directory may not contain itself")
		}
		if index > 0 && bytes.Compare(r.DirectoryChildren[index-1], child) >= 0 {
			return errors.New("directory child NameIDs must be unique and sorted")
		}
	}
	if err := r.Policy.Validate(); err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	if len(r.Nonce) < 16 || r.Timestamp <= 0 || r.Timestamp > now.Add(10*time.Minute).UnixNano() {
		return errors.New("invalid timestamp or nonce")
	}
	if r.Generation == 0 && len(r.PreviousHash) != 0 {
		return errors.New("generation zero must not have a predecessor")
	}
	if r.Generation > 0 && len(r.PreviousHash) != 32 {
		return errors.New("later generation requires a predecessor hash")
	}
	payload, err := signingBytes(NameRecordDomain, r.unsigned())
	if err != nil {
		return err
	}
	if len(r.Signature) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(r.Signer), payload, r.Signature) {
		return errors.New("invalid name-record signature")
	}
	if bytes.Equal(r.Signer, r.Owner) {
		return nil
	}
	if r.Capability == nil || !bytes.Equal(r.Capability.Namespace, r.Namespace) || !bytes.Equal(r.Capability.Delegate, r.Signer) {
		return errors.New("signer is neither owner nor matching delegate")
	}
	if err := r.Capability.Validate(ed25519.PublicKey(r.Owner), now); err != nil {
		return err
	}
	if !pathWithinPrefix(r.Path, r.Capability.PathPrefix) {
		return errors.New("name path is outside delegated prefix")
	}
	if r.Capability.Permissions&(PermissionWrite|PermissionDelete|PermissionAdmin) == 0 {
		return errors.New("delegated capability grants no applicable permission")
	}
	return nil
}

func pathWithinPrefix(value, prefix string) bool {
	return value == prefix || prefix == "/" || strings.HasPrefix(value, strings.TrimSuffix(prefix, "/")+"/")
}

func (r *NameRecord) Marshal() ([]byte, error) { return MarshalCanonical(r) }

func DecodeNameRecord(raw []byte) (*NameRecord, error) {
	var record NameRecord
	if err := UnmarshalCanonical(raw, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (r *NameRecord) Hash() ([32]byte, error) {
	raw, err := r.Marshal()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}
