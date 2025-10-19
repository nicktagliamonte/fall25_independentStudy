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

// HandshakeGate installs network notifications that
// - run the handshake when a connection is established, closing the peer on failure
// - reset any non-handshake streams until the peer is verified
type HandshakeGate struct {
	h      host.Host
	local  HandshakeLocal
	policy HandshakePolicy

	mu       sync.RWMutex
	verified map[peer.ID]struct{}
}

// InstallHandshakeGate registers the notifiee on the host and returns the gate instance.
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

type handshakeNotifiee struct{ gate *HandshakeGate }

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

func (n *handshakeNotifiee) Disconnected(_ network.Network, _ network.Conn) {}

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

func (n *handshakeNotifiee) ClosedStream(_ network.Network, _ network.Stream) {}
func (n *handshakeNotifiee) Listen(_ network.Network, _ ma.Multiaddr)         {}
func (n *handshakeNotifiee) ListenClose(_ network.Network, _ ma.Multiaddr)    {}

func (g *HandshakeGate) markVerified(pid peer.ID) {
	g.mu.Lock()
	g.verified[pid] = struct{}{}
	g.mu.Unlock()
	// Tag the peer in the connection manager as a lightweight hint for metrics/policy
	g.h.ConnManager().TagPeer(pid, handshakeOkTag, 1)
}

func (g *HandshakeGate) isVerified(pid peer.ID) bool {
	g.mu.RLock()
	_, ok := g.verified[pIDEqual(pid)]
	g.mu.RUnlock()
	return ok
}

// pIDEqual is a tiny helper to keep the exact type consistent in map lookups.
func pIDEqual(id peer.ID) peer.ID { return id }
