// Purpose: PeerStore with datastore persistence, scoring, and dial-candidate selection.

package net

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	ds "github.com/ipfs/go-datastore"
	dsnames "github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-datastore/query"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	peerstoreNS       = "/peers"
	defaultStaleAge   = 24 * time.Hour
	defaultMaxFail    = 8
	defaultExpireNone = int64(0)
)

// PeerRecord captures persistent metadata for a known peer.
type PeerRecord struct {
	PeerID        string   `json:"peer_id"`
	Addrs         []string `json:"addrs"`
	Services      uint64   `json:"services"`
	LastSeenUnix  int64    `json:"last_seen_unix"`
	LastTriedUnix int64    `json:"last_tried_unix"`
	LastSuccUnix  int64    `json:"last_succ_unix"`
	FailureCount  int      `json:"failure_count"`
	Source        string   `json:"source"`
	Score         float64  `json:"score"`
	ExpireAtUnix  int64    `json:"expire_at_unix"`
}

// PeerStore maintains an in-memory index backed by a datastore namespace.
type PeerStore struct {
	mu   sync.RWMutex
	ds   ds.Batching
	nsp  ds.Batching
	byID map[string]*PeerRecord

	// policy knobs
	staleAge time.Duration
	maxFail  int
	maxKnown int
}

// NewPeerStore constructs a PeerStore and loads existing records from ds under a private namespace.
func NewPeerStore(store ds.Batching) (*PeerStore, error) {
	ns := dsnames.Wrap(store, ds.NewKey(peerstoreNS))
	ps := &PeerStore{
		ds:       store,
		nsp:      ns,
		byID:     make(map[string]*PeerRecord),
		staleAge: defaultStaleAge,
		maxFail:  defaultMaxFail,
		maxKnown: 5000,
	}
	if err := ps.loadAll(context.Background()); err != nil {
		return nil, err
	}
	return ps, nil
}

// SetPolicy allows overriding defaults for pruning.
func (ps *PeerStore) SetPolicy(staleAge time.Duration, maxFailures int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if staleAge > 0 {
		ps.staleAge = staleAge
	}
	if maxFailures > 0 {
		ps.maxFail = maxFailures
	}
}

// SetMaxKnown sets a soft cap for the number of peers tracked.
func (ps *PeerStore) SetMaxKnown(n int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.maxKnown = n
}

// Upsert records a peer with optional services and source. Addrs are merged and deduplicated.
func (ps *PeerStore) Upsert(p peer.ID, addrs []ma.Multiaddr, services uint64, source string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.maxKnown > 0 && len(ps.byID) >= ps.maxKnown {
		// soft cap: do not add new peers if capacity reached
		if _, exists := ps.byID[p.String()]; !exists {
			return nil
		}
	}
	id := p.String()
	rec := ps.byID[id]
	now := time.Now().Unix()
	if rec == nil {
		rec = &PeerRecord{PeerID: id}
		ps.byID[id] = rec
	}
	// merge addresses
	merged := make(map[string]struct{}, len(rec.Addrs)+len(addrs))
	for _, a := range rec.Addrs {
		merged[a] = struct{}{}
	}
	for _, a := range addrs {
		merged[a.String()] = struct{}{}
	}
	rec.Addrs = rec.Addrs[:0]
	for a := range merged {
		rec.Addrs = append(rec.Addrs, a)
	}
	// fields
	if services != 0 {
		rec.Services = services
	}
	if source != "" {
		rec.Source = source
	}
	rec.LastSeenUnix = now
	if rec.ExpireAtUnix == 0 {
		rec.ExpireAtUnix = defaultExpireNone
	}
	rec.Score = ps.computeScoreLocked(rec, 0)
	return ps.saveLocked(rec)
}

// RecordDialAttempt notes a connection attempt to the peer.
func (ps *PeerStore) RecordDialAttempt(p peer.ID) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	rec := ps.byID[p.String()]
	if rec == nil {
		return errors.New("unknown peer")
	}
	rec.LastTriedUnix = time.Now().Unix()
	rec.Score = ps.computeScoreLocked(rec, 0)
	return ps.saveLocked(rec)
}

// RecordDialFailure increments failure counters and updates score.
func (ps *PeerStore) RecordDialFailure(p peer.ID) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	rec := ps.byID[p.String()]
	if rec == nil {
		return errors.New("unknown peer")
	}
	rec.LastTriedUnix = time.Now().Unix()
	rec.FailureCount++
	rec.Score = ps.computeScoreLocked(rec, 0)
	return ps.saveLocked(rec)
}

// RecordDialSuccess resets failures, updates timestamps and score.
func (ps *PeerStore) RecordDialSuccess(p peer.ID) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	rec := ps.byID[p.String()]
	if rec == nil {
		return errors.New("unknown peer")
	}
	now := time.Now().Unix()
	rec.LastSeenUnix = now
	rec.LastSuccUnix = now
	rec.FailureCount = 0
	rec.Score = ps.computeScoreLocked(rec, 0)
	return ps.saveLocked(rec)
}

// GetDialCandidates returns up to limit peer infos, sorted by score and recency.
// wantServices is a bitmask; if non-zero, candidates with matching services rank higher.
// exclude contains peer IDs to skip.
func (ps *PeerStore) GetDialCandidates(limit int, wantServices uint64, exclude map[peer.ID]bool) ([]peer.AddrInfo, []PeerRecord) {
	ps.mu.RLock()
	// snapshot
	records := make([]*PeerRecord, 0, len(ps.byID))
	for _, r := range ps.byID {
		// copy value for scoring with wantServices
		cp := *r
		cp.Score = ps.computeScoreLocked(&cp, wantServices)
		records = append(records, &cp)
	}
	ps.mu.RUnlock()

	// filter and rank
	now := time.Now().Unix()
	out := make([]*PeerRecord, 0, len(records))
	for _, r := range records {
		if r.ExpireAtUnix != 0 && r.ExpireAtUnix <= now {
			continue
		}
		if r.FailureCount >= ps.maxFail {
			continue
		}
		pid, err := peer.Decode(r.PeerID)
		if err != nil {
			continue
		}
		if exclude != nil && exclude[pid] {
			continue
		}
		out = append(out, r)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// tie-breaker: newer success, then newer seen, then fewer failures
		if out[i].LastSuccUnix != out[j].LastSuccUnix {
			return out[i].LastSuccUnix > out[j].LastSuccUnix
		}
		if out[i].LastSeenUnix != out[j].LastSeenUnix {
			return out[i].LastSeenUnix > out[j].LastSeenUnix
		}
		return out[i].FailureCount < out[j].FailureCount
	})

	if limit <= 0 || limit > len(out) {
		limit = len(out)
	}
	selected := out[:limit]
	infos := make([]peer.AddrInfo, 0, limit)
	retMeta := make([]PeerRecord, 0, limit)
	for _, r := range selected {
		pid, err := peer.Decode(r.PeerID)
		if err != nil {
			continue
		}
		var addrs []ma.Multiaddr
		for _, s := range r.Addrs {
			if a, err := ma.NewMultiaddr(s); err == nil {
				addrs = append(addrs, a)
			}
		}
		infos = append(infos, peer.AddrInfo{ID: pid, Addrs: addrs})
		retMeta = append(retMeta, *r)
	}
	return infos, retMeta
}

// Prune removes peers that exceed failure limits or are stale.
func (ps *PeerStore) Prune() (removed int, err error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-ps.staleAge).Unix()
	for id, r := range ps.byID {
		if r.FailureCount >= ps.maxFail || (r.LastSeenUnix != 0 && r.LastSeenUnix < cutoff) || (r.ExpireAtUnix != 0 && r.ExpireAtUnix <= now.Unix()) {
			if err := ps.nsp.Delete(context.Background(), ds.NewKey(escapeKey(id))); err != nil {
				return removed, err
			}
			delete(ps.byID, id)
			removed++
		}
	}
	return removed, nil
}

// loadAll loads all peer records from the namespaced datastore.
func (ps *PeerStore) loadAll(ctx context.Context) error {
	q, err := ps.nsp.Query(ctx, query.Query{Prefix: "/"})
	if err != nil {
		return err
	}
	defer q.Close()
	for r := range q.Next() {
		if r.Error != nil {
			return r.Error
		}
		var pr PeerRecord
		if err := json.Unmarshal(r.Value, &pr); err != nil {
			continue
		}
		if pr.PeerID == "" {
			// derive from key if missing
			pr.PeerID = unescapeKey(strings.TrimPrefix(r.Key, "/"))
		}
		ps.byID[pr.PeerID] = &pr
	}
	return nil
}

func (ps *PeerStore) saveLocked(rec *PeerRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := ds.NewKey(escapeKey(rec.PeerID))
	return ps.nsp.Put(context.Background(), key, b)
}

// computeScoreLocked calculates a score from metadata. If wantServices is non-zero,
// matching services receive a large boost.
func (ps *PeerStore) computeScoreLocked(rec *PeerRecord, wantServices uint64) float64 {
	score := 0.0
	// prefer service matches strongly
	if wantServices != 0 {
		if rec.Services&wantServices != 0 {
			score += 100.0
		}
	}
	now := time.Now().Unix()
	// recent success up to 50 points (decays 1 point per minute)
	if rec.LastSuccUnix > 0 {
		mins := (now - rec.LastSuccUnix) / 60
		gain := 50 - float64(mins)
		if gain < 0 {
			gain = 0
		}
		score += gain
	}
	// recent seen up to 20 points (decays 1 point per 10 minutes)
	if rec.LastSeenUnix > 0 {
		tens := (now - rec.LastSeenUnix) / (60 * 10)
		gain := 20 - float64(tens)
		if gain < 0 {
			gain = 0
		}
		score += gain
	}
	// penalize failures 5 points each
	score -= float64(rec.FailureCount * 5)
	return score
}

func escapeKey(id string) string {
	// datastore keys are path-like; ensure no '/' from peer IDs
	return strings.ReplaceAll(id, "/", "_")
}

func unescapeKey(k string) string {
	return strings.ReplaceAll(k, "_", "/")
}
