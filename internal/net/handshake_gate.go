// Purpose: Enforce verack immediately on connection and gate streams until verified.

package net

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	ma "github.com/multiformats/go-multiaddr"
)

const handshakeOkTag = "handshake_ok"

// HandshakeGate installs network notifications that
// - run the handshake when a connection is established, closing the peer on failure
// - reset any non-handshake streams until the peer is verified
// - enforce per-peer resource cap (streams) when AttackMitigation has ResourceCap
type HandshakeGate struct {
	h      host.Host
	local  HandshakeLocal
	policy HandshakePolicy

	mu       sync.RWMutex
	verified map[peer.ID]struct{}
	inflight map[peer.ID]struct{}

	onVerified func(peer.ID)

	capIncremented sync.Map // map[network.Stream]struct{} for streams we Increment'd
}

// InstallHandshakeGate registers the notifiee on the host and returns the gate instance.
// The returned gate begins enforcing the handshake on every newly established
// connection immediately; there is no separate "start" step.
//
// Parameters:
//   - h (host.Host): the libp2p host to gate; a network notifiee is registered on h.Network().
//   - local (HandshakeLocal): this node's handshake parameters, used when initiating the handshake.
//   - policy (HandshakePolicy): validation, anti-replay, and attack-mitigation policy applied to each connecting peer.
//
// Returns:
//   - *HandshakeGate: the installed gate, tracking verified peers and stream resource usage.
func InstallHandshakeGate(h host.Host, local HandshakeLocal, policy HandshakePolicy) *HandshakeGate {
	g := &HandshakeGate{
		h:        h,
		local:    local,
		policy:   policy,
		verified: make(map[peer.ID]struct{}),
		inflight: make(map[peer.ID]struct{}),
	}

	// Implement a custom notifiee to avoid relying on NotifyBundle field names.
	h.Network().Notify(&handshakeNotifiee{gate: g})
	return g
}

// InstallHandshakeGateWithCallback is like InstallHandshakeGate, but invokes cb on handshake success.
//
// Parameters:
//   - h (host.Host): the libp2p host to gate.
//   - local (HandshakeLocal): this node's handshake parameters.
//   - policy (HandshakePolicy): validation, anti-replay, and attack-mitigation policy.
//   - cb (func(peer.ID)): invoked with the remote peer's ID each time its handshake succeeds.
//
// Returns:
//   - *HandshakeGate: the installed gate.
func InstallHandshakeGateWithCallback(h host.Host, local HandshakeLocal, policy HandshakePolicy, cb func(peer.ID)) *HandshakeGate {
	g := InstallHandshakeGate(h, local, policy)
	g.onVerified = cb
	return g
}

// handshakeNotifiee implements the libp2p network.Notifiee interface on behalf of a
// HandshakeGate, reacting to connection and stream lifecycle events.
type handshakeNotifiee struct{ gate *HandshakeGate }

// Connected is called by libp2p when a new connection is established. It first
// checks ban list and rate-limit gates (closing the connection immediately if
// either rejects the peer), then asynchronously runs PerformHandshake against the
// remote peer. On handshake failure it records misbehavior, may ban the peer, and
// closes the connection; on success it registers the peer with the eclipse limiter
// (if configured) and marks the peer verified via markVerified.
//
// Parameters:
//   - _ (network.Network): the network the connection belongs to; unused.
//   - c (network.Conn): the newly established connection.
func (n *handshakeNotifiee) Connected(_ network.Network, c network.Conn) {
	pid := c.RemotePeer()
	g := n.gate
	// Connected notifications can precede peerstore address propagation. Keep
	// the address from the connection itself so a verification retry can dial
	// after either side closes the initial transport.
	g.h.Peerstore().AddAddr(pid, c.RemoteMultiaddr(), peerstore.TempAddrTTL)
	if am := g.policy.AttackMitigation; am != nil {
		if am.BanList.IsBanned(pid) {
			g.h.Network().ClosePeer(pid)
			return
		}
		if !am.RateLimiter.Allow(pid) {
			g.h.Network().ClosePeer(pid)
			return
		}
	}
	g.mu.Lock()
	if _, ok := g.verified[pid]; ok {
		g.mu.Unlock()
		return
	}
	if _, ok := g.inflight[pid]; ok {
		g.mu.Unlock()
		return
	}
	g.inflight[pid] = struct{}{}
	g.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), policyTimeout(g.policy))
	go func() {
		defer cancel()
		defer func() {
			g.mu.Lock()
			delete(g.inflight, pid)
			g.mu.Unlock()
		}()
		var handshakeErr error
		for attempt := 0; attempt < 3; attempt++ {
			if _, handshakeErr = PerformHandshake(ctx, g.h, pid, g.policy, g.local); handshakeErr == nil {
				break
			}
			delay := time.Duration(attempt+1) * 100 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				attempt = 3
			case <-timer.C:
			}
		}
		if handshakeErr != nil {
			err := handshakeErr
			log.Printf("handshake gate: verification with %s failed: %v", pid, err)
			if am := g.policy.AttackMitigation; am != nil {
				am.Misbehavior.AddMisbehavior(pid, 20)
				if am.Misbehavior.ShouldDisconnect(pid) {
					am.BanList.Ban(pid)
				}
			}
			g.h.Network().ClosePeer(pid)
			return
		}
		if am := g.policy.AttackMitigation; am != nil {
			_ = am.Eclipse.Register(ctx, pid, g.h.Peerstore().Addrs(pid))
		}
		g.markVerified(pid)
	}()
}

// Disconnected is called by libp2p when a connection closes. It unregisters the
// remote peer from the eclipse limiter (if AttackMitigation is configured) so
// its subnet/ASN slot becomes available again. When the peer has no remaining
// connections, its verification state and connection-manager tag are removed:
// a later transport must complete verack again.
//
// Parameters:
//   - _ (network.Network): the network the connection belonged to; unused.
//   - c (network.Conn): the connection that was closed.
func (n *handshakeNotifiee) Disconnected(_ network.Network, c network.Conn) {
	pid := c.RemotePeer()
	if am := n.gate.policy.AttackMitigation; am != nil {
		am.Eclipse.Unregister(pid)
	}
	if len(n.gate.h.Network().ConnsToPeer(pid)) == 0 {
		n.gate.mu.Lock()
		delete(n.gate.verified, pid)
		delete(n.gate.inflight, pid)
		n.gate.mu.Unlock()
		n.gate.h.ConnManager().UntagPeer(pid, handshakeOkTag)
	}
}

// OpenedStream is called by libp2p when a new stream is opened. Non-handshake
// streams from peers that are not yet verified are reset immediately. If a
// per-peer stream resource cap is configured (AttackMitigation.ResourceCap), the
// stream is also reset when the cap is exceeded; otherwise the stream is recorded
// in capIncremented so ClosedStream can later decrement the peer's usage.
//
// Parameters:
//   - _ (network.Network): the network the stream belongs to; unused.
//   - s (network.Stream): the newly opened stream.
func (n *handshakeNotifiee) OpenedStream(_ network.Network, s network.Stream) {
	g := n.gate
	pid := s.Conn().RemotePeer()
	allow := string(s.Protocol()) == HandshakeProtocolID || g.isVerified(pid)
	if !allow {
		_ = s.Reset()
		return
	}
	if am := g.policy.AttackMitigation; am != nil && am.ResourceCap != nil {
		if !am.ResourceCap.Increment(pid) {
			_ = s.Reset()
			return
		}
		g.capIncremented.Store(s, struct{}{})
	}
}

// ClosedStream is called by libp2p when a stream closes. If this stream had been
// counted against the peer's resource cap (recorded by OpenedStream), it is
// decremented here to release the slot.
//
// Parameters:
//   - _ (network.Network): the network the stream belonged to; unused.
//   - s (network.Stream): the stream that was closed.
func (n *handshakeNotifiee) ClosedStream(_ network.Network, s network.Stream) {
	g := n.gate
	if am := g.policy.AttackMitigation; am != nil && am.ResourceCap != nil {
		if _, ok := g.capIncremented.LoadAndDelete(s); ok {
			am.ResourceCap.Decrement(s.Conn().RemotePeer())
		}
	}
}

// Listen is a no-op implementation required by the network.Notifiee interface.
func (n *handshakeNotifiee) Listen(_ network.Network, _ ma.Multiaddr) {}

// ListenClose is a no-op implementation required by the network.Notifiee interface.
func (n *handshakeNotifiee) ListenClose(_ network.Network, _ ma.Multiaddr) {}

// markVerified records pid as having completed the handshake, tags the peer in the
// host's connection manager with handshakeOkTag (a hint for metrics/policy), and
// invokes the gate's onVerified callback if set.
//
// Parameters:
//   - pid (peer.ID): the peer that completed the handshake successfully.
func (g *HandshakeGate) markVerified(pid peer.ID) {
	g.mu.Lock()
	// A handshake can finish concurrently with transport teardown. Do not let
	// that late result resurrect verification state for a disconnected peer.
	if len(g.h.Network().ConnsToPeer(pid)) == 0 {
		g.mu.Unlock()
		return
	}
	if _, exists := g.verified[pid]; exists {
		g.mu.Unlock()
		return
	}
	g.verified[pid] = struct{}{}
	g.mu.Unlock()
	// Tag the peer in the connection manager as a lightweight hint for metrics/policy
	g.h.ConnManager().TagPeer(pid, handshakeOkTag, 1)
	if g.onVerified != nil {
		g.onVerified(pid)
	}
}

// MarkVerified admits a connected peer after the local handshake responder has
// validated the peer and completed the version/verack exchange. It is safe to
// call more than once and ignores peers that have already disconnected.
func (g *HandshakeGate) MarkVerified(pid peer.ID) {
	g.markVerified(pid)
}

// isVerified reports whether pid has already completed the handshake.
//
// Parameters:
//   - pid (peer.ID): the peer to check.
//
// Returns:
//   - bool: true if pid is in the gate's verified set.
func (g *HandshakeGate) isVerified(pid peer.ID) bool {
	g.mu.RLock()
	_, ok := g.verified[pIDEqual(pid)]
	g.mu.RUnlock()
	return ok
}

// pIDEqual is a tiny helper to keep the exact type consistent in map lookups.
//
// Parameters:
//   - id (peer.ID): the peer ID to pass through.
//
// Returns:
//   - peer.ID: id, unchanged.
func pIDEqual(id peer.ID) peer.ID { return id }
