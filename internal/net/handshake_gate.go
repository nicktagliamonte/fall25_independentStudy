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
// - enforce per-peer resource cap (streams) when AttackMitigation has ResourceCap
type HandshakeGate struct {
	h      host.Host
	local  HandshakeLocal
	policy HandshakePolicy

	mu       sync.RWMutex
	verified map[peer.ID]struct{}

	onVerified func(peer.ID)

	capIncremented sync.Map // map[network.Stream]struct{} for streams we Increment'd
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

// InstallHandshakeGateWithCallback is like InstallHandshakeGate, but invokes cb on handshake success.
func InstallHandshakeGateWithCallback(h host.Host, local HandshakeLocal, policy HandshakePolicy, cb func(peer.ID)) *HandshakeGate {
	g := InstallHandshakeGate(h, local, policy)
	g.onVerified = cb
	return g
}

type handshakeNotifiee struct{ gate *HandshakeGate }

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
	ctx, cancel := context.WithTimeout(context.Background(), policyTimeout(g.policy))
	go func() {
		defer cancel()
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

func (n *handshakeNotifiee) Disconnected(_ network.Network, c network.Conn) {
	if am := n.gate.policy.AttackMitigation; am != nil {
		am.Eclipse.Unregister(c.RemotePeer())
	}
}

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

func (n *handshakeNotifiee) ClosedStream(_ network.Network, s network.Stream) {
	g := n.gate
	if am := g.policy.AttackMitigation; am != nil && am.ResourceCap != nil {
		if _, ok := g.capIncremented.LoadAndDelete(s); ok {
			am.ResourceCap.Decrement(s.Conn().RemotePeer())
		}
	}
}
func (n *handshakeNotifiee) Listen(_ network.Network, _ ma.Multiaddr)         {}
func (n *handshakeNotifiee) ListenClose(_ network.Network, _ ma.Multiaddr)    {}

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

func (g *HandshakeGate) isVerified(pid peer.ID) bool {
	g.mu.RLock()
	_, ok := g.verified[pIDEqual(pid)]
	g.mu.RUnlock()
	return ok
}

// pIDEqual is a tiny helper to keep the exact type consistent in map lookups.
func pIDEqual(id peer.ID) peer.ID { return id }
