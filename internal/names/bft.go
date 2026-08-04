package names

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	ds "github.com/ipfs/go-datastore"
	recordvalidator "github.com/libp2p/go-libp2p-record"
	"github.com/libp2p/go-libp2p/core/routing"
)

var _ recordvalidator.Validator = (*NamespaceRootValidator)(nil)
var _ recordvalidator.Validator = (*CertifiedNameValidator)(nil)

const (
	NamespaceRootDomain = "tarsus-namespace-root-v1\x00"
	QuorumVoteDomain    = "tarsus-quorum-vote-v1\x00"
	CertifiedNameDomain = "tarsus-certified-name-v1\x00"
)

type NamespaceRoot struct {
	Version        uint64   `json:"version" cbor:"version"`
	Namespace      []byte   `json:"namespace" cbor:"namespace"`
	Owner          []byte   `json:"owner" cbor:"owner"`
	Validators     [][]byte `json:"validators" cbor:"validators"`
	FaultThreshold uint16   `json:"fault_threshold" cbor:"fault_threshold"`
	CommitteeEpoch uint64   `json:"committee_epoch" cbor:"committee_epoch"`
	Timestamp      int64    `json:"timestamp_ns" cbor:"timestamp_ns"`
	Nonce          []byte   `json:"nonce" cbor:"nonce"`
	Signature      []byte   `json:"signature,omitempty" cbor:"signature,omitempty"`
}

func (r *NamespaceRoot) unsigned() NamespaceRoot {
	out := *r
	out.Signature = nil
	return out
}

func (r *NamespaceRoot) Sign(owner ed25519.PrivateKey) error {
	if len(owner) != ed25519.PrivateKeySize {
		return errors.New("invalid namespace-root owner key")
	}
	r.Owner = append([]byte(nil), owner.Public().(ed25519.PublicKey)...)
	payload, err := signingBytes(NamespaceRootDomain, r.unsigned())
	if err != nil {
		return err
	}
	r.Signature = ed25519.Sign(owner, payload)
	return nil
}

func (r *NamespaceRoot) Validate(now time.Time) error {
	if r == nil || r.Version != FormatVersion || len(r.Namespace) != 32 ||
		len(r.Owner) != ed25519.PublicKeySize || r.FaultThreshold == 0 ||
		r.CommitteeEpoch == 0 || len(r.Nonce) < 16 {
		return errors.New("invalid namespace-root schema")
	}
	want := 3*int(r.FaultThreshold) + 1
	if len(r.Validators) != want {
		return fmt.Errorf("committee has %d validators; want 3f+1=%d", len(r.Validators), want)
	}
	seen := make(map[string]struct{}, len(r.Validators))
	for _, validator := range r.Validators {
		if len(validator) != ed25519.PublicKeySize {
			return errors.New("invalid committee validator key")
		}
		key := string(validator)
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate committee validator")
		}
		seen[key] = struct{}{}
	}
	if r.Timestamp <= 0 || r.Timestamp > now.Add(10*time.Minute).UnixNano() {
		return errors.New("invalid namespace-root timestamp")
	}
	payload, err := signingBytes(NamespaceRootDomain, r.unsigned())
	if err != nil {
		return err
	}
	if len(r.Signature) != ed25519.SignatureSize ||
		!ed25519.Verify(ed25519.PublicKey(r.Owner), payload, r.Signature) {
		return errors.New("invalid namespace-root signature")
	}
	return nil
}

func (r *NamespaceRoot) QuorumSize() int { return 2*int(r.FaultThreshold) + 1 }

func (r *NamespaceRoot) HasValidator(key []byte) bool {
	for _, validator := range r.Validators {
		if bytes.Equal(validator, key) {
			return true
		}
	}
	return false
}

func DHTNamespaceRootKey(namespace NamespaceID) string {
	return "/namespace-roots/" + namespace.String()
}

type NamespaceRootValidator struct{ Now func() time.Time }

func (v *NamespaceRootValidator) now() time.Time {
	if v != nil && v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *NamespaceRootValidator) Validate(key string, value []byte) error {
	var root NamespaceRoot
	if err := UnmarshalCanonical(value, &root); err != nil {
		return err
	}
	if err := root.Validate(v.now()); err != nil {
		return err
	}
	var namespace NamespaceID
	copy(namespace[:], root.Namespace)
	if key != DHTNamespaceRootKey(namespace) {
		return errors.New("namespace-root DHT key mismatch")
	}
	return nil
}

func (v *NamespaceRootValidator) Select(key string, values [][]byte) (int, error) {
	selected := -1
	var epoch uint64
	var selectedRaw []byte
	for i, raw := range values {
		if err := v.Validate(key, raw); err != nil {
			continue
		}
		var root NamespaceRoot
		_ = UnmarshalCanonical(raw, &root)
		if selected < 0 || root.CommitteeEpoch > epoch {
			selected, epoch, selectedRaw = i, root.CommitteeEpoch, raw
		} else if root.CommitteeEpoch == epoch && !bytes.Equal(raw, selectedRaw) {
			return -1, errors.New("equal-epoch namespace-root fork")
		}
	}
	if selected < 0 {
		return -1, routing.ErrNotFound
	}
	return selected, nil
}

type QuorumPhase string

const (
	PhasePrepare   QuorumPhase = "prepare"
	PhasePrecommit QuorumPhase = "precommit"
	PhaseCommit    QuorumPhase = "commit"
)

func validQuorumPhase(phase QuorumPhase) bool {
	return phase == PhasePrepare || phase == PhasePrecommit || phase == PhaseCommit
}

type QuorumVote struct {
	Version        uint64      `json:"version" cbor:"version"`
	Namespace      []byte      `json:"namespace" cbor:"namespace"`
	NameID         []byte      `json:"name_id" cbor:"name_id"`
	Generation     uint64      `json:"generation" cbor:"generation"`
	RecordHash     []byte      `json:"record_hash" cbor:"record_hash"`
	PreviousHash   []byte      `json:"previous_hash,omitempty" cbor:"previous_hash,omitempty"`
	CommitteeEpoch uint64      `json:"committee_epoch" cbor:"committee_epoch"`
	View           uint64      `json:"view" cbor:"view"`
	Phase          QuorumPhase `json:"phase" cbor:"phase"`
	Validator      []byte      `json:"validator" cbor:"validator"`
	Timestamp      int64       `json:"timestamp_ns" cbor:"timestamp_ns"`
	Nonce          []byte      `json:"nonce" cbor:"nonce"`
	Signature      []byte      `json:"signature,omitempty" cbor:"signature,omitempty"`
}

func (v *QuorumVote) unsigned() QuorumVote { out := *v; out.Signature = nil; return out }

func (v *QuorumVote) Sign(key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("invalid quorum-vote key")
	}
	v.Validator = append([]byte(nil), key.Public().(ed25519.PublicKey)...)
	payload, err := signingBytes(QuorumVoteDomain, v.unsigned())
	if err != nil {
		return err
	}
	v.Signature = ed25519.Sign(key, payload)
	return nil
}

func (v *QuorumVote) Validate(root *NamespaceRoot, now time.Time) error {
	if v == nil || v.Version != FormatVersion || !validQuorumPhase(v.Phase) ||
		len(v.Namespace) != 32 || len(v.NameID) != 32 || len(v.RecordHash) != 32 ||
		len(v.Validator) != ed25519.PublicKeySize || len(v.Nonce) < 16 {
		return errors.New("invalid quorum-vote schema")
	}
	if root == nil || !bytes.Equal(v.Namespace, root.Namespace) ||
		v.CommitteeEpoch != root.CommitteeEpoch || !root.HasValidator(v.Validator) {
		return errors.New("quorum vote is outside the authenticated committee")
	}
	if v.Generation == 0 {
		if len(v.PreviousHash) != 0 {
			return errors.New("generation-zero vote has a predecessor")
		}
	} else if len(v.PreviousHash) != 32 {
		return errors.New("later quorum vote lacks a predecessor")
	}
	if v.Timestamp <= 0 || v.Timestamp > now.Add(10*time.Minute).UnixNano() {
		return errors.New("invalid quorum-vote timestamp")
	}
	payload, err := signingBytes(QuorumVoteDomain, v.unsigned())
	if err != nil {
		return err
	}
	if len(v.Signature) != ed25519.SignatureSize ||
		!ed25519.Verify(ed25519.PublicKey(v.Validator), payload, v.Signature) {
		return errors.New("invalid quorum-vote signature")
	}
	return nil
}

type QuorumCertificate struct {
	Version        uint64       `json:"version" cbor:"version"`
	Namespace      []byte       `json:"namespace" cbor:"namespace"`
	NameID         []byte       `json:"name_id" cbor:"name_id"`
	Generation     uint64       `json:"generation" cbor:"generation"`
	RecordHash     []byte       `json:"record_hash" cbor:"record_hash"`
	PreviousHash   []byte       `json:"previous_hash,omitempty" cbor:"previous_hash,omitempty"`
	CommitteeEpoch uint64       `json:"committee_epoch" cbor:"committee_epoch"`
	View           uint64       `json:"view" cbor:"view"`
	Phase          QuorumPhase  `json:"phase" cbor:"phase"`
	Votes          []QuorumVote `json:"votes" cbor:"votes"`
}

func (q *QuorumCertificate) Validate(root *NamespaceRoot, now time.Time) error {
	if q == nil || q.Version != FormatVersion || !validQuorumPhase(q.Phase) ||
		len(q.Namespace) != 32 || len(q.NameID) != 32 || len(q.RecordHash) != 32 {
		return errors.New("invalid quorum-certificate schema")
	}
	if root == nil || !bytes.Equal(q.Namespace, root.Namespace) ||
		q.CommitteeEpoch != root.CommitteeEpoch {
		return errors.New("quorum certificate is outside the authenticated committee")
	}
	seen := make(map[string]struct{}, len(q.Votes))
	valid := 0
	for i := range q.Votes {
		vote := &q.Votes[i]
		if err := vote.Validate(root, now); err != nil {
			return err
		}
		if vote.Generation != q.Generation || vote.View != q.View ||
			vote.Phase != q.Phase || !bytes.Equal(vote.NameID, q.NameID) ||
			!bytes.Equal(vote.RecordHash, q.RecordHash) ||
			!bytes.Equal(vote.PreviousHash, q.PreviousHash) {
			return errors.New("quorum certificate contains a mismatched vote")
		}
		key := string(vote.Validator)
		if _, duplicate := seen[key]; duplicate {
			return errors.New("quorum certificate contains duplicate validators")
		}
		seen[key] = struct{}{}
		valid++
	}
	if valid < root.QuorumSize() {
		return fmt.Errorf("quorum certificate has %d votes; want %d", valid, root.QuorumSize())
	}
	return nil
}

type CertifiedNameRecord struct {
	Version uint64            `json:"version" cbor:"version"`
	Root    NamespaceRoot     `json:"root" cbor:"root"`
	Record  []byte            `json:"record_cbor" cbor:"record_cbor"`
	Commit  QuorumCertificate `json:"commit_qc" cbor:"commit_qc"`
}

func (c *CertifiedNameRecord) Validate(now time.Time) (*NameRecord, error) {
	if c == nil || c.Version != FormatVersion {
		return nil, errors.New("invalid certified-name version")
	}
	if err := c.Root.Validate(now); err != nil {
		return nil, err
	}
	record, err := DecodeNameRecord(c.Record)
	if err != nil {
		return nil, err
	}
	if err := record.ValidateEnvelope(now); err != nil {
		return nil, err
	}
	if !bytes.Equal(record.Namespace, c.Root.Namespace) ||
		!bytes.Equal(record.Owner, c.Root.Owner) {
		return nil, errors.New("name record is not bound to namespace root")
	}
	hash := sha256.Sum256(c.Record)
	if c.Commit.Phase != PhaseCommit || c.Commit.Generation != record.Generation ||
		!bytes.Equal(c.Commit.NameID, record.NameID) ||
		!bytes.Equal(c.Commit.RecordHash, hash[:]) ||
		!bytes.Equal(c.Commit.PreviousHash, record.PreviousHash) {
		return nil, errors.New("commit certificate does not bind the name record")
	}
	if err := c.Commit.Validate(&c.Root, now); err != nil {
		return nil, err
	}
	return record, nil
}

func DHTCertifiedNameKey(id NameID) string { return "/certified-names/" + id.String() }

type CertifiedNameValidator struct{ Now func() time.Time }

func (v *CertifiedNameValidator) now() time.Time {
	if v != nil && v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *CertifiedNameValidator) Validate(key string, value []byte) error {
	var certified CertifiedNameRecord
	if err := UnmarshalCanonical(value, &certified); err != nil {
		return err
	}
	record, err := certified.Validate(v.now())
	if err != nil {
		return err
	}
	id := bytesToNameID(record.NameID)
	if key != DHTCertifiedNameKey(id) {
		return errors.New("certified-name DHT key mismatch")
	}
	return nil
}

func (v *CertifiedNameValidator) Select(key string, values [][]byte) (int, error) {
	selected := -1
	var generation uint64
	var selectedRaw []byte
	for i, raw := range values {
		if err := v.Validate(key, raw); err != nil {
			continue
		}
		var certified CertifiedNameRecord
		_ = UnmarshalCanonical(raw, &certified)
		record, _ := DecodeNameRecord(certified.Record)
		if selected < 0 || record.Generation > generation {
			selected, generation, selectedRaw = i, record.Generation, raw
		} else if record.Generation == generation && !bytes.Equal(raw, selectedRaw) {
			return -1, errors.New("equal-generation certified-name fork")
		}
	}
	if selected < 0 {
		return -1, routing.ErrNotFound
	}
	return selected, nil
}

// QuorumJournal persists non-equivocation votes and precommit locks before
// returning a signature. A process restart therefore cannot make an honest
// validator sign a conflicting value for the same view or violate its lock.
type QuorumJournal struct {
	Datastore ds.Batching
	Private   ed25519.PrivateKey
	Now       func() time.Time
	// Head returns the validator's verified current head. Configuring it makes
	// every vote enforce the generation/previous-hash transition locally.
	Head func(context.Context, NameID) (*NameRecord, []byte, error)
}

func (j *QuorumJournal) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func quorumVoteKey(id NameID, generation, view uint64, phase QuorumPhase, validator []byte) ds.Key {
	return ds.NewKey(fmt.Sprintf("/bft/votes/%s/%020d/%020d/%s/%x", id.String(), generation, view, phase, validator))
}

func quorumLockKey(id NameID, generation uint64, validator []byte) ds.Key {
	return ds.NewKey(fmt.Sprintf("/bft/locks/%s/%020d/%x", id.String(), generation, validator))
}

func (j *QuorumJournal) Vote(ctx context.Context, root *NamespaceRoot, raw []byte, phase QuorumPhase, view uint64, justify *QuorumCertificate) (*QuorumVote, error) {
	if j == nil || j.Datastore == nil || len(j.Private) != ed25519.PrivateKeySize {
		return nil, errors.New("quorum journal is not configured")
	}
	if err := root.Validate(j.now()); err != nil {
		return nil, err
	}
	public := j.Private.Public().(ed25519.PublicKey)
	if !root.HasValidator(public) {
		return nil, errors.New("local signer is not a committee validator")
	}
	record, err := DecodeNameRecord(raw)
	if err != nil {
		return nil, err
	}
	if err := record.ValidateEnvelope(j.now()); err != nil {
		return nil, err
	}
	if !bytes.Equal(record.Namespace, root.Namespace) || !bytes.Equal(record.Owner, root.Owner) {
		return nil, errors.New("proposal is outside namespace root")
	}
	id := bytesToNameID(record.NameID)
	if j.Head != nil {
		current, currentRaw, headErr := j.Head(ctx, id)
		switch {
		case errors.Is(headErr, ErrNotFound) && record.Generation == 0:
			if err := record.Validate(j.now(), nil); err != nil {
				return nil, err
			}
		case headErr != nil:
			return nil, fmt.Errorf("read validator head: %w", headErr)
		case current != nil && current.Generation == record.Generation:
			currentHash := sha256.Sum256(currentRaw)
			proposalHash := sha256.Sum256(raw)
			if !bytes.Equal(currentHash[:], proposalHash[:]) {
				return nil, errors.New("proposal conflicts with the committed generation")
			}
		case current != nil:
			if err := record.Validate(j.now(), current); err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("proposal does not extend a verified head")
		}
	}
	hash := sha256.Sum256(raw)
	if phase == PhasePrecommit || phase == PhaseCommit {
		want := PhasePrepare
		if phase == PhaseCommit {
			want = PhasePrecommit
		}
		if justify == nil || justify.Phase != want ||
			!bytes.Equal(justify.RecordHash, hash[:]) || justify.View != view {
			return nil, errors.New("proposal lacks the required same-view justification")
		}
		if err := justify.Validate(root, j.now()); err != nil {
			return nil, err
		}
	}
	if locked, err := j.Datastore.Get(ctx, quorumLockKey(id, record.Generation, public)); err == nil &&
		!bytes.Equal(locked, hash[:]) {
		return nil, errors.New("validator is locked on a conflicting proposal")
	} else if err != nil && !errors.Is(err, ds.ErrNotFound) {
		return nil, err
	}
	voteKey := quorumVoteKey(id, record.Generation, view, phase, public)
	if existing, err := j.Datastore.Get(ctx, voteKey); err == nil {
		var vote QuorumVote
		if err := UnmarshalCanonical(existing, &vote); err != nil {
			return nil, err
		}
		if !bytes.Equal(vote.RecordHash, hash[:]) {
			return nil, errors.New("validator already voted for a conflicting proposal")
		}
		return &vote, nil
	} else if !errors.Is(err, ds.ErrNotFound) {
		return nil, err
	}
	vote := &QuorumVote{Version: FormatVersion, Namespace: append([]byte(nil), record.Namespace...), NameID: append([]byte(nil), record.NameID...), Generation: record.Generation, RecordHash: hash[:], PreviousHash: append([]byte(nil), record.PreviousHash...), CommitteeEpoch: root.CommitteeEpoch, View: view, Phase: phase, Timestamp: j.now().UnixNano(), Nonce: make([]byte, 16)}
	if _, err := randRead(vote.Nonce); err != nil {
		return nil, err
	}
	if err := vote.Sign(j.Private); err != nil {
		return nil, err
	}
	encoded, err := MarshalCanonical(vote)
	if err != nil {
		return nil, err
	}
	batch, err := j.Datastore.Batch(ctx)
	if err != nil {
		return nil, err
	}
	if phase == PhasePrecommit {
		if err := batch.Put(ctx, quorumLockKey(id, record.Generation, public), hash[:]); err != nil {
			return nil, err
		}
	}
	if err := batch.Put(ctx, voteKey, encoded); err != nil {
		return nil, err
	}
	if err := batch.Commit(ctx); err != nil {
		return nil, err
	}
	return vote, nil
}

func AssembleQuorumCertificate(root *NamespaceRoot, votes []QuorumVote, now time.Time) (*QuorumCertificate, error) {
	if len(votes) == 0 {
		return nil, errors.New("cannot assemble an empty quorum certificate")
	}
	first := votes[0]
	qc := &QuorumCertificate{Version: FormatVersion, Namespace: append([]byte(nil), first.Namespace...), NameID: append([]byte(nil), first.NameID...), Generation: first.Generation, RecordHash: append([]byte(nil), first.RecordHash...), PreviousHash: append([]byte(nil), first.PreviousHash...), CommitteeEpoch: first.CommitteeEpoch, View: first.View, Phase: first.Phase, Votes: append([]QuorumVote(nil), votes...)}
	if err := qc.Validate(root, now); err != nil {
		return nil, err
	}
	return qc, nil
}

// randRead is a variable only to make nonce failures injectable in tests.
var randRead = func(value []byte) (int, error) {
	return rand.Reader.Read(value)
}
