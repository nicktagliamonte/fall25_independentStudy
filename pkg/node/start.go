// Purpose: Start/Close lifecycle for the embedded node service.

package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	stded25519 "crypto/ed25519"
	"crypto/sha256"

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

type service struct {
	h               host.Host
	dht             *kaddht.IpfsDHT
	stack           *mystore.Stack
	peerStore       *myhost.PeerStore
	metrics         *ctrl.NodeMetrics
	cancel          context.CancelFunc
	basePolicy      myhost.HandshakePolicy
	onHandshake     func(peerID string, info map[string]any)
	onAck           func(peerID string, status string)
	wg              sync.WaitGroup
	controlAddr     string
	controlShutdown func(context.Context) error
	stopIBLT        func()
}

// Start launches the node with the provided options and returns a Service.
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
	metrics := &ctrl.NodeMetrics{}

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

	// Datastore + blockstore
	var bs bstore.Blockstore
	var datastore ds.Batching
	if opts.StorePath != "" {
		var err error
		bs, datastore, err = mystore.NewPersistentBlockstore(opts.StorePath)
		if err != nil {
			_ = h.Close()
			cancel()
			return nil, err
		}
	} else {
		bs, datastore = mystore.NewEphemeralBlockstore()
	}

	// Peer store (before DHT so we can bootstrap from handshake discoveries)
	peerStore, err := myhost.NewPeerStore(datastore)
	if err != nil {
		_ = h.Close()
		cancel()
		return nil, err
	}

	// Seeds: DHT bootstrap peers + opts.BootstrapPeers (populate PeerStore before DHT)
	seedAddrs := append([]string{}, myhost.DefaultDHTBootstrapAddrs...)
	seedAddrs = append(seedAddrs, opts.BootstrapPeers...)
	seenSeeds := make(map[string]struct{})
	for _, saddr := range seedAddrs {
		if saddr == "" {
			continue
		}
		if _, ok := seenSeeds[saddr]; ok {
			continue
		}
		seenSeeds[saddr] = struct{}{}
		if maddr, err := multiaddr.NewMultiaddr(saddr); err == nil {
			if info, err := peer.AddrInfoFromP2pAddr(maddr); err == nil && info.ID != h.ID() {
				_ = peerStore.Upsert(info.ID, info.Addrs, 0, "seed")
			}
		}
	}

	// Storage stack: DHT with DynamicRouter fallback. Token routing (key-based) is primary.
	stack, d, dynamicRouter, err := BuildStackWithDHT(ctx, h, bs, datastore, peerStore, opts.DHTClientMode)
	metrics.SetDHTBootstrapPeers(int64(len(myhost.DefaultDHTBootstrapAddrs) + len(opts.BootstrapPeers)))
	if err != nil {
		_ = h.Close()
		cancel()
		return nil, err
	}

	// Admission policy
	basePolicy := myhost.HandshakePolicy{Timeout: 10 * time.Second, MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0)}
	if opts.RequireToken || (len(opts.CAPubKeysB64) > 0 && opts.Token != "") {
		basePolicy.RequireCredential = true
		basePolicy.AuthScheme = "token-ed25519-v1"
		for _, s := range opts.CAPubKeysB64 {
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil || len(b) != 32 {
				stack.Close()
				_ = h.Close()
				cancel()
				return nil, errors.New("invalid CAPubKeysB64 entry")
			}
			basePolicy.CAPubKeys = append(basePolicy.CAPubKeys, b)
		}
		basePolicy.Token = opts.Token
	}
	stopAntiReplay := myhost.EnableAntiReplay(ctx, &basePolicy)
	defer stopAntiReplay()
	_ = myhost.EnableAttackMitigation(ctx, &basePolicy)

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
	myhost.RegisterHandshakeWithPeers(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, ListenAddrs: hostAddrsStrings(h)}, basePolicy, func(max int) []peer.AddrInfo {
		infos, _ := peerStore.GetDialCandidates(max, 0, nil)
		return infos
	})

	s := &service{h: h, dht: d, stack: stack, peerStore: peerStore, metrics: metrics, cancel: cancel, basePolicy: basePolicy, onHandshake: opts.OnHandshake, onAck: opts.OnAck}

	providerRecords := mystore.NewLocalProviderRecords()
	providerRecords.AddAllFromDatastore(ctx, stack.Datastore)
	stack.ProviderRecords = providerRecords
	aq := mystore.NewAnnounceQueue()
	stack.AnnounceQueue = aq

	// Create repair protocol and gateway using DHT tuple space (before recovery callback setup)
	var repairProtocol *mystore.RepairProtocol
	var gateway *mygateway.Gateway
	if d != nil {
		dhtAdapter := mytuplespace.NewDHTValueStoreAdapter(d)
		dhtTS := mytuplespace.NewDHTTupleSpace(dhtAdapter)
		tokenized := opts.RequireToken || (len(opts.CAPubKeysB64) > 0 && opts.Token != "")
		repairProtocol = mystore.NewRepairProtocol(stack, h, dhtTS, tokenized)

		var baseTS mytuplespace.TupleSpace = dhtTS
		if opts.TSHAddr != "" {
			p2pTS := mytuplespace.NewP2PTupleSpace(opts.TSHAddr, 0x7f000001, "sng40")
			p2pTS.SetPermissionChecker(myhost.NewHandshakePermissionChecker(basePolicy))
			router := mytuplespace.NewRouter(dhtTS, p2pTS, nil)
			baseTS = router
		}
		tokenTS := mytuplespace.NewTokenFallbackTupleSpace(dhtAdapter, baseTS)
		gateway = mygateway.NewGateway(stack.Router, tokenTS)
		if ts := gateway.TokenStore(); ts != nil {
			stack.TokenStore = ts
		}
	}

	pcm := myhost.NewPeerConnectivityMonitor(h,
		myhost.PartitionMonitorOnPartitionEvent(func(e myhost.PartitionEvent) { aq.SetPartitioned(true) }),
		myhost.PartitionMonitorOnRecovery(func() {
			aq.SetPartitioned(false)
			stack.FlushQueuedAnnouncements(ctx)
			// Trigger repair protocol for missing replicas after partition recovery
			if repairProtocol != nil {
				stack.TriggerRepairForAllCIDsOnRecovery(ctx, h, repairProtocol)
			}
		}))
	go pcm.Start(ctx)
	mystore.StartPeriodicReannounce(ctx, providerRecords, stack.Blockstore, mystore.DefaultReannounceInterval, ctrl.NodeMetricsProviderSink(metrics))
	s.stopIBLT = InstallCatalogIBLT(ctx, h, stack)

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

	// Connection security verification (ECDH/encryption)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
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
				if am := basePolicy.AttackMitigation; am != nil {
					if am.BanList.IsBanned(pid) {
						continue
					}
					if ok, _ := am.Eclipse.CanAllow(ctx, pid, info.Addrs); !ok {
						continue
					}
				}
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
				pol := basePolicy
				pol.Timeout = opts.DialTimeout
				if res, err := myhost.PerformHandshakeWithState(context.Background(), h, pid, pol, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, WantPeerlist: true, ListenAddrs: hostAddrsStrings(h)}); err == nil {
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
					pol := basePolicy
					pol.Timeout = 5 * time.Second
					if res, err := myhost.PerformHandshakeWithState(context.Background(), h, pid, pol, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, WantPeerlist: true, ListenAddrs: hostAddrsStrings(h)}); err == nil {
						for _, info := range res.Learned {
							if info.ID == h.ID() {
								continue
							}
							_ = myhost.UpsertLearnedPeer(peerStore, basePolicy.AttackMitigation, info.ID, info.Addrs, 0, "gossip")
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

	// Wire message metrics for P2P message counting (put, get, lookup)
	stack.MessageSink = ctrl.NodeMetricsMessageSink(metrics)
	stack.HopSink = ctrl.NodeMetricsHopSink(metrics)

	// Control server
	addr, shutdown, err := ctrl.Start(ctx, h, stack, peerStore, metrics, func() { cancel() }, dynamicRouter, repairProtocol, gateway, "")
	if err != nil {
		stack.Close()
		_ = d.Close()
		_ = h.Close()
		cancel()
		return nil, err
	}
	s.controlAddr = addr
	s.controlShutdown = shutdown

	return s, nil
}

func (s *service) Close(ctx context.Context) error {
	if s.controlShutdown != nil {
		_ = s.controlShutdown(ctx)
	}
	if s.stopIBLT != nil {
		s.stopIBLT()
	}
	s.cancel()
	s.wg.Wait()
	s.stack.Close()
	if s.dht != nil {
		_ = s.dht.Close()
	}
	return s.h.Close()
}

func (s *service) Status(ctx context.Context) (Status, error) {
	head, height, _ := mystore.GetHead(ctx, s.stack.Datastore)
	st := Status{
		PeerID: s.h.ID().String(),
		Head:   head.String(),
		Height: height,
	}
	for _, a := range s.h.Addrs() {
		st.Addrs = append(st.Addrs, a.String())
	}
	snap := s.metrics.Snapshot()
	st.Metrics.DialsAttempted = snap.DialsAttempted
	st.Metrics.DialsSucceeded = snap.DialsSucceeded
	st.Metrics.DialsFailed = snap.DialsFailed
	st.Metrics.PeersPruned = snap.PeersPruned
	st.Metrics.GossipLearned = snap.GossipLearned
	return st, nil
}

func (s *service) PutRaw(ctx context.Context, data []byte) (string, int, error) {
	req := struct {
		Data string `json:"data"`
	}{Data: string(data)}
	buf, _ := json.Marshal(req)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Post("http://"+s.controlAddr+"/put", "application/json", bytes.NewReader(buf))
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", 0, fmt.Errorf("put failed: %s", string(body))
	}
	var out struct {
		CID string `json:"cid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, err
	}
	return out.CID, len(data), nil
}

func (s *service) GetRawFrom(ctx context.Context, providerAddr string, providerPeer string, cidStr string, timeout time.Duration) ([]byte, error) {
	req := struct {
		CID     string `json:"cid"`
		Addr    string `json:"from_addr"`
		Peer    string `json:"from_peer"`
		Timeout string `json:"timeout"`
	}{CID: cidStr, Addr: providerAddr, Peer: providerPeer, Timeout: timeout.String()}
	buf, _ := json.Marshal(req)
	resp, err := (&http.Client{Timeout: timeout + 5*time.Second}).Post("http://"+s.controlAddr+"/get", "application/json", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get failed: %s", string(body))
	}
	var out struct {
		DataB64 string `json:"data_b64"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.DataB64)
}

func (s *service) ListImmediatePeerIDs(ctx context.Context) ([]string, error) {
	var ids []string
	for _, pid := range s.h.Network().Peers() {
		if pid != s.h.ID() {
			ids = append(ids, pid.String())
		}
	}
	return ids, nil
}

func (s *service) RestoreFromManifest(ctx context.Context, cids []string, concurrency int, timeout time.Duration, byteBudget int64) (RestoreStats, error) {
	req := struct {
		CIDs        []string `json:"cids"`
		Concurrency int      `json:"concurrency"`
		Timeout     string   `json:"timeout"`
		ByteBudget  int64    `json:"byte_budget"`
	}{CIDs: cids, Concurrency: concurrency, Timeout: timeout.String(), ByteBudget: byteBudget}
	buf, _ := json.Marshal(req)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Post("http://"+s.controlAddr+"/restore", "application/json", bytes.NewReader(buf))
	if err != nil {
		return RestoreStats{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return RestoreStats{}, fmt.Errorf("restore submit failed: %s", string(body))
	}
	var submit struct {
		Job string `json:"job"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&submit); err != nil {
		return RestoreStats{}, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	pollDeadline := time.Now().Add(timeout * time.Duration(len(cids)+1))
	for time.Now().Before(pollDeadline) {
		select {
		case <-ctx.Done():
			return RestoreStats{}, ctx.Err()
		default:
		}
		time.Sleep(500 * time.Millisecond)
		r2, err := client.Get("http://" + s.controlAddr + "/restore/status?id=" + submit.Job)
		if err != nil {
			continue
		}
		var st struct {
			OK     int   `json:"ok"`
			Failed int   `json:"failed"`
			Bytes  int64 `json:"bytes"`
			Done   bool  `json:"done"`
		}
		if json.NewDecoder(r2.Body).Decode(&st) != nil {
			_ = r2.Body.Close()
			continue
		}
		_ = r2.Body.Close()
		if st.Done {
			return RestoreStats{OK: st.OK, Failed: st.Failed, Bytes: st.Bytes}, nil
		}
	}
	return RestoreStats{}, fmt.Errorf("restore poll timeout")
}
