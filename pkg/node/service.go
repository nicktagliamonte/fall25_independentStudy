// Purpose: Define the embedded node service.

package node

import (
	"context"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// service is the concrete implementation of the Service interface returned
// by Start. It bundles the libp2p host, the storage/bitswap stack, the peer
// store, metrics counters, and the plumbing needed to stop the node's
// background goroutines and the control-plane HTTP server. All exported
// behavior is accessed through the Service interface methods below; the
// struct itself is unexported and constructed only by Start.
type service struct {
	// h is the libp2p host this node runs on (identity, transport, streams).
	h host.Host
	// stack bundles the blockstore, datastore, and Bitswap exchange used for
	// content-addressed storage and retrieval.
	stack *mystore.Stack
	// peerStore tracks known peers, dial history, and pruning policy.
	peerStore *myhost.PeerStore
	// metrics accumulates counters (dials, gossip, restores, pruning) that
	// back Status's Metrics field.
	metrics *ctrl.NodeMetrics
	// controlShutdown, if non-nil, shuts down the control-plane HTTP server
	// started by Start; invoked by Close before the host/stack are torn down.
	controlShutdown func(context.Context) error
	// cancel stops the context that all background loops (pruning, dial
	// maintenance, gossip) select on; invoked by Close.
	cancel context.CancelFunc
	// wg tracks the background loops spawned by Start so Close can wait for
	// them to exit (bounded by the ctx passed to Close).
	wg sync.WaitGroup
	// basePolicy is the handshake admission policy computed from Options at
	// Start time (token/CA requirements, min agent version, timeout, etc.);
	// reused by GetRawFrom when performing an ephemeral outbound handshake.
	basePolicy myhost.HandshakePolicy
	// onHandshake mirrors Options.OnHandshake, retained for use by service
	// methods that need to fire the callback outside of Start's own loops.
	onHandshake func(peerID string, info map[string]any)
	// onAck mirrors Options.OnAck, invoked by GetRawFrom after a successful
	// ephemeral handshake with a content provider.
	onAck func(peerID string, status string)
}

// Close implements Service.Close. It cancels the internal context (stopping
// the pruning, dial-maintenance, and gossip goroutines started by Start),
// waits for them to exit or for ctx to be done (whichever comes first — on
// deadline it proceeds with a best-effort shutdown regardless of whether the
// goroutines have finished), then shuts down the control-plane HTTP server,
// closes the Bitswap exchange, and closes the libp2p host. Teardown errors
// from the control server, Bitswap, and the host are intentionally
// swallowed (not returned) so that shutdown always attempts every step;
// Close therefore always returns nil in the current implementation. Close is
// not safe to call concurrently with itself, and calling it more than once
// will panic or misbehave (s.cancel and s.h.Close are not guarded against
// repeated invocation).
func (s *service) Close(ctx context.Context) error {
	// Stop background work
	if s.cancel != nil {
		s.cancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		// proceed with best-effort shutdown on deadline
	}
	// Shutdown control server before tearing down host/stack
	if s.controlShutdown != nil {
		_ = s.controlShutdown(ctx)
	}
	if s.stack != nil && s.stack.Bitswap != nil {
		_ = s.stack.Bitswap.Close()
	}
	if s.h != nil {
		_ = s.h.Close()
	}
	return nil
}

// Status implements Service.Status. It reads the current chain head/height
// from the datastore (mystore.GetHead), the local host's PeerID and
// listen/advertised addresses, and a snapshot of the node's metrics
// counters, and assembles them into a Status value. ctx is passed through to
// GetHead for cancellation. The returned error is always nil in the current
// implementation — GetHead's own error is intentionally discarded, so a
// datastore read failure surfaces only as a zero-value Head/Height rather
// than as an error. head, height, and err are only checked as (head,
// height, _); if head is not "defined" (mystore's cid.Cid zero value),
// Status.Head is left as the empty string.
func (s *service) Status(ctx context.Context) (Status, error) {
	head, height, _ := mystore.GetHead(ctx, s.stack.Datastore)
	out := Status{
		PeerID: s.h.ID().String(),
		Addrs:  hostAddrsStrings(s.h),
		Head:   "",
		Height: height,
	}
	if head.Defined() {
		out.Head = head.String()
	}
	snap := s.metrics.Snapshot()
	out.Metrics.DialsAttempted = snap.DialsAttempted
	out.Metrics.DialsSucceeded = snap.DialsSucceeded
	out.Metrics.DialsFailed = snap.DialsFailed
	out.Metrics.PeersPruned = snap.PeersPruned
	out.Metrics.GossipLearned = snap.GossipLearned
	return out, nil
}

// PutRaw implements Service.PutRaw. It stores data as a single raw
// content-addressed block in the local blockstore, indexing it via
// mystore.PutRawBlockIndexed so it can be looked up by CID later (e.g. by
// GetRawFrom or another node's RestoreFromManifest).
//
// Parameters:
//   - ctx: bounds the underlying datastore/blockstore write.
//   - data: the raw bytes to store; no chunking or DAG structure is applied,
//     the whole slice becomes one block.
//
// Returns:
//   - string: the string encoding of the resulting content ID (CID), empty
//     on error.
//   - int: the number of bytes stored, equal to len(data); 0 on error.
//   - error: non-nil if the underlying block/index write fails (e.g.
//     datastore error); nil on success.
func (s *service) PutRaw(ctx context.Context, data []byte) (string, int, error) {
	c, err := mystore.PutRawBlockIndexed(ctx, s.stack.Datastore, s.stack.BlockSvc, data)
	if err != nil {
		return "", 0, err
	}
	return c.String(), len(data), nil
}

// GetRawFrom implements Service.GetRawFrom. It fetches a single block by CID
// from a specific remote provider, bypassing content routing/discovery
// entirely (the provider is given explicitly rather than looked up).
//
// Parameters:
//   - ctx: parent context for the whole operation; also used (via
//     context.WithTimeout) to bound the dial and fetch phases.
//   - providerAddr: a single multiaddr string for the provider (parsed with
//     multiaddr.NewMultiaddr); must be a valid multiaddr or an error is
//     returned.
//   - providerPeer: the provider's libp2p peer ID, base58/CID-encoded
//     (parsed with peer.Decode); must be valid or an error is returned.
//   - cidStr: the string encoding of the content ID to fetch (parsed with
//     cid.Decode); must be valid or an error is returned.
//   - timeout: the budget for both the dial and the fetch phases
//     (applied independently to each via context.WithTimeout). If <= 0, it
//     defaults to 20 seconds.
//
// Behavior: if providerPeer decodes to the local host's own PeerID, the
// block is served directly from the local indexed blockstore
// (mystore.GetBlockIndexed) without any network activity. Otherwise, an
// ephemeral storage stack is created with a staticContentRouter that always
// resolves the given CID to the given provider (mystore.NewStackWithRouter),
// the local host connects to the provider, a best-effort handshake is
// performed using the node's basePolicy (failures here are ignored — the
// fetch proceeds regardless of handshake outcome, only s.onAck is skipped),
// and then the block is fetched over Bitswap via that ephemeral stack. The
// ephemeral stack's Bitswap instance is closed via defer before returning.
//
// Returns:
//   - []byte: the fetched block data; nil on error.
//   - error: non-nil if providerAddr/providerPeer/cidStr fail to parse, if
//     creating the ephemeral stack fails, if the connect (dial) phase fails,
//     or if the block fetch itself fails or times out. A failed or skipped
//     handshake is NOT treated as an error — the code proceeds to fetch the
//     block regardless (handshake success only gates the s.onAck callback).
func (s *service) GetRawFrom(ctx context.Context, providerAddr string, providerPeer string, cidStr string, timeout time.Duration) ([]byte, error) {
	maddr, err := multiaddr.NewMultiaddr(providerAddr)
	if err != nil {
		return nil, err
	}
	pid, err := peer.Decode(providerPeer)
	if err != nil {
		return nil, err
	}
	info := peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{maddr}}
	c, err := cid.Decode(cidStr)
	if err != nil {
		return nil, err
	}
	if pid == s.h.ID() {
		return mystore.GetBlockIndexed(ctx, s.stack.Datastore, s.stack.BlockSvc, c)
	}
	// ephemeral stack with static router
	router := &staticContentRouter{provider: info}
	st, err := mystore.NewStackWithRouter(ctx, s.h, router)
	if err != nil {
		return nil, err
	}
	defer st.Bitswap.Close()
	d := timeout
	if d <= 0 {
		d = 20 * time.Second
	}
	ctxDial, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	if err := s.h.Connect(ctxDial, info); err != nil {
		return nil, err
	}
	// Perform gate handshake using stored base policy
	if _, err := myhost.PerformHandshakeWithState(ctx, s.h, pid, myhost.HandshakePolicy{Timeout: d, MinAgentVersion: s.basePolicy.MinAgentVersion, ServicesAllow: s.basePolicy.ServicesAllow, RequireCredential: s.basePolicy.RequireCredential, AuthScheme: s.basePolicy.AuthScheme, CAPubKeys: s.basePolicy.CAPubKeys, Token: s.basePolicy.Token}, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}); err == nil {
		if s.onAck != nil {
			s.onAck(pid.String(), "ok")
		}
	}
	fetchCtx, cancel2 := context.WithTimeout(ctx, d)
	defer cancel2()
	return mystore.GetBlockIndexed(fetchCtx, s.stack.Datastore, st.BlockSvc, c)
}

// ListImmediatePeerIDs implements Service.ListImmediatePeerIDs. It queries
// the libp2p network's current peer set (s.h.Network().Peers()) — i.e. peers
// the host currently has an open connection to — and returns their
// string-encoded peer IDs, excluding the local host's own ID (which the
// underlying network layer can otherwise include for loopback/self
// connections). ctx is accepted for interface-uniformity but is not
// currently used (the peer list is read synchronously and cannot block).
// The returned error is always nil in the current implementation.
func (s *service) ListImmediatePeerIDs(ctx context.Context) ([]string, error) {
	peers := s.h.Network().Peers()
	out := make([]string, 0, len(peers))
	for _, pid := range peers {
		if pid == s.h.ID() {
			continue
		}
		out = append(out, pid.String())
	}
	return out, nil
}

// RestoreFromManifest implements Service.RestoreFromManifest. It fetches a
// list of content IDs (typically produced by a prior snapshot of another
// node, used to repair this node's local store after a restart) using a
// pool of worker goroutines, bounded concurrency, a per-item fetch timeout,
// and an optional cumulative byte budget across the whole job.
//
// Parameters:
//   - ctx: parent context; if canceled, the task-feeding goroutine stops
//     submitting new work (already-dispatched in-flight fetches are not
//     forcibly canceled beyond their own per-item timeout context, which is
//     itself derived from ctx). ctx.Err() is returned as part of the result
//     (see Returns below).
//   - cids: the list of string-encoded content IDs to fetch, in no
//     particular required order; duplicates are fetched independently (no
//     de-duplication is performed).
//   - concurrency: the number of worker goroutines to run in parallel. If <=
//     0, it defaults to 4.
//   - timeout: the per-item fetch timeout applied via context.WithTimeout
//     around each individual mystore.GetBlock call. If <= 0, it defaults to
//     20 seconds.
//   - byteBudget: an optional cap on total bytes fetched across the whole
//     job. If <= 0, the budget is unlimited. Once the running total in
//     stats.Bytes reaches or exceeds byteBudget, workers stop picking up new
//     tasks (checked both when a worker dequeues a task and, best-effort,
//     by the feeder goroutine after enqueuing each task) — this is a soft,
//     racy limit: bytes already in flight when the budget is crossed can
//     still land, and remaining queued CIDs that were never dequeued are
//     simply left unprocessed (not counted as Failed).
//
// Behavior: increments s.metrics' RestoresStarted counter once at the start.
// Each CID is decoded (cid.Decode); a decode failure counts as Failed
// without consuming network I/O. Each successful fetch increments
// stats.OK, adds the block's length to stats.Bytes, and updates
// s.metrics (AddRestoresOK/AddRestoreBytes); each fetch error (including
// per-item timeout) increments stats.Failed and s.metrics.AddRestoresFailed.
// stats access is protected by an internal mutex since multiple workers
// update it concurrently.
//
// Returns:
//   - RestoreStats: the accumulated OK count, Failed count, and total Bytes
//     fetched (see RestoreStats). Returned by value once all workers have
//     drained the task channel and returned (via wg.Wait()).
//   - error: the result of ctx.Err() at the time all workers finish — nil if
//     ctx was never canceled/expired, or the context's error (e.g.
//     context.Canceled or context.DeadlineExceeded) otherwise. Note this is
//     NOT an aggregate of individual per-item fetch errors; those are only
//     reflected in RestoreStats.Failed.
func (s *service) RestoreFromManifest(ctx context.Context, cids []string, concurrency int, timeout time.Duration, byteBudget int64) (RestoreStats, error) {
	if concurrency <= 0 {
		concurrency = 4
	}
	s.metrics.IncRestoresStarted()
	type task struct {
		c string
	}
	var stats RestoreStats
	var mu sync.Mutex
	todo := make(chan task)
	var wg sync.WaitGroup
	// worker
	worker := func() {
		defer wg.Done()
		for t := range todo {
			// check global budget
			mu.Lock()
			if byteBudget > 0 && stats.Bytes >= byteBudget {
				mu.Unlock()
				return
			}
			mu.Unlock()
			// parse cid
			c, err := cid.Decode(t.c)
			if err != nil {
				mu.Lock()
				stats.Failed++
				mu.Unlock()
				continue
			}
			// per-item timeout
			d := timeout
			if d <= 0 {
				d = 20 * time.Second
			}
			ctx2, cancel := context.WithTimeout(ctx, d)
			b, err := mystore.GetBlock(ctx2, s.stack.BlockSvc, c)
			cancel()
			mu.Lock()
			if err != nil {
				stats.Failed++
				s.metrics.AddRestoresFailed(1)
			} else {
				stats.OK++
				sz := int64(len(b))
				stats.Bytes += sz
				s.metrics.AddRestoresOK(1)
				s.metrics.AddRestoreBytes(sz)
			}
			mu.Unlock()
		}
	}
	// start workers
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go worker()
	}
	// feed tasks
	go func() {
		defer close(todo)
		for _, s := range cids {
			select {
			case <-ctx.Done():
				return
			default:
			}
			todo <- task{c: s}
			// optional budget early check
			mu.Lock()
			if byteBudget > 0 && stats.Bytes >= byteBudget {
				mu.Unlock()
				return
			}
			mu.Unlock()
		}
	}()
	wg.Wait()
	return stats, ctx.Err()
}
