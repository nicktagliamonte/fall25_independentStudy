// Purpose: Start/Close lifecycle for the embedded node service.

package node

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	stded25519 "crypto/ed25519"
	"crypto/sha256"

	routinghelpers "github.com/libp2p/go-libp2p-routing-helpers"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multiaddr"
	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// Start launches an embedded node with the provided options and returns a
// running Service, or an error if any stage of setup fails.
//
// Parameters:
//   - parent: the parent context governing the node's lifetime. Start
//     derives an internal cancelable context from it (via
//     context.WithCancel); canceling parent (or calling the returned
//     Service's Close) stops all of Start's background goroutines. Start
//     itself does not block on parent — it returns once setup completes.
//   - opts: configuration for the node; see Options for field-by-field
//     semantics and defaulting. Start mutates its local copy of opts (value
//     receiver) to apply defaults for ListenMultiaddrs, MinOutbound,
//     PerIPDialLimit, and DialTimeout before using them; the caller's
//     original Options value is not modified.
//
// Setup performed, in order (each failure closes any resources already
// opened and returns a non-nil error, leaving no partially-started
// goroutines behind):
//  1. Apply defaults to unset Options fields.
//  2. Create the libp2p host: using a persistent key at opts.KeyPath if set,
//     else a deterministic key derived from opts.EphemeralSeed if set, else
//     a fresh random identity.
//  3. Create the storage stack: a persistent blockstore at opts.StorePath if
//     set (with a null content router, since DHT-based routing is out of
//     scope for this package), else an in-memory stack.
//  4. Create the PeerStore over the same datastore as the storage stack, and
//     a fresh NodeMetrics.
//  5. Build the handshake admission policy (basePolicy) from
//     opts.RequireToken/opts.Token/opts.CAPubKeysB64, decoding and
//     validating each CA public key (must be 32 bytes); an invalid entry
//     aborts startup with an error.
//  6. Register the inbound handshake responder/gate (advertising current
//     chain head/height and listen addrs) and a peer-sample provider used to
//     answer WantPeerlist handshake requests from GetDialCandidates.
//  7. Construct the *service and start three background goroutines tracked
//     via its sync.WaitGroup: a peer-store pruning loop (every 5 minutes), a
//     dial-maintenance loop (continuously tries to reach opts.MinOutbound
//     outbound connections, respecting opts.PerIPDialLimit and
//     opts.DialTimeout, with exponential-ish backoff per candidate's prior
//     failure count capped at 5 minutes), and a gossip loop (every 2
//     minutes, re-handshakes with connected peers to learn new peer
//     addresses via HandshakeResult.Learned).
//  8. Seed the PeerStore with opts.BootstrapPeers (deduplicated, parsed as
//     p2p multiaddrs, skipping the local node's own ID or unparseable
//     entries).
//  9. Start the control-plane HTTP server (internal/control.Start), wiring
//     its shutdown trigger to cancel the node's internal context.
//
// Returns:
//   - Service: non-nil on success, ready for immediate use (Status, PutRaw,
//     GetRawFrom, ListImmediatePeerIDs, RestoreFromManifest, Close).
//   - error: non-nil if key loading, host creation, storage stack creation,
//     PeerStore creation, CA public key validation, or control-server
//     startup fails. On any such failure, resources already created in that
//     call (host, stack/Bitswap) are closed and the internal context is
//     canceled before returning, so the caller does not need to perform any
//     cleanup on error.
func Start(parent context.Context, opts Options) (Service, error) {
	// Defaults
	if len(opts.ListenMultiaddrs) == 0 {
		opts.ListenMultiaddrs = []string{
			"/ip4/0.0.0.0/tcp/2893",
			"/ip4/0.0.0.0/udp/2894/quic-v1",
		}
	}
	if opts.MinOutbound <= 0 {
		opts.MinOutbound = 4
	}
	if opts.PerIPDialLimit <= 0 {
		opts.PerIPDialLimit = 3
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}

	ctx, cancel := context.WithCancel(parent)

	// Host
	var h host.Host
	if opts.KeyPath != "" {
		priv, err := myhost.LoadOrCreatePrivateKey(opts.KeyPath)
		if err != nil {
			cancel()
			return nil, err
		}
		hh, err := myhost.NewHostWithPriv(ctx, opts.ListenMultiaddrs, priv)
		if err != nil {
			cancel()
			return nil, err
		}
		h = hh
	} else {
		if opts.EphemeralSeed != "" {
			// Derive deterministic Ed25519 key from seed
			sum := sha256.Sum256([]byte(opts.EphemeralSeed))
			key := stded25519.NewKeyFromSeed(sum[:])
			priv, err := crypto.UnmarshalEd25519PrivateKey([]byte(key))
			if err != nil {
				cancel()
				return nil, err
			}
			hh, err := myhost.NewHostWithPriv(ctx, opts.ListenMultiaddrs, priv)
			if err != nil {
				cancel()
				return nil, err
			}
			h = hh
		} else {
			hh, err := myhost.NewHost(ctx, opts.ListenMultiaddrs)
			if err != nil {
				cancel()
				return nil, err
			}
			h = hh
		}
	}

	// Storage
	var stack *mystore.Stack
	if opts.StorePath != "" {
		bs, d, err := mystore.NewPersistentBlockstore(opts.StorePath)
		if err != nil {
			_ = h.Close()
			cancel()
			return nil, err
		}
		var router routing.ContentRouting = routinghelpers.Null{}
		st, err := mystore.NewStackFromBlockstore(ctx, h, bs, d, router)
		if err != nil {
			_ = h.Close()
			cancel()
			return nil, err
		}
		stack = st
	} else {
		st, err := mystore.NewStack(ctx, h)
		if err != nil {
			_ = h.Close()
			cancel()
			return nil, err
		}
		stack = st
	}

	// Peer store + metrics
	peerStore, err := myhost.NewPeerStore(stack.Datastore)
	if err != nil {
		_ = stack.Bitswap.Close()
		_ = h.Close()
		cancel()
		return nil, err
	}
	metrics := &ctrl.NodeMetrics{}

	// Admission policy
	basePolicy := myhost.HandshakePolicy{Timeout: 10 * time.Second, MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0)}
	if opts.RequireToken || (len(opts.CAPubKeysB64) > 0 && opts.Token != "") {
		basePolicy.RequireCredential = true
		basePolicy.AuthScheme = "token-ed25519-v1"
		for _, s := range opts.CAPubKeysB64 {
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil || len(b) != 32 {
				_ = stack.Bitswap.Close()
				_ = h.Close()
				cancel()
				return nil, errors.New("invalid CAPubKeysB64 entry")
			}
			basePolicy.CAPubKeys = append(basePolicy.CAPubKeys, b)
		}
		basePolicy.Token = opts.Token
	}

	// Register handshake and gate with current state
	head, height, _ := mystore.GetHead(ctx, stack.Datastore)
	headStr := ""
	if head.Defined() {
		headStr = head.String()
	}
	myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height, ListenAddrs: hostAddrsStrings(h)}, basePolicy)
	_ = myhost.InstallHandshakeGateWithCallback(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, basePolicy, func(pid peer.ID) {
		_, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String())
		if opts.OnHandshake != nil {
			opts.OnHandshake(pid.String(), map[string]any{"direction": "inbound"})
		}
	})
	myhost.RegisterHandshakeWithPeers(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, ListenAddrs: hostAddrsStrings(h)}, myhost.HandshakePolicy{Timeout: 10 * time.Second}, func(max int) []peer.AddrInfo {
		infos, _ := peerStore.GetDialCandidates(max, 0, nil)
		return infos
	})

	s := &service{h: h, stack: stack, peerStore: peerStore, metrics: metrics, cancel: cancel, basePolicy: basePolicy, onHandshake: opts.OnHandshake, onAck: opts.OnAck}

	// Pruning loop
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				removed, _ := peerStore.Prune()
				metrics.AddPeersPruned(removed)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Dial maintenance loop
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		backoffBase := time.Second
		maxBackoff := 5 * time.Minute
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			conns := h.Network().Conns()
			outbound := 0
			exclude := make(map[peer.ID]bool)
			for _, c := range conns {
				if c.Stat().Direction == network.DirOutbound {
					outbound++
				}
				exclude[c.RemotePeer()] = true
			}
			if outbound >= opts.MinOutbound {
				time.Sleep(2 * time.Second)
				continue
			}
			needed := opts.MinOutbound - outbound
			cands, metas := peerStore.GetDialCandidates(needed*2, 0, exclude)
			if len(cands) == 0 {
				time.Sleep(5 * time.Second)
				continue
			}
			perIP := make(map[string]int)
			for i, info := range cands {
				// enforce per-IP dial limit
				for _, a := range info.Addrs {
					if v, err := a.ValueForProtocol(multiaddr.P_IP4); err == nil && v != "" {
						if perIP[v] >= opts.PerIPDialLimit {
							continue
						}
						perIP[v]++
						break
					}
					if v, err := a.ValueForProtocol(multiaddr.P_IP6); err == nil && v != "" {
						if perIP[v] >= opts.PerIPDialLimit {
							continue
						}
						perIP[v]++
						break
					}
				}
				pid := info.ID
				_ = peerStore.RecordDialAttempt(pid)
				metrics.IncDialsAttempted()
				ctxDial, cancelDial := context.WithTimeout(ctx, opts.DialTimeout)
				err := h.Connect(ctxDial, info)
				cancelDial()
				if err != nil {
					_ = peerStore.RecordDialFailure(pid)
					metrics.IncDialsFailed()
					bo := time.Duration(1+metas[i].FailureCount) * backoffBase
					if bo > maxBackoff {
						bo = maxBackoff
					}
					time.Sleep(bo)
					continue
				}
				_ = peerStore.RecordDialSuccess(pid)
				metrics.IncDialsSucceeded()
				// Non-fatal handshake + peerlist
				if res, err := myhost.PerformHandshakeWithState(context.Background(), h, pid, myhost.HandshakePolicy{Timeout: opts.DialTimeout}, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, WantPeerlist: true, ListenAddrs: hostAddrsStrings(h)}); err == nil {
					if opts.OnHandshake != nil {
						opts.OnHandshake(pid.String(), map[string]any{"direction": "outbound", "remote_height": res.RemoteStateHeight})
					}
					if opts.OnAck != nil {
						opts.OnAck(pid.String(), "ok")
					}
				}
				outbound++
				if outbound >= opts.MinOutbound {
					break
				}
			}
			time.Sleep(2 * time.Second)
		}
	}()

	// Gossip loop
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				peers := h.Network().Peers()
				for _, pid := range peers {
					if pid == h.ID() {
						continue
					}
					if res, err := myhost.PerformHandshakeWithState(context.Background(), h, pid, myhost.HandshakePolicy{Timeout: 5 * time.Second}, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, WantPeerlist: true, ListenAddrs: hostAddrsStrings(h)}); err == nil {
						for _, info := range res.Learned {
							if info.ID == h.ID() {
								continue
							}
							_ = peerStore.Upsert(info.ID, info.Addrs, 0, "gossip")
						}
						metrics.AddGossipLearned(len(res.Learned))
						if opts.OnHandshake != nil {
							opts.OnHandshake(pid.String(), map[string]any{"direction": "gossip", "remote_height": res.RemoteStateHeight})
						}
					}
				}
			}
		}
	}()

	// Seeds
	if len(opts.BootstrapPeers) > 0 {
		seen := make(map[string]struct{})
		for _, saddr := range opts.BootstrapPeers {
			if _, ok := seen[saddr]; ok {
				continue
			}
			seen[saddr] = struct{}{}
			if maddr, err := multiaddr.NewMultiaddr(saddr); err == nil {
				if info, err := peer.AddrInfoFromP2pAddr(maddr); err == nil && info.ID != h.ID() {
					_ = peerStore.Upsert(info.ID, info.Addrs, 0, "seed")
				}
			}
		}
	}

	// Control server
	addr, shutdown, err := ctrl.Start(ctx, h, stack, peerStore, metrics, func() { cancel() })
	if err != nil {
		_ = stack.Bitswap.Close()
		_ = h.Close()
		cancel()
		return nil, err
	}
	_ = addr // kept for future status
	s.controlShutdown = shutdown

	return s, nil
}
