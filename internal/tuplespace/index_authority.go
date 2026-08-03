package tuplespace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/pht"
)

const (
	indexAuthorityRecordKey = "/pht/__authority__"

	defaultIndexAuthoritySettle = 2 * time.Second
	defaultIndexAuthorityLease  = 90 * time.Second
	defaultIndexAuthorityMargin = 2 * time.Second
	indexAuthorityRevalidation  = 5 * time.Second
)

var errStaleIndexAuthority = errors.New("stale index mutation authority")

// indexAuthorityRecord is a leased, fenced shard-writer claim. Version is a
// renewal sequence ordered after (Epoch, Writer) by the DHT validator.
type indexAuthorityRecord struct {
	Epoch      uint64 `json:"epoch"`
	Writer     string `json:"writer"`
	Version    uint64 `json:"version"`
	ValidAfter int64  `json:"valid_after_unix_nano"`
	ExpiresAt  int64  `json:"expires_at_unix_nano"`
}

func (r indexAuthorityRecord) fence() pht.WriteFence {
	return pht.WriteFence{Epoch: r.Epoch, Writer: r.Writer}
}

func (r indexAuthorityRecord) validAfter() time.Time {
	return time.Unix(0, r.ValidAfter)
}

func (r indexAuthorityRecord) expiresAt() time.Time {
	return time.Unix(0, r.ExpiresAt)
}

type indexAuthorityState struct {
	mu          sync.Mutex
	cached      *indexAuthorityRecord
	validatedAt time.Time
}

// indexAuthorityManager elects one leased writer per PHT shard. Concurrent
// claims use the same epoch and are deterministically fenced by writer ID.
// Claimants wait for a propagation window before accepting mutations.
type indexAuthorityManager struct {
	self     peer.ID
	resolver TupleOwnerResolver
	stores   []pht.ValueStore
	ownerKey string
	states   []indexAuthorityState
	settle   time.Duration
	lease    time.Duration
	margin   time.Duration
	metrics  indexAuthorityMetrics
}

type successorOwnerResolver interface {
	ResolveTupleOwnerAfter(context.Context, string, string) (peer.ID, error)
}

type indexAuthorityMetrics struct {
	claims      atomic.Uint64
	transitions atomic.Uint64
	renewals    atomic.Uint64
	rejections  atomic.Uint64
}

type indexAuthorityStats struct {
	claims      uint64
	transitions uint64
	renewals    uint64
	rejections  uint64
}

func newIndexAuthorityManager(
	self peer.ID,
	resolver TupleOwnerResolver,
	stores []pht.ValueStore,
) (*indexAuthorityManager, error) {
	return newIndexAuthorityManagerForKey(self, resolver, stores, indexOwnershipKey)
}

func newIndexAuthorityManagerForKey(
	self peer.ID,
	resolver TupleOwnerResolver,
	stores []pht.ValueStore,
	ownerKey string,
) (*indexAuthorityManager, error) {
	if self == "" || resolver == nil || len(stores) == 0 {
		return nil, errors.New("self, owner resolver, and authority stores required")
	}
	if ownerKey == "" {
		return nil, errors.New("index ownership key required")
	}
	return &indexAuthorityManager{
		self:     self,
		resolver: resolver,
		stores:   stores,
		ownerKey: ownerKey,
		states:   make([]indexAuthorityState, len(stores)),
		settle:   defaultIndexAuthoritySettle,
		lease:    defaultIndexAuthorityLease,
		margin:   defaultIndexAuthorityMargin,
	}, nil
}

func (m *indexAuthorityManager) setTiming(settle, lease, margin time.Duration) {
	if settle < 0 {
		settle = 0
	}
	if lease <= 0 {
		lease = defaultIndexAuthorityLease
	}
	if margin < 0 {
		margin = 0
	}
	if margin*2 >= lease {
		margin = lease / 4
	}
	m.settle = settle
	m.lease = lease
	m.margin = margin
}

func (m *indexAuthorityManager) resolve(ctx context.Context, shard int) (pht.WriteFence, error) {
	if shard < 0 || shard >= len(m.states) {
		return pht.WriteFence{}, fmt.Errorf("invalid authority shard %d", shard)
	}
	state := &m.states[shard]
	state.mu.Lock()
	defer state.mu.Unlock()

	for attempt := 0; attempt < 4; attempt++ {
		now := time.Now()
		if state.cached != nil && m.usable(*state.cached, now) {
			return state.cached.fence(), nil
		}

		current, err := m.read(ctx, shard)
		if err != nil && !isMissingAuthority(err) {
			return pht.WriteFence{}, fmt.Errorf("read index authority: %w", err)
		}
		if err == nil {
			// The store read may straddle ValidAfter. Never evaluate a fresh
			// record against the timestamp captured before that potentially
			// blocking read: doing so can mistake an already-valid lease for a
			// future lease and wait until its full expiry.
			now = time.Now()
			if wait := time.Until(current.validAfter()); wait > 0 && now.Before(current.expiresAt()) {
				if err := waitUntilAuthority(ctx, current.validAfter()); err != nil {
					return pht.WriteFence{}, err
				}
				now = time.Now()
			}
			if m.usable(current, now) {
				state.cached = &current
				state.validatedAt = now
				return current.fence(), nil
			}
			if now.Before(current.expiresAt()) {
				if err := waitForAuthority(ctx, time.Until(current.expiresAt())); err != nil {
					return pht.WriteFence{}, err
				}
				now = time.Now()
			}
		}

		owner, err := m.resolveCandidate(ctx, shard, current.Writer)
		if err != nil {
			return pht.WriteFence{}, fmt.Errorf("resolve authority candidate: %w", err)
		}
		epoch := uint64(1)
		notBefore := now.Add(m.settle)
		if current.Epoch > 0 {
			epoch = current.Epoch + 1
			if expires := current.expiresAt(); expires.After(notBefore) {
				notBefore = expires
			}
		}
		claim := indexAuthorityRecord{
			Epoch:      epoch,
			Writer:     owner.String(),
			Version:    1,
			ValidAfter: notBefore.UnixNano(),
			ExpiresAt:  notBefore.Add(m.lease).UnixNano(),
		}
		m.metrics.claims.Add(1)
		writeErr := m.write(ctx, shard, claim)
		if err := waitForAuthority(ctx, time.Until(notBefore)); err != nil {
			return pht.WriteFence{}, err
		}
		winner, err := m.read(ctx, shard)
		if err != nil {
			if writeErr != nil && !isMissingAuthority(err) {
				return pht.WriteFence{}, fmt.Errorf(
					"publish index authority: %v; confirm winner: %w",
					writeErr,
					err,
				)
			}
			if isMissingAuthority(err) {
				continue
			}
			return pht.WriteFence{}, fmt.Errorf("confirm index authority: %w", err)
		}
		if m.usable(winner, time.Now()) {
			if winner.Epoch == claim.Epoch &&
				winner.Writer == claim.Writer &&
				winner.Epoch > 1 {
				m.metrics.transitions.Add(1)
			}
			state.cached = &winner
			state.validatedAt = time.Now()
			return winner.fence(), nil
		}
	}
	return pht.WriteFence{}, errors.New("index authority did not converge")
}

// failover publishes a higher fencing epoch whose candidate excludes the
// failed writer. Calls are serialized per shard; concurrent callers reuse a
// winner already installed by the first caller instead of repeatedly bumping
// the epoch.
func (m *indexAuthorityManager) failover(
	ctx context.Context,
	shard int,
	failed pht.WriteFence,
) (pht.WriteFence, error) {
	if shard < 0 || shard >= len(m.states) {
		return pht.WriteFence{}, fmt.Errorf("invalid authority shard %d", shard)
	}
	state := &m.states[shard]
	state.mu.Lock()
	defer state.mu.Unlock()

	now := time.Now()
	if state.cached != nil &&
		pht.CompareWriteFences(state.cached.fence(), failed) != 0 &&
		m.usable(*state.cached, now) {
		return state.cached.fence(), nil
	}
	current, err := m.read(ctx, shard)
	if err != nil {
		return pht.WriteFence{}, fmt.Errorf("read failed index authority: %w", err)
	}
	if pht.CompareWriteFences(current.fence(), failed) != 0 &&
		time.Now().Before(current.validAfter()) &&
		time.Now().Before(current.expiresAt()) {
		// Another caller has already installed a stronger pending winner.
		// Wait for that claim instead of immediately creating yet another
		// epoch while its propagation window is still open.
		if err := waitUntilAuthority(ctx, current.validAfter()); err != nil {
			return pht.WriteFence{}, err
		}
		current, err = m.read(ctx, shard)
		if err != nil {
			return pht.WriteFence{}, fmt.Errorf("confirm pending failover authority: %w", err)
		}
		now = time.Now()
	}
	if pht.CompareWriteFences(current.fence(), failed) != 0 && m.usable(current, now) {
		state.cached = &current
		state.validatedAt = now
		return current.fence(), nil
	}
	owner, err := m.resolveCandidate(ctx, shard, current.Writer)
	if err != nil {
		return pht.WriteFence{}, fmt.Errorf("resolve failover authority candidate: %w", err)
	}
	notBefore := now.Add(m.settle)
	claim := indexAuthorityRecord{
		Epoch:      current.Epoch + 1,
		Writer:     owner.String(),
		Version:    1,
		ValidAfter: notBefore.UnixNano(),
		ExpiresAt:  notBefore.Add(m.lease).UnixNano(),
	}
	m.metrics.claims.Add(1)
	writeErr := m.write(ctx, shard, claim)
	if err := waitForAuthority(ctx, time.Until(notBefore)); err != nil {
		return pht.WriteFence{}, err
	}
	var winner indexAuthorityRecord
	for attempt := 0; attempt < 4; attempt++ {
		winner, err = m.read(ctx, shard)
		if err == nil {
			if wait := time.Until(winner.validAfter()); wait > 0 &&
				time.Now().Before(winner.expiresAt()) {
				if err := waitUntilAuthority(ctx, winner.validAfter()); err != nil {
					return pht.WriteFence{}, err
				}
			} else if m.usable(winner, time.Now()) {
				break
			}
		}
		if attempt < 3 {
			if err := waitForAuthority(ctx, time.Duration(attempt+1)*50*time.Millisecond); err != nil {
				return pht.WriteFence{}, err
			}
		}
	}
	if err != nil {
		if writeErr != nil {
			return pht.WriteFence{}, fmt.Errorf(
				"publish failover authority: %v; confirm winner: %w",
				writeErr,
				err,
			)
		}
		return pht.WriteFence{}, fmt.Errorf("confirm failover authority: %w", err)
	}
	if !m.usable(winner, time.Now()) {
		return pht.WriteFence{}, errors.New("failover authority did not converge after retries")
	}
	if winner.Epoch > current.Epoch {
		m.metrics.transitions.Add(1)
	}
	state.cached = &winner
	state.validatedAt = time.Now()
	return winner.fence(), nil
}

func (m *indexAuthorityManager) resolveCandidate(
	ctx context.Context,
	shard int,
	previousWriter string,
) (peer.ID, error) {
	key := fmt.Sprintf("%s:%d", m.ownerKey, shard)
	if previousWriter == "" {
		return m.resolver.ResolveTupleOwner(ctx, key)
	}
	if resolver, ok := m.resolver.(successorOwnerResolver); ok {
		return resolver.ResolveTupleOwnerAfter(ctx, key, previousWriter)
	}
	candidate, err := m.resolver.ResolveTupleOwner(ctx, key)
	if err != nil {
		return "", err
	}
	if candidate.String() == previousWriter {
		return "", fmt.Errorf("authority resolver reselected failed writer %s", previousWriter)
	}
	return candidate, nil
}

// validateForApply periodically revalidates the leased authority record and
// always checks its local expiry. The persisted PHT epoch/writer is the commit
// fence on every node write, so a short authority-cache interval removes one
// replicated DHT read from the mutation hot path without allowing a stale
// writer to overwrite a node adopted by a newer epoch.
func (m *indexAuthorityManager) validateForApply(
	ctx context.Context,
	shard int,
	fence pht.WriteFence,
) error {
	if shard < 0 || shard >= len(m.states) {
		return fmt.Errorf("invalid authority shard %d", shard)
	}
	state := &m.states[shard]
	state.mu.Lock()
	defer state.mu.Unlock()

	now := time.Now()
	if state.cached != nil &&
		state.cached.Writer == m.self.String() &&
		pht.CompareWriteFences(state.cached.fence(), fence) == 0 &&
		m.usable(*state.cached, now) &&
		now.Sub(state.validatedAt) < indexAuthorityRevalidation &&
		time.Until(state.cached.expiresAt()) > m.lease/2+m.margin {
		return nil
	}
	current, err := m.read(ctx, shard)
	if err != nil {
		return fmt.Errorf("validate index authority: %w", err)
	}
	state.cached = &current
	state.validatedAt = now
	if current.Writer != m.self.String() ||
		pht.CompareWriteFences(current.fence(), fence) != 0 ||
		!m.usable(current, now) {
		m.metrics.rejections.Add(1)
		return fmt.Errorf(
			"%w: requested epoch=%d writer=%q, current epoch=%d writer=%q",
			errStaleIndexAuthority,
			fence.Epoch,
			fence.Writer,
			current.Epoch,
			current.Writer,
		)
	}

	if time.Until(current.expiresAt()) <= m.lease/2+m.margin {
		current.Version++
		current.ExpiresAt = now.Add(m.lease).UnixNano()
		if err := m.write(ctx, shard, current); err != nil {
			state.cached = nil
			state.validatedAt = time.Time{}
			m.metrics.rejections.Add(1)
			return fmt.Errorf("%w: renew index authority: %v", errStaleIndexAuthority, err)
		}
		m.metrics.renewals.Add(1)
		state.cached = &current
		state.validatedAt = now
	}
	return nil
}

func (m *indexAuthorityManager) snapshot() indexAuthorityStats {
	return indexAuthorityStats{
		claims:      m.metrics.claims.Load(),
		transitions: m.metrics.transitions.Load(),
		renewals:    m.metrics.renewals.Load(),
		rejections:  m.metrics.rejections.Load(),
	}
}

func (m *indexAuthorityManager) invalidate(shard int, fence pht.WriteFence) {
	if shard < 0 || shard >= len(m.states) {
		return
	}
	state := &m.states[shard]
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.cached != nil && pht.CompareWriteFences(state.cached.fence(), fence) == 0 {
		state.cached = nil
		state.validatedAt = time.Time{}
	}
}

func (m *indexAuthorityManager) usable(record indexAuthorityRecord, now time.Time) bool {
	return record.Epoch > 0 &&
		record.Writer != "" &&
		!now.Before(record.validAfter()) &&
		now.Before(record.expiresAt().Add(-m.margin))
}

func (m *indexAuthorityManager) read(ctx context.Context, shard int) (indexAuthorityRecord, error) {
	data, err := m.stores[shard].GetValue(ctx, indexAuthorityRecordKey)
	if err != nil {
		return indexAuthorityRecord{}, err
	}
	var record indexAuthorityRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return indexAuthorityRecord{}, fmt.Errorf("decode authority record: %w", err)
	}
	if record.Epoch == 0 || record.Writer == "" ||
		record.ValidAfter <= 0 || record.ExpiresAt <= record.ValidAfter {
		return indexAuthorityRecord{}, errors.New("invalid authority record")
	}
	return record, nil
}

func (m *indexAuthorityManager) write(
	ctx context.Context,
	shard int,
	record indexAuthorityRecord,
) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return m.stores[shard].PutValue(ctx, indexAuthorityRecordKey, data)
}

func isMissingAuthority(err error) bool {
	return errors.Is(err, routing.ErrNotFound) ||
		strings.Contains(strings.ToLower(err.Error()), "not found")
}

func waitForAuthority(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitUntilAuthority(ctx context.Context, deadline time.Time) error {
	for {
		wait := time.Until(deadline)
		if wait <= 0 {
			return nil
		}
		if err := waitForAuthority(ctx, wait); err != nil {
			return err
		}
	}
}
