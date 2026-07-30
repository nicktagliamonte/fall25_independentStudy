package tuplespace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/pht"
)

const (
	tupleStateNamespace       = "/tuplestate/"
	defaultTupleStateSettle   = 250 * time.Millisecond
	defaultTupleStateLease    = 15 * time.Second
	defaultTupleStateMargin   = 500 * time.Millisecond
	maxDurableOperationResult = 256
	maxDurableTupleStateBytes = maxTupleRequestBytes
	tupleStateLockStripes     = 256
)

var errStaleTupleAuthority = errors.New("stale tuple authority")

// tupleFence identifies the only owner allowed to commit one exact-name
// multiset. Epoch changes fence an old owner after failover; Writer provides a
// deterministic winner when claimants race in the same epoch.
type tupleFence struct {
	Epoch  uint64 `json:"epoch"`
	Writer string `json:"writer"`
}

func compareTupleFences(a, b tupleFence) int {
	if a.Epoch < b.Epoch {
		return -1
	}
	if a.Epoch > b.Epoch {
		return 1
	}
	return strings.Compare(a.Writer, b.Writer)
}

type durableTupleResult struct {
	RequestID string `json:"request_id"`
	Operation string `json:"operation"`
	Value     []byte `json:"value,omitempty"`
}

// durableTupleState is both the replicated multiset and its ownership record.
// Successful mutating request results are committed in the same version as the
// mutation, allowing a successor to answer a retry without applying it twice.
type durableTupleState struct {
	Epoch      uint64               `json:"epoch"`
	Writer     string               `json:"writer"`
	Version    uint64               `json:"version"`
	Name       string               `json:"name"`
	Values     [][]byte             `json:"values,omitempty"`
	Results    []durableTupleResult `json:"results,omitempty"`
	ValidAfter int64                `json:"valid_after_unix_nano"`
	ExpiresAt  int64                `json:"expires_at_unix_nano"`
}

func (s durableTupleState) fence() tupleFence {
	return tupleFence{Epoch: s.Epoch, Writer: s.Writer}
}

func (s durableTupleState) validAfter() time.Time {
	return time.Unix(0, s.ValidAfter)
}

func (s durableTupleState) expiresAt() time.Time {
	return time.Unix(0, s.ExpiresAt)
}

type durableTupleStore struct {
	self     peer.ID
	resolver TupleOwnerResolver
	store    pht.ValueStore
	settle   time.Duration
	lease    time.Duration
	margin   time.Duration
	locks    [tupleStateLockStripes]sync.Mutex
}

func newDurableTupleStore(
	self peer.ID,
	resolver TupleOwnerResolver,
	store pht.ValueStore,
) (*durableTupleStore, error) {
	if self == "" || resolver == nil || store == nil {
		return nil, errors.New("self, tuple owner resolver, and tuple state store required")
	}
	return &durableTupleStore{
		self:     self,
		resolver: resolver,
		store:    store,
		settle:   defaultTupleStateSettle,
		lease:    defaultTupleStateLease,
		margin:   defaultTupleStateMargin,
	}, nil
}

func (d *durableTupleStore) setTiming(settle, lease, margin time.Duration) {
	if settle < 0 {
		settle = 0
	}
	if lease <= 0 {
		lease = defaultTupleStateLease
	}
	if margin < 0 {
		margin = 0
	}
	if margin*2 >= lease {
		margin = lease / 4
	}
	d.settle = settle
	d.lease = lease
	d.margin = margin
}

// resolve returns the persisted writer when state exists, even just after its
// lease expired. The caller tries that writer first; a live writer can renew
// without moving data, while an unreachable writer is replaced by failover.
func (d *durableTupleStore) resolve(ctx context.Context, name string) (tupleFence, error) {
	state, err := d.read(ctx, name)
	if err == nil {
		return state.fence(), nil
	}
	if !isMissingTupleState(err) {
		return tupleFence{}, err
	}
	owner, err := d.resolver.ResolveTupleOwner(ctx, name)
	if err != nil {
		return tupleFence{}, err
	}
	if owner == "" {
		return tupleFence{}, errors.New("tuple owner resolver returned an empty peer ID")
	}
	return tupleFence{Epoch: 1, Writer: owner.String()}, nil
}

// failover copies the committed multiset and request-result window into a
// higher epoch. It waits out the old lease before publishing, so a transient
// route failure cannot immediately displace a healthy owner.
func (d *durableTupleStore) failover(
	ctx context.Context,
	name string,
	failed tupleFence,
) (tupleFence, error) {
	lock := d.lockFor(name)
	lock.Lock()
	defer lock.Unlock()

	state, err := d.read(ctx, name)
	if err != nil {
		if !isMissingTupleState(err) {
			return tupleFence{}, err
		}
		state = durableTupleState{
			Epoch:   failed.Epoch,
			Writer:  failed.Writer,
			Version: 0,
			Name:    name,
		}
	} else if compareTupleFences(state.fence(), failed) != 0 {
		return state.fence(), nil
	}

	if wait := time.Until(state.expiresAt().Add(d.margin)); state.ExpiresAt > 0 && wait > 0 {
		if err := waitForAuthority(ctx, wait); err != nil {
			return tupleFence{}, err
		}
		latest, readErr := d.read(ctx, name)
		if readErr == nil {
			if compareTupleFences(latest.fence(), failed) != 0 {
				return latest.fence(), nil
			}
			state = latest
		} else if !isMissingTupleState(readErr) {
			return tupleFence{}, readErr
		}
	}

	owner, err := d.resolveSuccessor(ctx, name, failed.Writer)
	if err != nil {
		return tupleFence{}, err
	}
	now := time.Now()
	state.Epoch = max(state.Epoch, failed.Epoch) + 1
	state.Writer = owner.String()
	state.Version++
	state.Name = name
	state.ValidAfter = now.Add(d.settle).UnixNano()
	state.ExpiresAt = now.Add(d.settle + d.lease).UnixNano()
	writeErr := d.write(ctx, state)
	if err := waitForAuthority(ctx, d.settle); err != nil {
		return tupleFence{}, err
	}
	winner, err := d.read(ctx, name)
	if err != nil {
		if writeErr != nil {
			return tupleFence{}, fmt.Errorf("publish tuple failover: %v; confirm winner: %w", writeErr, err)
		}
		return tupleFence{}, fmt.Errorf("confirm tuple failover: %w", err)
	}
	if time.Now().Before(winner.validAfter()) {
		if err := waitUntilAuthority(ctx, winner.validAfter()); err != nil {
			return tupleFence{}, err
		}
	}
	return winner.fence(), nil
}

func (d *durableTupleStore) resolveSuccessor(
	ctx context.Context,
	name string,
	previous string,
) (peer.ID, error) {
	if resolver, ok := d.resolver.(successorOwnerResolver); ok {
		return resolver.ResolveTupleOwnerAfter(ctx, name, previous)
	}
	owner, err := d.resolver.ResolveTupleOwner(ctx, name)
	if err != nil {
		return "", err
	}
	if owner.String() == previous {
		return "", fmt.Errorf("tuple resolver reselected failed writer %s", previous)
	}
	return owner, nil
}

func (d *durableTupleStore) apply(
	ctx context.Context,
	req tupleWireRequest,
) ([]byte, error) {
	if req.Name == "" || isTuplePattern(req.Name) {
		return nil, errors.New("durable tuple operation requires an exact name")
	}
	requested := tupleFence{Epoch: req.Epoch, Writer: req.Writer}
	if requested.Epoch == 0 || requested.Writer == "" {
		return nil, errors.New("durable tuple operation requires an ownership fence")
	}

	lock := d.lockFor(req.Name)
	lock.Lock()
	defer lock.Unlock()

	state, err := d.read(ctx, req.Name)
	if err != nil {
		if !isMissingTupleState(err) {
			return nil, err
		}
		if requested.Epoch != 1 || requested.Writer != d.self.String() {
			return nil, &tupleAuthorityError{Fence: requested}
		}
		now := time.Now()
		state = durableTupleState{
			Epoch:      requested.Epoch,
			Writer:     requested.Writer,
			Version:    0,
			Name:       req.Name,
			ValidAfter: now.UnixNano(),
			ExpiresAt:  now.Add(d.lease).UnixNano(),
		}
	} else {
		if state.Name != req.Name {
			return nil, errors.New("tuple-state key/name mismatch")
		}
		if compareTupleFences(state.fence(), requested) != 0 ||
			state.Writer != d.self.String() {
			return nil, &tupleAuthorityError{Fence: state.fence()}
		}
		if wait := time.Until(state.validAfter()); wait > 0 {
			if err := waitUntilAuthority(ctx, state.validAfter()); err != nil {
				return nil, err
			}
		}
		if !time.Now().Before(state.expiresAt().Add(-d.margin)) {
			var renewErr error
			state, renewErr = d.renewLocked(ctx, state)
			if renewErr != nil {
				return nil, renewErr
			}
		}
	}

	if result, ok := findDurableResult(state.Results, req.RequestID); ok {
		if result.Operation != req.Operation {
			return nil, errors.New("tuple request ID reused for a different operation")
		}
		return append([]byte(nil), result.Value...), nil
	}

	var value []byte
	mutated := false
	switch req.Operation {
	case "put":
		if len(req.Value) == 0 {
			return nil, errors.New("tuple value required")
		}
		state.Values = append(state.Values, append([]byte(nil), req.Value...))
		mutated = true
	case "replace":
		if len(req.Value) == 0 {
			return nil, errors.New("tuple value required")
		}
		state.Values = [][]byte{append([]byte(nil), req.Value...)}
		mutated = true
	case "read":
		if len(state.Values) == 0 {
			return nil, ErrTupleNotFound
		}
		value = append([]byte(nil), state.Values[0]...)
	case "get":
		if len(state.Values) == 0 {
			return nil, ErrTupleNotFound
		}
		value = append([]byte(nil), state.Values[0]...)
		state.Values[0] = nil
		state.Values = state.Values[1:]
		mutated = true
	default:
		return nil, fmt.Errorf("unsupported tuple operation %q", req.Operation)
	}
	if !mutated {
		return value, nil
	}

	state.Version++
	state.ExpiresAt = time.Now().Add(d.lease).UnixNano()
	if req.RequestID != "" {
		state.Results = append(state.Results, durableTupleResult{
			RequestID: req.RequestID,
			Operation: req.Operation,
			Value:     append([]byte(nil), value...),
		})
		if overflow := len(state.Results) - maxDurableOperationResult; overflow > 0 {
			copy(state.Results, state.Results[overflow:])
			state.Results = state.Results[:maxDurableOperationResult]
		}
	}
	if err := d.commit(ctx, state); err != nil {
		return nil, err
	}
	return value, nil
}

func (d *durableTupleStore) renewLocked(
	ctx context.Context,
	state durableTupleState,
) (durableTupleState, error) {
	now := time.Now()
	state.Epoch++
	state.Writer = d.self.String()
	state.Version++
	state.ValidAfter = now.Add(d.settle).UnixNano()
	state.ExpiresAt = now.Add(d.settle + d.lease).UnixNano()
	writeErr := d.write(ctx, state)
	if err := waitForAuthority(ctx, d.settle); err != nil {
		return durableTupleState{}, err
	}
	winner, err := d.read(ctx, state.Name)
	if err != nil {
		if writeErr != nil {
			return durableTupleState{}, fmt.Errorf("renew tuple authority: %v; confirm winner: %w", writeErr, err)
		}
		return durableTupleState{}, err
	}
	if compareTupleFences(winner.fence(), state.fence()) != 0 ||
		winner.Writer != d.self.String() {
		return durableTupleState{}, &tupleAuthorityError{Fence: winner.fence()}
	}
	return winner, nil
}

func (d *durableTupleStore) commit(ctx context.Context, state durableTupleState) error {
	expected, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(expected) > maxDurableTupleStateBytes {
		return fmt.Errorf(
			"durable tuple state is %d bytes; maximum is %d",
			len(expected),
			maxDurableTupleStateBytes,
		)
	}
	putErr := d.store.PutValue(ctx, tupleStateKey(state.Name), expected)
	deadline := time.Now().Add(d.settle)
	for {
		confirmed, readErr := d.store.GetValue(ctx, tupleStateKey(state.Name))
		if readErr == nil && bytes.Equal(confirmed, expected) {
			return nil
		}
		if readErr == nil {
			var winner durableTupleState
			if json.Unmarshal(confirmed, &winner) == nil &&
				compareTupleFences(winner.fence(), state.fence()) > 0 {
				return &tupleAuthorityError{Fence: winner.fence()}
			}
		}
		if time.Now().After(deadline) || d.settle == 0 {
			if putErr != nil {
				return fmt.Errorf("write tuple state: %v; confirmation failed: %v", putErr, readErr)
			}
			return errors.New("tuple state commit was not confirmed")
		}
		if err := waitForAuthority(ctx, 25*time.Millisecond); err != nil {
			return err
		}
	}
}

func (d *durableTupleStore) read(
	ctx context.Context,
	name string,
) (durableTupleState, error) {
	data, err := d.store.GetValue(ctx, tupleStateKey(name))
	if err != nil {
		return durableTupleState{}, err
	}
	var state durableTupleState
	if err := json.Unmarshal(data, &state); err != nil {
		return durableTupleState{}, fmt.Errorf("decode durable tuple state: %w", err)
	}
	if state.Epoch == 0 || state.Writer == "" || state.Name != name ||
		state.ValidAfter <= 0 || state.ExpiresAt <= state.ValidAfter {
		return durableTupleState{}, errors.New("invalid durable tuple state")
	}
	return state, nil
}

func (d *durableTupleStore) write(ctx context.Context, state durableTupleState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > maxDurableTupleStateBytes {
		return fmt.Errorf(
			"durable tuple state is %d bytes; maximum is %d",
			len(data),
			maxDurableTupleStateBytes,
		)
	}
	return d.store.PutValue(ctx, tupleStateKey(state.Name), data)
}

func (d *durableTupleStore) values(
	ctx context.Context,
	name string,
) ([][]byte, error) {
	state, err := d.read(ctx, name)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, len(state.Values))
	for i := range state.Values {
		out[i] = append([]byte(nil), state.Values[i]...)
	}
	return out, nil
}

func (d *durableTupleStore) lockFor(name string) *sync.Mutex {
	hash := sha256.Sum256([]byte(name))
	return &d.locks[int(hash[0])]
}

func tupleStateKey(name string) string {
	hash := sha256.Sum256([]byte(name))
	return tupleStateNamespace + hex.EncodeToString(hash[:])
}

func findDurableResult(
	results []durableTupleResult,
	requestID string,
) (durableTupleResult, bool) {
	if requestID == "" {
		return durableTupleResult{}, false
	}
	for i := len(results) - 1; i >= 0; i-- {
		if results[i].RequestID == requestID {
			return results[i], true
		}
	}
	return durableTupleResult{}, false
}

func isMissingTupleState(err error) bool {
	return errors.Is(err, routing.ErrNotFound) ||
		strings.Contains(strings.ToLower(err.Error()), "not found")
}

type tupleAuthorityError struct {
	Fence tupleFence
}

func (e *tupleAuthorityError) Error() string {
	return fmt.Sprintf(
		"%v: current epoch=%d writer=%q",
		errStaleTupleAuthority,
		e.Fence.Epoch,
		e.Fence.Writer,
	)
}

func (e *tupleAuthorityError) Unwrap() error {
	return errStaleTupleAuthority
}
