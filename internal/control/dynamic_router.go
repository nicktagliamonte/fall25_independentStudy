// Purpose: Dynamic content router that can map specific CIDs to provider peers at runtime.

package control

import (
	"context"
	"sync"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
)

type DynamicRouter struct {
	mu       sync.RWMutex
	byCIDStr map[string]peer.AddrInfo
}

func NewDynamicRouter() *DynamicRouter {
	return &DynamicRouter{byCIDStr: make(map[string]peer.AddrInfo)}
}

func (r *DynamicRouter) SetProviderForCID(c cid.Cid, p peer.AddrInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byCIDStr[c.String()] = p
}

func (r *DynamicRouter) ClearProviderForCID(c cid.Cid) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byCIDStr, c.String())
}

// routing.ContentRouting implementation
func (r *DynamicRouter) Provide(ctx context.Context, c cid.Cid, b bool) error  { return nil }
func (r *DynamicRouter) ProvideMany(ctx context.Context, keys []cid.Cid) error { return nil }

func (r *DynamicRouter) FindProvidersAsync(ctx context.Context, c cid.Cid, count int) <-chan peer.AddrInfo {
	out := make(chan peer.AddrInfo, 1)
	go func() {
		defer close(out)
		r.mu.RLock()
		p, ok := r.byCIDStr[c.String()]
		r.mu.RUnlock()
		if !ok {
			return
		}
		select {
		case out <- p:
		case <-ctx.Done():
			return
		}
	}()
	return out
}

func (r *DynamicRouter) FindProviders(ctx context.Context, c cid.Cid) ([]peer.AddrInfo, error) {
	r.mu.RLock()
	p, ok := r.byCIDStr[c.String()]
	r.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	return []peer.AddrInfo{p}, nil
}

func (r *DynamicRouter) Ready() bool { return true }
