package names

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"

	recordvalidator "github.com/libp2p/go-libp2p-record"
	"github.com/libp2p/go-libp2p/core/routing"
)

var _ recordvalidator.Validator = (*NameValidator)(nil)

type NameValidator struct {
	Now func() time.Time
}

func (v *NameValidator) now() time.Time {
	if v != nil && v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *NameValidator) Validate(key string, value []byte) error {
	record, err := DecodeNameRecord(value)
	if err != nil {
		return err
	}
	if err := validateRecordEnvelope(record, v.now()); err != nil {
		return err
	}
	if key != DHTNameKey(bytesToNameID(record.NameID)) {
		return errors.New("DHT name key does not match derived NameID")
	}
	return nil
}

func (v *NameValidator) Select(key string, values [][]byte) (int, error) {
	selected := -1
	var current *NameRecord
	var currentRaw []byte
	for i, raw := range values {
		if err := v.Validate(key, raw); err != nil {
			continue
		}
		candidate, _ := DecodeNameRecord(raw)
		if selected < 0 || candidate.Generation > current.Generation {
			selected, current, currentRaw = i, candidate, raw
			continue
		}
		if candidate.Generation == current.Generation && !bytes.Equal(raw, currentRaw) {
			return -1, errors.New("equal-generation name-record fork")
		}
	}
	if selected < 0 {
		return -1, routing.ErrNotFound
	}
	return selected, nil
}

func bytesToNameID(value []byte) NameID {
	var id NameID
	copy(id[:], value)
	return id
}

type ProviderClaim struct {
	Version    uint64 `json:"version" cbor:"version"`
	ContentKey []byte `json:"content_key" cbor:"content_key"`
	Provider   []byte `json:"provider" cbor:"provider"`
	Address    string `json:"address" cbor:"address"`
	Expires    int64  `json:"expires_ns" cbor:"expires_ns"`
	Timestamp  int64  `json:"timestamp_ns" cbor:"timestamp_ns"`
	Nonce      []byte `json:"nonce" cbor:"nonce"`
	Signature  []byte `json:"signature,omitempty" cbor:"signature,omitempty"`
}

func (c *ProviderClaim) unsigned() ProviderClaim { out := *c; out.Signature = nil; return out }

func (c *ProviderClaim) Sign(key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("invalid provider private key")
	}
	// Always allocate record-owned storage. Some key providers expose slices
	// backed by a live host identity; reusing c.Provider's capacity could write
	// into that shared memory while libp2p reads it.
	c.Provider = append([]byte(nil), key.Public().(ed25519.PublicKey)...)
	payload, err := signingBytes(ProviderClaimDomain, c.unsigned())
	if err != nil {
		return err
	}
	c.Signature = ed25519.Sign(key, payload)
	return nil
}

func (c *ProviderClaim) Validate(now time.Time) error {
	if c.Version != FormatVersion || len(c.ContentKey) != 32 || len(c.Provider) != 32 || strings.TrimSpace(c.Address) == "" || len(c.Nonce) < 16 {
		return errors.New("invalid provider-claim schema")
	}
	if c.Timestamp <= 0 || c.Expires <= now.UnixNano() || c.Expires <= c.Timestamp {
		return errors.New("provider claim is expired or has invalid times")
	}
	payload, err := signingBytes(ProviderClaimDomain, c.unsigned())
	if err != nil {
		return err
	}
	if len(c.Signature) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(c.Provider), payload, c.Signature) {
		return errors.New("invalid provider-claim signature")
	}
	return nil
}

type ProviderClaimValidator struct{ Now func() time.Time }

func (v *ProviderClaimValidator) Validate(key string, value []byte) error {
	var claim ProviderClaim
	if err := UnmarshalCanonical(value, &claim); err != nil {
		return err
	}
	now := time.Now()
	if v != nil && v.Now != nil {
		now = v.Now()
	}
	if err := claim.Validate(now); err != nil {
		return err
	}
	want := fmt.Sprintf("/providers/%x/%x", claim.ContentKey, claim.Provider)
	if key != want {
		return errors.New("provider DHT key does not match claim")
	}
	return nil
}

func (v *ProviderClaimValidator) Select(key string, values [][]byte) (int, error) {
	selected := -1
	var newest int64
	for i, raw := range values {
		if err := v.Validate(key, raw); err != nil {
			continue
		}
		var claim ProviderClaim
		_ = UnmarshalCanonical(raw, &claim)
		if selected < 0 || claim.Timestamp > newest {
			selected, newest = i, claim.Timestamp
		}
	}
	if selected < 0 {
		return -1, routing.ErrNotFound
	}
	return selected, nil
}

type LeaseScope struct {
	NameID     []byte `json:"name_id,omitempty" cbor:"name_id,omitempty"`
	Namespace  []byte `json:"namespace,omitempty" cbor:"namespace,omitempty"`
	PathPrefix string `json:"path_prefix,omitempty" cbor:"path_prefix,omitempty"`
}

type LeaseRecord struct {
	Version    uint64      `json:"version" cbor:"version"`
	Scope      LeaseScope  `json:"scope" cbor:"scope"`
	Owner      []byte      `json:"owner" cbor:"owner"`
	Holder     []byte      `json:"holder" cbor:"holder"`
	Fencing    uint64      `json:"fencing" cbor:"fencing"`
	Issued     int64       `json:"issued_ns" cbor:"issued_ns"`
	Expires    int64       `json:"expires_ns" cbor:"expires_ns"`
	Nonce      []byte      `json:"nonce" cbor:"nonce"`
	Capability *Capability `json:"capability,omitempty" cbor:"capability,omitempty"`
	Signature  []byte      `json:"signature,omitempty" cbor:"signature,omitempty"`
}

func (l *LeaseRecord) unsigned() LeaseRecord { out := *l; out.Signature = nil; return out }

func (l *LeaseRecord) Sign(key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("invalid lease private key")
	}
	payload, err := signingBytes(LeaseDomain, l.unsigned())
	if err != nil {
		return err
	}
	l.Signature = ed25519.Sign(key, payload)
	return nil
}

func (l *LeaseRecord) Validate(now time.Time) error {
	if l.Version != FormatVersion || len(l.Owner) != 32 || len(l.Holder) != 32 || l.Fencing == 0 || len(l.Nonce) < 16 {
		return errors.New("invalid lease schema")
	}
	exact := len(l.Scope.NameID) == 32 && len(l.Scope.Namespace) == 0 && l.Scope.PathPrefix == ""
	subtree := len(l.Scope.NameID) == 0 && len(l.Scope.Namespace) == 32 && l.Scope.PathPrefix != ""
	if exact == subtree {
		return errors.New("lease must have exactly one exact-name or subtree scope")
	}
	if subtree {
		normalized, err := NormalizePath(l.Scope.PathPrefix)
		if err != nil || normalized != l.Scope.PathPrefix {
			return errors.New("invalid subtree scope")
		}
	}
	if l.Issued <= 0 || l.Expires <= now.UnixNano() || l.Expires <= l.Issued || l.Expires-l.Issued > int64(5*time.Minute) {
		return errors.New("lease is expired or exceeds five-minute bound")
	}
	signer := ed25519.PublicKey(l.Holder)
	if !bytes.Equal(l.Holder, l.Owner) {
		if l.Capability == nil || !bytes.Equal(l.Capability.Delegate, l.Holder) || l.Capability.Permissions&PermissionWrite == 0 {
			return errors.New("lease holder is unauthorized")
		}
		if err := l.Capability.Validate(ed25519.PublicKey(l.Owner), now); err != nil {
			return err
		}
		if subtree && (!bytes.Equal(l.Capability.Namespace, l.Scope.Namespace) || !pathWithinPrefix(l.Scope.PathPrefix, l.Capability.PathPrefix)) {
			return errors.New("capability does not authorize lease subtree")
		}
	}
	payload, err := signingBytes(LeaseDomain, l.unsigned())
	if err != nil {
		return err
	}
	if len(l.Signature) != ed25519.SignatureSize || !ed25519.Verify(signer, payload, l.Signature) {
		return errors.New("invalid lease signature")
	}
	return nil
}

func LeaseKey(scope LeaseScope) (string, error) {
	if len(scope.NameID) == 32 {
		return "/leases/name/" + fmt.Sprintf("%x", scope.NameID), nil
	}
	if len(scope.Namespace) == 32 && scope.PathPrefix != "" {
		return "/leases/subtree/" + fmt.Sprintf("%x", scope.Namespace) + "/" + fmt.Sprintf("%x", []byte(scope.PathPrefix)), nil
	}
	return "", errors.New("invalid lease scope")
}

type LeaseValidator struct{ Now func() time.Time }

func (v *LeaseValidator) Validate(key string, value []byte) error {
	var lease LeaseRecord
	if err := UnmarshalCanonical(value, &lease); err != nil {
		return err
	}
	now := time.Now()
	if v != nil && v.Now != nil {
		now = v.Now()
	}
	if err := lease.Validate(now); err != nil {
		return err
	}
	want, _ := LeaseKey(lease.Scope)
	if key != want {
		return errors.New("lease DHT key does not match scope")
	}
	return nil
}

func (v *LeaseValidator) Select(key string, values [][]byte) (int, error) {
	selected := -1
	var fence uint64
	var issued int64
	for i, raw := range values {
		if err := v.Validate(key, raw); err != nil {
			continue
		}
		var lease LeaseRecord
		_ = UnmarshalCanonical(raw, &lease)
		if selected < 0 || lease.Fencing > fence || lease.Fencing == fence && lease.Issued > issued {
			selected, fence, issued = i, lease.Fencing, lease.Issued
		}
	}
	if selected < 0 {
		return -1, routing.ErrNotFound
	}
	return selected, nil
}
