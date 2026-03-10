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

type service struct {
	h               host.Host
	stack           *mystore.Stack
	peerStore       *myhost.PeerStore
	metrics         *ctrl.NodeMetrics
	controlShutdown func(context.Context) error
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	basePolicy      myhost.HandshakePolicy
	onHandshake     func(peerID string, info map[string]any)
	onAck           func(peerID string, status string)
}

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

func (s *service) PutRaw(ctx context.Context, data []byte) (string, int, error) {
	c, err := mystore.PutRawBlockIndexed(ctx, s.stack.Datastore, s.stack.BlockSvc, data)
	if err != nil {
		return "", 0, err
	}
	return c.String(), len(data), nil
}

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

// ListImmediatePeerIDs returns currently connected peers (immediate neighbors).
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

// RestoreFromManifest fetches CIDs with bounded concurrency, per-item timeout, and a total byte budget.
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
