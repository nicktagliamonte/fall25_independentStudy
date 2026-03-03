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
// Provide is delegated to primary only.
type FallbackContentRouter struct {
	Primary   routing.ContentRouting
	Fallback  routing.ContentRouting
}

// NewFallbackContentRouter returns a ContentRouting that tries primary then fallback.
func NewFallbackContentRouter(primary, fallback routing.ContentRouting) *FallbackContentRouter {
	return &FallbackContentRouter{Primary: primary, Fallback: fallback}
}

// Provide announces to primary only (fallback holds runtime hints, not provider records).
func (f *FallbackContentRouter) Provide(ctx context.Context, c cid.Cid, b bool) error {
	return f.Primary.Provide(ctx, c, b)
}

// ProvideMany delegates to primary.
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
