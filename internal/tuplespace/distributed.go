// Purpose: Repository-native distributed tuple-space transport and ownership.
package tuplespace

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	kbucket "github.com/libp2p/go-libp2p-kbucket"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/pht"
)

const (
	// NativeTupleProtocolID is the libp2p protocol used by Tarsus tuple owners.
	NativeTupleProtocolID        protocol.ID = "/tarsus/tuplespace/1.0.0"
	defaultTupleTimeout                      = 45 * time.Second
	establishedStreamOpenTimeout             = 5 * time.Second
	tupleRouteAttemptTimeout                 = 8 * time.Second
	maxTupleRequestBytes                     = 1 << 20
	// Route work is conserved across recursive branches, so these values cap
	// one request at 64 relay visits rather than multiplying at every hop.
	// Four branches tolerate multiple unusable established connections while
	// remaining bounded at and above the 50-node correctness gate.
	maxTupleRouteWork       = 64
	maxTupleRouteBranches   = 4
	maxTupleMemoEntries     = 4096
	maxTupleFenceEntries    = 65536
	maxDurableTupleAttempts = 8
	handshakeVerifiedTag    = "handshake_ok"
)

var errNoTupleOverlayRoute = errors.New("no tuple overlay route")

// TupleOwnerResolver deterministically chooses the peer that serializes exact
// operations for a tuple name. Production resolvers should derive ownership
// from the shared DHT keyspace.
type TupleOwnerResolver interface {
	ResolveTupleOwner(ctx context.Context, tupleName string) (peer.ID, error)
}

type tupleWireRequest struct {
	Operation   string   `json:"operation"`
	Name        string   `json:"name"`
	Value       []byte   `json:"value,omitempty"`
	Expected    []byte   `json:"expected,omitempty"`
	RequestID   string   `json:"request_id,omitempty"`
	Target      string   `json:"target,omitempty"`
	Visited     []string `json:"visited,omitempty"`
	RouteBudget int      `json:"route_budget,omitempty"`
	Epoch       uint64   `json:"epoch,omitempty"`
	Writer      string   `json:"writer,omitempty"`
}

func (d *DistributedTupleSpace) CompareAndSwapExact(ctx context.Context, name string, expected, next []byte) error {
	if name == "" || isTuplePattern(name) {
		return errors.New("CAS requires an exact name")
	}
	_, err := d.exact(ctx, tupleWireRequest{Operation: "cas", Name: name, Expected: expected, Value: next})
	return err
}

func (d *DistributedTupleSpace) ReadExact(ctx context.Context, name string) ([]byte, error) {
	if name == "" || isTuplePattern(name) {
		return nil, errors.New("exact name required")
	}
	return d.exact(ctx, tupleWireRequest{Operation: "read", Name: name})
}

type tupleWireResponse struct {
	Value         []byte `json:"value,omitempty"`
	Error         string `json:"error,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	CurrentEpoch  uint64 `json:"current_epoch,omitempty"`
	CurrentWriter string `json:"current_writer,omitempty"`
}

// DistributedTupleSpace routes exact operations to a deterministic owner. Each
// owner executes requests through NativeTupleSpace, making its mutex the
// per-owner serialization boundary. Associative operations currently query all
// reachable peers; the distributed index can supply a narrower candidate set
// without changing the ownership or consume protocol.
type DistributedTupleSpace struct {
	host                 host.Host
	resolver             TupleOwnerResolver
	local                *NativeTupleSpace
	timeout              time.Duration
	requireVerifiedPeers bool
	requestSequence      atomic.Uint64
	memoMu               sync.Mutex
	memo                 map[string]*tupleMemoEntry
	durable              *durableTupleStore
	fenceMu              sync.Mutex
	fences               map[string]tupleFence
	fenceOrder           []string
}

type tupleMemoEntry struct {
	done      chan struct{}
	value     []byte
	err       error
	completed time.Time
}

// NewDistributedTupleSpace installs the native tuple protocol on h.
func NewDistributedTupleSpace(h host.Host, resolver TupleOwnerResolver) (*DistributedTupleSpace, error) {
	if h == nil {
		return nil, errors.New("host required")
	}
	if resolver == nil {
		return nil, errors.New("tuple owner resolver required")
	}
	d := &DistributedTupleSpace{
		host:     h,
		resolver: resolver,
		local:    NewNativeTupleSpace(),
		timeout:  defaultTupleTimeout,
		memo:     make(map[string]*tupleMemoEntry),
		fences:   make(map[string]tupleFence),
	}
	h.SetStreamHandler(NativeTupleProtocolID, d.handleStream)
	return d, nil
}

// SetRequireVerifiedPeers makes tuple streams wait for the host's handshake
// gate to verify a newly dialed peer. Production nodes enable this after
// installing the gate; direct libp2p tests may leave it disabled.
func (d *DistributedTupleSpace) SetRequireVerifiedPeers(required bool) {
	d.requireVerifiedPeers = required
}

// EnableDurableState makes exact-name tuple operations commit their multiset,
// ownership fence, and retry results to the replicated DHT before replying.
// It is enabled by production node construction; the in-memory mode remains
// available for focused transport tests and standalone use.
func (d *DistributedTupleSpace) EnableDurableState(store pht.ValueStore) error {
	durable, err := newDurableTupleStore(d.host.ID(), d.resolver, store)
	if err != nil {
		return err
	}
	durable.project = d.local
	d.durable = durable
	return nil
}

// SetDurableStateTiming changes claim propagation and lease timing for tests.
// Production uses the defaults established by EnableDurableState.
func (d *DistributedTupleSpace) SetDurableStateTiming(
	settle time.Duration,
	lease time.Duration,
	margin time.Duration,
) {
	if d.durable != nil {
		d.durable.setTiming(settle, lease, margin)
	}
}

// Close removes the protocol handler. It does not close the shared libp2p host.
func (d *DistributedTupleSpace) Close() {
	if d != nil && d.host != nil {
		d.host.RemoveStreamHandler(NativeTupleProtocolID)
	}
}

func (d *DistributedTupleSpace) TsPut(name string, value []byte) (int, error) {
	if name == "" {
		return TSPUT_ER, errors.New("tuple name required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	_, err := d.exact(ctx, tupleWireRequest{Operation: "put", Name: name, Value: value})
	if err != nil {
		return TSPUT_ER, err
	}
	return 0, nil
}

// TsReplace routes an exact-name singleton update to the same deterministic
// tuple owner used by Put, Read, and Get.
func (d *DistributedTupleSpace) TsReplace(name string, value []byte) (int, error) {
	if name == "" {
		return TSPUT_ER, errors.New("tuple name required")
	}
	if isTuplePattern(name) {
		return TSPUT_ER, errors.New("tuple replacement requires an exact name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	_, err := d.exact(ctx, tupleWireRequest{Operation: "replace", Name: name, Value: value})
	if err != nil {
		return TSPUT_ER, err
	}
	return 0, nil
}

func (d *DistributedTupleSpace) TsRead(expr string) ([]byte, error) {
	return d.TsReadContext(context.Background(), expr)
}

// TsReadContext is the context-aware read path used by indexed candidate
// verification so one query has one end-to-end deadline rather than a fresh
// timeout for every stale candidate.
func (d *DistributedTupleSpace) TsReadContext(parent context.Context, expr string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, d.timeout)
	defer cancel()
	if !isTuplePattern(expr) {
		return d.exact(ctx, tupleWireRequest{Operation: "read", Name: expr})
	}
	return d.associative(ctx, "read", expr)
}

func (d *DistributedTupleSpace) TsGet(expr string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	if !isTuplePattern(expr) {
		return d.exact(ctx, tupleWireRequest{Operation: "get", Name: expr})
	}
	return d.associative(ctx, "get", expr)
}

func (d *DistributedTupleSpace) exact(ctx context.Context, req tupleWireRequest) ([]byte, error) {
	if d.durable != nil {
		return d.exactDurable(ctx, req)
	}
	owner, err := d.resolver.ResolveTupleOwner(ctx, req.Name)
	if err != nil {
		return nil, fmt.Errorf("resolve tuple owner: %w", err)
	}
	if owner == "" {
		return nil, errors.New("tuple owner resolver returned an empty peer ID")
	}
	return d.requestPeer(ctx, owner, req)
}

func (d *DistributedTupleSpace) exactDurable(
	ctx context.Context,
	req tupleWireRequest,
) ([]byte, error) {
	if req.RequestID == "" {
		req.RequestID = fmt.Sprintf(
			"%s-%x-%x",
			d.host.ID(),
			time.Now().UnixNano(),
			d.requestSequence.Add(1),
		)
	}
	fence, cached := d.cachedTupleFence(req.Name)
	if !cached {
		var err error
		fence, err = d.durable.resolve(ctx, req.Name)
		if err != nil {
			return nil, fmt.Errorf("resolve durable tuple owner: %w", err)
		}
		d.cacheTupleFence(req.Name, fence)
	}
	// Every request ID is committed with the durable tuple state, so following
	// several consecutive lease redirects is idempotent. Large clusters can
	// legitimately advance through more than four short tuple-owner epochs
	// while routing converges.
	for attempt := 0; attempt < maxDurableTupleAttempts; attempt++ {
		owner, decodeErr := peer.Decode(fence.Writer)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode durable tuple owner: %w", decodeErr)
		}
		req.Epoch = fence.Epoch
		req.Writer = fence.Writer
		value, requestErr := d.requestPeer(ctx, owner, req)
		if requestErr == nil || errors.Is(requestErr, ErrTupleNotFound) {
			d.cacheTupleFence(req.Name, fence)
			return value, requestErr
		}
		var stale *tupleAuthorityError
		if errors.As(requestErr, &stale) {
			if stale.Fence.Epoch == 0 || stale.Fence.Writer == "" {
				return nil, requestErr
			}
			fence = stale.Fence
			d.cacheTupleFence(req.Name, fence)
			continue
		}
		var application *tupleApplicationError
		if errors.As(requestErr, &application) || owner == d.host.ID() {
			return nil, requestErr
		}
		var err error
		fence, err = d.durable.failover(ctx, req.Name, fence)
		if err != nil {
			return nil, fmt.Errorf("fail over durable tuple owner: %w", err)
		}
		d.cacheTupleFence(req.Name, fence)
	}
	return nil, errors.New("durable tuple authority did not converge")
}

func (d *DistributedTupleSpace) cachedTupleFence(name string) (tupleFence, bool) {
	d.fenceMu.Lock()
	fence, ok := d.fences[name]
	d.fenceMu.Unlock()
	return fence, ok
}

func (d *DistributedTupleSpace) cacheTupleFence(name string, fence tupleFence) {
	if name == "" || fence.Epoch == 0 || fence.Writer == "" {
		return
	}
	d.fenceMu.Lock()
	defer d.fenceMu.Unlock()
	if current, exists := d.fences[name]; exists {
		if compareTupleFences(fence, current) >= 0 {
			d.fences[name] = fence
		}
		return
	}
	d.fences[name] = fence
	d.fenceOrder = append(d.fenceOrder, name)
	for len(d.fences) > maxTupleFenceEntries {
		oldest := d.fenceOrder[0]
		d.fenceOrder[0] = ""
		d.fenceOrder = d.fenceOrder[1:]
		delete(d.fences, oldest)
	}
}

// associative queries peers in stable peer-ID order. A consuming operation
// stops after the first owner atomically removes a match. Failures from one
// unreachable owner do not prevent trying the remaining reachable owners.
func (d *DistributedTupleSpace) associative(ctx context.Context, operation, expr string) ([]byte, error) {
	if _, err := compileTupleMatcher(expr); err != nil {
		return nil, err
	}
	peers := append([]peer.ID(nil), d.host.Network().Peers()...)
	peers = append(peers, d.host.ID())
	sort.Slice(peers, func(i, j int) bool { return peers[i].String() < peers[j].String() })

	var lastErr error
	for _, owner := range peers {
		value, err := d.requestPeer(ctx, owner, tupleWireRequest{
			Operation: operation,
			Name:      expr,
		})
		if err == nil {
			return value, nil
		}
		lastErr = err
	}
	if lastErr != nil && !errors.Is(lastErr, ErrTupleNotFound) {
		return nil, lastErr
	}
	return nil, ErrTupleNotFound
}

func (d *DistributedTupleSpace) requestPeer(ctx context.Context, owner peer.ID, req tupleWireRequest) ([]byte, error) {
	if req.RequestID == "" {
		req.RequestID = fmt.Sprintf(
			"%s-%x-%x",
			d.host.ID(),
			time.Now().UnixNano(),
			d.requestSequence.Add(1),
		)
	}
	if owner == d.host.ID() {
		return d.applyLocalOnce(ctx, req)
	}
	req.Target = owner.String()
	req.RouteBudget = maxTupleRouteWork
	req.Visited = appendVisitedPeer(req.Visited, d.host.ID())
	value, err := d.forwardTupleRequest(ctx, req)
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, errNoTupleOverlayRoute) {
		return nil, err
	}
	if err := ensureTuplePeerAddress(ctx, d.host, d.resolver, owner); err != nil {
		return nil, fmt.Errorf("resolve tuple-owner address: %w", err)
	}
	return d.requestPeerDirect(ctx, owner, req)
}

func (d *DistributedTupleSpace) requestPeerDirect(
	ctx context.Context,
	owner peer.ID,
	req tupleWireRequest,
) ([]byte, error) {
	stream, err := openTuplePeerStream(ctx, d.host, owner, NativeTupleProtocolID, d.requireVerifiedPeers)
	if err != nil {
		return nil, fmt.Errorf("open tuple-owner stream: %w", err)
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	if err := json.NewEncoder(stream).Encode(req); err != nil {
		_ = stream.Reset()
		return nil, fmt.Errorf("write tuple request: %w", err)
	}
	var response tupleWireResponse
	if err := json.NewDecoder(io.LimitReader(stream, maxTupleRequestBytes)).Decode(&response); err != nil {
		return nil, fmt.Errorf("read tuple response: %w", err)
	}
	if response.Error != "" {
		switch response.ErrorCode {
		case "not_found":
			return nil, ErrTupleNotFound
		case "conflict":
			return nil, ErrTupleCASConflict
		case "stale_authority":
			return nil, &tupleAuthorityError{Fence: tupleFence{
				Epoch:  response.CurrentEpoch,
				Writer: response.CurrentWriter,
			}}
		case "application":
			return nil, &tupleApplicationError{message: response.Error}
		}
		if response.Error == ErrTupleNotFound.Error() {
			return nil, ErrTupleNotFound
		}
		return nil, errors.New(response.Error)
	}
	return response.Value, nil
}

func (d *DistributedTupleSpace) forwardTupleRequest(
	ctx context.Context,
	req tupleWireRequest,
) ([]byte, error) {
	target, err := peer.Decode(req.Target)
	if err != nil {
		return nil, fmt.Errorf("decode tuple route target: %w", err)
	}
	if target == d.host.ID() {
		return d.applyLocalOnce(ctx, req)
	}
	if req.RouteBudget <= 0 {
		return nil, fmt.Errorf("%w: route budget exhausted for %s", errNoTupleOverlayRoute, target)
	}
	branchLimit := 1
	if routeStartedHere(req.Visited, d.host.ID()) {
		branchLimit = maxTupleRouteBranches
	}
	req.Visited = appendVisitedPeer(req.Visited, d.host.ID())
	candidates := connectedRouteCandidates(d.host, target, req.Visited)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: no unvisited neighbor for %s", errNoTupleOverlayRoute, target)
	}
	if len(candidates) > branchLimit {
		candidates = candidates[:branchLimit]
	}
	if len(candidates) > req.RouteBudget {
		candidates = candidates[:req.RouteBudget]
	}
	remainingBudget := req.RouteBudget - len(candidates)
	budgetPerBranch := remainingBudget / len(candidates)
	extraBudget := remainingBudget % len(candidates)
	type routeResult struct {
		value []byte
		err   error
	}
	routeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan routeResult, len(candidates))
	for candidateIndex, next := range candidates {
		next := next
		forwarded := req
		forwarded.RouteBudget = budgetPerBranch
		if candidateIndex < extraBudget {
			forwarded.RouteBudget++
		}
		go func() {
			attemptCtx, attemptCancel := boundedAttemptContext(
				routeCtx,
				tupleRouteAttemptTimeout,
			)
			value, err := d.requestPeerDirect(attemptCtx, next, forwarded)
			attemptCancel()
			results <- routeResult{value: value, err: err}
		}()
	}
	var lastErr error
	for range candidates {
		result := <-results
		if result.err == nil {
			cancel()
			return result.value, nil
		}
		if errors.Is(result.err, ErrTupleNotFound) {
			cancel()
			return nil, ErrTupleNotFound
		}
		lastErr = result.err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("%w: target %s: %v", errNoTupleOverlayRoute, target, lastErr)
}

func appendVisitedPeer(visited []string, id peer.ID) []string {
	encoded := id.String()
	for _, existing := range visited {
		if existing == encoded {
			return visited
		}
	}
	return append(visited, encoded)
}

// routeStartedHere distinguishes the one originating fan-out from relays.
// Relays receive a path that does not yet contain themselves and advance only
// one branch, preventing recursive speculative trees from outliving a
// successful sibling request.
func routeStartedHere(visited []string, id peer.ID) bool {
	encoded := id.String()
	for _, existing := range visited {
		if existing == encoded {
			return true
		}
	}
	return false
}

func connectedRouteCandidates(h host.Host, target peer.ID, visited []string) []peer.ID {
	excluded := make(map[string]struct{}, len(visited)+1)
	for _, encoded := range visited {
		excluded[encoded] = struct{}{}
	}
	excluded[h.ID().String()] = struct{}{}
	candidates := make([]peer.ID, 0, len(h.Network().Peers()))
	for _, candidate := range h.Network().Peers() {
		if _, skip := excluded[candidate.String()]; skip {
			continue
		}
		if h.Network().Connectedness(candidate) != network.Connected {
			continue
		}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i] == target {
			return true
		}
		if candidates[j] == target {
			return false
		}
		return kbucket.Closer(candidates[i], candidates[j], string(target))
	})
	return candidates
}

type tuplePeerFinder interface {
	FindPeer(context.Context, peer.ID) (peer.AddrInfo, error)
}

func ensureTuplePeerAddress(
	ctx context.Context,
	h host.Host,
	resolver TupleOwnerResolver,
	owner peer.ID,
) error {
	if owner == h.ID() || h.Network().Connectedness(owner) == network.Connected {
		return nil
	}
	finder, canResolve := resolver.(tuplePeerFinder)
	var resolvedErr error
	if canResolve {
		info, err := finder.FindPeer(ctx, owner)
		if err == nil {
			if info.ID == "" {
				info.ID = owner
			}
			if info.ID != owner || len(info.Addrs) == 0 {
				resolvedErr = fmt.Errorf("peer lookup returned no addresses for %s", owner)
			} else if err := h.Connect(ctx, info); err == nil {
				return nil
			} else {
				resolvedErr = fmt.Errorf("connect via resolved addresses %v: %w", info.Addrs, err)
			}
		} else {
			resolvedErr = err
		}
	}
	known := peer.AddrInfo{ID: owner, Addrs: h.Peerstore().Addrs(owner)}
	if len(known.Addrs) > 0 {
		if err := h.Connect(ctx, known); err == nil {
			return nil
		} else if resolvedErr != nil {
			return fmt.Errorf(
				"resolved address attempt failed: %v; known addresses %v failed: %w",
				resolvedErr,
				known.Addrs,
				err,
			)
		} else {
			return fmt.Errorf("known addresses %v failed: %w", known.Addrs, err)
		}
	}
	if resolvedErr != nil {
		return resolvedErr
	}
	return nil
}

func openTuplePeerStream(ctx context.Context, h host.Host, owner peer.ID, protocolID protocol.ID, requireVerified bool) (network.Stream, error) {
	openCtx := ctx
	cancel := func() {}
	// Connectedness can briefly remain "connected" after an underlying path
	// becomes unusable. Bound only stream establishment on an existing
	// connection so overlay routing still has time to try another neighbor;
	// the stream itself retains the caller's full operation deadline.
	if h.Network().Connectedness(owner) == network.Connected {
		openCtx, cancel = context.WithTimeout(ctx, establishedStreamOpenTimeout)
	}
	defer cancel()
	info := peer.AddrInfo{ID: owner, Addrs: h.Peerstore().Addrs(owner)}
	if err := h.Connect(openCtx, info); err != nil {
		return nil, fmt.Errorf("connect to peer: %w", err)
	}
	if requireVerified {
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			tagInfo := h.ConnManager().GetTagInfo(owner)
			if tagInfo != nil && tagInfo.Tags[handshakeVerifiedTag] > 0 {
				break
			}
			select {
			case <-openCtx.Done():
				return nil, fmt.Errorf("wait for peer verification: %w", openCtx.Err())
			case <-ticker.C:
			}
		}
	}
	return h.NewStream(openCtx, owner, protocolID)
}

func (d *DistributedTupleSpace) handleStream(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(d.timeout))
	var req tupleWireRequest
	decoder := json.NewDecoder(bufio.NewReader(io.LimitReader(stream, maxTupleRequestBytes)))
	if err := decoder.Decode(&req); err != nil {
		_ = json.NewEncoder(stream).Encode(tupleWireResponse{Error: "decode tuple request: " + err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	var value []byte
	var err error
	if req.Target != "" && req.Target != d.host.ID().String() {
		value, err = d.forwardTupleRequest(ctx, req)
	} else {
		value, err = d.applyLocalOnce(ctx, req)
	}
	response := tupleWireResponse{Value: value}
	if err != nil {
		response.Error = err.Error()
		switch {
		case errors.Is(err, ErrTupleNotFound):
			response.ErrorCode = "not_found"
		case errors.Is(err, ErrTupleCASConflict):
			response.ErrorCode = "conflict"
		default:
			var stale *tupleAuthorityError
			if errors.As(err, &stale) {
				response.ErrorCode = "stale_authority"
				response.CurrentEpoch = stale.Fence.Epoch
				response.CurrentWriter = stale.Fence.Writer
			} else {
				response.ErrorCode = "application"
			}
		}
	}
	_ = json.NewEncoder(stream).Encode(response)
}

func (d *DistributedTupleSpace) applyLocal(req tupleWireRequest) ([]byte, error) {
	switch req.Operation {
	case "put":
		_, err := d.local.TsPut(req.Name, req.Value)
		return nil, err
	case "replace":
		_, err := d.local.TsReplace(req.Name, req.Value)
		return nil, err
	case "cas":
		return nil, d.local.CompareAndSwapExact(context.Background(), req.Name, req.Expected, req.Value)
	case "read":
		return d.local.TsRead(req.Name)
	case "get":
		return d.local.TsGet(req.Name)
	default:
		return nil, fmt.Errorf("unsupported tuple operation %q", req.Operation)
	}
}

func (d *DistributedTupleSpace) applyLocalOnce(
	ctx context.Context,
	req tupleWireRequest,
) ([]byte, error) {
	if d.durable != nil && !isTuplePattern(req.Name) {
		return d.durable.apply(ctx, req)
	}
	if req.RequestID == "" {
		return d.applyLocal(req)
	}
	d.memoMu.Lock()
	if existing := d.memo[req.RequestID]; existing != nil {
		d.memoMu.Unlock()
		select {
		case <-existing.done:
			return append([]byte(nil), existing.value...), existing.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	entry := &tupleMemoEntry{done: make(chan struct{})}
	d.memo[req.RequestID] = entry
	d.memoMu.Unlock()

	value, err := d.applyLocal(req)
	d.memoMu.Lock()
	entry.value = append([]byte(nil), value...)
	entry.err = err
	entry.completed = time.Now()
	close(entry.done)
	if err != nil {
		delete(d.memo, req.RequestID)
	} else {
		d.evictTupleMemoLocked()
	}
	d.memoMu.Unlock()
	return value, err
}

type tupleApplicationError struct {
	message string
}

func (e *tupleApplicationError) Error() string {
	return e.message
}

func (d *DistributedTupleSpace) evictTupleMemoLocked() {
	for len(d.memo) > maxTupleMemoEntries {
		var oldestID string
		var oldest time.Time
		for requestID, entry := range d.memo {
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
		delete(d.memo, oldestID)
	}
}

func boundedAttemptContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= timeout {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func isTuplePattern(expr string) bool {
	const regexMeta = `*.+?^$[]{}|()\`
	for _, char := range regexMeta {
		for _, exprChar := range expr {
			if exprChar == char {
				return true
			}
		}
	}
	return false
}
