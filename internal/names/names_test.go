package names

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"golang.org/x/crypto/curve25519"
)

func testKeys(t testing.TB) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return public, private
}

func testRecord(t testing.TB, ns NamespaceID, namePath string, owner ed25519.PublicKey, signer ed25519.PrivateKey, generation uint64, previous *NameRecord) *NameRecord {
	t.Helper()
	normalized, err := NormalizePath(namePath)
	if err != nil {
		t.Fatal(err)
	}
	id := DeriveNameID(ns, normalized)
	manifest := sha256.Sum256([]byte("manifest-" + string(rune(generation))))
	record := &NameRecord{Version: FormatVersion, Namespace: append([]byte(nil), ns[:]...), NameID: append([]byte(nil), id[:]...), Path: normalized, Kind: "file", Generation: generation, ManifestKey: manifest[:], Owner: append([]byte(nil), owner...), Policy: DefaultPolicy(), Timestamp: time.Now().UnixNano(), Nonce: bytes.Repeat([]byte{byte(generation + 1)}, 16)}
	if previous != nil {
		hash, err := previous.Hash()
		if err != nil {
			t.Fatal(err)
		}
		record.PreviousHash = hash[:]
	}
	if err := record.Sign(signer); err != nil {
		t.Fatal(err)
	}
	return record
}

func testService() *Service { return NewService(dssync.MutexWrap(ds.NewMapDatastore()), nil, nil) }

func TestNormalizeAndDeriveNameID(t *testing.T) {
	ns, err := NewNamespaceID()
	if err != nil {
		t.Fatal(err)
	}
	p, err := NormalizePath("//projects/./a.dat")
	if err != nil {
		t.Fatal(err)
	}
	if p != "/projects/a.dat" {
		t.Fatalf("normalized path = %q", p)
	}
	if _, err := NormalizePath("/projects/../secret"); err == nil {
		t.Fatal("accepted parent traversal")
	}
	id := DeriveNameID(ns, p)
	parsedNS, parsedPath, parsedID, err := ParseTarsusURI("tarsus://" + ns.String() + p)
	if err != nil {
		t.Fatal(err)
	}
	if parsedNS != ns || parsedPath != p || parsedID != id {
		t.Fatal("URI did not round trip")
	}
}

func TestCanonicalSignedRecordRejectsTampering(t *testing.T) {
	owner, private := testKeys(t)
	ns, _ := NewNamespaceID()
	record := testRecord(t, ns, "/a", owner, private, 0, nil)
	raw, err := record.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeNameRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	decoded.Path = "/b"
	if err := decoded.Validate(time.Now(), nil); err == nil {
		t.Fatal("tampered path passed validation")
	}
	nonCanonical := append([]byte{0xbf}, raw[1:]...)
	if _, err := DecodeNameRecord(nonCanonical); err == nil {
		t.Fatal("non-canonical encoding accepted")
	}
}

func TestCASAllowsExactlyOneConcurrentUpdate(t *testing.T) {
	owner, private := testKeys(t)
	ns, _ := NewNamespaceID()
	service := testService()
	initial := testRecord(t, ns, "/race", owner, private, 0, nil)
	initialRaw, _ := initial.Marshal()
	if _, err := service.Create(context.Background(), initialRaw); err != nil {
		t.Fatal(err)
	}
	updates := []*NameRecord{testRecord(t, ns, "/race", owner, private, 1, initial), testRecord(t, ns, "/race", owner, private, 1, initial)}
	updates[1].ManifestKey = sha256.New().Sum([]byte("different"))[:32]
	if err := updates[1].Sign(private); err != nil {
		t.Fatal(err)
	}
	var committed atomic.Int32
	var conflicts atomic.Int32
	var wg sync.WaitGroup
	for _, update := range updates {
		update := update
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := update.Marshal()
			_, err := service.Update(context.Background(), bytesToNameID(update.NameID), 0, raw)
			if err == nil {
				committed.Add(1)
			} else if errors.Is(err, ErrConflict) {
				conflicts.Add(1)
			} else {
				t.Errorf("unexpected update error: %v", err)
			}
		}()
	}
	wg.Wait()
	if committed.Load() != 1 || conflicts.Load() != 1 {
		t.Fatalf("commits=%d conflicts=%d", committed.Load(), conflicts.Load())
	}
}

func TestUnauthorizedPolicyAndTombstoneRejected(t *testing.T) {
	owner, ownerPrivate := testKeys(t)
	delegate, delegatePrivate := testKeys(t)
	ns, _ := NewNamespaceID()
	service := testService()
	initial := testRecord(t, ns, "/secure", owner, ownerPrivate, 0, nil)
	raw, _ := initial.Marshal()
	if _, err := service.Create(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	unauthorized := testRecord(t, ns, "/secure", owner, delegatePrivate, 1, initial)
	raw, _ = unauthorized.Marshal()
	if _, err := service.Update(context.Background(), bytesToNameID(initial.NameID), 0, raw); err == nil {
		t.Fatal("unauthorized update committed")
	}
	capability := &Capability{Version: FormatVersion, Namespace: ns[:], PathPrefix: "/secure", Delegate: delegate, Permissions: PermissionWrite, NotBefore: time.Now().Add(-time.Minute).UnixNano(), Expires: time.Now().Add(time.Hour).UnixNano(), Nonce: bytes.Repeat([]byte{3}, 16)}
	if err := capability.Sign(ownerPrivate); err != nil {
		t.Fatal(err)
	}
	policyUpdate := testRecord(t, ns, "/secure", owner, delegatePrivate, 1, initial)
	policyUpdate.Capability = capability
	policyUpdate.Policy.Replicas = 1
	policyUpdate.Policy.Placement = PlacementPolicy{Near: 1}
	if err := policyUpdate.Sign(delegatePrivate); err != nil {
		t.Fatal(err)
	}
	raw, _ = policyUpdate.Marshal()
	if _, err := service.Update(context.Background(), bytesToNameID(initial.NameID), 0, raw); err == nil {
		t.Fatal("write-only delegate changed policy")
	}
	tombstone := testRecord(t, ns, "/secure", owner, delegatePrivate, 1, initial)
	tombstone.Capability = capability
	tombstone.Tombstone = true
	tombstone.ManifestKey = nil
	if err := tombstone.Sign(delegatePrivate); err != nil {
		t.Fatal(err)
	}
	raw, _ = tombstone.Marshal()
	if _, err := service.Delete(context.Background(), bytesToNameID(initial.NameID), 0, raw); err == nil {
		t.Fatal("write-only delegate deleted name")
	}
}

func TestStrictPublicationDoesNotExposeHeadOnFailure(t *testing.T) {
	owner, private := testKeys(t)
	ns, _ := NewNamespaceID()
	store := dssync.MutexWrap(ds.NewMapDatastore())
	service := NewService(store, nil, func(context.Context, *NameRecord) error { return errors.New("only six providers") })
	record := testRecord(t, ns, "/strict", owner, private, 0, nil)
	raw, _ := record.Marshal()
	if _, err := service.Create(context.Background(), raw); err == nil {
		t.Fatal("strict publication succeeded")
	}
	if _, _, err := service.Get(context.Background(), bytesToNameID(record.NameID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("head became visible: %v", err)
	}
}

func TestSearchOnlyReturnsCurrentSearchableHeads(t *testing.T) {
	owner, private := testKeys(t)
	ns, _ := NewNamespaceID()
	service := testService()
	initial := testRecord(t, ns, "/projects/a.dat", owner, private, 0, nil)
	raw, _ := initial.Marshal()
	_, _ = service.Create(context.Background(), raw)
	update := testRecord(t, ns, "/projects/a.dat", owner, private, 1, initial)
	raw, _ = update.Marshal()
	_, _ = service.Update(context.Background(), bytesToNameID(initial.NameID), 0, raw)
	hidden := testRecord(t, ns, "/projects/hidden.dat", owner, private, 0, nil)
	hidden.Policy.Searchable = false
	_ = hidden.Sign(private)
	raw, _ = hidden.Marshal()
	_, _ = service.Create(context.Background(), raw)
	result, err := service.Search(context.Background(), "/projects/", ".dat", 4, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 || result.Records[0].Generation != 1 || result.Complete {
		t.Fatalf("unexpected search result: %+v", result)
	}
}

type memorySearchIndex struct {
	mu                   sync.Mutex
	entries              map[string]struct{}
	attempted, completed int
}

type memoryCASAuthority struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (m *memoryCASAuthority) Read(_ context.Context, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.values[name]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), value...), nil
}
func (m *memoryCASAuthority) CompareAndSwap(_ context.Context, name string, expected, next []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.values[name]
	if expected == nil {
		if ok {
			return ErrConflict
		}
	} else if !ok || !bytes.Equal(current, expected) {
		return ErrConflict
	}
	m.values[name] = append([]byte(nil), next...)
	return nil
}

func TestSharedExactAuthorityAllowsOneCommitAcrossServices(t *testing.T) {
	owner, private := testKeys(t)
	ns, _ := NewNamespaceID()
	authority := &memoryCASAuthority{values: map[string][]byte{}}
	first := testService()
	second := testService()
	first.SetAuthority(authority)
	second.SetAuthority(authority)
	initial := testRecord(t, ns, "/distributed-race", owner, private, 0, nil)
	raw, _ := initial.Marshal()
	if _, err := first.Create(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	updates := []*NameRecord{testRecord(t, ns, "/distributed-race", owner, private, 1, initial), testRecord(t, ns, "/distributed-race", owner, private, 1, initial)}
	alternate := sha256.Sum256([]byte("alternate"))
	updates[1].ManifestKey = alternate[:]
	_ = updates[1].Sign(private)
	var commits atomic.Int32
	var conflicts atomic.Int32
	var wg sync.WaitGroup
	for index, service := range []*Service{first, second} {
		update := updates[index]
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := update.Marshal()
			_, err := service.Update(context.Background(), bytesToNameID(update.NameID), 0, raw)
			if err == nil {
				commits.Add(1)
			} else if errors.Is(err, ErrConflict) {
				conflicts.Add(1)
			} else {
				t.Errorf("update: %v", err)
			}
		}()
	}
	wg.Wait()
	if commits.Load() != 1 || conflicts.Load() != 1 {
		t.Fatalf("commits=%d conflicts=%d", commits.Load(), conflicts.Load())
	}
}

func (m *memorySearchIndex) Insert(_ context.Context, entry string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[entry] = struct{}{}
	return nil
}
func (m *memorySearchIndex) Delete(_ context.Context, entry string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, entry)
	return nil
}
func (m *memorySearchIndex) Query(_ context.Context, prefix, suffix string) ([]string, int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for entry := range m.entries {
		out = append(out, entry)
	}
	return out, m.attempted, m.completed, nil
}

func TestDistributedSearchIndexCardinalityTracksNamesNotVersions(t *testing.T) {
	owner, private := testKeys(t)
	ns, _ := NewNamespaceID()
	service := testService()
	index := &memorySearchIndex{entries: map[string]struct{}{}, attempted: 4, completed: 3}
	service.SetSearchIndex(index)
	initial := testRecord(t, ns, "/indexed", owner, private, 0, nil)
	raw, _ := initial.Marshal()
	if _, err := service.Create(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	current := initial
	for generation := uint64(1); generation <= 4; generation++ {
		next := testRecord(t, ns, "/indexed", owner, private, generation, current)
		raw, _ = next.Marshal()
		if _, err := service.Update(context.Background(), bytesToNameID(initial.NameID), generation-1, raw); err != nil {
			t.Fatal(err)
		}
		current = next
	}
	if len(index.entries) != 1 {
		t.Fatalf("index entries=%d for one name and five versions", len(index.entries))
	}
	result, err := service.Search(context.Background(), "/ind", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 || result.Records[0].Generation != 4 || result.Complete {
		t.Fatalf("result=%+v", result)
	}
	tombstone := testRecord(t, ns, "/indexed", owner, private, 5, current)
	tombstone.Tombstone = true
	tombstone.ManifestKey = nil
	_ = tombstone.Sign(private)
	raw, _ = tombstone.Marshal()
	if _, err := service.Delete(context.Background(), bytesToNameID(initial.NameID), 4, raw); err != nil {
		t.Fatal(err)
	}
	if len(index.entries) != 0 {
		t.Fatal("tombstoned name remained indexed")
	}
}

func TestLeaseFencingSurvivesRelease(t *testing.T) {
	owner, private := testKeys(t)
	service := testService()
	namespace, _ := NewNamespaceID()
	nameRecord := testRecord(t, namespace, "/lease-name", owner, private, 0, nil)
	nameRaw, _ := nameRecord.Marshal()
	if _, err := service.Create(context.Background(), nameRaw); err != nil {
		t.Fatal(err)
	}
	id := bytesToArray(nameRecord.NameID)
	rogue, roguePrivate := testKeys(t)
	unauthorized := &LeaseRecord{Version: FormatVersion, Scope: LeaseScope{NameID: id[:]}, Owner: rogue, Holder: rogue, Fencing: 1, Issued: time.Now().UnixNano(), Expires: time.Now().Add(time.Minute).UnixNano(), Nonce: bytes.Repeat([]byte{9}, 16)}
	_ = unauthorized.Sign(roguePrivate)
	unauthorizedRaw, _ := MarshalCanonical(unauthorized)
	if _, err := service.AcquireLease(context.Background(), unauthorizedRaw); err == nil {
		t.Fatal("unauthorized exact-name lease acquired")
	}
	lease := &LeaseRecord{Version: FormatVersion, Scope: LeaseScope{NameID: id[:]}, Owner: owner, Holder: owner, Fencing: 1, Issued: time.Now().UnixNano(), Expires: time.Now().Add(time.Minute).UnixNano(), Nonce: bytes.Repeat([]byte{1}, 16)}
	_ = lease.Sign(private)
	raw, _ := MarshalCanonical(lease)
	if _, err := service.AcquireLease(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcquireLease(context.Background(), raw); !errors.Is(err, ErrLocked) {
		t.Fatalf("second holder result: %v", err)
	}
	if err := service.ReleaseLease(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	lease.Fencing = 2
	lease.Issued = time.Now().UnixNano()
	lease.Expires = time.Now().Add(time.Minute).UnixNano()
	lease.Nonce = bytes.Repeat([]byte{2}, 16)
	_ = lease.Sign(private)
	raw, _ = MarshalCanonical(lease)
	if _, err := service.AcquireLease(context.Background(), raw); err != nil {
		t.Fatalf("fence did not advance: %v", err)
	}
}

func TestEncryptedChunkReuseAndVerification(t *testing.T) {
	owner, signer := testKeys(t)
	_ = owner
	readerPrivate := make([]byte, 32)
	if _, err := rand.Read(readerPrivate); err != nil {
		t.Fatal(err)
	}
	readerPublic, _ := curve25519.X25519(readerPrivate, curve25519.Basepoint)
	blocks := map[ContentKey][]byte{}
	sink := func(_ context.Context, data []byte) (ContentKey, error) {
		key := sha256.Sum256(data)
		blocks[key] = append([]byte(nil), data...)
		return key, nil
	}
	firstData := append(bytes.Repeat([]byte{'a'}, ObjectChunkSize), bytes.Repeat([]byte{'b'}, ObjectChunkSize)...)
	firstData = append(firstData, bytes.Repeat([]byte{'c'}, ObjectChunkSize)...)
	firstData = append(firstData, bytes.Repeat([]byte{'d'}, ObjectChunkSize+1)...)
	first, err := BuildObject(context.Background(), bytes.NewReader(firstData), sink, BuildObjectOptions{Encryption: "private", KeyEpoch: 9, ReaderPublicKeys: [][]byte{readerPublic}, Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	key, err := OpenDataKey(first.Manifest.Envelopes[0], readerPrivate, 9)
	if err != nil {
		t.Fatal(err)
	}
	secondData := append([]byte(nil), firstData...)
	secondData[ObjectChunkSize+7] = 'z'
	second, err := BuildObject(context.Background(), bytes.NewReader(secondData), sink, BuildObjectOptions{Encryption: "private", KeyEpoch: 9, DataKey: key, ReaderPublicKeys: [][]byte{readerPublic}, Previous: first.Manifest, Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	if second.ReusedBlocks < 3 || second.NewBlocks != 2 {
		t.Fatalf("reuse=%d new=%d", second.ReusedBlocks, second.NewBlocks)
	}
	var rebuilt bytes.Buffer
	source := func(_ context.Context, key ContentKey) ([]byte, error) {
		value, ok := blocks[key]
		if !ok {
			return nil, ErrNotFound
		}
		return append([]byte(nil), value...), nil
	}
	if err := ReconstructObject(context.Background(), second.Manifest, source, key, &rebuilt); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rebuilt.Bytes(), secondData) {
		t.Fatal("reconstructed data mismatch")
	}
	wrongPrivate := make([]byte, 32)
	_, _ = rand.Read(wrongPrivate)
	if _, err := OpenDataKey(second.Manifest.Envelopes[0], wrongPrivate, 9); err == nil {
		t.Fatal("unauthorized reader opened envelope")
	}
	tamperedKey := bytesToArray(second.Manifest.Chunks[0].CiphertextKey)
	blocks[tamperedKey][0] ^= 1
	rebuilt.Reset()
	if err := ReconstructObject(context.Background(), second.Manifest, source, key, &rebuilt); err == nil {
		t.Fatal("tampered chunk reconstructed")
	}
}

func TestProviderAndNameDHTValidatorsRejectForgeryAndFork(t *testing.T) {
	provider, private := testKeys(t)
	content := sha256.Sum256([]byte("provider-content"))
	claim := &ProviderClaim{Version: FormatVersion, ContentKey: content[:], Provider: provider, Address: "/ip4/127.0.0.1/tcp/2893", Timestamp: time.Now().UnixNano(), Expires: time.Now().Add(time.Hour).UnixNano(), Nonce: bytes.Repeat([]byte{7}, 16)}
	if err := claim.Sign(private); err != nil {
		t.Fatal(err)
	}
	raw, _ := MarshalCanonical(claim)
	key := "/providers/" + contentKeyString(content) + "/" + contentKeyString(bytesToArray(provider))
	validator := &ProviderClaimValidator{}
	if err := validator.Validate(key, raw); err != nil {
		t.Fatal(err)
	}
	claim.Address = "/ip4/127.0.0.1/tcp/1"
	forged, _ := MarshalCanonical(claim)
	if err := validator.Validate(key, forged); err == nil {
		t.Fatal("forged provider claim accepted")
	}

	owner, ownerPrivate := testKeys(t)
	ns, _ := NewNamespaceID()
	record := testRecord(t, ns, "/fork", owner, ownerPrivate, 0, nil)
	first, _ := record.Marshal()
	fork := *record
	forkKey := sha256.Sum256([]byte("fork"))
	fork.ManifestKey = forkKey[:]
	_ = fork.Sign(ownerPrivate)
	second, _ := fork.Marshal()
	id := bytesToNameID(record.NameID)
	nameValidator := &NameValidator{}
	if _, err := nameValidator.Select(DHTNameKey(id), [][]byte{first, second}); err == nil {
		t.Fatal("equal-generation fork selected")
	}
}

func contentKeyString(value [32]byte) string { return fmt.Sprintf("%x", value[:]) }

func FuzzDecodeSignedStructures(f *testing.F) {
	f.Add([]byte{0xa0})
	f.Add([]byte("not-cbor"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		_, _ = DecodeNameRecord(raw)
		_, _ = DecodeObjectManifest(raw)
		var claim ProviderClaim
		_ = UnmarshalCanonical(raw, &claim)
	})
}
