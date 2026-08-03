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
	ErrNotFound = errors.New("mutable name not found")
	ErrConflict = errors.New("mutable name generation conflict")
	ErrLocked   = errors.New("mutable name lease is held")
)

const (
	currentPrefix        = "/mutable/names/"
	historyPrefix        = "/mutable/history/"
	leasePrefix          = "/mutable/leases/"
	leaseFencePrefix     = "/mutable/lease-fences/"
	namespaceOwnerPrefix = "/mutable/namespace-owners/"
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

func (s *Service) Get(ctx context.Context, id NameID) (*NameRecord, []byte, error) {
	if s.authority != nil {
		raw, err := s.authority.Read(ctx, authorityName(id))
		if err != nil {
			return nil, nil, err
		}
		record, err := DecodeNameRecord(raw)
		if err != nil {
			return nil, nil, err
		}
		if !bytes.Equal(record.NameID, id[:]) || validateRecordEnvelope(record, s.now()) != nil {
			return nil, nil, errors.New("exact authority returned invalid name record")
		}
		return record, raw, nil
	}
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
	if best == nil {
		return nil, nil, ErrNotFound
	}
	return best, bestRaw, nil
}

func authorityName(id NameID) string { return "__tarsus_name_v1__" + id.String() }

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
	if !wasIndexed && isIndexed {
		err = s.index.Insert(ctx, searchIndexEntry(next))
	}
	if wasIndexed && !isIndexed {
		err = s.index.Delete(ctx, searchIndexEntry(previous))
	}
	if err != nil {
		s.indexIncomplete.Store(true)
	}
}

func (s *Service) searchDistributedIndex(ctx context.Context, prefix, suffix string) (SearchResult, error) {
	entries, attempted, completed, queryErr := s.index.Query(ctx, prefix, suffix)
	result := SearchResult{Records: []*NameRecord{}, FanoutAttempted: attempted, FanoutCompleted: completed, Complete: queryErr == nil && attempted == completed && !s.indexIncomplete.Load(), Scope: "current-searchable-names"}
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
	if s.authority != nil {
		expected := currentRaw
		if currentErr != nil {
			expected = nil
		}
		if err := s.authority.CompareAndSwap(ctx, authorityLeaseName(requested.Scope), expected, raw); err != nil {
			return nil, err
		}
	}
	batch, err := s.datastore.Batch(ctx)
	if err != nil {
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
	if s.authority != nil {
		if err := s.authority.CompareAndSwap(ctx, authorityLeaseName(requested.Scope), currentRaw, raw); err != nil {
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
	if s.authority != nil {
		if err := s.authority.CompareAndSwap(ctx, authorityLeaseName(requested.Scope), currentRaw, nil); err != nil {
			return err
		}
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
