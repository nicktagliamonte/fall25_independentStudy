// Purpose: ContentRouting wrapper that filters FindProvidersAsync to only yield
// peers in the reachable partition (connected). Phase 5.2: route queries to
// reachable partition of DHT. Uses host.Network() only; no Phase 2 dependencies.

package control

import (
	"context"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
)

// ReachablePartitionRouter wraps ContentRouting and filters FindProvidersAsync
// to only yield peers that are currently connected. Under partition, Bitswap
// then only tries peers in the reachable partition instead of wasting time
// on unreachable ones.
type ReachablePartitionRouter struct {
	// Host is used to check current connectedness of candidate providers via
	// Host.Network().Connectedness, and to exclude the local peer from results.
	Host host.Host
	// Underlying is the wrapped ContentRouting implementation (e.g. the DHT)
	// that actually performs provide/discovery operations.
	Underlying routing.ContentRouting
}

// NewReachablePartitionRouter returns a router that filters provider results
// to connected peers only.
//
// Parameters:
//   - h (host.Host): host used to test peer connectedness.
//   - underlying (routing.ContentRouting): the wrapped router that performs the actual lookups.
//
// Returns:
//   - (*ReachablePartitionRouter): the wrapping router.
func NewReachablePartitionRouter(h host.Host, underlying routing.ContentRouting) *ReachablePartitionRouter {
	return &ReachablePartitionRouter{Host: h, Underlying: underlying}
}

// Provide delegates to underlying.
//
// Parameters:
//   - ctx (context.Context): request context.
//   - c (cid.Cid): the content ID being announced.
//   - b (bool): whether to also broadcast to the network (implementation-specific).
//
// Returns:
//   - (error): any error from the underlying router's Provide call.
func (r *ReachablePartitionRouter) Provide(ctx context.Context, c cid.Cid, b bool) error {
	return r.Underlying.Provide(ctx, c, b)
}

// ProvideMany delegates to underlying, using its native ProvideMany if it
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
func (r *ReachablePartitionRouter) ProvideMany(ctx context.Context, keys []cid.Cid) error {
	if pm, ok := r.Underlying.(interface {
		ProvideMany(context.Context, []cid.Cid) error
	}); ok {
		return pm.ProvideMany(ctx, keys)
	}
	for _, c := range keys {
		if err := r.Underlying.Provide(ctx, c, true); err != nil {
			return err
		}
	}
	return nil
}

// FindProvidersAsync prefers providers in the reachable partition (connected).
// If any connected providers exist, only those are yielded so Bitswap does not
// waste time on unreachable peers. If none are connected, all results pass
// through for discovery (normal first-fetch path). The local host is always
// excluded from results, and duplicate peer IDs from the underlying router are
// deduplicated.
//
// Parameters:
//   - ctx (context.Context): canceling ctx stops both draining the underlying channel and delivering results.
//   - c (cid.Cid): the content ID to find providers for.
//   - count (int): upper bound on the number of results yielded (0 or negative means unbounded); also used as the output channel's buffer size.
//
// Returns:
//   - (<-chan peer.AddrInfo): channel of filtered providers, closed once the underlying source is exhausted, count is reached, or ctx is done.
func (r *ReachablePartitionRouter) FindProvidersAsync(ctx context.Context, c cid.Cid, count int) <-chan peer.AddrInfo {
	out := make(chan peer.AddrInfo, count)
	go func() {
		defer close(out)
		if r.Host == nil || r.Underlying == nil {
			return
		}
		var connected, all []peer.AddrInfo
		seen := make(map[peer.ID]struct{})
		for info := range r.Underlying.FindProvidersAsync(ctx, c, count) {
			if r.Host.ID() == info.ID {
				continue
			}
			if _, ok := seen[info.ID]; ok {
				continue
			}
			seen[info.ID] = struct{}{}
			all = append(all, info)
			if r.Host.Network().Connectedness(info.ID) == network.Connected {
				connected = append(connected, info)
			}
		}
		toEmit := all
		if len(connected) > 0 {
			toEmit = connected
		}
		emitted := 0
		for _, info := range toEmit {
			if count > 0 && emitted >= count {
				break
			}
			select {
			case out <- info:
				emitted++
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
