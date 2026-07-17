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

// handshakeOkTag is the ConnManager peer tag applied (with value 1) once a peer has
// completed the handshake successfully, whether via HandshakeGate's automatic
// on-connect handshake or via an explicit PerformHandshake/PerformHandshakeWithState
// call. It is a lightweight hint for other code (e.g. metrics/dial policy) and is not
// read by anything in this package.
const handshakeOkTag = "handshake_ok"

// HandshakeGate installs network notifications that
// - run the handshake when a connection is established, closing the peer on failure
// - reset any non-handshake streams until the peer is verified
//
// It does NOT itself act on any peers learned during the automatic handshake (see
// handshakeNotifiee.Connected, which calls PerformHandshake and discards the returned
// peer list) — peer discovery/introduction on handshake is implemented separately by
// callers that explicitly invoke PerformHandshakeWithState (see pkg/node/run.go and
// pkg/node/start.go) and act on HandshakeResult.Learned themselves.
type HandshakeGate struct {
	h      host.Host
	local  HandshakeLocal
	policy HandshakePolicy

	mu sync.RWMutex
	// verified holds the set of peer IDs that have completed a handshake since this
	// gate was installed. Guarded by mu.
	verified map[peer.ID]struct{}

	// onVerified, if non-nil, is invoked (synchronously, from the connection-notify
	// goroutine) each time a peer is newly marked verified.
	onVerified func(peer.ID)
}

// InstallHandshakeGate constructs a HandshakeGate for host h using local as this
// node's advertised handshake fields and policy to validate remote peers, registers it
// as a network.Notifiee on h.Network() so it automatically runs on every new
// connection, and returns the gate instance. onVerified is left nil; use
// InstallHandshakeGateWithCallback to be notified of successful handshakes.
func InstallHandshakeGate(h host.Host, local HandshakeLocal, policy HandshakePolicy) *HandshakeGate {
	g := &HandshakeGate{
		h:        h,
		local:    local,
		policy:   policy,
		verified: make(map[peer.ID]struct{}),
	}

	// Implement a custom notifiee to avoid relying on NotifyBundle field names.
	h.Network().Notify(&handshakeNotifiee{gate: g})
	return g
}

// InstallHandshakeGateWithCallback is like InstallHandshakeGate, but sets cb as the
// gate's onVerified callback so cb(pid) is invoked (from the per-connection goroutine
// spawned in handshakeNotifiee.Connected) each time a peer newly completes the
// automatic on-connect handshake.
func InstallHandshakeGateWithCallback(h host.Host, local HandshakeLocal, policy HandshakePolicy, cb func(peer.ID)) *HandshakeGate {
	g := InstallHandshakeGate(h, local, policy)
	g.onVerified = cb
	return g
}

// handshakeNotifiee implements libp2p's network.Notifiee, delegating handshake
// enforcement to the wrapped HandshakeGate.
type handshakeNotifiee struct{ gate *HandshakeGate }

// Connected is called by libp2p whenever a new connection c is established (inbound or
// outbound). It spawns a goroutine that runs the initiator side of the handshake
// (PerformHandshake) against the remote peer, bounded by a context timeout derived
// from policyTimeout(gate.policy). If the handshake fails (bad version, failed
// credential check, I/O error, timeout, etc.), the entire connection to that peer is
// closed via Network().ClosePeer. If it succeeds, the peer is marked verified via
// markVerified (which also invokes onVerified, if set). Any peers the remote returned
// in the handshake's peer sample are discarded here — see the HandshakeGate doc comment.
func (n *handshakeNotifiee) Connected(_ network.Network, c network.Conn) {
	// Perform handshake on connect, drop peer on failure.
	pid := c.RemotePeer()
	g := n.gate
	ctx, cancel := context.WithTimeout(context.Background(), policyTimeout(g.policy))
	go func() {
		defer cancel()
		if _, err := PerformHandshake(ctx, g.h, pid, g.policy, g.local); err != nil {
			g.h.Network().ClosePeer(pid)
			return
		}
		g.markVerified(pid)
	}()
}

// Disconnected is a required network.Notifiee method; it is a no-op here. Note that a
// peer removed from HandshakeGate.verified is never re-added on this path — verified
// status is not cleared on disconnect, so a stale entry can persist in the map for a
// peer ID that later reconnects (the reconnect will re-run and re-verify the
// handshake, but the old map entry is never explicitly pruned either way; this is
// harmless since re-verification simply re-inserts the same key).
func (n *handshakeNotifiee) Disconnected(_ network.Network, _ network.Conn) {}

// OpenedStream is called by libp2p whenever a new stream s is opened on any connection.
// Streams using HandshakeProtocolID are always allowed through (so the handshake
// itself can proceed). Any other stream from a peer that has not yet completed
// verification (per HandshakeGate.isVerified) is immediately reset, effectively
// blocking application protocols until the handshake succeeds. Note there is an
// inherent race: streams opened concurrently with (before) the handshake goroutine's
// completion will be reset even for an eventually-legitimate peer; callers are
// expected to retry.
func (n *handshakeNotifiee) OpenedStream(_ network.Network, s network.Stream) {
	g := n.gate
	pid := s.Conn().RemotePeer()
	// Allow handshake protocol itself
	if string(s.Protocol()) == HandshakeProtocolID {
		return
	}
	// Gate all other streams until verified
	if !g.isVerified(pid) {
		_ = s.Reset()
	}
}

// ClosedStream, Listen, and ListenClose are required network.Notifiee methods; all
// three are no-ops for this gate.
func (n *handshakeNotifiee) ClosedStream(_ network.Network, _ network.Stream) {}
func (n *handshakeNotifiee) Listen(_ network.Network, _ ma.Multiaddr)         {}
func (n *handshakeNotifiee) ListenClose(_ network.Network, _ ma.Multiaddr)    {}

// markVerified records pid as verified (under g.mu), tags it in the host's ConnManager
// with handshakeOkTag, and, if g.onVerified is set, invokes it synchronously with pid.
// Safe for concurrent use; may be called concurrently for different peers.
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

// isVerified reports whether pid has completed the handshake (i.e. is present in
// g.verified). Safe for concurrent use (read-locked).
func (g *HandshakeGate) isVerified(pid peer.ID) bool {
	g.mu.RLock()
	_, ok := g.verified[pIDEqual(pid)]
	g.mu.RUnlock()
	return ok
}

// pIDEqual is a no-op identity function (returns id unchanged). It exists only as a
// documentation/readability aid at the map-lookup call site; it performs no actual
// normalization or comparison.
func pIDEqual(id peer.ID) peer.ID { return id }
