// Purpose: Dynamic content router that can map specific CIDs to provider peers at runtime.

package control

import (
	"context"
	"sync"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
)

// DynamicRouter is a minimal, in-memory implementation of the libp2p
// routing.ContentRouting interface that lets the control server pin a
// specific provider peer.AddrInfo to a specific CID at runtime, rather than
// relying on a DHT or other global routing table. It is used by the /get
// handler in server.go to build an ephemeral Bitswap-capable Stack (via
// mystore.NewStackWithRouter) that already "knows" which single peer holds
// the block being fetched, so no discovery/provide round-trip is needed.
//
// Provide/ProvideMany are no-ops because this router never announces
// content to anyone; it is purely a local lookup table populated by
// SetProviderForCID. It is safe for concurrent use; all map access is
// guarded by mu.
type DynamicRouter struct {
	mu sync.RWMutex
	// byCIDStr maps a CID's string form (cid.Cid.String()) to the single
	// peer.AddrInfo believed to hold that CID's block.
	byCIDStr map[string]peer.AddrInfo
}

// NewDynamicRouter constructs an empty DynamicRouter ready for use. The
// returned router initially has no CID->provider mappings; callers add them
// via SetProviderForCID before performing any FindProviders* lookups.
func NewDynamicRouter() *DynamicRouter {
	return &DynamicRouter{byCIDStr: make(map[string]peer.AddrInfo)}
}

// SetProviderForCID records p as the (sole) known provider for CID c,
// overwriting any previous mapping for the same CID. Safe for concurrent
// use; acquires the write lock for the duration of the map update.
func (r *DynamicRouter) SetProviderForCID(c cid.Cid, p peer.AddrInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byCIDStr[c.String()] = p
}

// ClearProviderForCID removes any provider mapping previously set for CID c
// via SetProviderForCID. It is a no-op if no mapping exists. Safe for
// concurrent use; acquires the write lock for the duration of the delete.
func (r *DynamicRouter) ClearProviderForCID(c cid.Cid) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byCIDStr, c.String())
}

// routing.ContentRouting implementation

// Provide is part of the routing.ContentRouting interface. DynamicRouter
// never announces content to a wider network, so this is a no-op and always
// returns a nil error regardless of ctx, c, or b (the "announce to network"
// flag).
func (r *DynamicRouter) Provide(ctx context.Context, c cid.Cid, b bool) error { return nil }

// ProvideMany is part of the routing.ContentRouting interface (batch form of
// Provide). Like Provide, it is a no-op and always returns a nil error;
// keys is ignored.
func (r *DynamicRouter) ProvideMany(ctx context.Context, keys []cid.Cid) error { return nil }

// FindProvidersAsync looks up the provider previously registered for c via
// SetProviderForCID and streams it (at most one peer.AddrInfo) on the
// returned channel. count is accepted for interface compatibility but
// ignored since this router only ever tracks a single provider per CID. The
// returned channel is always closed by the spawned goroutine, whether or
// not a provider was found; if none was found the channel is closed empty.
// If ctx is cancelled while the single result is being delivered, the send
// is abandoned and the channel is closed without a value.
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

// FindProviders is the synchronous counterpart to FindProvidersAsync. It
// returns a single-element slice containing the provider registered for c,
// or (nil, nil) if no provider has been set for that CID. It never returns
// a non-nil error; ctx is accepted for interface compatibility but unused.
func (r *DynamicRouter) FindProviders(ctx context.Context, c cid.Cid) ([]peer.AddrInfo, error) {
	r.mu.RLock()
	p, ok := r.byCIDStr[c.String()]
	r.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	return []peer.AddrInfo{p}, nil
}

// Ready always returns true: DynamicRouter has no warm-up/bootstrap phase
// and is usable immediately after construction.
func (r *DynamicRouter) Ready() bool { return true }
