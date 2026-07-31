package tuplespace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/pht"
)

const (
	indexMutationProtocolID      protocol.ID = "/tarsus/pht-mutation/1.0.0"
	indexOwnershipKey                        = "__tarsus_global_tuple_name_index__"
	maxConcurrentOwnerReads                  = 32
	defaultIndexMutationTimeout              = 60 * time.Second
	indexRouteAttemptTimeout                 = 15 * time.Second
	indexAuthorityAttemptTimeout             = 20 * time.Second
	maxIndexMutationAttempts                 = 5
	maxIndexMemoEntries                      = 4096
)

var errNoIndexOverlayRoute = errors.New("no index overlay route")

func isStaleIndexMutation(err error) bool {
	return errors.Is(err, errStaleIndexAuthority) ||
		errors.Is(err, pht.ErrStaleWriteFence)
}

type indexMutation struct {
	Operation   string         `json:"operation"`
	Key         string         `json:"key"`
	Shard       int            `json:"shard"`
	Fence       pht.WriteFence `json:"fence"`
	RequestID   string         `json:"request_id,omitempty"`
	Target      string         `json:"target,omitempty"`
	Visited     []string       `json:"visited,omitempty"`
	RouteBudget int            `json:"route_budget,omitempty"`
}

type indexMutationResponse struct {
	Error          string `json:"error,omitempty"`
	StaleAuthority bool   `json:"stale_authority,omitempty"`
	RouteFailure   bool   `json:"route_failure,omitempty"`
}

// IndexCoordinator serializes all PHT read-modify-write mutations at one
// deterministic overlay owner. Queries still read PHT nodes directly from the
// DHT and therefore do not pass through this coordinator.
type IndexCoordinator struct {
	host                 host.Host
	authority            *indexAuthorityManager
	indexes              []*pht.MutableIndex
	adoptions            []indexFenceAdoption
	timeout              time.Duration
	metrics              indexMutationMetrics
	requireVerifiedPeers bool
	requestSequence      atomic.Uint64
	memoMu               sync.Mutex
	memo                 map[string]*indexMemoEntry
}

type indexFenceAdoption struct {
	mu    sync.Mutex
	fence pht.WriteFence
}

type indexMemoEntry struct {
	done      chan struct{}
	err       error
	completed time.Time
}

type indexMutationMetrics struct {
	total      atomic.Uint64
	local      atomic.Uint64
	remote     atomic.Uint64
	failures   atomic.Uint64
	durationNS atomic.Uint64
	perShard   []atomic.Uint64
}

// IndexMutationStats is a monotonic snapshot of coordinator activity.
type IndexMutationStats struct {
	Total                uint64   `json:"total"`
	Local                uint64   `json:"local"`
	Remote               uint64   `json:"remote"`
	Failures             uint64   `json:"failures"`
	DurationNS           uint64   `json:"duration_ns"`
	AuthorityClaims      uint64   `json:"authority_claims"`
	AuthorityTransitions uint64   `json:"authority_transitions"`
	AuthorityRenewals    uint64   `json:"authority_renewals"`
	FenceRejections      uint64   `json:"fence_rejections"`
	PerShard             []uint64 `json:"per_shard"`
}

func NewIndexCoordinator(h host.Host, resolver TupleOwnerResolver, stores []pht.ValueStore) (*IndexCoordinator, error) {
	if h == nil || resolver == nil || len(stores) == 0 {
		return nil, errors.New("host, owner resolver, and PHT shard stores required")
	}
	indexes := make([]*pht.MutableIndex, len(stores))
	for shard, store := range stores {
		index, err := pht.NewMutableIndex(store)
		if err != nil {
			return nil, fmt.Errorf("PHT shard %d: %w", shard, err)
		}
		indexes[shard] = index
	}
	authority, err := newIndexAuthorityManager(h.ID(), resolver, stores)
	if err != nil {
		return nil, err
	}
	c := &IndexCoordinator{
		host:      h,
		authority: authority,
		indexes:   indexes,
		adoptions: make([]indexFenceAdoption, len(indexes)),
		timeout:   defaultIndexMutationTimeout,
		memo:      make(map[string]*indexMemoEntry),
	}
	c.metrics.perShard = make([]atomic.Uint64, len(indexes))
	h.SetStreamHandler(indexMutationProtocolID, c.handleStream)
	return c, nil
}

// SetRequireVerifiedPeers makes remote mutation streams wait for the host
// handshake gate before negotiating the PHT mutation protocol.
func (c *IndexCoordinator) SetRequireVerifiedPeers(required bool) {
	c.requireVerifiedPeers = required
}

// SetAuthorityTiming configures claim propagation, lease duration, and the
// no-acceptance safety margin. Production uses the defaults; focused tests can
// shorten the propagation window.
func (c *IndexCoordinator) SetAuthorityTiming(settle, lease, margin time.Duration) {
	c.authority.setTiming(settle, lease, margin)
}

func (c *IndexCoordinator) Close() {
	if c != nil && c.host != nil {
		c.host.RemoveStreamHandler(indexMutationProtocolID)
	}
}

func (c *IndexCoordinator) Insert(ctx context.Context, key string) error {
	_, err := c.InsertWithStats(ctx, key)
	return err
}

// InsertWithStats attributes mutation work to this exact call rather than
// inferring it from process-wide counter snapshots.
func (c *IndexCoordinator) InsertWithStats(ctx context.Context, key string) (IndexMutationStats, error) {
	shard := pht.ShardForKey(key, len(c.indexes))
	return c.mutate(ctx, indexMutation{Operation: "insert", Key: key, Shard: shard})
}

func (c *IndexCoordinator) Delete(ctx context.Context, key string) error {
	shard := pht.ShardForKey(key, len(c.indexes))
	_, err := c.mutate(ctx, indexMutation{Operation: "delete", Key: key, Shard: shard})
	return err
}

func (c *IndexCoordinator) mutate(ctx context.Context, mutation indexMutation) (stats IndexMutationStats, err error) {
	started := time.Now()
	if mutation.RequestID == "" {
		mutation.RequestID = fmt.Sprintf(
			"%s-%x-%x",
			c.host.ID(),
			time.Now().UnixNano(),
			c.requestSequence.Add(1),
		)
	}
	stats.Total = 1
	stats.PerShard = make([]uint64, len(c.metrics.perShard))
	c.metrics.total.Add(1)
	if mutation.Shard >= 0 && mutation.Shard < len(c.metrics.perShard) {
		c.metrics.perShard[mutation.Shard].Add(1)
		stats.PerShard[mutation.Shard] = 1
	}
	defer func() {
		stats.DurationNS = uint64(time.Since(started).Nanoseconds())
		c.metrics.durationNS.Add(stats.DurationNS)
		if err != nil {
			stats.Failures = 1
			c.metrics.failures.Add(1)
		}
	}()
	classified := false
	var lastErr error
	for attempt := 0; attempt < maxIndexMutationAttempts && ctx.Err() == nil; attempt++ {
		fence, resolveErr := c.authority.resolve(ctx, mutation.Shard)
		if resolveErr != nil {
			return stats, fmt.Errorf("resolve index authority: %w", resolveErr)
		}
		owner, decodeErr := peer.Decode(fence.Writer)
		if decodeErr != nil {
			return stats, fmt.Errorf("decode index authority owner: %w", decodeErr)
		}
		if !classified {
			if owner == c.host.ID() {
				stats.Local = 1
				c.metrics.local.Add(1)
			} else {
				stats.Remote = 1
				c.metrics.remote.Add(1)
			}
			classified = true
		}
		mutation.Fence = fence
		var response indexMutationResponse
		var dispatchErr error
		// A response timeout is ambiguous: the owner may be committing the
		// request while the return path is slow. Retry the same deduplicated
		// request once before declaring the fenced writer unreachable.
		for deliveryAttempt := 0; deliveryAttempt < 2 && ctx.Err() == nil; deliveryAttempt++ {
			attemptCtx, cancel := boundedAttemptContext(ctx, indexAuthorityAttemptTimeout)
			response, dispatchErr = c.dispatchMutation(attemptCtx, owner, mutation)
			cancel()
			if dispatchErr == nil && !response.RouteFailure {
				break
			}
		}
		if dispatchErr != nil || response.RouteFailure {
			if dispatchErr == nil {
				dispatchErr = fmt.Errorf("%w: %s", errNoIndexOverlayRoute, response.Error)
			}
			lastErr = dispatchErr
			c.authority.invalidate(mutation.Shard, fence)
			if _, failoverErr := c.authority.failover(ctx, mutation.Shard, fence); failoverErr != nil {
				return stats, fmt.Errorf(
					"index authority %s unreachable: %v; failover: %w",
					fence.Writer,
					dispatchErr,
					failoverErr,
				)
			}
			continue
		}
		if response.StaleAuthority {
			lastErr = errors.New(response.Error)
			c.authority.invalidate(mutation.Shard, fence)
			// The authority record can lag a stronger fence already committed
			// in the PHT. Re-reading the same stale record immediately only
			// repeats the rejection. Reconcile through failover: if the DHT now
			// exposes another live winner it is reused; otherwise a higher
			// epoch is published and necessarily dominates the persisted fence.
			if _, reconcileErr := c.authority.failover(
				ctx,
				mutation.Shard,
				fence,
			); reconcileErr != nil {
				lastErr = fmt.Errorf(
					"%v; reconcile stale index authority: %w",
					lastErr,
					reconcileErr,
				)
			}
			continue
		}
		if response.Error != "" {
			return stats, errors.New(response.Error)
		}
		return stats, nil
	}
	if ctx.Err() != nil {
		return stats, ctx.Err()
	}
	if lastErr != nil {
		return stats, lastErr
	}
	return stats, errors.New("index mutation did not converge")
}

func (c *IndexCoordinator) dispatchMutation(
	ctx context.Context,
	owner peer.ID,
	mutation indexMutation,
) (indexMutationResponse, error) {
	if owner == c.host.ID() {
		err := c.applyAuthorizedOnce(ctx, mutation)
		return indexMutationResponse{
			Error:          errorString(err),
			StaleAuthority: isStaleIndexMutation(err),
		}, nil
	}
	mutation.Target = owner.String()
	mutation.RouteBudget = maxTupleRouteWork
	mutation.Visited = appendVisitedPeer(nil, c.host.ID())
	response, err := c.forwardIndexMutation(ctx, mutation)
	if errors.Is(err, errNoIndexOverlayRoute) {
		if resolveErr := ensureTuplePeerAddress(
			ctx,
			c.host,
			c.authority.resolver,
			owner,
		); resolveErr != nil {
			return indexMutationResponse{}, fmt.Errorf(
				"overlay route failed: %v; resolve index-owner address: %w",
				err,
				resolveErr,
			)
		}
		response, err = c.sendMutationDirect(ctx, owner, mutation)
	}
	if err != nil {
		return indexMutationResponse{}, err
	}
	return response, nil
}

func (c *IndexCoordinator) sendMutationDirect(
	ctx context.Context,
	owner peer.ID,
	mutation indexMutation,
) (indexMutationResponse, error) {
	stream, err := openTuplePeerStream(ctx, c.host, owner, indexMutationProtocolID, c.requireVerifiedPeers)
	if err != nil {
		return indexMutationResponse{}, fmt.Errorf("open index-owner stream: %w", err)
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	if err := json.NewEncoder(stream).Encode(mutation); err != nil {
		return indexMutationResponse{}, fmt.Errorf("write index mutation: %w", err)
	}
	var response indexMutationResponse
	if err := json.NewDecoder(io.LimitReader(stream, maxTupleRequestBytes)).Decode(&response); err != nil {
		return indexMutationResponse{}, fmt.Errorf("read index response: %w", err)
	}
	return response, nil
}

func (c *IndexCoordinator) forwardIndexMutation(
	ctx context.Context,
	mutation indexMutation,
) (indexMutationResponse, error) {
	target, err := peer.Decode(mutation.Target)
	if err != nil {
		return indexMutationResponse{}, fmt.Errorf("decode index route target: %w", err)
	}
	if target == c.host.ID() {
		err := c.applyAuthorizedOnce(ctx, mutation)
		return indexMutationResponse{
			Error:          errorString(err),
			StaleAuthority: isStaleIndexMutation(err),
		}, nil
	}
	if mutation.RouteBudget <= 0 {
		return indexMutationResponse{}, fmt.Errorf(
			"%w: route budget exhausted for %s",
			errNoIndexOverlayRoute,
			target,
		)
	}
	branchLimit := 1
	if routeStartedHere(mutation.Visited, c.host.ID()) {
		branchLimit = maxTupleRouteBranches
	}
	mutation.Visited = appendVisitedPeer(mutation.Visited, c.host.ID())
	candidates := connectedRouteCandidates(c.host, target, mutation.Visited)
	if len(candidates) == 0 {
		return indexMutationResponse{}, fmt.Errorf(
			"%w: no unvisited neighbor for %s",
			errNoIndexOverlayRoute,
			target,
		)
	}
	if len(candidates) > branchLimit {
		candidates = candidates[:branchLimit]
	}
	if len(candidates) > mutation.RouteBudget {
		candidates = candidates[:mutation.RouteBudget]
	}
	remainingBudget := mutation.RouteBudget - len(candidates)
	budgetPerBranch := remainingBudget / len(candidates)
	extraBudget := remainingBudget % len(candidates)
	type routeResult struct {
		response indexMutationResponse
		err      error
	}
	routeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan routeResult, len(candidates))
	for candidateIndex, next := range candidates {
		next := next
		forwarded := mutation
		forwarded.RouteBudget = budgetPerBranch
		if candidateIndex < extraBudget {
			forwarded.RouteBudget++
		}
		go func() {
			attemptCtx, attemptCancel := boundedAttemptContext(
				routeCtx,
				indexRouteAttemptTimeout,
			)
			response, err := c.sendMutationDirect(attemptCtx, next, forwarded)
			attemptCancel()
			results <- routeResult{response: response, err: err}
		}()
	}
	var lastErr error
	for range candidates {
		result := <-results
		if result.err == nil && !result.response.RouteFailure {
			cancel()
			return result.response, nil
		}
		if result.err != nil {
			lastErr = result.err
		} else {
			lastErr = errors.New(result.response.Error)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return indexMutationResponse{}, fmt.Errorf(
		"%w: target %s: %v",
		errNoIndexOverlayRoute,
		target,
		lastErr,
	)
}

// Snapshot returns monotonic mutation counters without resetting them.
func (c *IndexCoordinator) Snapshot() IndexMutationStats {
	authority := c.authority.snapshot()
	stats := IndexMutationStats{
		Total:                c.metrics.total.Load(),
		Local:                c.metrics.local.Load(),
		Remote:               c.metrics.remote.Load(),
		Failures:             c.metrics.failures.Load(),
		DurationNS:           c.metrics.durationNS.Load(),
		AuthorityClaims:      authority.claims,
		AuthorityTransitions: authority.transitions,
		AuthorityRenewals:    authority.renewals,
		FenceRejections:      authority.rejections,
		PerShard:             make([]uint64, len(c.metrics.perShard)),
	}
	for shard := range c.metrics.perShard {
		stats.PerShard[shard] = c.metrics.perShard[shard].Load()
	}
	return stats
}

func (c *IndexCoordinator) handleStream(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(c.timeout))
	var mutation indexMutation
	if err := json.NewDecoder(io.LimitReader(stream, maxTupleRequestBytes)).Decode(&mutation); err != nil {
		_ = json.NewEncoder(stream).Encode(indexMutationResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	response := indexMutationResponse{}
	if mutation.Target != "" && mutation.Target != c.host.ID().String() {
		var err error
		response, err = c.forwardIndexMutation(ctx, mutation)
		if err != nil {
			response.Error = err.Error()
			response.RouteFailure = errors.Is(err, errNoIndexOverlayRoute)
		}
	} else {
		err := c.applyAuthorizedOnce(ctx, mutation)
		response.Error = errorString(err)
		response.StaleAuthority = isStaleIndexMutation(err)
	}
	_ = json.NewEncoder(stream).Encode(response)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (c *IndexCoordinator) applyAuthorized(ctx context.Context, mutation indexMutation) error {
	if mutation.Shard < 0 || mutation.Shard >= len(c.indexes) {
		return fmt.Errorf("invalid index shard %d", mutation.Shard)
	}
	if err := c.authority.validateForApply(ctx, mutation.Shard, mutation.Fence); err != nil {
		c.authority.invalidate(mutation.Shard, mutation.Fence)
		return err
	}
	if err := c.ensureFenceAdopted(ctx, mutation.Shard, mutation.Fence); err != nil {
		return err
	}
	switch mutation.Operation {
	case "insert":
		return c.indexes[mutation.Shard].InsertFenced(ctx, mutation.Key, mutation.Fence)
	case "delete":
		return c.indexes[mutation.Shard].DeleteFenced(ctx, mutation.Key, mutation.Fence)
	default:
		return fmt.Errorf("unsupported index mutation %q", mutation.Operation)
	}
}

func (c *IndexCoordinator) applyAuthorizedOnce(
	ctx context.Context,
	mutation indexMutation,
) error {
	if mutation.RequestID == "" {
		return c.applyAuthorized(ctx, mutation)
	}
	c.memoMu.Lock()
	if existing := c.memo[mutation.RequestID]; existing != nil {
		c.memoMu.Unlock()
		select {
		case <-existing.done:
			return existing.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	entry := &indexMemoEntry{done: make(chan struct{})}
	c.memo[mutation.RequestID] = entry
	c.memoMu.Unlock()

	err := c.applyAuthorized(ctx, mutation)
	c.memoMu.Lock()
	entry.err = err
	entry.completed = time.Now()
	close(entry.done)
	if err != nil {
		delete(c.memo, mutation.RequestID)
	} else {
		c.evictIndexMemoLocked()
	}
	c.memoMu.Unlock()
	return err
}

func (c *IndexCoordinator) evictIndexMemoLocked() {
	for len(c.memo) > maxIndexMemoEntries {
		var oldestID string
		var oldest time.Time
		for requestID, entry := range c.memo {
			if entry.completed.IsZero() {
				continue
			}
			if oldestID == "" || entry.completed.Before(oldest) {
				oldestID = requestID
				oldest = entry.completed
			}
		}
		if oldestID == "" {
			return
		}
		delete(c.memo, oldestID)
	}
}

func (c *IndexCoordinator) ensureFenceAdopted(
	ctx context.Context,
	shard int,
	fence pht.WriteFence,
) error {
	adoption := &c.adoptions[shard]
	adoption.mu.Lock()
	defer adoption.mu.Unlock()
	if pht.CompareWriteFences(adoption.fence, fence) == 0 {
		return nil
	}
	if err := c.indexes[shard].AdoptFence(ctx, fence); err != nil {
		return fmt.Errorf("adopt index authority fence: %w", err)
	}
	adoption.fence = fence
	return nil
}

// IndexedTupleSpace maintains a distributed tuple-name index around an
// authoritative tuple space. Index records are hints: every candidate is
// verified by an exact operation at its tuple owner.
type IndexedTupleSpace struct {
	base            TupleSpace
	stores          []pht.ValueStore
	coordinator     *IndexCoordinator
	timeout         time.Duration
	mutationTimeout time.Duration
	bloomPruning    bool
	indexedNames    sync.Map
	mutationLocks   []sync.Mutex
}

// IndexedQueryStats aggregates direct index and owner-verification work across
// all PHT shards for one tuple read.
type IndexedQueryStats struct {
	QueryKind          string                   `json:"query_kind"`
	ShardsContacted    int                      `json:"shards_contacted"`
	ShardsSucceeded    int                      `json:"shards_succeeded"`
	ShardsFailed       int                      `json:"shards_failed"`
	NodesFetched       int                      `json:"nodes_fetched"`
	BranchesConsidered int                      `json:"branches_considered"`
	BranchesPruned     int                      `json:"branches_pruned"`
	IndexCandidates    int                      `json:"index_candidates"`
	IndexMatches       int                      `json:"index_matches"`
	OwnerAttempts      int                      `json:"owner_attempts"`
	VerifiedMatches    int                      `json:"verified_matches"`
	DurationNS         int64                    `json:"duration_ns"`
	ShardStats         []IndexedShardQueryStats `json:"shard_stats,omitempty"`
}

// IndexedShardQueryStats preserves the per-shard evidence behind aggregate
// query counters. In particular, a partial query now identifies the failed
// shard and its error instead of reporting only ShardsFailed.
type IndexedShardQueryStats struct {
	Shard              int    `json:"shard"`
	Succeeded          bool   `json:"succeeded"`
	Error              string `json:"error,omitempty"`
	NodesFetched       int    `json:"nodes_fetched"`
	BranchesConsidered int    `json:"branches_considered"`
	BranchesPruned     int    `json:"branches_pruned"`
	IndexCandidates    int    `json:"index_candidates"`
	IndexMatches       int    `json:"index_matches"`
}

func NewIndexedTupleSpace(base TupleSpace, stores []pht.ValueStore, coordinator *IndexCoordinator) (*IndexedTupleSpace, error) {
	if base == nil || len(stores) == 0 || coordinator == nil {
		return nil, errors.New("base tuple space, PHT shard stores, and index coordinator required")
	}
	return &IndexedTupleSpace{
		base:            base,
		stores:          stores,
		coordinator:     coordinator,
		timeout:         defaultTupleTimeout,
		mutationTimeout: defaultIndexMutationTimeout,
		bloomPruning:    true,
		mutationLocks:   make([]sync.Mutex, len(stores)),
	}, nil
}

// SetBloomPruning enables or disables Bloom-based branch pruning. Disabling it
// is intended for controlled experiments; exact verification remains enabled.
func (i *IndexedTupleSpace) SetBloomPruning(enabled bool) {
	i.bloomPruning = enabled
}

func (i *IndexedTupleSpace) TsPut(name string, value []byte) (int, error) {
	code, _, err := i.TsPutWithMutationStats(name, value)
	return code, err
}

// TsPutWithMutationStats returns mutation work attributable to this tuple put.
func (i *IndexedTupleSpace) TsPutWithMutationStats(name string, value []byte) (int, IndexMutationStats, error) {
	shard := pht.ShardForKey(name, len(i.stores))
	i.mutationLocks[shard].Lock()
	ctx, cancel := context.WithTimeout(context.Background(), i.mutationTimeout)
	// Index first: a stale hint is safe, while an unindexed live tuple would be
	// invisible to associative queries.
	stats := IndexMutationStats{PerShard: make([]uint64, len(i.stores))}
	if _, indexed := i.indexedNames.Load(name); !indexed {
		var err error
		stats, err = i.coordinator.InsertWithStats(ctx, name)
		if err != nil {
			cancel()
			i.mutationLocks[shard].Unlock()
			return TSPUT_ER, stats, err
		}
		i.indexedNames.Store(name, struct{}{})
	}
	cancel()
	i.mutationLocks[shard].Unlock()
	code, err := i.base.TsPut(name, value)
	return code, stats, err
}

// TsReplace updates a singleton tuple and reasserts its PHT membership first.
// Renewable records serve as low-rate anti-entropy for index writes that may
// have raced during an ownership transition. Refresh callers stagger these
// operations; reassertion also repairs records left by older software.
func (i *IndexedTupleSpace) TsReplace(name string, value []byte) (int, error) {
	replacer, ok := i.base.(NamedTupleReplacer)
	if !ok {
		return TSPUT_ER, errors.New("base tuple space does not support replacement")
	}
	shard := pht.ShardForKey(name, len(i.stores))
	i.mutationLocks[shard].Lock()
	defer i.mutationLocks[shard].Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), i.mutationTimeout)
	defer cancel()
	if err := i.coordinator.Insert(ctx, name); err != nil {
		return TSPUT_ER, err
	}
	i.indexedNames.Store(name, struct{}{})
	return replacer.TsReplace(name, value)
}

// MutationSnapshot returns the coordinator counters visible from this node.
func (i *IndexedTupleSpace) MutationSnapshot() IndexMutationStats {
	return i.coordinator.Snapshot()
}

func (i *IndexedTupleSpace) TsRead(expr string) ([]byte, error) {
	value, _, err := i.TsReadWithStats(expr)
	return value, err
}

// TsReadWithStats performs a read and returns direct query-cost metrics.
func (i *IndexedTupleSpace) TsReadWithStats(expr string) ([]byte, IndexedQueryStats, error) {
	started := time.Now()
	stats := IndexedQueryStats{}
	if !isTuplePattern(expr) {
		stats.QueryKind = "exact"
		value, err := i.base.TsRead(expr)
		stats.OwnerAttempts = 1
		if err == nil {
			stats.VerifiedMatches = 1
		}
		stats.DurationNS = time.Since(started).Nanoseconds()
		return value, stats, err
	}
	names, stats, err := i.candidatesWithStats(expr)
	if err != nil {
		stats.DurationNS = time.Since(started).Nanoseconds()
		return nil, stats, err
	}
	value, attempts, err := i.readFirstCandidate(names)
	stats.OwnerAttempts = attempts
	if err == nil {
		stats.VerifiedMatches = 1
		stats.DurationNS = time.Since(started).Nanoseconds()
		return value, stats, nil
	}
	stats.DurationNS = time.Since(started).Nanoseconds()
	return nil, stats, err
}

type contextTupleReader interface {
	TsReadContext(context.Context, string) ([]byte, error)
}

// readFirstCandidate verifies index hints with bounded concurrency and one
// overall deadline. Index entries are hints and may be stale after partial
// writes or ownership changes; verifying them serially would multiply the
// tuple timeout by the number of stale candidates.
func (i *IndexedTupleSpace) readFirstCandidate(names []string) ([]byte, int, error) {
	if len(names) == 0 {
		return nil, 0, ErrTupleNotFound
	}
	ctx, cancel := context.WithTimeout(context.Background(), i.timeout)
	defer cancel()

	workerCount := maxConcurrentOwnerReads
	if len(names) < workerCount {
		workerCount = len(names)
	}
	jobs := make(chan string)
	var attempts atomic.Int64
	var wg sync.WaitGroup
	var resultOnce sync.Once
	var result []byte
	var found bool

	read := func(name string) ([]byte, error) {
		if contextual, ok := i.base.(contextTupleReader); ok {
			return contextual.TsReadContext(ctx, name)
		}
		done := make(chan struct {
			value []byte
			err   error
		}, 1)
		go func() {
			value, err := i.base.TsRead(name)
			done <- struct {
				value []byte
				err   error
			}{value: value, err: err}
		}()
		select {
		case outcome := <-done:
			return outcome.value, outcome.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	wg.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case name, ok := <-jobs:
					if !ok {
						return
					}
					attempts.Add(1)
					value, err := read(name)
					if err == nil {
						resultOnce.Do(func() {
							result = append([]byte(nil), value...)
							found = true
							cancel()
						})
						return
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, name := range names {
			select {
			case jobs <- name:
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()
	if found {
		return result, int(attempts.Load()), nil
	}
	if ctx.Err() != nil {
		return nil, int(attempts.Load()), ctx.Err()
	}
	return nil, int(attempts.Load()), ErrTupleNotFound
}

func (i *IndexedTupleSpace) TsGet(expr string) ([]byte, error) {
	if !isTuplePattern(expr) {
		value, err := i.base.TsGet(expr)
		if err == nil {
			i.removeIfExhausted(expr)
		}
		return value, err
	}
	names, _, err := i.candidatesWithStats(expr)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		value, getErr := i.base.TsGet(name)
		if getErr != nil {
			continue
		}
		i.removeIfExhausted(name)
		return value, nil
	}
	return nil, ErrTupleNotFound
}

func (i *IndexedTupleSpace) candidates(expr string) ([]string, error) {
	names, _, err := i.candidatesWithStats(expr)
	return names, err
}

func (i *IndexedTupleSpace) candidatesWithStats(expr string) ([]string, IndexedQueryStats, error) {
	query := pht.ParseQuery(expr)
	stats := IndexedQueryStats{ShardsContacted: len(i.stores)}
	simpleWildcard := isSimpleWildcard(expr)
	if !simpleWildcard {
		stats.QueryKind = "regex"
	} else {
		switch query.Kind {
		case pht.QueryPrefix:
			stats.QueryKind = "prefix"
		case pht.QuerySubstring:
			stats.QueryKind = "substring"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), i.timeout)
	defer cancel()
	type result struct {
		shard int
		names []string
		stats pht.QueryStats
		err   error
	}
	results := make(chan result, len(i.stores))
	for shard, store := range i.stores {
		go func(shard int, store pht.ValueStore) {
			var names []string
			var queryStats pht.QueryStats
			var err error
			if !simpleWildcard {
				names, queryStats, err = pht.RegexQueryDHTWithStats(ctx, store, expr)
			} else {
				switch query.Kind {
				case pht.QueryPrefix:
					names, queryStats, err = pht.PrefixQueryDHTWithStats(ctx, store, query.Prefix)
				case pht.QuerySubstring:
					names, queryStats, err = pht.ExecuteSubstringQueryWithStatsAndPruning(ctx, store, query.Substring, 0, i.bloomPruning)
				default:
					err = ErrTupleNotFound
				}
			}
			results <- result{shard: shard, names: names, stats: queryStats, err: err}
		}(shard, store)
	}
	var parts [][]string
	var lastErr error
	for range i.stores {
		result := <-results
		shardStats := IndexedShardQueryStats{
			Shard:              result.shard,
			Succeeded:          result.err == nil,
			NodesFetched:       result.stats.NodesFetched,
			BranchesConsidered: result.stats.BranchesConsidered,
			BranchesPruned:     result.stats.BranchesPruned,
			IndexCandidates:    result.stats.Candidates,
			IndexMatches:       result.stats.Matches,
		}
		if result.err != nil {
			shardStats.Error = result.err.Error()
			stats.ShardStats = append(stats.ShardStats, shardStats)
			stats.ShardsFailed++
			lastErr = result.err
			continue
		}
		stats.ShardStats = append(stats.ShardStats, shardStats)
		stats.ShardsSucceeded++
		stats.NodesFetched += result.stats.NodesFetched
		stats.BranchesConsidered += result.stats.BranchesConsidered
		stats.BranchesPruned += result.stats.BranchesPruned
		stats.IndexCandidates += result.stats.Candidates
		stats.IndexMatches += result.stats.Matches
		parts = append(parts, result.names)
	}
	sort.Slice(stats.ShardStats, func(a, b int) bool {
		return stats.ShardStats[a].Shard < stats.ShardStats[b].Shard
	})
	names := pht.CombineResults(parts...)
	if len(names) == 0 && lastErr != nil {
		return nil, stats, lastErr
	}
	return names, stats, nil
}

func (i *IndexedTupleSpace) removeIfExhausted(name string) {
	if _, err := i.base.TsRead(name); err == nil {
		return
	}
	shard := pht.ShardForKey(name, len(i.stores))
	i.mutationLocks[shard].Lock()
	defer i.mutationLocks[shard].Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), i.mutationTimeout)
	defer cancel()
	if err := i.coordinator.Delete(ctx, name); err == nil {
		i.indexedNames.Delete(name)
	}
}
