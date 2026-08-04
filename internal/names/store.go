package names

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ds "github.com/ipfs/go-datastore"
	dsq "github.com/ipfs/go-datastore/query"
	"github.com/libp2p/go-libp2p/core/routing"
)

var (
	ErrNotFound              = errors.New("mutable name not found")
	ErrConflict              = errors.New("mutable name generation conflict")
	ErrLocked                = errors.New("mutable name lease is held")
	ErrCertificationRequired = errors.New("mutable name requires a quorum certificate")
)

const (
	currentPrefix        = "/mutable/names/"
	historyPrefix        = "/mutable/history/"
	leasePrefix          = "/mutable/leases/"
	leaseFencePrefix     = "/mutable/lease-fences/"
	namespaceOwnerPrefix = "/mutable/namespace-owners/"
	certifiedPrefix      = "/mutable/certified/"
	certifiedHistory     = "/mutable/certified-history/"
	namespaceRootPrefix  = "/mutable/namespace-roots/"
	leaseRegistryPrefix  = "/mutable/lease-registries/"
)

type PublicationGate func(context.Context, *NameRecord) error

type Service struct {
	datastore       ds.Batching
	network         routing.ValueStore
	now             func() time.Time
	gate            PublicationGate
	locks           sync.Map
	leaseMu         sync.Mutex
	index           SearchIndex
	indexIncomplete atomic.Bool
	indexPending    atomic.Int64
	indexError      atomic.Value
	onCommit        func(*NameRecord)
	authority       CASAuthority
}

// SearchIndex is the secondary PHT/Bloom plane. It contains one entry per
// current searchable logical name, never versions or provider copies.
type SearchIndex interface {
	Insert(context.Context, string) error
	Delete(context.Context, string) error
	Query(context.Context, string, string) ([]string, int, int, error)
}

type CASAuthority interface {
	Read(context.Context, string) ([]byte, error)
	CompareAndSwap(context.Context, string, []byte, []byte) error
}

type leaseRegistryEntry struct {
	Lease   []byte `json:"lease_cbor" cbor:"lease_cbor"`
	Path    string `json:"path" cbor:"path"`
	Subtree bool   `json:"subtree" cbor:"subtree"`
	Expires int64  `json:"expires_ns" cbor:"expires_ns"`
	Fencing uint64 `json:"fencing" cbor:"fencing"`
	Holder  []byte `json:"holder" cbor:"holder"`
}

type leaseRegistry struct {
	Version   uint64               `json:"version" cbor:"version"`
	Namespace []byte               `json:"namespace" cbor:"namespace"`
	Revision  uint64               `json:"revision" cbor:"revision"`
	Entries   []leaseRegistryEntry `json:"entries" cbor:"entries"`
}

func NewService(datastore ds.Batching, network routing.ValueStore, gate PublicationGate) *Service {
	return &Service{datastore: datastore, network: network, now: time.Now, gate: gate}
}

func (s *Service) SetSearchIndex(index SearchIndex)     { s.index = index }
func (s *Service) SetCommitHook(hook func(*NameRecord)) { s.onCommit = hook }
func (s *Service) SetAuthority(authority CASAuthority)  { s.authority = authority }

func (s *Service) lockFor(id NameID) *sync.Mutex {
	value, _ := s.locks.LoadOrStore(id.String(), &sync.Mutex{})
	return value.(*sync.Mutex)
}

func currentKey(id NameID) ds.Key { return ds.NewKey(currentPrefix + id.String()) }
func historyKey(id NameID, generation uint64) ds.Key {
	return ds.NewKey(fmt.Sprintf("%s%s/%020d", historyPrefix, id.String(), generation))
}
func certifiedKey(id NameID) ds.Key { return ds.NewKey(certifiedPrefix + id.String()) }
func certifiedHistoryKey(id NameID, generation uint64) ds.Key {
	return ds.NewKey(fmt.Sprintf("%s%s/%020d", certifiedHistory, id.String(), generation))
}
func namespaceRootKey(namespace []byte) ds.Key {
	return ds.NewKey(namespaceRootPrefix + fmt.Sprintf("%x", namespace))
}

func (s *Service) Create(ctx context.Context, raw []byte) (*NameRecord, error) {
	record, err := DecodeNameRecord(raw)
	if err != nil {
		return nil, err
	}
	if err := record.Validate(s.now(), nil); err != nil {
		return nil, err
	}
	if err := s.ensureNamespaceOwner(ctx, record); err != nil {
		return nil, err
	}
	id := bytesToNameID(record.NameID)
	if s.certificationRequired(ctx, id) {
		return nil, ErrCertificationRequired
	}
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	if _, err := s.datastore.Get(ctx, currentKey(id)); err == nil {
		return nil, ErrConflict
	} else if !errors.Is(err, ds.ErrNotFound) {
		return nil, err
	}
	if err := s.checkPublication(ctx, record); err != nil {
		return nil, err
	}
	if s.authority != nil {
		if err := s.authority.CompareAndSwap(ctx, authorityName(id), nil, raw); err != nil {
			return nil, err
		}
	}
	if err := s.persist(ctx, id, record.Generation, raw); err != nil {
		return nil, err
	}
	s.updateSearchIndex(ctx, nil, record)
	if s.onCommit != nil {
		s.onCommit(record)
	}
	s.publish(ctx, id, raw)
	return record, nil
}

func (s *Service) Update(ctx context.Context, id NameID, expected uint64, raw []byte) (*NameRecord, error) {
	if s.certificationRequired(ctx, id) {
		return nil, ErrCertificationRequired
	}
	next, err := DecodeNameRecord(raw)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(next.NameID, id[:]) {
		return nil, errors.New("request NameID does not match record")
	}
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	currentRaw, err := s.authoritativeCurrent(ctx, id)
	if errors.Is(err, ds.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	current, err := DecodeNameRecord(currentRaw)
	if err != nil {
		return nil, err
	}
	if current.Generation != expected {
		return nil, ErrConflict
	}
	if err := next.Validate(s.now(), current); err != nil {
		return nil, err
	}
	if err := s.checkPublication(ctx, next); err != nil {
		return nil, err
	}
	if s.authority != nil {
		if err := s.authority.CompareAndSwap(ctx, authorityName(id), currentRaw, raw); err != nil {
			return nil, err
		}
	}
	if err := s.persist(ctx, id, next.Generation, raw); err != nil {
		return nil, err
	}
	s.updateSearchIndex(ctx, current, next)
	if s.onCommit != nil {
		s.onCommit(next)
	}
	s.publish(ctx, id, raw)
	return next, nil
}

func (s *Service) Delete(ctx context.Context, id NameID, expected uint64, raw []byte) (*NameRecord, error) {
	record, err := DecodeNameRecord(raw)
	if err != nil {
		return nil, err
	}
	if !record.Tombstone {
		return nil, errors.New("delete requires a signed tombstone")
	}
	return s.Update(ctx, id, expected, raw)
}

// CommitCertified is the Byzantine-fault-tolerant mutation path. The mutable
// head advances only after a commit certificate from 2f+1 members of the
// owner-authenticated 3f+1 committee validates. The existing exact-owner CAS
// remains the atomic serialization point, so a certificate cannot bypass the
// generation rule or strict data-publication gate.
func (s *Service) CommitCertified(ctx context.Context, raw []byte) (*CertifiedNameRecord, *NameRecord, error) {
	var certified CertifiedNameRecord
	if err := UnmarshalCanonical(raw, &certified); err != nil {
		return nil, nil, err
	}
	record, err := certified.Validate(s.now())
	if err != nil {
		return nil, nil, err
	}
	id := bytesToNameID(record.NameID)
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	currentRaw, currentErr := s.authoritativeCurrent(ctx, id)
	var current *NameRecord
	sameCommittedHead := false
	if currentErr == nil {
		current, err = DecodeNameRecord(currentRaw)
		if err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(currentErr, ds.ErrNotFound) && !errors.Is(currentErr, ErrNotFound) {
		return nil, nil, currentErr
	}
	if current != nil && current.Generation == record.Generation {
		if !bytes.Equal(currentRaw, certified.Record) {
			return nil, nil, errors.New("equal-generation certified-name fork")
		}
		sameCommittedHead = true
	} else {
		if err := record.Validate(s.now(), current); err != nil {
			return nil, nil, err
		}
	}
	if err := s.ensureNamespaceOwner(ctx, record); err != nil {
		return nil, nil, err
	}
	rootRaw, err := MarshalCanonical(&certified.Root)
	if err != nil {
		return nil, nil, err
	}
	if existingRoot, rootErr := s.datastore.Get(ctx, namespaceRootKey(record.Namespace)); rootErr == nil {
		if !bytes.Equal(existingRoot, rootRaw) {
			return nil, nil, errors.New("namespace-root conflict; v1 committee membership is immutable")
		}
	} else if !errors.Is(rootErr, ds.ErrNotFound) {
		return nil, nil, rootErr
	}
	if s.authority != nil && !sameCommittedHead {
		if err := s.authority.CompareAndSwap(ctx, authorityNamespaceRootName(record.Namespace), nil, rootRaw); err != nil {
			existingRoot, readErr := s.authority.Read(ctx, authorityNamespaceRootName(record.Namespace))
			if readErr != nil || !bytes.Equal(existingRoot, rootRaw) {
				return nil, nil, errors.New("namespace-root authority conflict")
			}
		}
	}
	if err := s.checkPublication(ctx, record); err != nil {
		return nil, nil, err
	}
	if err := s.requireCertifiedMode(ctx, id); err != nil {
		return nil, nil, err
	}
	if s.authority != nil {
		expected := currentRaw
		if current == nil {
			expected = nil
		}
		if err := s.authority.CompareAndSwap(ctx, authorityName(id), expected, certified.Record); err != nil {
			return nil, nil, err
		}
	}
	batch, err := s.datastore.Batch(ctx)
	if err != nil {
		return nil, nil, err
	}
	for key, value := range map[ds.Key][]byte{
		currentKey(id):                             certified.Record,
		historyKey(id, record.Generation):          certified.Record,
		certifiedKey(id):                           raw,
		certifiedHistoryKey(id, record.Generation): raw,
		namespaceRootKey(certified.Root.Namespace): rootRaw,
	} {
		if err := batch.Put(ctx, key, value); err != nil {
			return nil, nil, err
		}
	}
	if err := batch.Commit(ctx); err != nil {
		return nil, nil, err
	}
	if !sameCommittedHead {
		s.updateSearchIndex(ctx, current, record)
	}
	if s.onCommit != nil && !sameCommittedHead {
		s.onCommit(record)
	}
	if !sameCommittedHead {
		s.publish(ctx, id, certified.Record)
		if s.network != nil {
			_ = s.network.PutValue(ctx, DHTNamespaceRootKey(bytesToNamespaceID(certified.Root.Namespace)), rootRaw)
			_ = s.network.PutValue(ctx, DHTCertifiedNameKey(id), raw)
		}
	}
	return &certified, record, nil
}

// WriteBackCertified durably installs a commit certificate on a quorum
// follower. Agreement is already established by the 2f+1 commit signatures;
// re-entering the crash-only exact-owner protocol here would add an owner
// election to every acknowledgment and can prevent BFT write completion.
// The follower still rejects a stale certificate, an equal-generation fork,
// or a different immutable v1 committee.
func (s *Service) WriteBackCertified(ctx context.Context, raw []byte) (*CertifiedNameRecord, *NameRecord, error) {
	var certified CertifiedNameRecord
	if err := UnmarshalCanonical(raw, &certified); err != nil {
		return nil, nil, err
	}
	record, err := certified.Validate(s.now())
	if err != nil {
		return nil, nil, err
	}
	id := bytesToNameID(record.NameID)
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	var previous *NameRecord
	if currentRaw, currentErr := s.datastore.Get(ctx, currentKey(id)); currentErr == nil {
		previous, err = DecodeNameRecord(currentRaw)
		if err != nil {
			return nil, nil, err
		}
		if previous.Generation > record.Generation {
			return nil, nil, errors.New("certified write-back is stale")
		}
		if previous.Generation == record.Generation && !bytes.Equal(currentRaw, certified.Record) {
			return nil, nil, errors.New("equal-generation certified-name fork")
		}
	} else if !errors.Is(currentErr, ds.ErrNotFound) {
		return nil, nil, currentErr
	}
	rootRaw, err := MarshalCanonical(&certified.Root)
	if err != nil {
		return nil, nil, err
	}
	if existingRoot, rootErr := s.datastore.Get(ctx, namespaceRootKey(record.Namespace)); rootErr == nil {
		if !bytes.Equal(existingRoot, rootRaw) {
			return nil, nil, errors.New("namespace-root conflict; v1 committee membership is immutable")
		}
	} else if !errors.Is(rootErr, ds.ErrNotFound) {
		return nil, nil, rootErr
	}
	if owner, ownerErr := s.datastore.Get(ctx, namespaceOwnerKey(record.Namespace)); ownerErr == nil {
		if !bytes.Equal(owner, record.Owner) {
			return nil, nil, errors.New("namespace is owned by a different key")
		}
	} else if !errors.Is(ownerErr, ds.ErrNotFound) {
		return nil, nil, ownerErr
	}
	batch, err := s.datastore.Batch(ctx)
	if err != nil {
		return nil, nil, err
	}
	for key, value := range map[ds.Key][]byte{
		currentKey(id):                             certified.Record,
		historyKey(id, record.Generation):          certified.Record,
		certifiedKey(id):                           raw,
		certifiedHistoryKey(id, record.Generation): raw,
		namespaceRootKey(certified.Root.Namespace): rootRaw,
		namespaceOwnerKey(record.Namespace):        append([]byte(nil), record.Owner...),
	} {
		if err := batch.Put(ctx, key, value); err != nil {
			return nil, nil, err
		}
	}
	if err := batch.Commit(ctx); err != nil {
		return nil, nil, err
	}
	if previous == nil || previous.Generation < record.Generation {
		s.updateSearchIndex(ctx, previous, record)
	}
	return &certified, record, nil
}

// GetCertified resolves only quorum-certified heads. It never upgrades an
// unsigned or merely owner-signed DHT value into Byzantine agreement.
func (s *Service) GetCertified(ctx context.Context, id NameID) (*CertifiedNameRecord, *NameRecord, []byte, error) {
	var bestRaw []byte
	if s.datastore != nil {
		bestRaw, _ = s.datastore.Get(ctx, certifiedKey(id))
	}
	bestCertified, bestRecord := decodeCertified(bestRaw, s.now(), id)
	if s.network != nil {
		if remoteRaw, err := s.network.GetValue(ctx, DHTCertifiedNameKey(id)); err == nil {
			remoteCertified, remoteRecord := decodeCertified(remoteRaw, s.now(), id)
			if remoteRecord != nil && (bestRecord == nil || remoteRecord.Generation > bestRecord.Generation) {
				bestRaw, bestCertified, bestRecord = remoteRaw, remoteCertified, remoteRecord
			} else if remoteRecord != nil && bestRecord != nil && remoteRecord.Generation == bestRecord.Generation {
				if !bytes.Equal(remoteCertified.Record, bestCertified.Record) {
					return nil, nil, nil, errors.New("equal-generation certified-name fork")
				}
				if len(remoteCertified.Commit.Votes) > len(bestCertified.Commit.Votes) {
					bestRaw, bestCertified, bestRecord = remoteRaw, remoteCertified, remoteRecord
				}
			}
		}
	}
	if bestRecord == nil {
		return nil, nil, nil, ErrNotFound
	}
	return bestCertified, bestRecord, bestRaw, nil
}

func decodeCertified(raw []byte, now time.Time, id NameID) (*CertifiedNameRecord, *NameRecord) {
	if len(raw) == 0 {
		return nil, nil
	}
	var certified CertifiedNameRecord
	if err := UnmarshalCanonical(raw, &certified); err != nil {
		return nil, nil
	}
	record, err := certified.Validate(now)
	if err != nil || !bytes.Equal(record.NameID, id[:]) {
		return nil, nil
	}
	return &certified, record
}

func bytesToNamespaceID(value []byte) NamespaceID {
	var id NamespaceID
	copy(id[:], value)
	return id
}

func mustCanonical(value any) []byte {
	raw, _ := MarshalCanonical(value)
	return raw
}

func (s *Service) Get(ctx context.Context, id NameID) (*NameRecord, []byte, error) {
	var localRaw []byte
	if s.datastore != nil {
		cached, _ := s.datastore.Get(ctx, currentKey(id))
		local, _ := DecodeNameRecord(localRaw)
		candidate, _ := DecodeNameRecord(cached)
		if local == nil || candidate != nil && candidate.Generation > local.Generation {
			localRaw = cached
		}
	}
	bestRaw := localRaw
	best, _ := DecodeNameRecord(localRaw)
	if s.network != nil {
		if remoteRaw, err := s.network.GetValue(ctx, DHTNameKey(id)); err == nil {
			if remote, decodeErr := DecodeNameRecord(remoteRaw); decodeErr == nil && validateRecordEnvelope(remote, s.now()) == nil && bytes.Equal(remote.NameID, id[:]) {
				if best == nil || remote.Generation > best.Generation {
					best, bestRaw = remote, remoteRaw
				}
			}
		}
	}
	// Resolution is a direct NameID DHT lookup. The fenced exact owner is the
	// mutation/CAS authority, not the normal read path: routing every read
	// through that protocol added an owner election and an overlay stream to a
	// lookup that already has a canonical DHT key. The update path performs its
	// PutValue synchronously before returning, so normal post-commit reads see
	// the committed head without another authority round trip.
	// The authority remains a recovery fallback when neither the local cache
	// nor the DHT can supply a verified record.
	if best == nil && s.authority != nil {
		authorityRaw, err := s.authority.Read(ctx, authorityName(id))
		if err == nil {
			authorityRecord, decodeErr := DecodeNameRecord(authorityRaw)
			if decodeErr == nil && bytes.Equal(authorityRecord.NameID, id[:]) && validateRecordEnvelope(authorityRecord, s.now()) == nil {
				best, bestRaw = authorityRecord, authorityRaw
			}
		}
	}
	if best == nil {
		return nil, nil, ErrNotFound
	}
	return best, bestRaw, nil
}

// GetAgreementHead is used by validators before voting. Unlike the raw
// compatibility resolver, it refuses to downgrade a name after certified
// mode has been established at the shared authority.
func (s *Service) GetAgreementHead(ctx context.Context, id NameID) (*NameRecord, []byte, error) {
	if s.certificationRequired(ctx, id) {
		certified, record, _, err := s.GetCertified(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		return record, append([]byte(nil), certified.Record...), nil
	}
	return s.Get(ctx, id)
}

func authorityName(id NameID) string { return "__tarsus_name_v1__" + id.String() }

func authorityCertifiedModeName(id NameID) string {
	return "__tarsus_certified_mode_v1__" + id.String()
}

func authorityNamespaceRootName(namespace []byte) string {
	return "__tarsus_namespace_root_v1__" + fmt.Sprintf("%x", namespace)
}

func (s *Service) certificationRequired(ctx context.Context, id NameID) bool {
	if s.authority != nil {
		if raw, err := s.authority.Read(ctx, authorityCertifiedModeName(id)); err == nil && bytes.Equal(raw, []byte{1}) {
			return true
		}
	}
	_, err := s.datastore.Get(ctx, certifiedKey(id))
	return err == nil
}

func (s *Service) requireCertifiedMode(ctx context.Context, id NameID) error {
	if s.authority != nil {
		if err := s.authority.CompareAndSwap(ctx, authorityCertifiedModeName(id), nil, []byte{1}); err != nil {
			current, readErr := s.authority.Read(ctx, authorityCertifiedModeName(id))
			if readErr != nil || !bytes.Equal(current, []byte{1}) {
				return errors.New("cannot establish certified-name mode")
			}
		}
	}
	return nil
}

func namespaceOwnerKey(namespace []byte) ds.Key {
	return ds.NewKey(namespaceOwnerPrefix + fmt.Sprintf("%x", namespace))
}

func (s *Service) ensureNamespaceOwner(ctx context.Context, record *NameRecord) error {
	key := namespaceOwnerKey(record.Namespace)
	if current, err := s.datastore.Get(ctx, key); err == nil {
		if !bytes.Equal(current, record.Owner) {
			return errors.New("namespace is owned by a different key")
		}
		return nil
	} else if !errors.Is(err, ds.ErrNotFound) {
		return err
	}
	if s.authority != nil {
		name := "__tarsus_namespace_v1__" + fmt.Sprintf("%x", record.Namespace)
		if err := s.authority.CompareAndSwap(ctx, name, nil, record.Owner); err != nil {
			current, readErr := s.authority.Read(ctx, name)
			if readErr != nil || !bytes.Equal(current, record.Owner) {
				return errors.New("namespace ownership conflict")
			}
		}
	}
	return s.datastore.Put(ctx, key, record.Owner)
}

func (s *Service) authoritativeCurrent(ctx context.Context, id NameID) ([]byte, error) {
	if s.authority != nil {
		return s.authority.Read(ctx, authorityName(id))
	}
	return s.datastore.Get(ctx, currentKey(id))
}

func (s *Service) checkPublication(ctx context.Context, record *NameRecord) error {
	if record.Tombstone || !record.Policy.StrictPublish || s.gate == nil {
		return nil
	}
	if err := s.gate(ctx, record); err != nil {
		return fmt.Errorf("strict publication rejected: %w", err)
	}
	return nil
}

func (s *Service) persist(ctx context.Context, id NameID, generation uint64, raw []byte) error {
	batch, err := s.datastore.Batch(ctx)
	if err != nil {
		return err
	}
	if err := batch.Put(ctx, currentKey(id), raw); err != nil {
		return err
	}
	if err := batch.Put(ctx, historyKey(id, generation), raw); err != nil {
		return err
	}
	return batch.Commit(ctx)
}

func (s *Service) publish(ctx context.Context, id NameID, raw []byte) {
	if s.network == nil {
		return
	}
	// A failed DHT write does not undo the fenced local authority commit. The
	// controller retries publication; resolution can still reach the owner.
	_ = s.network.PutValue(ctx, DHTNameKey(id), raw)
}

type SearchResult struct {
	Records         []*NameRecord `json:"records"`
	FanoutAttempted int           `json:"fanout_attempted"`
	FanoutCompleted int           `json:"fanout_completed"`
	Complete        bool          `json:"complete"`
	Scope           string        `json:"scope"`
	IndexRepairs    int64         `json:"index_repairs_pending,omitempty"`
	IncompleteCause string        `json:"incomplete_cause,omitempty"`
}

func (s *Service) Search(ctx context.Context, prefix, suffix string, fanoutAttempted, fanoutCompleted int) (SearchResult, error) {
	if s.index != nil {
		return s.searchDistributedIndex(ctx, prefix, suffix)
	}
	result := SearchResult{Records: []*NameRecord{}, FanoutAttempted: fanoutAttempted, FanoutCompleted: fanoutCompleted, Complete: fanoutAttempted == fanoutCompleted, Scope: "current-searchable-names"}
	queryResult, err := s.datastore.Query(ctx, dsq.Query{Prefix: currentPrefix})
	if err != nil {
		return result, err
	}
	defer queryResult.Close()
	for entry := range queryResult.Next() {
		if entry.Error != nil {
			result.Complete = false
			continue
		}
		record, err := DecodeNameRecord(entry.Value)
		if err != nil || validateRecordEnvelope(record, s.now()) != nil {
			result.Complete = false
			continue
		}
		if record.Tombstone || !record.Policy.Searchable {
			continue
		}
		if prefix != "" && !strings.HasPrefix(record.Path, prefix) {
			continue
		}
		if suffix != "" && !strings.HasSuffix(record.Path, suffix) {
			continue
		}
		result.Records = append(result.Records, record)
	}
	sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].Path < result.Records[j].Path })
	return result, nil
}

func searchIndexEntry(record *NameRecord) string {
	return record.Path + "\x00" + fmt.Sprintf("%x", record.Namespace)
}

func parseSearchIndexEntry(entry string) (NameID, error) {
	cut := strings.LastIndexByte(entry, 0)
	if cut < 0 {
		return NameID{}, errors.New("malformed name index entry")
	}
	namespace, err := ParseNamespaceID(entry[cut+1:])
	if err != nil {
		return NameID{}, err
	}
	normalized, err := NormalizePath(entry[:cut])
	if err != nil || normalized != entry[:cut] {
		return NameID{}, errors.New("malformed indexed path")
	}
	return DeriveNameID(namespace, normalized), nil
}

func (s *Service) updateSearchIndex(ctx context.Context, previous, next *NameRecord) {
	if s.index == nil {
		return
	}
	wasIndexed := previous != nil && !previous.Tombstone && previous.Policy.Searchable
	isIndexed := next != nil && !next.Tombstone && next.Policy.Searchable
	var err error
	operation := ""
	entry := ""
	if !wasIndexed && isIndexed {
		operation, entry = "insert", searchIndexEntry(next)
		err = s.index.Insert(ctx, entry)
	}
	if wasIndexed && !isIndexed {
		operation, entry = "delete", searchIndexEntry(previous)
		err = s.index.Delete(ctx, entry)
	}
	if err != nil {
		s.indexIncomplete.Store(true)
		s.indexPending.Add(1)
		s.indexError.Store(err.Error())
		go s.repairSearchIndex(operation, entry)
	}
}

// repairSearchIndex is the policy-controller retry path for the secondary
// index. NameRecord commit remains authoritative; an index failure is exposed
// as incomplete search until this idempotent repair succeeds.
func (s *Service) repairSearchIndex(operation, entry string) {
	delay := 100 * time.Millisecond
	for {
		timer := time.NewTimer(delay)
		<-timer.C
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		var err error
		switch operation {
		case "insert":
			err = s.index.Insert(ctx, entry)
		case "delete":
			err = s.index.Delete(ctx, entry)
		default:
			err = errors.New("unknown search-index repair operation")
		}
		cancel()
		if err == nil {
			if s.indexPending.Add(-1) == 0 {
				s.indexIncomplete.Store(false)
				s.indexError.Store("")
			}
			return
		}
		s.indexError.Store(err.Error())
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func (s *Service) annotateIndexCompleteness(result *SearchResult) {
	result.IndexRepairs = s.indexPending.Load()
	if value := s.indexError.Load(); value != nil {
		result.IncompleteCause, _ = value.(string)
	}
}

func (s *Service) searchDistributedIndex(ctx context.Context, prefix, suffix string) (SearchResult, error) {
	entries, attempted, completed, queryErr := s.index.Query(ctx, prefix, suffix)
	result := SearchResult{Records: []*NameRecord{}, FanoutAttempted: attempted, FanoutCompleted: completed, Complete: queryErr == nil && attempted == completed && !s.indexIncomplete.Load(), Scope: "current-searchable-names"}
	s.annotateIndexCompleteness(&result)
	seen := make(map[NameID]struct{})
	for _, entry := range entries {
		id, err := parseSearchIndexEntry(entry)
		if err != nil {
			result.Complete = false
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		record, _, err := s.Get(ctx, id)
		if err != nil {
			result.Complete = false
			continue
		}
		if record.Tombstone || !record.Policy.Searchable || prefix != "" && !strings.HasPrefix(record.Path, prefix) || suffix != "" && !strings.HasSuffix(record.Path, suffix) {
			continue
		}
		result.Records = append(result.Records, record)
	}
	sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].Path < result.Records[j].Path })
	return result, queryErr
}

func (s *Service) Rename(ctx context.Context, oldID, newID NameID, oldExpected uint64, newRaw, tombstoneRaw []byte) error {
	if s.certificationRequired(ctx, oldID) || s.certificationRequired(ctx, newID) {
		return ErrCertificationRequired
	}
	ids := []NameID{oldID, newID}
	sort.Slice(ids, func(i, j int) bool { return bytes.Compare(ids[i][:], ids[j][:]) < 0 })
	first := s.lockFor(ids[0])
	second := s.lockFor(ids[1])
	first.Lock()
	defer first.Unlock()
	second.Lock()
	defer second.Unlock()
	oldRaw, err := s.datastore.Get(ctx, currentKey(oldID))
	if err != nil {
		return ErrNotFound
	}
	oldRecord, err := DecodeNameRecord(oldRaw)
	if err != nil {
		return err
	}
	if oldRecord.Generation != oldExpected {
		return ErrConflict
	}
	if _, err := s.datastore.Get(ctx, currentKey(newID)); err == nil {
		return ErrConflict
	} else if !errors.Is(err, ds.ErrNotFound) {
		return err
	}
	newRecord, err := DecodeNameRecord(newRaw)
	if err != nil {
		return err
	}
	tombstone, err := DecodeNameRecord(tombstoneRaw)
	if err != nil {
		return err
	}
	if !bytes.Equal(newRecord.NameID, newID[:]) || !bytes.Equal(tombstone.NameID, oldID[:]) || !tombstone.Tombstone {
		return errors.New("rename records do not match requested names")
	}
	if err := newRecord.Validate(s.now(), nil); err != nil {
		return err
	}
	if err := tombstone.Validate(s.now(), oldRecord); err != nil {
		return err
	}
	if err := s.checkPublication(ctx, newRecord); err != nil {
		return err
	}
	batch, err := s.datastore.Batch(ctx)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		id     NameID
		record *NameRecord
		raw    []byte
	}{{newID, newRecord, newRaw}, {oldID, tombstone, tombstoneRaw}} {
		if err := batch.Put(ctx, currentKey(item.id), item.raw); err != nil {
			return err
		}
		if err := batch.Put(ctx, historyKey(item.id, item.record.Generation), item.raw); err != nil {
			return err
		}
	}
	if err := batch.Commit(ctx); err != nil {
		return err
	}
	s.updateSearchIndex(ctx, nil, newRecord)
	s.updateSearchIndex(ctx, oldRecord, tombstone)
	if s.onCommit != nil {
		s.onCommit(newRecord)
		s.onCommit(tombstone)
	}
	s.publish(ctx, newID, newRaw)
	s.publish(ctx, oldID, tombstoneRaw)
	return nil
}

func leaseDSKey(scope LeaseScope) (ds.Key, error) {
	key, err := LeaseKey(scope)
	if err != nil {
		return ds.Key{}, err
	}
	return ds.NewKey(leasePrefix + strings.TrimPrefix(key, "/leases/")), nil
}

func (s *Service) AcquireLease(ctx context.Context, raw []byte) (*LeaseRecord, error) {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	var requested LeaseRecord
	if err := UnmarshalCanonical(raw, &requested); err != nil {
		return nil, err
	}
	if err := requested.Validate(s.now()); err != nil {
		return nil, err
	}
	if err := s.authorizeLease(ctx, &requested); err != nil {
		return nil, err
	}
	key, _ := leaseDSKey(requested.Scope)
	currentRaw, currentErr := s.leaseCurrent(ctx, requested.Scope, key)
	if currentErr == nil {
		var current LeaseRecord
		if UnmarshalCanonical(currentRaw, &current) == nil && current.Expires > s.now().UnixNano() {
			return nil, ErrLocked
		}
	}
	nextFence := s.nextFence(ctx, key)
	if currentErr == nil {
		var current LeaseRecord
		if UnmarshalCanonical(currentRaw, &current) == nil && current.Fencing >= nextFence {
			nextFence = current.Fencing + 1
		}
	}
	if requested.Fencing != nextFence {
		return nil, ErrConflict
	}
	namespace, lockPath, subtree, err := s.leaseNamespacePath(ctx, &requested)
	if err != nil {
		return nil, err
	}
	if err := s.reserveLease(ctx, namespace, lockPath, subtree, &requested, raw); err != nil {
		return nil, err
	}
	if s.authority != nil {
		expected := currentRaw
		if currentErr != nil {
			expected = nil
		}
		if err := s.authority.CompareAndSwap(ctx, authorityLeaseName(requested.Scope), expected, raw); err != nil {
			_ = s.removeLeaseReservation(ctx, namespace, &requested)
			return nil, err
		}
	}
	batch, err := s.datastore.Batch(ctx)
	if err != nil {
		_ = s.removeLeaseReservation(ctx, namespace, &requested)
		return nil, err
	}
	if err := batch.Put(ctx, key, raw); err != nil {
		return nil, err
	}
	var encodedFence [8]byte
	binary.BigEndian.PutUint64(encodedFence[:], requested.Fencing)
	if err := batch.Put(ctx, leaseFenceKey(key), encodedFence[:]); err != nil {
		return nil, err
	}
	if err := batch.Commit(ctx); err != nil {
		_ = s.removeLeaseReservation(ctx, namespace, &requested)
		return nil, err
	}
	if s.network != nil {
		if networkKey, err := LeaseKey(requested.Scope); err == nil {
			_ = s.network.PutValue(ctx, networkKey, raw)
		}
	}
	return &requested, nil
}

func (s *Service) RenewLease(ctx context.Context, raw []byte) (*LeaseRecord, error) {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	var requested LeaseRecord
	if err := UnmarshalCanonical(raw, &requested); err != nil {
		return nil, err
	}
	if err := requested.Validate(s.now()); err != nil {
		return nil, err
	}
	key, _ := leaseDSKey(requested.Scope)
	currentRaw, err := s.leaseCurrent(ctx, requested.Scope, key)
	if err != nil {
		return nil, ErrNotFound
	}
	var current LeaseRecord
	if err := UnmarshalCanonical(currentRaw, &current); err != nil {
		return nil, err
	}
	if current.Expires <= s.now().UnixNano() || requested.Fencing != current.Fencing || !bytes.Equal(requested.Holder, current.Holder) || requested.Issued < current.Issued {
		return nil, ErrConflict
	}
	namespace, _, _, err := s.leaseNamespacePath(ctx, &requested)
	if err != nil {
		return nil, err
	}
	if err := s.replaceLeaseReservation(ctx, namespace, &requested, raw); err != nil {
		return nil, err
	}
	if s.authority != nil {
		if err := s.authority.CompareAndSwap(ctx, authorityLeaseName(requested.Scope), currentRaw, raw); err != nil {
			_ = s.replaceLeaseReservation(ctx, namespace, &current, currentRaw)
			return nil, err
		}
	}
	if err := s.datastore.Put(ctx, key, raw); err != nil {
		return nil, err
	}
	return &requested, nil
}

func (s *Service) ReleaseLease(ctx context.Context, raw []byte) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	var requested LeaseRecord
	if err := UnmarshalCanonical(raw, &requested); err != nil {
		return err
	}
	if err := requested.Validate(s.now()); err != nil {
		return err
	}
	key, _ := leaseDSKey(requested.Scope)
	currentRaw, err := s.leaseCurrent(ctx, requested.Scope, key)
	if err != nil {
		return ErrNotFound
	}
	var current LeaseRecord
	if err := UnmarshalCanonical(currentRaw, &current); err != nil {
		return err
	}
	if requested.Fencing != current.Fencing || !bytes.Equal(requested.Holder, current.Holder) {
		return ErrConflict
	}
	namespace, _, _, err := s.leaseNamespacePath(ctx, &requested)
	if err != nil {
		return err
	}
	if s.authority != nil {
		if err := s.authority.CompareAndSwap(ctx, authorityLeaseName(requested.Scope), currentRaw, nil); err != nil {
			return err
		}
	}
	if err := s.removeLeaseReservation(ctx, namespace, &requested); err != nil {
		return err
	}
	return s.datastore.Delete(ctx, key)
}

func authorityLeaseName(scope LeaseScope) string {
	key, _ := LeaseKey(scope)
	return "__tarsus_lease_v1__" + key
}
func (s *Service) leaseCurrent(ctx context.Context, scope LeaseScope, key ds.Key) ([]byte, error) {
	if s.authority != nil {
		return s.authority.Read(ctx, authorityLeaseName(scope))
	}
	return s.datastore.Get(ctx, key)
}

func (s *Service) nextFence(ctx context.Context, key ds.Key) uint64 {
	currentRaw, err := s.datastore.Get(ctx, leaseFenceKey(key))
	if err != nil || len(currentRaw) != 8 {
		return 1
	}
	return binary.BigEndian.Uint64(currentRaw) + 1
}

func (s *Service) authorizeLease(ctx context.Context, lease *LeaseRecord) error {
	if len(lease.Scope.NameID) == 32 {
		record, _, err := s.Get(ctx, bytesToNameID(lease.Scope.NameID))
		if err != nil {
			return err
		}
		if !bytes.Equal(record.Owner, lease.Owner) {
			return errors.New("lease owner does not own exact name")
		}
		return nil
	}
	owner, err := s.datastore.Get(ctx, namespaceOwnerKey(lease.Scope.Namespace))
	if err != nil && s.authority != nil {
		owner, err = s.authority.Read(ctx, "__tarsus_namespace_v1__"+fmt.Sprintf("%x", lease.Scope.Namespace))
	}
	if err != nil {
		return errors.New("unknown namespace owner")
	}
	if !bytes.Equal(owner, lease.Owner) {
		return errors.New("lease owner does not own subtree namespace")
	}
	return nil
}

func leaseFenceKey(key ds.Key) ds.Key {
	return ds.NewKey(leaseFencePrefix + strings.TrimPrefix(key.String(), leasePrefix))
}

func leaseRegistryDSKey(namespace []byte) ds.Key {
	return ds.NewKey(leaseRegistryPrefix + fmt.Sprintf("%x", namespace))
}

func authorityLeaseRegistryName(namespace []byte) string {
	return "__tarsus_lease_registry_v1__" + fmt.Sprintf("%x", namespace)
}

func (s *Service) leaseNamespacePath(ctx context.Context, lease *LeaseRecord) ([]byte, string, bool, error) {
	if len(lease.Scope.NameID) == 32 {
		record, _, err := s.Get(ctx, bytesToNameID(lease.Scope.NameID))
		if err != nil {
			return nil, "", false, err
		}
		return append([]byte(nil), record.Namespace...), record.Path, false, nil
	}
	return append([]byte(nil), lease.Scope.Namespace...), lease.Scope.PathPrefix, true, nil
}

func (s *Service) currentLeaseRegistry(ctx context.Context, namespace []byte) ([]byte, *leaseRegistry, error) {
	var raw []byte
	var err error
	if s.authority != nil {
		raw, err = s.authority.Read(ctx, authorityLeaseRegistryName(namespace))
	} else {
		raw, err = s.datastore.Get(ctx, leaseRegistryDSKey(namespace))
	}
	if errors.Is(err, ds.ErrNotFound) || errors.Is(err, ErrNotFound) {
		return nil, &leaseRegistry{Version: FormatVersion, Namespace: append([]byte(nil), namespace...)}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var registry leaseRegistry
	if err := UnmarshalCanonical(raw, &registry); err != nil {
		return nil, nil, err
	}
	if registry.Version != FormatVersion || !bytes.Equal(registry.Namespace, namespace) {
		return nil, nil, errors.New("invalid lease registry")
	}
	return raw, &registry, nil
}

func overlappingLeasePaths(a string, aSubtree bool, b string, bSubtree bool) bool {
	if !aSubtree && !bSubtree {
		return a == b
	}
	if aSubtree && bSubtree {
		return pathWithinPrefix(a, b) || pathWithinPrefix(b, a)
	}
	if aSubtree {
		return pathWithinPrefix(b, a)
	}
	return pathWithinPrefix(a, b)
}

func (s *Service) writeLeaseRegistry(ctx context.Context, namespace, expected []byte, registry *leaseRegistry) error {
	registry.Revision++
	next, err := MarshalCanonical(registry)
	if err != nil {
		return err
	}
	if s.authority != nil {
		if err := s.authority.CompareAndSwap(ctx, authorityLeaseRegistryName(namespace), expected, next); err != nil {
			return err
		}
	}
	return s.datastore.Put(ctx, leaseRegistryDSKey(namespace), next)
}

func (s *Service) reserveLease(ctx context.Context, namespace []byte, lockPath string, subtree bool, lease *LeaseRecord, raw []byte) error {
	expected, registry, err := s.currentLeaseRegistry(ctx, namespace)
	if err != nil {
		return err
	}
	now := s.now().UnixNano()
	active := registry.Entries[:0]
	for _, entry := range registry.Entries {
		if entry.Expires <= now {
			continue
		}
		if overlappingLeasePaths(lockPath, subtree, entry.Path, entry.Subtree) {
			return ErrLocked
		}
		active = append(active, entry)
	}
	registry.Entries = append(active, leaseRegistryEntry{Lease: append([]byte(nil), raw...), Path: lockPath, Subtree: subtree, Expires: lease.Expires, Fencing: lease.Fencing, Holder: append([]byte(nil), lease.Holder...)})
	return s.writeLeaseRegistry(ctx, namespace, expected, registry)
}

func (s *Service) replaceLeaseReservation(ctx context.Context, namespace []byte, lease *LeaseRecord, raw []byte) error {
	expected, registry, err := s.currentLeaseRegistry(ctx, namespace)
	if err != nil {
		return err
	}
	replaced := false
	for i := range registry.Entries {
		entry := &registry.Entries[i]
		if entry.Fencing == lease.Fencing && bytes.Equal(entry.Holder, lease.Holder) {
			var existing LeaseRecord
			if UnmarshalCanonical(entry.Lease, &existing) == nil && leaseScopesEqual(existing.Scope, lease.Scope) {
				entry.Lease = append([]byte(nil), raw...)
				entry.Expires = lease.Expires
				replaced = true
				break
			}
		}
	}
	if !replaced {
		return ErrConflict
	}
	return s.writeLeaseRegistry(ctx, namespace, expected, registry)
}

func (s *Service) removeLeaseReservation(ctx context.Context, namespace []byte, lease *LeaseRecord) error {
	expected, registry, err := s.currentLeaseRegistry(ctx, namespace)
	if err != nil {
		return err
	}
	entries := registry.Entries[:0]
	removed := false
	for _, entry := range registry.Entries {
		var existing LeaseRecord
		if !removed && entry.Fencing == lease.Fencing && bytes.Equal(entry.Holder, lease.Holder) &&
			UnmarshalCanonical(entry.Lease, &existing) == nil && leaseScopesEqual(existing.Scope, lease.Scope) {
			removed = true
			continue
		}
		entries = append(entries, entry)
	}
	if !removed {
		return ErrNotFound
	}
	registry.Entries = entries
	return s.writeLeaseRegistry(ctx, namespace, expected, registry)
}

func leaseScopesEqual(a, b LeaseScope) bool {
	return bytes.Equal(a.NameID, b.NameID) && bytes.Equal(a.Namespace, b.Namespace) && a.PathPrefix == b.PathPrefix
}
