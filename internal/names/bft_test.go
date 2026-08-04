package names

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"golang.org/x/crypto/curve25519"
)

func TestSevenMemberQuorumCertificateAndDurableLock(t *testing.T) {
	now := time.Now()
	owner, ownerPrivate := testKeys(t)
	namespace, _ := NewNamespaceID()
	validators := make([][]byte, 7)
	privateKeys := make([]ed25519.PrivateKey, 7)
	for i := range validators {
		public, private := testKeys(t)
		validators[i], privateKeys[i] = public, private
	}
	root := &NamespaceRoot{Version: FormatVersion, Namespace: namespace[:], Validators: validators, FaultThreshold: 2, CommitteeEpoch: 1, Timestamp: now.UnixNano(), Nonce: bytes.Repeat([]byte{1}, 16)}
	if err := root.Sign(ownerPrivate); err != nil {
		t.Fatal(err)
	}
	if err := root.Validate(now); err != nil {
		t.Fatal(err)
	}
	record := testRecord(t, namespace, "/bft/object", owner, ownerPrivate, 0, nil)
	raw, _ := record.Marshal()
	journals := make([]*QuorumJournal, 7)
	for i := range journals {
		journals[i] = &QuorumJournal{Datastore: dssync.MutexWrap(ds.NewMapDatastore()), Private: privateKeys[i], Now: func() time.Time { return now }}
	}
	votePhase := func(phase QuorumPhase, justify *QuorumCertificate) []QuorumVote {
		t.Helper()
		votes := make([]QuorumVote, 0, 5)
		for i := 0; i < 5; i++ {
			vote, err := journals[i].Vote(context.Background(), root, raw, phase, 1, justify)
			if err != nil {
				t.Fatalf("%s vote %d: %v", phase, i, err)
			}
			votes = append(votes, *vote)
		}
		return votes
	}
	prepare, err := AssembleQuorumCertificate(root, votePhase(PhasePrepare, nil), now)
	if err != nil {
		t.Fatal(err)
	}
	precommit, err := AssembleQuorumCertificate(root, votePhase(PhasePrecommit, prepare), now)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := AssembleQuorumCertificate(root, votePhase(PhaseCommit, precommit), now)
	if err != nil {
		t.Fatal(err)
	}
	certified := &CertifiedNameRecord{Version: FormatVersion, Root: *root, Record: raw, Commit: *commit}
	if _, err := certified.Validate(now); err != nil {
		t.Fatal(err)
	}

	fork := *record
	alternate := sha256.Sum256([]byte("alternate manifest"))
	fork.ManifestKey = alternate[:]
	if err := fork.Sign(ownerPrivate); err != nil {
		t.Fatal(err)
	}
	forkRaw, _ := fork.Marshal()
	restarted := &QuorumJournal{Datastore: journals[0].Datastore, Private: privateKeys[0], Now: func() time.Time { return now }}
	if _, err := restarted.Vote(context.Background(), root, forkRaw, PhasePrepare, 2, nil); err == nil {
		t.Fatal("durably locked validator signed a conflicting later-view proposal")
	}

	short := *commit
	short.Votes = short.Votes[:4]
	if err := short.Validate(root, now); err == nil {
		t.Fatal("four-of-seven certificate passed five-vote threshold")
	}
	duplicate := *commit
	duplicate.Votes = append([]QuorumVote(nil), commit.Votes...)
	duplicate.Votes[4] = duplicate.Votes[0]
	if err := duplicate.Validate(root, now); err == nil {
		t.Fatal("duplicate validator counted twice")
	}
}

func TestNamespaceRootAndCertifiedRecordRejectTampering(t *testing.T) {
	now := time.Now()
	owner, ownerPrivate := testKeys(t)
	namespace, _ := NewNamespaceID()
	validators := make([][]byte, 7)
	privateKeys := make([]ed25519.PrivateKey, 7)
	for i := range validators {
		validators[i], privateKeys[i] = testKeys(t)
	}
	root := &NamespaceRoot{Version: FormatVersion, Namespace: namespace[:], Validators: validators, FaultThreshold: 2, CommitteeEpoch: 1, Timestamp: now.UnixNano(), Nonce: bytes.Repeat([]byte{2}, 16)}
	_ = root.Sign(ownerPrivate)
	tampered := *root
	tampered.CommitteeEpoch++
	if err := tampered.Validate(now); err == nil {
		t.Fatal("tampered namespace root verified")
	}
	record := testRecord(t, namespace, "/certified", owner, ownerPrivate, 0, nil)
	raw, _ := record.Marshal()
	hash := sha256.Sum256(raw)
	votes := make([]QuorumVote, 5)
	for i := range votes {
		votes[i] = QuorumVote{Version: FormatVersion, Namespace: namespace[:], NameID: record.NameID, RecordHash: hash[:], CommitteeEpoch: 1, View: 1, Phase: PhaseCommit, Timestamp: now.UnixNano(), Nonce: bytes.Repeat([]byte{byte(i + 3)}, 16)}
		_ = votes[i].Sign(privateKeys[i])
	}
	qc, err := AssembleQuorumCertificate(root, votes, now)
	if err != nil {
		t.Fatal(err)
	}
	certified := &CertifiedNameRecord{Version: FormatVersion, Root: *root, Record: raw, Commit: *qc}
	certified.Record = append([]byte(nil), raw...)
	certified.Record[len(certified.Record)-1] ^= 1
	if _, err := certified.Validate(now); err == nil {
		t.Fatal("tampered certified record verified")
	}
}

func TestPrivateManifestUsesKeyedIntegrityTags(t *testing.T) {
	_, signer := testKeys(t)
	readerPrivate := make([]byte, 32)
	_, _ = rand.Read(readerPrivate)
	readerPublic, _ := curvePublic(readerPrivate)
	data := bytes.Repeat([]byte("predictable"), 8192)
	sink := func(_ context.Context, raw []byte) (ContentKey, error) { return sha256.Sum256(raw), nil }
	built, err := BuildObject(context.Background(), bytes.NewReader(data), sink, BuildObjectOptions{Encryption: "private", KeyEpoch: 1, ReaderPublicKeys: [][]byte{readerPublic}, Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	if built.Manifest.Integrity != IntegrityHMACV1 {
		t.Fatalf("integrity mode = %q", built.Manifest.Integrity)
	}
	rawChunkHash := sha256.Sum256(data)
	if bytes.Equal(rawChunkHash[:], built.Manifest.Chunks[0].PlaintextHash) {
		t.Fatal("private manifest leaked raw plaintext chunk hash")
	}
	rawObjectHash := sha256.Sum256(data)
	if bytes.Equal(rawObjectHash[:], built.Manifest.PlaintextDigest) {
		t.Fatal("private manifest leaked raw plaintext object digest")
	}
}

func TestCertifiedCommitIsTheResolvedBFTHead(t *testing.T) {
	now := time.Now()
	owner, ownerPrivate := testKeys(t)
	namespace, _ := NewNamespaceID()
	validators := make([][]byte, 7)
	privateKeys := make([]ed25519.PrivateKey, 7)
	for i := range validators {
		validators[i], privateKeys[i] = testKeys(t)
	}
	root := &NamespaceRoot{Version: FormatVersion, Namespace: namespace[:], Validators: validators, FaultThreshold: 2, CommitteeEpoch: 1, Timestamp: now.UnixNano(), Nonce: bytes.Repeat([]byte{8}, 16)}
	if err := root.Sign(ownerPrivate); err != nil {
		t.Fatal(err)
	}
	record := testRecord(t, namespace, "/bft/committed", owner, ownerPrivate, 0, nil)
	record.Policy.StrictPublish = false
	if err := record.Sign(ownerPrivate); err != nil {
		t.Fatal(err)
	}
	recordRaw, _ := record.Marshal()
	hash := sha256.Sum256(recordRaw)
	votes := make([]QuorumVote, 5)
	for i := range votes {
		votes[i] = QuorumVote{Version: FormatVersion, Namespace: namespace[:], NameID: record.NameID, RecordHash: hash[:], CommitteeEpoch: 1, View: 4, Phase: PhaseCommit, Timestamp: now.UnixNano(), Nonce: bytes.Repeat([]byte{byte(20 + i)}, 16)}
		if err := votes[i].Sign(privateKeys[i]); err != nil {
			t.Fatal(err)
		}
	}
	qc, err := AssembleQuorumCertificate(root, votes, now)
	if err != nil {
		t.Fatal(err)
	}
	certified := &CertifiedNameRecord{Version: FormatVersion, Root: *root, Record: recordRaw, Commit: *qc}
	certifiedRaw, _ := MarshalCanonical(certified)
	network := &memoryNameValueStore{values: map[string][]byte{}}
	service := NewService(dssync.MutexWrap(ds.NewMapDatastore()), network, nil)
	committed, head, err := service.CommitCertified(context.Background(), certifiedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Commit.View != 4 || head.Generation != 0 {
		t.Fatalf("unexpected certified commit: %+v", committed)
	}
	id := bytesToNameID(record.NameID)
	resolved, resolvedRecord, resolvedRaw, err := service.GetCertified(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Commit.View != 4 || resolvedRecord.Generation != 0 || !bytes.Equal(resolvedRaw, certifiedRaw) {
		t.Fatal("certified head did not round trip")
	}
	if _, ok := network.values[DHTCertifiedNameKey(id)]; !ok {
		t.Fatal("certified head was not published under its dedicated DHT key")
	}
	short := *certified
	short.Commit = *qc
	short.Commit.Votes = append([]QuorumVote(nil), qc.Votes[:4]...)
	shortRaw, _ := MarshalCanonical(&short)
	other := NewService(dssync.MutexWrap(ds.NewMapDatastore()), nil, nil)
	if _, _, err := other.CommitCertified(context.Background(), shortRaw); err == nil {
		t.Fatal("service committed a four-of-seven certificate")
	}
}

func TestQuorumJournalRejectsProposalThatSkipsCommittedHead(t *testing.T) {
	now := time.Now()
	owner, ownerPrivate := testKeys(t)
	validator, validatorPrivate := testKeys(t)
	namespace, _ := NewNamespaceID()
	validators := [][]byte{validator}
	for len(validators) < 4 {
		public, _ := testKeys(t)
		validators = append(validators, public)
	}
	root := &NamespaceRoot{Version: FormatVersion, Namespace: namespace[:], Validators: validators, FaultThreshold: 1, CommitteeEpoch: 1, Timestamp: now.UnixNano(), Nonce: bytes.Repeat([]byte{9}, 16)}
	_ = root.Sign(ownerPrivate)
	current := testRecord(t, namespace, "/ordered", owner, ownerPrivate, 0, nil)
	currentRaw, _ := current.Marshal()
	skipped := testRecord(t, namespace, "/ordered", owner, ownerPrivate, 2, current)
	skippedRaw, _ := skipped.Marshal()
	journal := &QuorumJournal{Datastore: dssync.MutexWrap(ds.NewMapDatastore()), Private: validatorPrivate, Now: func() time.Time { return now }, Head: func(context.Context, NameID) (*NameRecord, []byte, error) {
		return current, currentRaw, nil
	}}
	if _, err := journal.Vote(context.Background(), root, skippedRaw, PhasePrepare, 1, nil); err == nil {
		t.Fatal("validator voted for a proposal that skipped a generation")
	}
}

func curvePublic(private []byte) ([]byte, error) {
	return curve25519.X25519(private, curve25519.Basepoint)
}
