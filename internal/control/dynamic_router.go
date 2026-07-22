// Purpose: Dynamic content router that can map specific CIDs to provider peers at runtime.

package control

import (
	"context"
	"sync"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
)

// DynamicRouter is an in-memory, explicitly-populated content router that maps
// specific CIDs to a single known provider peer at runtime. It implements
// (a subset of) routing.ContentRouting so it can be composed with other
// routers (e.g. via NewFallbackContentRouter) to give hard-coded routing
// hints precedence over, or as a fallback to, DHT-based discovery.
type DynamicRouter struct {
	// mu guards byCIDStr against concurrent reads/writes.
	mu sync.RWMutex
	// byCIDStr maps a CID's string form to the single peer known to provide it.
	byCIDStr map[string]peer.AddrInfo
}

// NewDynamicRouter returns an empty DynamicRouter with no CID→peer mappings.
//
// Returns:
//   - (*DynamicRouter): a newly allocated, empty router.
func NewDynamicRouter() *DynamicRouter {
	return &DynamicRouter{byCIDStr: make(map[string]peer.AddrInfo)}
}

// ClearProviderForCID removes any explicit provider hint recorded for c, so
// future lookups for that CID no longer resolve through this router. It is
// called by the /delete handler to invalidate stale routing hints after a
// block is removed.
//
// Parameters:
//   - c (cid.Cid): the content ID whose provider hint should be forgotten.
//
// Returns: (none)
func (r *DynamicRouter) ClearProviderForCID(c cid.Cid) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byCIDStr, c.String())
}

// routing.ContentRouting implementation

// Provide is a no-op: DynamicRouter never announces provider records, it only
// serves explicit CID→peer hints that were set out-of-band.
//
// Parameters:
//   - ctx (context.Context): unused.
//   - c (cid.Cid): unused.
//   - b (bool): unused.
//
// Returns: (error) always nil.
func (r *DynamicRouter) Provide(ctx context.Context, c cid.Cid, b bool) error { return nil }

// ProvideMany is a no-op for the same reason as Provide.
//
// Parameters:
//   - ctx (context.Context): unused.
//   - keys ([]cid.Cid): unused.
//
// Returns: (error) always nil.
func (r *DynamicRouter) ProvideMany(ctx context.Context, keys []cid.Cid) error { return nil }

// FindProvidersAsync looks up the single explicit provider recorded for c (if
// any) and streams it on the returned channel. The channel is closed after at
// most one value is sent (or immediately if no hint is recorded, or if ctx is
// canceled before the value can be delivered).
//
// Parameters:
//   - ctx (context.Context): cancels delivery of the buffered result.
//   - c (cid.Cid): the content ID to resolve.
//   - count (int): unused (at most one provider is ever known).
//
// Returns:
//   - (<-chan peer.AddrInfo): channel yielding zero or one provider, then closed.
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

// FindProviders is the synchronous counterpart to FindProvidersAsync: it
// returns the single explicit provider recorded for c, if any.
//
// Parameters:
//   - ctx (context.Context): unused.
//   - c (cid.Cid): the content ID to resolve.
//
// Returns:
//   - ([]peer.AddrInfo): nil if no hint is recorded, otherwise a slice with exactly one entry.
//   - (error): always nil.
func (r *DynamicRouter) FindProviders(ctx context.Context, c cid.Cid) ([]peer.AddrInfo, error) {
	r.mu.RLock()
	p, ok := r.byCIDStr[c.String()]
	r.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	return []peer.AddrInfo{p}, nil
}

// Ready reports that the router is always ready to serve lookups, since it
// holds only in-memory state with no bootstrap or connectivity requirement.
//
// Returns:
//   - (bool): always true.
func (r *DynamicRouter) Ready() bool { return true }
