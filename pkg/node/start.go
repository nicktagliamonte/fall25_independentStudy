// Purpose: Start/Close lifecycle for the embedded node service.

package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// service is the concrete Service implementation returned by Start. It holds
// the running node's libp2p host, DHT, storage stack, and peerstore, plus the
// bookkeeping needed to stop all background goroutines and the control server
// on Close. Service methods that perform data-plane operations (PutRaw,
// GetRawFrom, RestoreFromManifest) do so by issuing HTTP requests to the
// node's own local control server rather than calling into the stack
// directly, so behavior matches the CLI's --daemon mode.
type service struct {
	h               host.Host                                // the libp2p host for this node.
	dht             *kaddht.IpfsDHT                          // the Kademlia DHT instance backing routing/token storage; may be nil.
	stack           *mystore.Stack                           // the storage stack (blockstore, routing table, locks).
	peerStore       *myhost.PeerStore                        // known peers and dial-candidate bookkeeping.
	metrics         *ctrl.NodeMetrics                        // counters exposed via Status and the control server's /metrics endpoint.
	cancel          context.CancelFunc                       // cancels the context all background goroutines and the DHT/host share.
	basePolicy      myhost.HandshakePolicy                   // the handshake/admission policy applied to inbound and outbound connections.
	onHandshake     func(peerID string, info map[string]any) // optional hook invoked after each handshake; copied from Options.OnHandshake.
	onAck           func(peerID string, status string)       // optional hook invoked after a successful outbound handshake; copied from Options.OnAck.
	wg              sync.WaitGroup                           // tracks background goroutines (pruning, security check, dialer, gossip) so Close can wait for them to exit.
	controlAddr     string                                   // "host:port" of the node's local HTTP control server.
	controlShutdown func(context.Context) error              // shuts down the control server; set by ctrl.Start.
	stopIBLT        func()                                   // stops the periodic catalog IBLT exchange loop; set by InstallCatalogIBLT.
	stopAntiReplay  func()                                   // stops the anti-replay tracker (nonce/hash/timestamp checks); set by EnableAntiReplay via buildNodeSubsystems.
}

// Start assembles and launches an embedded node: it creates (or loads) the
// libp2p host identity, opens the blockstore/datastore, seeds the peerstore,
// builds the DHT-backed storage stack (BuildStackWithDHT), configures the
// handshake/admission policy, wires the repair protocol and gateway, and
// starts all background maintenance loops (peer pruning, connection security
// verification, outbound dial maintenance, peer gossip, and periodic catalog
// IBLT exchange). It then starts the node's local HTTP control server, which
// backs the Service methods (PutRaw, GetRawFrom, etc.). On any failure it
// unwinds whatever was already created (closing the host, stack, and DHT as
// applicable) before returning the error.
//
// Defaults applied when the corresponding Options field is unset: listen
// addrs default to TCP/2893 + QUIC/2894; ClusterNodeCount falls back to the
// CLUSTER_NODE_COUNT environment variable; PerIPDialLimit defaults to 3;
// DialTimeout defaults to 10s.
//
// Parameters:
//   - parent (context.Context): parent context; a child context is derived and canceled on Close.
//   - opts (Options): configuration for identity, networking, storage, admission, and control-plane hooks.
//
// Returns:
//   - Service: the running node handle, or nil on error.
//   - error: non-nil if any subsystem (host, blockstore, peerstore, DHT/stack, control server) fails to start.
func Start(parent context.Context, opts Options) (Service, error) {
	// Defaults
	if len(opts.ListenMultiaddrs) == 0 {
		opts.ListenMultiaddrs = []string{
			"/ip4/0.0.0.0/tcp/2893",
			"/ip4/0.0.0.0/udp/2894/quic-v1",
		}
	}
	if opts.ClusterNodeCount == 0 {
		if v := os.Getenv("CLUSTER_NODE_COUNT"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				opts.ClusterNodeCount = n
			}
		}
	}
	if opts.PerIPDialLimit <= 0 {
		opts.PerIPDialLimit = 3
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}

	ctx, cancel := context.WithCancel(parent)
	s := &service{cancel: cancel}

	subs, err := buildNodeSubsystems(ctx, nodeSubsystemsConfig{
		KeyPath:               opts.KeyPath,
		EphemeralSeed:         opts.EphemeralSeed,
		ListenMultiaddrs:      opts.ListenMultiaddrs,
		StorePath:             opts.StorePath,
		BootstrapPeers:        opts.BootstrapPeers,
		DHTClientMode:         opts.DHTClientMode,
		MinOutbound:           opts.MinOutbound,
		ClusterNodeCount:      opts.ClusterNodeCount,
		RequireToken:          opts.RequireToken,
		Token:                 opts.Token,
		CAPubKeysB64:          opts.CAPubKeysB64,
		TSHAddr:               opts.TSHAddr,
		ReportBootstrapMetric: true,
		OnGateHandshake: func(pid peer.ID) {
			if opts.OnHandshake != nil {
				opts.OnHandshake(pid.String(), map[string]any{"direction": "inbound"})
			}
		},
		Cancel: cancel,
		WG:     &s.wg,
	})
	if err != nil {
		return nil, err
	}

	s.h = subs.Host
	s.dht = subs.DHT
	s.stack = subs.Stack
	s.peerStore = subs.PeerStore
	s.metrics = subs.Metrics
	s.basePolicy = subs.PolicyBase
	s.onHandshake = opts.OnHandshake
	s.onAck = opts.OnAck
	s.stopIBLT = subs.StopIBLT
	s.stopAntiReplay = subs.StopAntiReplay

	// Register handshake and gate with current state
	myhost.RegisterHandshake(s.h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: subs.HeadStr, StateHeight: subs.Height, ListenAddrs: hostAddrsStrings(s.h)}, s.basePolicy)
	myhost.RegisterHandshakeWithPeers(s.h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, ListenAddrs: hostAddrsStrings(s.h)}, s.basePolicy, func(max int) []peer.AddrInfo {
		infos, _ := s.peerStore.GetDialCandidates(max, 0, nil)
		return infos
	})

	// Dial maintenance loop
	startDialMaintenanceLoop(ctx, s.h, s.peerStore, s.basePolicy, s.metrics, dialLoopConfig{
		MinOutbound:      opts.MinOutbound,
		ClusterNodeCount: opts.ClusterNodeCount,
		PerIPDialLimit:   opts.PerIPDialLimit,
		DialTimeout:      opts.DialTimeout,
		OnHandshakeSuccess: func(pid peer.ID, res *myhost.HandshakeResult) {
			if opts.OnHandshake != nil {
				opts.OnHandshake(pid.String(), map[string]any{"direction": "outbound", "remote_height": res.RemoteStateHeight})
			}
			if opts.OnAck != nil {
				opts.OnAck(pid.String(), "ok")
			}
		},
		WG: &s.wg,
	})

	// Gossip loop
	startGossipLoop(ctx, s.h, s.basePolicy, func(pid peer.ID, res *myhost.HandshakeResult) {
		for _, info := range res.Learned {
			if info.ID == s.h.ID() {
				continue
			}
			_ = myhost.UpsertLearnedPeer(s.peerStore, s.basePolicy.AttackMitigation, info.ID, info.Addrs, 0, "gossip")
		}
		s.metrics.AddGossipLearned(len(res.Learned))
		if opts.OnHandshake != nil {
			opts.OnHandshake(pid.String(), map[string]any{"direction": "gossip", "remote_height": res.RemoteStateHeight})
		}
	}, &s.wg)

	// Control server
	addr, shutdown, err := ctrl.Start(ctx, s.h, s.stack, s.peerStore, s.metrics, func() { cancel() }, subs.DynamicRouter, subs.RepairProtocol, subs.Gateway, "")
	if err != nil {
		s.stack.Close()
		if s.dht != nil {
			_ = s.dht.Close()
		}
		_ = s.h.Close()
		cancel()
		return nil, err
	}
	s.controlAddr = addr
	s.controlShutdown = shutdown

	return s, nil
}

// Close shuts down the node in dependency order: it stops the control server,
// stops the catalog IBLT exchange loop and the anti-replay tracker, cancels
// the shared context (signaling all background goroutines to exit), waits
// for those goroutines to finish, closes the storage stack and DHT, and
// finally closes the libp2p host.
//
// Parameters:
//   - ctx (context.Context): passed through to the control server's shutdown; not otherwise used to bound this call.
//
// Returns:
//   - error: the result of closing the libp2p host; errors from the control server shutdown and DHT close are ignored.
func (s *service) Close(ctx context.Context) error {
	if s.controlShutdown != nil {
		_ = s.controlShutdown(ctx)
	}
	if s.stopIBLT != nil {
		s.stopIBLT()
	}
	if s.stopAntiReplay != nil {
		s.stopAntiReplay()
	}
	s.cancel()
	s.wg.Wait()
	s.stack.Close()
	if s.dht != nil {
		_ = s.dht.Close()
	}
	return s.h.Close()
}

// Status reports this node's peer ID, listen addresses, current state
// head/height, and a snapshot of its running metrics counters.
//
// Parameters:
//   - ctx (context.Context): used to read the current state head from the datastore.
//
// Returns:
//   - Status: the populated status snapshot.
//   - error: always nil; reserved for future use and interface-compatibility with Service.
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

// PutRaw stores data as a new block by POSTing it to this node's own local
// control server's /put endpoint (JSON body, base string-encoded data), then
// decodes the resulting CID from the response.
//
// Parameters:
//   - ctx (context.Context): accepted for interface compatibility; not used to bound the HTTP call (a fixed 15s client timeout is used instead).
//   - data ([]byte): the raw bytes to store as a block.
//
// Returns:
//   - string: the string-encoded CID of the stored block.
//   - int: the number of bytes stored (len(data)).
//   - error: non-nil if the HTTP request fails, returns non-200, or the response cannot be decoded.
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

// GetRawFrom fetches the block identified by cidStr from the given provider
// by POSTing the request to this node's own local control server's /get
// endpoint, then base64-decodes the returned data.
//
// Parameters:
//   - ctx (context.Context): accepted for interface compatibility; not used to bound the HTTP call directly (the client timeout is timeout+5s instead).
//   - providerAddr (string): the provider's multiaddr to fetch from.
//   - providerPeer (string): the provider's string-encoded peer ID.
//   - cidStr (string): the string-encoded CID to fetch.
//   - timeout (time.Duration): the fetch budget passed through to the control server; the HTTP client timeout is set to timeout+5s to allow for that budget plus overhead.
//
// Returns:
//   - []byte: the fetched block bytes.
//   - error: non-nil if the HTTP request fails, returns non-200, or the response cannot be decoded.
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

// ListImmediatePeerIDs returns the string-encoded peer IDs of all peers this
// node's libp2p network layer currently reports as connected, excluding this
// node's own ID. Unlike PutRaw/GetRawFrom, this reads directly from the local
// host rather than going through the control server.
//
// Parameters:
//   - ctx (context.Context): accepted for interface compatibility; not used.
//
// Returns:
//   - []string: string-encoded peer IDs of connected neighbors.
//   - error: always nil; reserved for future use and interface-compatibility with Service.
func (s *service) ListImmediatePeerIDs(ctx context.Context) ([]string, error) {
	var ids []string
	for _, pid := range s.h.Network().Peers() {
		if pid != s.h.ID() {
			ids = append(ids, pid.String())
		}
	}
	return ids, nil
}

// RestoreFromManifest submits a restore job (a set of CIDs to fetch) to this
// node's own local control server's /restore endpoint, then polls
// /restore/status until the job reports done or the poll deadline elapses.
// The poll deadline is timeout multiplied by len(cids)+1, so it scales with
// the size of the manifest.
//
// Parameters:
//   - ctx (context.Context): checked between polls; if canceled, the method returns immediately with ctx.Err().
//   - cids ([]string): string-encoded CIDs to fetch.
//   - concurrency (int): how many CIDs the restore job should fetch in parallel.
//   - timeout (time.Duration): per-job budget hint sent to the server; also used to size the client-side poll deadline.
//   - byteBudget (int64): optional cap on total bytes fetched by the job; 0 means no cap.
//
// Returns:
//   - RestoreStats: the OK/Failed/Bytes counts once the job reports done.
//   - error: non-nil if job submission fails, decoding fails, ctx is canceled, or the poll deadline is reached before the job finishes.
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
