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
	// peerstoreNS is the datastore key prefix (namespace) under which all peer
	// records are stored, keeping them separate from other data in the shared store.
	peerstoreNS = "/peers"
	// defaultStaleAge is the default age (since LastSeenUnix) after which a peer
	// record is eligible for removal by Prune, unless overridden via SetPolicy.
	defaultStaleAge = 24 * time.Hour
	// defaultMaxFail is the default consecutive-failure threshold at/above which a
	// peer is excluded from GetDialCandidates and eligible for removal by Prune,
	// unless overridden via SetPolicy.
	defaultMaxFail = 8
	// defaultExpireNone is the zero value for PeerRecord.ExpireAtUnix, meaning "does
	// not expire".
	defaultExpireNone = int64(0)
)

// PeerRecord captures persistent metadata for a known peer, as stored (JSON-encoded)
// in the datastore and returned (as a value copy) by GetDialCandidates.
type PeerRecord struct {
	// PeerID is the string form (peer.ID.String()) of the peer's identity; also used
	// (escaped via escapeKey) as the datastore key for this record.
	PeerID string `json:"peer_id"`
	// Addrs is the deduplicated set of known multiaddr strings for this peer,
	// accumulated across calls to Upsert.
	Addrs []string `json:"addrs"`
	// Services is the most recently observed non-zero services bitmask advertised by
	// this peer (see Upsert — a zero value passed to Upsert does not clear it).
	Services uint64 `json:"services"`
	// LastSeenUnix is the Unix timestamp of the most recent Upsert (or dial success,
	// which also updates it) for this peer; 0 if never set.
	LastSeenUnix int64 `json:"last_seen_unix"`
	// LastTriedUnix is the Unix timestamp of the most recent dial attempt recorded via
	// RecordDialAttempt or RecordDialFailure; 0 if never attempted.
	LastTriedUnix int64 `json:"last_tried_unix"`
	// LastSuccUnix is the Unix timestamp of the most recent successful dial recorded
	// via RecordDialSuccess; 0 if never succeeded.
	LastSuccUnix int64 `json:"last_succ_unix"`
	// FailureCount is the number of consecutive dial failures since the last success;
	// reset to 0 by RecordDialSuccess and incremented by RecordDialFailure.
	FailureCount int `json:"failure_count"`
	// Source is a free-form label describing how this peer was learned (e.g. "seed",
	// "gossip", "handshake"); overwritten by Upsert whenever a non-empty source is
	// passed.
	Source string `json:"source"`
	// Score is the most recently computed ranking score (see computeScoreLocked);
	// recomputed and persisted on every mutating call. Note the persisted value
	// reflects the last computation's wantServices argument (often 0), while
	// GetDialCandidates recomputes a fresh, non-persisted score per call using its own
	// wantServices.
	Score float64 `json:"score"`
	// ExpireAtUnix, if non-zero, is a Unix timestamp after which the record is treated
	// as expired: excluded from GetDialCandidates and removed by Prune. 0 means "does
	// not expire" (defaultExpireNone).
	ExpireAtUnix int64 `json:"expire_at_unix"`
}

// PeerStore maintains an in-memory index of PeerRecord values backed by a namespaced
// datastore for persistence. All exported methods are safe for concurrent use (guarded
// by mu); the zero value is not usable — construct via NewPeerStore.
type PeerStore struct {
	mu sync.RWMutex
	// ds is the underlying (un-namespaced) datastore passed to NewPeerStore.
	ds ds.Batching
	// nsp is ds wrapped with the peerstoreNS namespace; all record reads/writes go
	// through nsp so keys don't collide with other data sharing the same underlying
	// datastore.
	nsp ds.Batching
	// byID is the in-memory index of peer records keyed by peer.ID.String(), kept in
	// sync with nsp on every mutation.
	byID map[string]*PeerRecord

	// policy knobs
	// staleAge is the age threshold used by Prune (see defaultStaleAge).
	staleAge time.Duration
	// maxFail is the failure-count threshold used by GetDialCandidates (to exclude)
	// and Prune (to remove); see defaultMaxFail.
	maxFail int
	// maxKnown is a soft cap on the number of distinct peers tracked; once reached,
	// Upsert stops admitting brand-new peer IDs (existing ones can still be updated).
	// 0 (or negative) disables the cap.
	maxKnown int
}

// NewPeerStore constructs a PeerStore backed by store, wrapping it in the peerstoreNS
// namespace so records don't collide with other data kept in the same underlying
// datastore. Default policy values are staleAge=24h (defaultStaleAge), maxFail=8
// (defaultMaxFail), and maxKnown=5000; use SetPolicy/SetMaxKnown to override. All
// existing records under the namespace are eagerly loaded into memory (via loadAll)
// before returning. Returns a non-nil error if the initial load fails (e.g. datastore
// query error or, per-record, propagates any query.Result.Error — malformed JSON for
// an individual record is tolerated and simply skipped, not an error).
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

// SetPolicy overrides the staleAge and maxFail thresholds used by GetDialCandidates
// and Prune. staleAge is only applied if positive (values <= 0 are silently ignored
// and leave the existing value unchanged); the same holds for maxFailures. Safe for
// concurrent use.
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

// SetMaxKnown sets the soft cap (maxKnown) on the number of distinct peers Upsert will
// admit. Unlike SetPolicy's fields, n is applied unconditionally, including
// non-positive values (which disable the cap per the Upsert check "ps.maxKnown > 0").
// Safe for concurrent use.
func (ps *PeerStore) SetMaxKnown(n int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.maxKnown = n
}

// Upsert records or updates metadata for peer p:
//   - if the maxKnown soft cap is reached and p is not already known, the call is a
//     silent no-op (returns nil, nothing is written) — existing peers can still be
//     updated past the cap;
//   - addrs (multiaddr values) are merged into the existing address set for p and
//     deduplicated by string representation (order is not preserved — merging is done
//     via a map);
//   - services, if non-zero, replaces the stored Services bitmask (a zero value leaves
//     the previously stored value untouched, so callers cannot use Upsert to explicitly
//     clear services back to 0);
//   - source, if non-empty, replaces the stored Source label (empty leaves it
//     unchanged);
//   - LastSeenUnix is set to the current time;
//   - the record's Score is recomputed (via computeScoreLocked with wantServices=0) and
//     the record is persisted to the datastore.
//
// Returns a non-nil error only if persisting the updated record to the datastore
// fails (via saveLocked); the in-memory index is updated regardless. Safe for
// concurrent use.
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

// RecordDialAttempt updates LastTriedUnix to the current time for peer p and
// recomputes its Score (wantServices=0), then persists the record. Returns an error
// ("unknown peer") if p has no existing record (Upsert must be called first); returns
// a non-nil error if persisting the update fails. Safe for concurrent use.
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

// RecordDialFailure updates LastTriedUnix to the current time, increments
// FailureCount, recomputes Score (wantServices=0), and persists the record for peer p.
// Returns an error ("unknown peer") if p has no existing record; returns a non-nil
// error if persisting the update fails. Safe for concurrent use.
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

// RecordDialSuccess updates LastSeenUnix and LastSuccUnix to the current time, resets
// FailureCount to 0, recomputes Score (wantServices=0), and persists the record for
// peer p. Returns an error ("unknown peer") if p has no existing record; returns a
// non-nil error if persisting the update fails. Safe for concurrent use.
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

// GetDialCandidates selects and ranks known peers as dial candidates.
//
// It takes a point-in-time snapshot of all records, recomputing each one's Score with
// the given wantServices (a services bitmask; if non-zero, peers whose Services
// overlaps it get a +100 boost — see computeScoreLocked); this recomputed score is
// NOT persisted back to the datastore. Records are then filtered to drop:
//   - expired records (ExpireAtUnix != 0 and <= now);
//   - records at/above the maxFail failure threshold;
//   - records whose PeerID fails to decode as a peer.ID (defensive/corrupt-data case);
//   - records whose peer.ID is present (true) in the exclude map (exclude may be nil,
//     meaning no exclusions).
//
// Remaining candidates are sorted descending by Score, breaking ties by (in order)
// more recent LastSuccUnix, then more recent LastSeenUnix, then fewer FailureCount.
// Up to limit candidates are returned (limit <= 0 or > available count means "return
// all"); records whose Addrs fail to parse as multiaddrs are dropped from that entry's
// AddrInfo silently.
//
// Returns two parallel slices: peer.AddrInfo values suitable for h.Connect, and the
// corresponding PeerRecord snapshots (with the wantServices-adjusted Score) — index i
// in each slice refers to the same peer. Safe for concurrent use (read-locked for the
// snapshot phase only).
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

// Prune removes, from both the in-memory index and the underlying datastore, every
// peer record that meets any of: FailureCount >= maxFail; LastSeenUnix is non-zero and
// older than staleAge; or ExpireAtUnix is non-zero and has passed. Iteration order over
// ps.byID (a Go map) is unspecified, but every matching record is visited regardless of
// order.
//
// Returns the count of removed records and a non-nil error if a datastore delete
// fails; on such a failure, Prune stops early (returns immediately) — records not yet
// visited are left untouched, and removed reflects only records successfully deleted
// before the error. Safe for concurrent use with the rest of PeerStore, but not
// re-entrant with itself in a way that matters (mu.Lock excludes concurrent Prune
// calls from interleaving).
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

// loadAll queries every key under the namespaced datastore (ps.nsp) and populates
// ps.byID from the results. It is called once, from NewPeerStore, before the store is
// returned to the caller, so it does not itself take ps.mu.
//
// For each result: a query-level error (r.Error) aborts loadAll immediately with that
// error; a record whose value fails to json.Unmarshal into a PeerRecord is skipped
// (not an error); a record with an empty PeerID field has it derived from the
// datastore key instead (via unescapeKey, reversing escapeKey's '/' -> '_' mapping),
// which handles records that predate the PeerID field being added to the JSON schema.
//
// Returns nil on success, or the first query/result error encountered.
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

// saveLocked JSON-marshals rec and writes it to the namespaced datastore under a key
// derived from rec.PeerID (via escapeKey). Must be called with ps.mu already held (by
// convention, all current callers hold the write lock). Returns a non-nil error if
// marshaling or the datastore Put fails.
func (ps *PeerStore) saveLocked(rec *PeerRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := ds.NewKey(escapeKey(rec.PeerID))
	return ps.nsp.Put(context.Background(), key, b)
}

// computeScoreLocked computes a ranking score for rec, higher meaning a more desirable
// dial candidate. Despite the name (mirroring the *Locked convention used by
// saveLocked), this function does not itself read/write any PeerStore-protected state
// (rec is passed by pointer and ps.mu is not touched) — the name simply documents that
// it's only meant to be called while ps.mu is already held by the caller. Components:
//   - +100 if wantServices is non-zero and rec.Services shares at least one bit with it;
//   - up to +50 for a recent RecordDialSuccess, decaying linearly to 0 over 50 minutes
//     (1 point lost per elapsed minute since LastSuccUnix);
//   - up to +20 for a recent Upsert/sighting, decaying linearly to 0 over 200 minutes
//     (1 point lost per elapsed 10-minute period since LastSeenUnix);
//   - -5 per FailureCount.
//
// Returns the resulting float64 score (may be negative).
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

// escapeKey converts a peer ID string into a datastore-safe key by replacing every '/'
// with '_', since datastore keys are path-like and a raw '/' in id would otherwise be
// interpreted as a path separator. Note: standard peer.ID string encodings (base58btc
// or multibase-prefixed) do not contain '/', so in practice this is a no-op; it exists
// as a defensive normalization.
func escapeKey(id string) string {
	// datastore keys are path-like; ensure no '/' from peer IDs
	return strings.ReplaceAll(id, "/", "_")
}

// unescapeKey reverses escapeKey by replacing every '_' with '/'. Note this is not a
// safe general-purpose inverse: if a peer ID string ever legitimately contained '_'
// (not the case for current peer.ID encodings), unescapeKey would incorrectly turn it
// into '/'. It is only used by loadAll as a fallback to recover PeerID from the
// datastore key for legacy records that predate the PeerID JSON field.
func unescapeKey(k string) string {
	return strings.ReplaceAll(k, "_", "/")
}
