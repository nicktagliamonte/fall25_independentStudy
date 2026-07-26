// Purpose: Shared subsystem construction for the two node entry points
// (run.go's "run" CLI subcommand and start.go's Start library entry point).
// Both independently assembled the same host/blockstore/peerstore/DHT stack,
// handshake policy, repair protocol/gateway, and background maintenance
// loops; this file centralizes that construction in buildNodeSubsystems
// (plus the dial-maintenance and gossip loop skeletons in
// startDialMaintenanceLoop/startGossipLoop), while leaving every
// caller-specific divergence (credential enforcement, peer-store policy,
// bootstrap-metric reporting, post-handshake follow-up behavior, etc.)
// controlled by explicit parameters/hooks rather than silently picking one
// side's behavior.
package node

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log"
	"sync"
	"time"

	stded25519 "crypto/ed25519"

	bstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	mygateway "github.com/nicktagliamonte/fall25_independentStudy/internal/gateway"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
	mytuplespace "github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

// nodeSubsystemsConfig configures buildNodeSubsystems. Field names mirror
// Options (api.go) where the purpose is identical, since start.go's Start
// derives most of these directly from its Options parameter. run.go's "run"
// subcommand populates the same fields from CLI flags/env vars; any field
// that only one of the two current callers uses says so in its comment.
type nodeSubsystemsConfig struct {
	// KeyPath, if non-empty, loads (or creates) a persistent libp2p private
	// key from this path for the host identity.
	KeyPath string
	// EphemeralSeed, if non-empty and KeyPath is empty, deterministically
	// derives an ed25519 identity from SHA-256(EphemeralSeed). Only used by
	// start.go's Start (via Options.EphemeralSeed); run.go's "run" has no
	// equivalent flag and always passes "".
	EphemeralSeed string
	// ListenMultiaddrs are the multiaddrs the libp2p host listens on.
	ListenMultiaddrs []string
	// StorePath, if non-empty, is the path for a persistent blockstore and
	// datastore; empty uses an ephemeral in-memory store.
	StorePath string

	// BootstrapPeers are extra seed multiaddrs added on top of
	// myhost.DefaultDHTBootstrapAddrs before the peerstore/DHT bootstrap
	// list is built. run.go's "run" pre-merges its --seed/--seed-file/
	// SNG40_SEEDS sources into this slice before calling
	// buildNodeSubsystems; start.go passes Options.BootstrapPeers directly.
	BootstrapPeers []string
	// DHTClientMode, if true, runs the DHT in client (query-only) mode.
	// run.go's "run" subcommand has no such flag and always passes false.
	DHTClientMode bool

	// HasPeerPolicy, if true, applies StaleAge/MaxFailures to the peerstore
	// via SetPolicy. Only run.go's "run" subcommand sets this (from
	// --stale-age/--max-fail); start.go's Start has no equivalent Options
	// fields and leaves the peerstore's built-in defaults in place.
	HasPeerPolicy bool
	StaleAge      time.Duration
	MaxFailures   int
	// MaxKnown, if > 0, caps tracked peerstore entries via SetMaxKnown. Only
	// run.go's "run" subcommand sets this (from --max-known).
	MaxKnown int

	// MinOutbound/ClusterNodeCount parameterize BuildStackWithDHT-adjacent
	// bookkeeping shared with the dial loop's dialLoopConfig.
	MinOutbound      int
	ClusterNodeCount int

	// RequireToken/Token/CAPubKeysB64 configure handshake credential
	// enforcement, exactly as Options does. run.go's "run" subcommand has no
	// equivalent flags/env handling and always passes the zero values (so
	// RequireCredential is never enabled and no CA pubkeys are parsed),
	// matching its current behavior of never enforcing credentials on the
	// daemon's own handshake policy.
	RequireToken bool
	Token        string
	CAPubKeysB64 []string

	// TSHAddr is the optional TSH (tuple-space) daemon address. run.go's
	// "run" subcommand reads this from the TSH_ADDR env var; start.go passes
	// Options.TSHAddr.
	TSHAddr string

	// ReportBootstrapMetric, if true, calls metrics.SetDHTBootstrapPeers
	// after the stack is built. Only start.go's Start sets this; run.go's
	// "run" subcommand never reported this metric and continues not to.
	ReportBootstrapMetric bool

	// OnGateHandshake, if set, is invoked (in addition to the always-run
	// AppendPeerAddedIfNew bookkeeping) from the InstallHandshakeGateWithCallback
	// callback. Only start.go's Start sets this, to invoke Options.OnHandshake
	// with direction "inbound".
	OnGateHandshake func(pid peer.ID)

	// Cancel is the CancelFunc for ctx, called on every internal error path
	// before returning (mirroring start.go's original per-branch cancel()
	// calls). Both callers pass their real cancel function; run.go's "run"
	// case also has a top-level `defer cancelMain()` already registered, so
	// calling it again here is redundant but harmless (CancelFunc is
	// idempotent).
	Cancel func()

	// WG, if non-nil, is used to track the pruning and connection-security
	// background loops (Add(1) before each goroutine, Done() on exit) so a
	// caller can wait for them via wg.Wait() during shutdown. start.go's
	// Start passes &service.wg (its Close() method waits on it); run.go's
	// "run" case passes nil, matching its original behavior of never
	// waiting for these loops to exit before returning.
	WG *sync.WaitGroup
}

// nodeSubsystems bundles everything buildNodeSubsystems constructs and that
// its two callers (run.go's "run" case and start.go's Start) need afterward,
// either to finish their own caller-specific wiring (handshake responder
// registration, dial/gossip loop hooks) or to manage shutdown.
type nodeSubsystems struct {
	Host          host.Host
	DHT           *kaddht.IpfsDHT
	Stack         *mystore.Stack
	DynamicRouter *ctrl.DynamicRouter
	PeerStore     *myhost.PeerStore
	Metrics       *ctrl.NodeMetrics
	PolicyBase    myhost.HandshakePolicy

	ProviderRecords *mystore.LocalProviderRecords
	AnnounceQueue   *mystore.AnnounceQueue
	RepairProtocol  *mystore.RepairProtocol
	Gateway         *mygateway.Gateway

	// HeadStr/Height are this node's state head/height as of construction
	// time (used to seed handshake responders and as the "are we behind"
	// baseline for dial/gossip suffix-sync follow-up logic).
	HeadStr string
	Height  int64

	// StopAntiReplay/StopIBLT are cleanup funcs for the anti-replay tracker
	// and the catalog IBLT exchange loop, respectively. Deliberately left for
	// each caller to invoke on its own schedule rather than deferred inside
	// buildNodeSubsystems itself, since each entry point's notion of "real
	// shutdown" differs: run.go's "run" case defers both at the call site,
	// which only returns at true node shutdown; start.go's Start stores both
	// on its service struct and calls them from Close(), which is the
	// embedded-library caller's real shutdown point. (StopAntiReplay was
	// previously deferred at Start()'s own return instead, stopping the
	// anti-replay tracker almost immediately after startup rather than at
	// Close() — see git history for that bug; it now matches StopIBLT's
	// store-and-call-in-Close pattern.)
	StopAntiReplay func()
	StopIBLT       func()
}

// buildNodeSubsystems assembles the libp2p host, blockstore/peerstore, DHT
// storage stack, handshake policy (anti-replay + attack mitigation +
// credential enforcement), repair protocol/gateway, connectivity monitor,
// periodic reannounce, catalog IBLT exchange, peer-pruning loop,
// connection-security-verification loop: the subsystem set that run.go's
// "run" case and start.go's Start both need. It does not install handshake
// responders (RegisterHandshake/RegisterHandshakeWithPeers), start the
// dial-maintenance/gossip loops (see startDialMaintenanceLoop and
// startGossipLoop), or start the control server (ctrl.Start) — each caller
// does those itself, in the same relative order as before this refactor
// (handshake responders, then dial/gossip loops, then the control server),
// since those pieces differ between the two callers in ways beyond simple
// parameterization (see each field's doc comment above for the specific
// divergences preserved via cfg).
//
// On any failure, whatever was already constructed is unwound (closing the
// host/stack/DHT as applicable) and cfg.Cancel is invoked before returning
// the error, mirroring start.go's original per-branch error handling.
//
// Parameters:
//   - ctx (context.Context): shared context for the host, DHT, and all
//     background loops; canceled via cfg.Cancel on construction failure.
//   - cfg (nodeSubsystemsConfig): configuration; see field docs for which
//     caller sets what.
//
// Returns:
//   - *nodeSubsystems: the constructed subsystem set, or nil on error.
//   - error: non-nil if any subsystem fails to construct.
func buildNodeSubsystems(ctx context.Context, cfg nodeSubsystemsConfig) (*nodeSubsystems, error) {
	metrics := &ctrl.NodeMetrics{}

	// Host identity.
	var h host.Host
	if cfg.KeyPath != "" {
		priv, err := myhost.LoadOrCreatePrivateKey(cfg.KeyPath)
		if err != nil {
			cfg.Cancel()
			return nil, err
		}
		hh, err := myhost.NewHostWithPriv(ctx, cfg.ListenMultiaddrs, priv)
		if err != nil {
			cfg.Cancel()
			return nil, err
		}
		h = hh
	} else if cfg.EphemeralSeed != "" {
		sum := sha256.Sum256([]byte(cfg.EphemeralSeed))
		key := stded25519.NewKeyFromSeed(sum[:])
		priv, err := crypto.UnmarshalEd25519PrivateKey([]byte(key))
		if err != nil {
			cfg.Cancel()
			return nil, err
		}
		hh, err := myhost.NewHostWithPriv(ctx, cfg.ListenMultiaddrs, priv)
		if err != nil {
			cfg.Cancel()
			return nil, err
		}
		h = hh
	} else {
		hh, err := myhost.NewHost(ctx, cfg.ListenMultiaddrs)
		if err != nil {
			cfg.Cancel()
			return nil, err
		}
		h = hh
	}

	// Datastore + blockstore (persistent or ephemeral).
	var bs bstore.Blockstore
	var datastore ds.Batching
	if cfg.StorePath != "" {
		var err error
		bs, datastore, err = mystore.NewPersistentBlockstore(cfg.StorePath)
		if err != nil {
			_ = h.Close()
			cfg.Cancel()
			return nil, err
		}
	} else {
		bs, datastore = mystore.NewEphemeralBlockstore()
	}

	// PeerStore (before DHT so we can bootstrap from seeds).
	peerStore, err := myhost.NewPeerStore(datastore)
	if err != nil {
		_ = h.Close()
		cfg.Cancel()
		return nil, err
	}
	if cfg.HasPeerPolicy {
		peerStore.SetPolicy(cfg.StaleAge, cfg.MaxFailures)
	}
	if cfg.MaxKnown > 0 {
		peerStore.SetMaxKnown(cfg.MaxKnown)
	}

	// Seeds: DHT bootstrap + caller-supplied extras.
	seedAddrs := append([]string{}, myhost.DefaultDHTBootstrapAddrs...)
	seedAddrs = append(seedAddrs, cfg.BootstrapPeers...)
	seenSeeds := make(map[string]struct{})
	for _, saddr := range seedAddrs {
		if saddr == "" {
			continue
		}
		if _, ok := seenSeeds[saddr]; ok {
			continue
		}
		seenSeeds[saddr] = struct{}{}
		maddr, err := multiaddr.NewMultiaddr(saddr)
		if err != nil {
			continue
		}
		if info, err := peer.AddrInfoFromP2pAddr(maddr); err == nil && info.ID != h.ID() {
			_ = peerStore.Upsert(info.ID, info.Addrs, 0, "seed")
		}
	}

	// Storage stack: DHT with DynamicRouter fallback.
	stack, dht, dynamicRouter, err := BuildStackWithDHT(ctx, h, bs, datastore, peerStore, cfg.DHTClientMode)
	if err != nil {
		_ = h.Close()
		cfg.Cancel()
		return nil, err
	}
	if cfg.ReportBootstrapMetric {
		metrics.SetDHTBootstrapPeers(int64(len(myhost.DefaultDHTBootstrapAddrs) + len(cfg.BootstrapPeers)))
	}

	// Admission policy: base fields always set; credential enforcement only
	// if requested (either forced via RequireToken, or implied by a
	// non-empty CAPubKeysB64+Token pair).
	policyBase := myhost.HandshakePolicy{Timeout: 10 * time.Second, MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0)}
	if cfg.RequireToken || (len(cfg.CAPubKeysB64) > 0 && cfg.Token != "") {
		policyBase.RequireCredential = true
		policyBase.AuthScheme = "token-ed25519-v1"
		for _, s := range cfg.CAPubKeysB64 {
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil || len(b) != 32 {
				stack.Close()
				_ = h.Close()
				cfg.Cancel()
				return nil, errors.New("invalid CAPubKeysB64 entry")
			}
			policyBase.CAPubKeys = append(policyBase.CAPubKeys, b)
		}
		policyBase.Token = cfg.Token
	}
	stopAntiReplay := myhost.EnableAntiReplay(ctx, &policyBase)
	_ = myhost.EnableAttackMitigation(ctx, &policyBase)

	// Current state head/height, and the handshake gate (inbound admission).
	head, height, _ := mystore.GetHead(ctx, stack.Datastore)
	headStr := ""
	if head.Defined() {
		headStr = head.String()
	}
	_ = myhost.InstallHandshakeGateWithCallback(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, policyBase, func(pid peer.ID) {
		_, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String())
		if cfg.OnGateHandshake != nil {
			cfg.OnGateHandshake(pid)
		}
	})

	// Provider records + announce queue.
	providerRecords := mystore.NewLocalProviderRecords()
	providerRecords.AddAllFromDatastore(ctx, stack.Datastore)
	stack.ProviderRecords = providerRecords
	aq := mystore.NewAnnounceQueue()
	stack.AnnounceQueue = aq

	// Repair protocol + gateway, using the DHT tuple space.
	var repairProtocol *mystore.RepairProtocol
	var gateway *mygateway.Gateway
	if dht != nil {
		dhtAdapter := mytuplespace.NewDHTValueStoreAdapter(dht)
		dhtTS := mytuplespace.NewDHTTupleSpace(dhtAdapter)
		tokenized := cfg.RequireToken || (len(cfg.CAPubKeysB64) > 0 && cfg.Token != "")
		repairProtocol = mystore.NewRepairProtocol(stack, h, dhtTS, tokenized)

		var baseTS mytuplespace.TupleSpace = dhtTS
		if cfg.TSHAddr != "" {
			p2pTS := mytuplespace.NewP2PTupleSpace(cfg.TSHAddr, 0x7f000001, "sng40")
			p2pTS.SetPermissionChecker(myhost.NewHandshakePermissionChecker(policyBase))
			router := mytuplespace.NewRouter(dhtTS, p2pTS, nil)
			baseTS = router
		}
		tokenTS := mytuplespace.NewTokenFallbackTupleSpace(dhtAdapter, baseTS)
		gateway = mygateway.NewGateway(stack.Router, tokenTS)
		if ts := gateway.TokenStore(); ts != nil {
			stack.TokenStore = ts
		}
	}
	if repairProtocol != nil {
		repairProtocol.StartAdvertisingStorageAvailability(ctx)
	}

	// Partition monitor: flush queued announcements and trigger repair on recovery.
	pcm := myhost.NewPeerConnectivityMonitor(h,
		myhost.PartitionMonitorOnPartitionEvent(func(e myhost.PartitionEvent) { aq.SetPartitioned(true) }),
		myhost.PartitionMonitorOnRecovery(func() {
			aq.SetPartitioned(false)
			stack.FlushQueuedAnnouncements(ctx)
			if repairProtocol != nil {
				stack.TriggerRepairForAllCIDsOnRecovery(ctx, h, repairProtocol)
			}
		}))
	go pcm.Start(ctx)
	mystore.StartPeriodicReannounce(ctx, providerRecords, stack.Blockstore, mystore.DefaultReannounceInterval, ctrl.NodeMetricsProviderSink(metrics))
	stopIBLT := InstallCatalogIBLT(ctx, h, stack)

	// Periodic pruning of stale/failing peers.
	if cfg.WG != nil {
		cfg.WG.Add(1)
	}
	go func() {
		if cfg.WG != nil {
			defer cfg.WG.Done()
		}
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

	// Connection security verification (ECDH/encryption).
	if cfg.WG != nil {
		cfg.WG.Add(1)
	}
	go func() {
		if cfg.WG != nil {
			defer cfg.WG.Done()
		}
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := myhost.VerifyECDHKeyDerivationUsed(h); err != nil {
					log.Printf("connection security: %v", err)
				}
				if err := myhost.EnsureAllTrafficEncrypted(h); err != nil {
					log.Printf("connection security: %v", err)
				}
			}
		}
	}()

	// Wire message/hop metrics sinks.
	stack.MessageSink = ctrl.NodeMetricsMessageSink(metrics)
	stack.HopSink = ctrl.NodeMetricsHopSink(metrics)

	return &nodeSubsystems{
		Host:            h,
		DHT:             dht,
		Stack:           stack,
		DynamicRouter:   dynamicRouter,
		PeerStore:       peerStore,
		Metrics:         metrics,
		PolicyBase:      policyBase,
		ProviderRecords: providerRecords,
		AnnounceQueue:   aq,
		RepairProtocol:  repairProtocol,
		Gateway:         gateway,
		HeadStr:         headStr,
		Height:          height,
		StopAntiReplay:  stopAntiReplay,
		StopIBLT:        stopIBLT,
	}, nil
}

// dialLoopConfig holds the dial-maintenance-loop parameters that differ by
// caller (drawn from CLI flags in run.go's "run" case, or from Options in
// start.go's Start), plus an optional post-handshake hook and WaitGroup.
type dialLoopConfig struct {
	MinOutbound      int
	ClusterNodeCount int
	PerIPDialLimit   int
	DialTimeout      time.Duration
	// OnHandshakeSuccess, if set, is invoked after each non-fatal successful
	// post-connect handshake, with the peer and handshake result. This is
	// where the two callers' genuinely divergent follow-up behavior lives:
	// run.go's "run" case connects to a bounded subset of learned peers and
	// attempts a suffix sync if the remote is ahead in state height;
	// start.go's Start only invokes its Options.OnHandshake/OnAck hooks.
	OnHandshakeSuccess func(pid peer.ID, res *myhost.HandshakeResult)
	// WG, if non-nil, is tracked via Add(1)/Done() for the loop's goroutine.
	// start.go's Start passes &service.wg; run.go's "run" case passes nil.
	WG *sync.WaitGroup
}

// startDialMaintenanceLoop runs the shared outbound dial-maintenance loop:
// it tops up outbound connections toward effectiveOutboundTarget, honoring
// cfg.PerIPDialLimit and any ban/eclipse mitigation on policyBase, then
// performs a post-connect handshake and (only on success) invokes
// cfg.OnHandshakeSuccess so each caller can layer its own divergent
// follow-up behavior.
//
// Parameters:
//   - ctx (context.Context): loop lifetime; returns promptly on ctx.Done().
//   - h (host.Host): the libp2p host to dial from.
//   - peerStore (*myhost.PeerStore): dial-candidate source and bookkeeping.
//   - policyBase (myhost.HandshakePolicy): base policy; a per-dial copy has Timeout overridden.
//   - metrics (*ctrl.NodeMetrics): dial attempt/success/failure counters.
//   - cfg (dialLoopConfig): per-caller targets/limits, success hook, and WaitGroup.
func startDialMaintenanceLoop(ctx context.Context, h host.Host, peerStore *myhost.PeerStore, policyBase myhost.HandshakePolicy, metrics *ctrl.NodeMetrics, cfg dialLoopConfig) {
	if cfg.WG != nil {
		cfg.WG.Add(1)
	}
	go func() {
		if cfg.WG != nil {
			defer cfg.WG.Done()
		}
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
			target := effectiveOutboundTarget(cfg.MinOutbound, cfg.ClusterNodeCount, peerStore.CountKnownPeersWithAddrs(h.ID()))
			if outbound >= target {
				time.Sleep(2 * time.Second)
				continue
			}
			needed := target - outbound
			cands, metas := peerStore.GetDialCandidates(needed*2, 0, exclude)
			if len(cands) == 0 {
				// nothing to dial; sleep a bit
				time.Sleep(5 * time.Second)
				continue
			}
			perIP := make(map[string]int)
			for i, info := range cands {
				// enforce per-IP dial limit
				for _, a := range info.Addrs {
					if v, err := a.ValueForProtocol(multiaddr.P_IP4); err == nil && v != "" {
						if perIP[v] >= cfg.PerIPDialLimit {
							continue
						}
						perIP[v]++
						break
					}
					if v, err := a.ValueForProtocol(multiaddr.P_IP6); err == nil && v != "" {
						if perIP[v] >= cfg.PerIPDialLimit {
							continue
						}
						perIP[v]++
						break
					}
				}
				pid := info.ID
				if am := policyBase.AttackMitigation; am != nil {
					if am.BanList.IsBanned(pid) {
						continue
					}
					if ok, _ := am.Eclipse.CanAllow(ctx, pid, info.Addrs); !ok {
						continue
					}
				}
				_ = peerStore.RecordDialAttempt(pid)
				metrics.IncDialsAttempted()
				// Try to connect with timeout
				ctxDial, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
				err := h.Connect(ctxDial, info)
				cancel()
				if err != nil {
					_ = peerStore.RecordDialFailure(pid)
					metrics.IncDialsFailed()
					// incremental backoff per failure count
					bo := time.Duration(1+metas[i].FailureCount) * backoffBase
					if bo > maxBackoff {
						bo = maxBackoff
					}
					time.Sleep(bo)
					continue
				}
				_ = peerStore.RecordDialSuccess(pid)
				metrics.IncDialsSucceeded()
				// post-connect, attempt handshake (non-fatal), with want peerlist
				pol := policyBase
				pol.Timeout = cfg.DialTimeout
				if res, err := myhost.PerformHandshakeWithState(context.Background(), h, pid, pol, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, WantPeerlist: true, ListenAddrs: hostAddrsStrings(h)}); err == nil {
					if cfg.OnHandshakeSuccess != nil {
						cfg.OnHandshakeSuccess(pid, res)
					}
				}
				// if we've satisfied outbound, break
				outbound++
				if outbound >= effectiveOutboundTarget(cfg.MinOutbound, cfg.ClusterNodeCount, peerStore.CountKnownPeersWithAddrs(h.ID())) {
					break
				}
			}
			// small pause before next maintenance iteration
			time.Sleep(2 * time.Second)
		}
	}()
}

// startGossipLoop runs the shared periodic peer-gossip loop: every 2
// minutes, it performs a peerlist-requesting handshake with every currently
// connected peer and (only on success) invokes onHandshakeSuccess so each
// caller can layer its own divergent follow-up behavior (run.go's "run" case
// records learned peers, updates gossip-learned metrics, and attempts a
// suffix sync if the remote is ahead; start.go's Start does the same
// learned-peer/metrics bookkeeping but invokes its Options.OnHandshake hook
// instead of syncing).
//
// Parameters:
//   - ctx (context.Context): loop lifetime; returns promptly on ctx.Done().
//   - h (host.Host): the libp2p host whose connected peers are gossiped with.
//   - policyBase (myhost.HandshakePolicy): base policy; a per-handshake copy has Timeout overridden to 5s.
//   - onHandshakeSuccess (func(peer.ID, *myhost.HandshakeResult)): invoked after each successful gossip handshake.
//   - wg (*sync.WaitGroup): if non-nil, tracked via Add(1)/Done() for the loop's goroutine.
func startGossipLoop(ctx context.Context, h host.Host, policyBase myhost.HandshakePolicy, onHandshakeSuccess func(pid peer.ID, res *myhost.HandshakeResult), wg *sync.WaitGroup) {
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			defer wg.Done()
		}
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
					pol := policyBase
					pol.Timeout = 5 * time.Second
					if res, err := myhost.PerformHandshakeWithState(context.Background(), h, pid, pol, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, WantPeerlist: true, ListenAddrs: hostAddrsStrings(h)}); err == nil {
						if onHandshakeSuccess != nil {
							onHandshakeSuccess(pid, res)
						}
					}
				}
			}
		}
	}()
}
