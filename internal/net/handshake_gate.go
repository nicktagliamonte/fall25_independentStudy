// Purpose: Enforce verack immediately on connection and gate streams until verified.

package net

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const handshakeOkTag = "handshake_ok"

// DefaultMaxInFlightHandshakes bounds the number of responder handshakes
// HandshakeGate will run concurrently (one goroutine per inbound connection).
// It is a DoS backstop, not a normal-path throttle: the per-peer rate limiter
// (AttackMitigation.RateLimiter) already gates new handshake *attempts*, but
// nothing previously capped how many could be in flight at once across all
// peers. This default is generous enough that legitimate bursts of inbound
// connections are never throttled by it in normal operation.
const DefaultMaxInFlightHandshakes = 256

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

	onVerified func(peer.ID)

	capIncremented sync.Map // map[network.Stream]struct{} for streams we Increment'd

	// handshakeSem bounds the number of concurrent in-flight responder
	// handshake goroutines spawned by Connected. Acquiring is non-blocking:
	// a connection notifiee callback must never block, so when the semaphore
	// is full the new connection is simply skipped (closed) rather than
	// queued. Sized by MaxInFlightHandshakes (default DefaultMaxInFlightHandshakes).
	handshakeSem chan struct{}
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
		h:            h,
		local:        local,
		policy:       policy,
		verified:     make(map[peer.ID]struct{}),
		handshakeSem: make(chan struct{}, DefaultMaxInFlightHandshakes),
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
// The handshake goroutine only runs while a slot is available in the gate's
// handshakeSem; this bounds the number of concurrent in-flight handshakes as a
// DoS backstop. Acquisition is non-blocking (this callback must not block), so
// if the semaphore is full the connection is closed immediately instead of
// spawning an unbounded goroutine.
//
// Parameters:
//   - _ (network.Network): the network the connection belongs to; unused.
//   - c (network.Conn): the newly established connection.
func (n *handshakeNotifiee) Connected(_ network.Network, c network.Conn) {
	pid := c.RemotePeer()
	g := n.gate
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
	select {
	case g.handshakeSem <- struct{}{}:
	default:
		// Too many handshakes in flight; drop this connection rather than
		// spawning an unbounded goroutine or blocking the notifiee callback.
		g.h.Network().ClosePeer(pid)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), policyTimeout(g.policy))
	go func() {
		defer cancel()
		defer func() { <-g.handshakeSem }()
		if _, err := PerformHandshake(ctx, g.h, pid, g.policy, g.local); err != nil {
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
// remote peer from the eclipse limiter (if AttackMitigation is configured) so its
// subnet/ASN slot becomes available again. Note that it does not remove the peer
// from the gate's verified set.
//
// Parameters:
//   - _ (network.Network): the network the connection belonged to; unused.
//   - c (network.Conn): the connection that was closed.
func (n *handshakeNotifiee) Disconnected(_ network.Network, c network.Conn) {
	if am := n.gate.policy.AttackMitigation; am != nil {
		am.Eclipse.Unregister(c.RemotePeer())
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
	g.verified[pid] = struct{}{}
	g.mu.Unlock()
	// Tag the peer in the connection manager as a lightweight hint for metrics/policy
	g.h.ConnManager().TagPeer(pid, handshakeOkTag, 1)
	if g.onVerified != nil {
		g.onVerified(pid)
	}
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
