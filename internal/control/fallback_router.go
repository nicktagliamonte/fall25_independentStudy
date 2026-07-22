// Purpose: ContentRouting that tries primary (DHT) first, then fallback (explicit hints).

package control

import (
	"context"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
)

// FallbackContentRouter composes primary and fallback ContentRouting.
// FindProvidersAsync tries primary first; if no providers found, tries fallback.
// Provide is delegated to primary only. This is used in Start (server.go) to
// compose the stack's DHT-backed router as primary with a DynamicRouter (or
// other explicit-hint router) as fallback.
type FallbackContentRouter struct {
	// Primary is queried first for both Provide and FindProvidersAsync.
	Primary routing.ContentRouting
	// Fallback is only consulted by FindProvidersAsync, and only when Primary
	// yields no providers.
	Fallback routing.ContentRouting
}

// NewFallbackContentRouter returns a ContentRouting that tries primary then fallback.
//
// Parameters:
//   - primary (routing.ContentRouting): router consulted first for provide/discovery.
//   - fallback (routing.ContentRouting): router consulted for discovery only when primary yields nothing.
//
// Returns:
//   - (*FallbackContentRouter): the composed router.
func NewFallbackContentRouter(primary, fallback routing.ContentRouting) *FallbackContentRouter {
	return &FallbackContentRouter{Primary: primary, Fallback: fallback}
}

// Provide announces to primary only (fallback holds runtime hints, not provider records).
//
// Parameters:
//   - ctx (context.Context): request context.
//   - c (cid.Cid): the content ID being announced.
//   - b (bool): whether to also broadcast to the network (implementation-specific).
//
// Returns:
//   - (error): any error from the primary router's Provide call.
func (f *FallbackContentRouter) Provide(ctx context.Context, c cid.Cid, b bool) error {
	return f.Primary.Provide(ctx, c, b)
}

// ProvideMany delegates to primary, using its native ProvideMany if it
// implements one (an optional optimization interface not part of
// routing.ContentRouting), otherwise falling back to calling Provide once per
// key.
//
// Parameters:
//   - ctx (context.Context): request context.
//   - keys ([]cid.Cid): content IDs to announce.
//
// Returns:
//   - (error): the first error encountered, or nil if all announcements succeeded.
func (f *FallbackContentRouter) ProvideMany(ctx context.Context, keys []cid.Cid) error {
	if pm, ok := f.Primary.(interface {
		ProvideMany(context.Context, []cid.Cid) error
	}); ok {
		return pm.ProvideMany(ctx, keys)
	}
	for _, c := range keys {
		if err := f.Primary.Provide(ctx, c, true); err != nil {
			return err
		}
	}
	return nil
}

// FindProvidersAsync tries primary first; if no results, tries fallback.
// Results from both sources are deduplicated by peer ID and the yielded count
// is capped at count (when count > 0).
//
// Parameters:
//   - ctx (context.Context): canceling ctx stops draining either source and delivering results.
//   - c (cid.Cid): the content ID to find providers for.
//   - count (int): upper bound on the number of results yielded (0 or negative means unbounded); also used as the output channel's buffer size and passed through to both underlying routers.
//
// Returns:
//   - (<-chan peer.AddrInfo): channel of deduplicated providers (primary first, then fallback), closed once both sources are exhausted, count is reached, or ctx is done.
func (f *FallbackContentRouter) FindProvidersAsync(ctx context.Context, c cid.Cid, count int) <-chan peer.AddrInfo {
	out := make(chan peer.AddrInfo, count)
	go func() {
		defer close(out)
		seen := make(map[peer.ID]struct{})
		add := func(info peer.AddrInfo) bool {
			if _, ok := seen[info.ID]; ok {
				return true
			}
			seen[info.ID] = struct{}{}
			select {
			case out <- info:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for info := range f.Primary.FindProvidersAsync(ctx, c, count) {
			if !add(info) {
				return
			}
			if count > 0 && len(seen) >= count {
				return
			}
		}
		for info := range f.Fallback.FindProvidersAsync(ctx, c, count) {
			if !add(info) {
				return
			}
			if count > 0 && len(seen) >= count {
				return
			}
		}
	}()
	return out
}
