// Purpose: Library entrypoint and CLI implementation for the symmetric node.

package node

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"time"

	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	bstore "github.com/ipfs/boxo/blockstore"
	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multiaddr"
	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	mygateway "github.com/nicktagliamonte/fall25_independentStudy/internal/gateway"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mypht "github.com/nicktagliamonte/fall25_independentStudy/internal/pht"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
	mytuplespace "github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

// stringSlice implements flag.Value to support repeatable string flags
// (e.g. --listen, --seed), accumulating one entry per occurrence of the flag.
type stringSlice []string

// String returns the flag.Value textual representation of s, used by the
// flag package when printing usage/defaults.
//
// Returns:
//   - string: the Go-syntax representation of the underlying []string.
func (s *stringSlice) String() string { return fmt.Sprint([]string(*s)) }

// Set appends v to s. It implements flag.Value.Set and is called once per
// occurrence of the flag on the command line, which is what makes the flag
// repeatable.
//
// Parameters:
//   - v (string): the flag value provided for this occurrence.
//
// Returns:
//   - error: always nil.
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// printBanner prints the node's peer ID and listen addresses to stdout in the
// CLI's standard "PeerID:"/"Addr:" line format.
//
// Parameters:
//   - hID (string): the string-encoded peer ID to print.
//   - addrs ([]string): the multiaddrs to print, one per line.
func printBanner(hID string, addrs []string) {
	fmt.Println("PeerID:", hID)
	for _, a := range addrs {
		fmt.Println("Addr:", a)
	}
}

// bestPublicIPv4 returns a best-guess public IPv4 address for this machine.
// It first scans local network interfaces for the first up, non-loopback
// IPv4 address that is not RFC1918 private and not link-local; if none is
// found, it falls back to fetchPublicIPv4 to query an external service for
// the machine's public egress IP.
//
// Returns:
//   - string: a public-looking IPv4 address, or "" if none could be determined.
func bestPublicIPv4() string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if (iface.Flags&net.FlagUp) == 0 || (iface.Flags&net.FlagLoopback) != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue // not IPv4
			}
			// skip RFC1918 and link-local
			if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}
			return ip.String()
		}
	}
	// Fallback: query external service to learn public egress IP
	if ip := fetchPublicIPv4(); ip != "" {
		return ip
	}
	return ""
}

// printDerivedPublicAddrs prints, for each addr containing an "/ip4/"
// component, a derived "Public Addr:" line with that component's IP replaced
// by the machine's detected public IPv4 (from bestPublicIPv4), keeping the
// rest of the multiaddr (port/transport) unchanged. This is purely a display
// helper: it does not change what the node actually listens on. If no public
// IPv4 can be determined, nothing is printed.
//
// Parameters:
//   - addrs ([]string): the host's listen multiaddrs to derive public-facing addresses from.
func printDerivedPublicAddrs(addrs []string) {
	pub := bestPublicIPv4()
	if pub == "" {
		return
	}
	for _, a := range addrs {
		if strings.Contains(a, "/ip4/") {
			parts := strings.SplitN(a, "/ip4/", 2)
			if len(parts) == 2 {
				remainder := parts[1]
				if i := strings.IndexByte(remainder, '/'); i >= 0 {
					remainder = remainder[i:]
				} else {
					remainder = ""
				}
				fmt.Println("Public Addr:", "/ip4/"+pub+remainder)
			}
		}
	}
}

// fetchPublicIPv4 contacts a small list of external HTTP services in order
// (api.ipify.org, then checkip.amazonaws.com), each with a 1.5s timeout,
// returning the first response that parses as a public (non-private,
// non-loopback) IPv4 address. Used as bestPublicIPv4's fallback when no
// suitable address is found on local interfaces.
//
// Returns:
//   - string: the discovered public IPv4 address, or "" if all endpoints fail or return an unusable address.
func fetchPublicIPv4() string {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	endpoints := []string{
		"https://api.ipify.org",         // plain text IPv4
		"https://checkip.amazonaws.com", // plain text IPv4
	}
	for _, url := range endpoints {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		s := strings.TrimSpace(string(b))
		ip := net.ParseIP(s)
		if ip != nil {
			ip4 := ip.To4()
			if ip4 != nil && !ip4.IsPrivate() && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	return ""
}

// hostAddrsStrings returns the string encoding of every multiaddr h is
// currently listening on/advertising.
//
// Parameters:
//   - h (host.Host): the libp2p host to read listen addresses from.
//
// Returns:
//   - []string: the string-encoded multiaddrs.
func hostAddrsStrings(h host.Host) []string {
	addrs := make([]string, 0, len(h.Addrs()))
	for _, a := range h.Addrs() {
		addrs = append(addrs, a.String())
	}
	return addrs
}

// minDuration returns the smaller of two durations.
//
// Parameters:
//   - a (time.Duration): first duration to compare.
//   - b (time.Duration): second duration to compare.
//
// Returns:
//   - time.Duration: whichever of a or b is smaller.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// dialWithTimeout attempts to connect h to info, bounding the attempt with a
// derived context that times out after d.
//
// Parameters:
//   - ctx (context.Context): parent context; a d-duration timeout is derived from it.
//   - h (host.Host): the libp2p host performing the dial.
//   - info (peer.AddrInfo): the peer and its addresses to connect to.
//   - d (time.Duration): the dial timeout.
//
// Returns:
//   - error: non-nil if the connection attempt fails or times out.
func dialWithTimeout(ctx context.Context, h host.Host, info peer.AddrInfo, d time.Duration) error {
	connectCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return h.Connect(connectCtx, info)
}

// getHandshakePolicyFromEnv builds a myhost.HandshakePolicy for the inline
// (non-daemon) CLI subcommands (put/connect/get/lookup-key), based on the
// SNG40_ENV/SNG40_CA_PUBS/SNG40_TOKEN environment variables surfaced by Run.
// pubs is parsed as a comma-separated list of base64-encoded 32-byte ed25519
// CA public keys; malformed or wrong-length entries are silently skipped.
// Credential enforcement (RequireCredential/AuthScheme/CAPubKeys/Token) is
// enabled if require is true, or if at least one valid CA pubkey was parsed
// and token is non-empty.
//
// Parameters:
//   - require (bool): forces RequireCredential on regardless of pubs/token (from SNG40_ENV).
//   - pubs (string): comma-separated base64-encoded 32-byte ed25519 CA public keys (from SNG40_CA_PUBS).
//   - token (string): the credential token to present/require (from SNG40_TOKEN).
//   - timeout (time.Duration): the handshake timeout to set on the returned policy.
//
// Returns:
//   - myhost.HandshakePolicy: the constructed policy, with MinAgentVersion "sng40/0.1.0" and all services allowed.
func getHandshakePolicyFromEnv(require bool, pubs string, token string, timeout time.Duration) myhost.HandshakePolicy {
	var caPubs [][]byte
	if pubs != "" {
		for _, s := range strings.Split(pubs, ",") {
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
			if err == nil && len(b) == 32 {
				caPubs = append(caPubs, b)
			}
		}
	}
	policyBase := myhost.HandshakePolicy{Timeout: timeout, MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0)}
	if require || (len(caPubs) > 0 && token != "") {
		policyBase.RequireCredential = true
		policyBase.AuthScheme = "token-ed25519-v1"
		policyBase.CAPubKeys = caPubs
		policyBase.Token = token
	}
	return policyBase
}

// Run is the entry point for the `node` CLI binary. It reads os.Args[1] as
// the subcommand and os.Args[2:] as that subcommand's flags, then dispatches:
//
//   - "run": starts a long-running node. Builds (or loads) a host identity,
//     an ephemeral or persistent blockstore, a peerstore seeded from
//     defaults/CLI/env/file, and a DHT-backed storage stack
//     (BuildStackWithDHT). Installs the handshake gate/responder, repair
//     protocol, gateway, connectivity monitor, periodic reannounce, catalog
//     IBLT exchange (InstallCatalogIBLT), peer pruning, connection-security
//     verification, outbound dial maintenance (targeting
//     effectiveOutboundTarget), and peer gossip as background goroutines.
//     Starts the HTTP control server and writes its address to the
//     --control file. With --daemon, instead re-execs itself in the
//     background (redirecting stdout/stderr to --log or a default log file)
//     and returns immediately. Blocks on ctx.Done() otherwise.
//   - "put": stores a block (from --data or --file). With --daemon, POSTs to
//     a running daemon's /put endpoint instead of running inline. In inline
//     mode, builds a throwaway host+stack, stores the block, updates the
//     routing table, and (with --serve) blocks forever serving inbound
//     requests while periodically checking connection security.
//   - "connect": dials a single peer by --addr/--peer, performing a
//     handshake and an opportunistic suffix sync if the peer is ahead in
//     state height. Delegates to a running daemon's /connect endpoint when
//     --daemon is set.
//   - "get": fetches a block by --cid from a specific --from-addr/--from-peer
//     provider, verifying it and optionally writing it to --out. Delegates
//     to a running daemon's /get endpoint when --daemon is set.
//   - "shutdown": sends a shutdown signal to a running daemon via its
//     control file.
//   - "restore": submits a restore job (a CID or file of CIDs) to a running
//     daemon's /restore endpoint with retries, then polls
//     /restore/status until done and prints a final metrics snapshot.
//   - "snapshot": proxies a running daemon's /snapshot endpoint (paginated
//     CID listing) to stdout.
//   - "neighbors": proxies a running daemon's /neighbors endpoint to stdout.
//   - "keygen": generates (or loads) a persistent libp2p private key at
//     --out and prints the derived peer ID.
//   - "lookup-key": runs a one-off, stateless DHT lookup (runLookupKey) for
//     a 64-hex-char key from a fresh node that only bootstraps against
//     --bootstrap, printing hop count/latency/found as JSON.
//
// Returns:
//   - error: a usage error if no subcommand (or an unknown one) is given, a flag-parsing/validation error, or any error from the dispatched subcommand's execution.
func Run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: %s <run|put|connect|get> [flags]", os.Args[0])
	}

	requireSNG40 := os.Getenv("SNG40_ENV") == "true"
	tokenSNG40 := os.Getenv("SNG40_TOKEN")
	pubsSNG40 := os.Getenv("SNG40_CA_PUBS")

	subcmd := os.Args[1]
	switch subcmd {
	// "run" starts a long-running node with the full background subsystem set.
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		var listenAddrs stringSlice
		var daemon bool
		var logPath string
		var controlPath string
		var keyPath string
		var storePath string
		var seedAddrs stringSlice
		var seedFile string
		var minOutbound int
		var clusterNodes int
		var dialTimeoutStr string
		var staleAgeStr string
		var maxFailures int
		var maxKnown int
		var perIPDialLimit int
		var indexShards int
		var disableBloomPruning bool
		var noDefaultBootstrap bool
		fs.Var(&listenAddrs, "listen", "multiaddr to listen on (repeatable)")
		fs.BoolVar(&daemon, "daemon", false, "run the node in the background and return immediately")
		fs.StringVar(&logPath, "log", "", "when backgrounding, write logs to this file (appended)")
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to write control endpoint info")
		fs.StringVar(&keyPath, "key", "", "path to persistent private key (optional)")
		fs.StringVar(&storePath, "store", "", "path to persistent blockstore (optional)")
		fs.Var(&seedAddrs, "seed", "seed peer multiaddr (repeatable)")
		fs.StringVar(&seedFile, "seed-file", "", "path to file with seed multiaddrs (one per line)")
		fs.IntVar(&minOutbound, "min-outbound", DefaultMinOutbound, "target minimum outbound peer connections (capped by --cluster-nodes or CLUSTER_NODE_COUNT or peerstore size)")
		fs.IntVar(&clusterNodes, "cluster-nodes", 0, "expected cluster size; caps min-outbound at N-1 (0 uses CLUSTER_NODE_COUNT env or peerstore)")
		fs.StringVar(&dialTimeoutStr, "dial-timeout", "10s", "dial timeout, e.g. 10s")
		fs.StringVar(&staleAgeStr, "stale-age", "24h", "consider peers stale after this duration")
		fs.IntVar(&maxFailures, "max-fail", 8, "evict peers after this many consecutive failures")
		fs.IntVar(&maxKnown, "max-known", 5000, "soft cap on tracked peers in PeerStore")
		fs.IntVar(&perIPDialLimit, "per-ip-dial-limit", 3, "maximum outbound dials per unique IP")
		fs.IntVar(&indexShards, "index-shards", mypht.DefaultShardCount, "number of independently owned PHT shards")
		fs.BoolVar(&disableBloomPruning, "disable-bloom-pruning", false, "disable Bloom pruning for controlled query ablation")
		fs.BoolVar(&noDefaultBootstrap, "no-default-bootstrap", false, "do not use public libp2p bootstrap peers (for explicitly bootstrapped private clusters)")
		_ = fs.Parse(os.Args[2:])
		if indexShards <= 0 {
			return errors.New("--index-shards must be positive")
		}
		if clusterNodes == 0 {
			if v := os.Getenv("CLUSTER_NODE_COUNT"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					clusterNodes = n
				}
			}
		}
		if len(listenAddrs) == 0 {
			listenAddrs = []string{
				"/ip4/0.0.0.0/tcp/2893",
				"/ip4/0.0.0.0/udp/2894/quic-v1",
			}
		}

		if daemon {
			// Re-exec ourselves and detach; propagate relevant flags
			childArgs := []string{"run"}
			for _, a := range listenAddrs {
				childArgs = append(childArgs, "--listen", a)
			}
			if logPath != "" {
				childArgs = append(childArgs, "--log", logPath)
			}
			if controlPath != "" {
				childArgs = append(childArgs, "--control", controlPath)
			}
			if keyPath != "" {
				childArgs = append(childArgs, "--key", keyPath)
			}
			if storePath != "" {
				childArgs = append(childArgs, "--store", storePath)
			}
			// seeds via repeated --seed flags
			for _, s := range seedAddrs {
				childArgs = append(childArgs, "--seed", s)
			}
			if seedFile != "" {
				childArgs = append(childArgs, "--seed-file", seedFile)
			}
			childArgs = append(childArgs, "--min-outbound", fmt.Sprintf("%d", minOutbound))
			if clusterNodes > 0 {
				childArgs = append(childArgs, "--cluster-nodes", fmt.Sprintf("%d", clusterNodes))
			}
			childArgs = append(childArgs, "--dial-timeout", dialTimeoutStr)
			childArgs = append(childArgs, "--stale-age", staleAgeStr)
			childArgs = append(childArgs, "--max-fail", fmt.Sprintf("%d", maxFailures))
			childArgs = append(childArgs, "--max-known", fmt.Sprintf("%d", maxKnown))
			childArgs = append(childArgs, "--per-ip-dial-limit", fmt.Sprintf("%d", perIPDialLimit))
			childArgs = append(childArgs, "--index-shards", fmt.Sprintf("%d", indexShards))
			if disableBloomPruning {
				childArgs = append(childArgs, "--disable-bloom-pruning")
			}
			if noDefaultBootstrap {
				childArgs = append(childArgs, "--no-default-bootstrap")
			}

			cmd := exec.Command(os.Args[0], childArgs...)
			// If a log path was provided, still attach child stdout/err to that file to catch early output.
			if logPath != "" {
				f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
				if err == nil {
					cmd.Stdout = f
					cmd.Stderr = f
				}
			} else {
				// Default background log file to avoid hijacking the current terminal
				_ = os.MkdirAll("/tmp/fall25_node", 0755)
				if f, err := os.OpenFile("/tmp/fall25_node/daemon.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
					cmd.Stdout = f
					cmd.Stderr = f
				}
			}
			cmd.Stdin = nil
			if err := cmd.Start(); err != nil {
				return err
			}
			fmt.Println("Started node in background. PID:", cmd.Process.Pid)
			return nil
		}

		ctx, cancelMain := context.WithCancel(context.Background())
		defer cancelMain()
		// Optional persistent key
		var h host.Host
		if keyPath != "" {
			priv, err := myhost.LoadOrCreatePrivateKey(keyPath)
			if err != nil {
				return err
			}
			h, err = myhost.NewHostWithPriv(ctx, listenAddrs, priv)
			if err != nil {
				return err
			}
		} else {
			var err error
			h, err = myhost.NewHost(ctx, listenAddrs)
			if err != nil {
				return err
			}
		}
		defer h.Close()

		// Datastore + blockstore (persistent or ephemeral)
		var bs bstore.Blockstore
		var datastore ds.Batching
		if storePath != "" {
			var err error
			bs, datastore, err = mystore.NewPersistentBlockstore(storePath)
			if err != nil {
				return err
			}
		} else {
			bs, datastore = mystore.NewEphemeralBlockstore()
		}

		// PeerStore (before DHT so we can bootstrap from seeds)
		peerStore, err := myhost.NewPeerStore(datastore)
		if err != nil {
			return err
		}
		if d, err := time.ParseDuration(staleAgeStr); err == nil {
			peerStore.SetPolicy(d, maxFailures)
		}
		if maxKnown > 0 {
			peerStore.SetMaxKnown(maxKnown)
		}

		// Seeds: DHT bootstrap + CLI/env/file
		var seeds []string
		if !noDefaultBootstrap {
			seeds = append(seeds, myhost.DefaultDHTBootstrapAddrs...)
		}
		seeds = append(seeds, seedAddrs...)
		if env := os.Getenv("SNG40_SEEDS"); env != "" {
			for _, s := range strings.Split(env, ",") {
				s = strings.TrimSpace(s)
				if s != "" {
					seeds = append(seeds, s)
				}
			}
		}
		if seedFile != "" {
			if b, err := os.ReadFile(seedFile); err == nil {
				for _, line := range strings.Split(string(b), "\n") {
					line = strings.TrimSpace(line)
					if line != "" && !strings.HasPrefix(line, "#") {
						seeds = append(seeds, line)
					}
				}
			}
		}
		seenSeeds := make(map[string]struct{})
		for _, s := range seeds {
			if s == "" {
				continue
			}
			if _, ok := seenSeeds[s]; ok {
				continue
			}
			seenSeeds[s] = struct{}{}
			maddr, err := multiaddr.NewMultiaddr(s)
			if err != nil {
				continue
			}
			if info, err := peer.AddrInfoFromP2pAddr(maddr); err == nil && info.ID != h.ID() {
				_ = peerStore.Upsert(info.ID, info.Addrs, 0, "seed")
			}
		}

		// Storage stack: DHT with DynamicRouter fallback
		stack, dht, dynamicRouter, err := BuildStackWithDHT(ctx, h, bs, datastore, peerStore, false)
		if err != nil {
			return err
		}
		defer func() {
			stack.Close()
			if dht != nil {
				_ = dht.Close()
			}
		}()

		policyBase := myhost.HandshakePolicy{MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0), Timeout: 10 * time.Second}
		stopAntiReplay := myhost.EnableAntiReplay(ctx, &policyBase)
		defer stopAntiReplay()
		_ = myhost.EnableAttackMitigation(ctx, &policyBase)

		// Install handshake responder and gate with state head/height
		head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		headStr := ""
		if head.Defined() {
			headStr = head.String()
		}
		handshakeGate := myhost.InstallHandshakeGateWithCallback(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, policyBase, func(pid peer.ID) {
			_, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String())
		})

		// Metrics
		metrics := &ctrl.NodeMetrics{}
		providerRecords := mystore.NewLocalProviderRecords()
		providerRecords.AddAllFromDatastore(ctx, stack.Datastore)
		stack.ProviderRecords = providerRecords
		aq := mystore.NewAnnounceQueue()
		stack.AnnounceQueue = aq

		// Create repair protocol and gateway using DHT tuple space (before recovery callback setup)
		var repairProtocol *mystore.RepairProtocol
		var gateway *mygateway.Gateway
		if dht != nil {
			dhtAdapter := mytuplespace.NewDHTValueStoreAdapter(dht)
			dhtTS := mytuplespace.NewDHTTupleSpace(dhtAdapter)

			var baseTS mytuplespace.TupleSpace = dhtTS
			if ownerResolver, err := mytuplespace.NewDHTTupleOwnerResolver(h.ID(), dht); err == nil {
				ownerResolver.SetMinimumCandidates(ownerElectionCandidateMinimum(clusterNodes))
				if nativeTS, err := mytuplespace.NewDistributedTupleSpace(h, ownerResolver); err == nil {
					nativeTS.SetRequireVerifiedPeers(true)
					baseTS = nativeTS
					if shardStores, err := mypht.NewShardStores(dhtAdapter, indexShards); err == nil {
						if indexCoordinator, err := mytuplespace.NewIndexCoordinator(h, ownerResolver, shardStores); err == nil {
							indexCoordinator.SetRequireVerifiedPeers(true)
							if indexedTS, err := mytuplespace.NewIndexedTupleSpace(nativeTS, shardStores, indexCoordinator); err == nil {
								indexedTS.SetBloomPruning(!disableBloomPruning)
								baseTS = indexedTS
							}
						}
					}
				}
			}
			if tshAddr := os.Getenv("TSH_ADDR"); tshAddr != "" {
				// Legacy compatibility only. The default production path uses
				// the repository-native DistributedTupleSpace above.
				p2pTS := mytuplespace.NewP2PTupleSpace(tshAddr, 0x7f000001, "sng40")
				p2pTS.SetPermissionChecker(myhost.NewHandshakePermissionChecker(policyBase))
				router := mytuplespace.NewRouter(dhtTS, p2pTS, nil)
				baseTS = router
			}
			repairProtocol = mystore.NewRepairProtocol(stack, h, baseTS, false) // tokenized: false for daemon mode
			tokenTS := mytuplespace.NewTokenFallbackTupleSpace(dhtAdapter, baseTS)
			gateway = mygateway.NewGateway(stack.Router, tokenTS)
			if ts := gateway.TokenStore(); ts != nil {
				stack.TokenStore = ts
			}
		}
		if repairProtocol != nil {
			repairProtocol.StartAdvertisingStorageAvailability(ctx)
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
		stopIBLT := InstallCatalogIBLT(ctx, h, stack)
		defer stopIBLT()
		// Periodic pruning of stale or failing peers
		go func() {
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
		go func() {
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

		// Register handshake responder for inbound peers with state summary AND peer sample
		// This combines state head/height with peer discovery functionality
		// Peer provider returns connected peers from network (not just peerstore) so new nodes learn about each other
		myhost.RegisterHandshakeWithPeersAndCallback(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height, ListenAddrs: hostAddrsStrings(h)}, policyBase, func(max int) []peer.AddrInfo {
			// Return connected peers from network (these are the peers we actually know about)
			connectedPeers := h.Network().Peers()
			infos := make([]peer.AddrInfo, 0, max)
			for _, pid := range connectedPeers {
				if pid == h.ID() {
					continue
				}
				if len(infos) >= max {
					break
				}
				addrs := h.Peerstore().Addrs(pid)
				if len(addrs) > 0 {
					infos = append(infos, peer.AddrInfo{ID: pid, Addrs: addrs})
				}
			}
			// If we don't have enough connected peers, supplement with peerstore candidates
			if len(infos) < max {
				cands, _ := peerStore.GetDialCandidates(max-len(infos), 0, nil)
				// Filter out already included peers
				seen := make(map[peer.ID]bool)
				for _, info := range infos {
					seen[info.ID] = true
				}
				for _, cand := range cands {
					if !seen[cand.ID] && len(infos) < max {
						infos = append(infos, cand)
					}
				}
			}
			return infos
		}, handshakeGate.MarkVerified)

		// Dialer loop: maintain minOutbound connections with backoff
		dialTimeout, err := time.ParseDuration(dialTimeoutStr)
		if err != nil {
			return err
		}
		go func() {
			backoffBase := time.Second
			maxBackoff := 5 * time.Minute
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				// Count current outbound connections
				conns := h.Network().Conns()
				outbound := 0
				exclude := make(map[peer.ID]bool)
				for _, c := range conns {
					if c.Stat().Direction == network.DirOutbound {
						outbound++
					}
					exclude[c.RemotePeer()] = true
				}
				target := effectiveOutboundTarget(minOutbound, clusterNodes, peerStore.CountKnownPeersWithAddrs(h.ID()))
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
							if perIP[v] >= perIPDialLimit {
								continue
							}
							perIP[v]++
							break
						}
						if v, err := a.ValueForProtocol(multiaddr.P_IP6); err == nil && v != "" {
							if perIP[v] >= perIPDialLimit {
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
					ctxDial, cancel := context.WithTimeout(ctx, dialTimeout)
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
					pol.Timeout = dialTimeout
					if res, err := myhost.PerformHandshakeWithState(context.Background(), h, pid, pol, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, WantPeerlist: true, ListenAddrs: hostAddrsStrings(h)}); err == nil {
						handshakeGate.MarkVerified(pid)
						// advance state head for this peer (best effort)
						if _, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String()); true {
							// no-op
						}
						learnedToConnect := make([]peer.AddrInfo, 0)
						for _, info2 := range res.Learned {
							if info2.ID == h.ID() {
								continue
							}
							_ = myhost.UpsertLearnedPeer(peerStore, policyBase.AttackMitigation, info2.ID, info2.Addrs, 0, "handshake")
							// Collect a small subset for immediate connection (cap at 2 per handshake)
							if len(learnedToConnect) < 2 {
								// Check if already connected
								if h.Network().Connectedness(info2.ID) != network.Connected {
									learnedToConnect = append(learnedToConnect, info2)
								}
							}
						}
						// Connect to learned peers (bounded, non-blocking)
						for _, info2 := range learnedToConnect {
							if am := policyBase.AttackMitigation; am != nil {
								if am.BanList.IsBanned(info2.ID) {
									continue
								}
								if ok, _ := am.Eclipse.CanAllow(ctx, info2.ID, info2.Addrs); !ok {
									continue
								}
							}
							_ = peerStore.RecordDialAttempt(info2.ID)
							metrics.IncDialsAttempted()
							ctxDial, cancel := context.WithTimeout(ctx, dialTimeout)
							err := h.Connect(ctxDial, info2)
							cancel()
							if err != nil {
								_ = peerStore.RecordDialFailure(info2.ID)
								metrics.IncDialsFailed()
							} else {
								_ = peerStore.RecordDialSuccess(info2.ID)
								metrics.IncDialsSucceeded()
							}
						}
						// If remote height is ahead, attempt suffix sync with budget
						if res.RemoteStateHeight > height {
							remoteHead, err := cid.Decode(res.RemoteStateHead)
							if err == nil {
								_, _, _, _ = mystore.SyncSuffix(context.Background(), stack.Datastore, stack.BlockSvc, remoteHead, res.RemoteStateHeight, mystore.SyncOptions{MaxDepth: 512, MaxBlockBytes: 1 << 20, Timeout: 5 * time.Second})
							}
						}
					}
					// if we've satisfied outbound, break
					outbound++
					if outbound >= effectiveOutboundTarget(minOutbound, clusterNodes, peerStore.CountKnownPeersWithAddrs(h.ID())) {
						break
					}
				}
				// small pause before next maintenance iteration
				time.Sleep(2 * time.Second)
			}
		}()

		// Gossip timer: periodically pull peer samples from connected peers
		go func() {
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
							handshakeGate.MarkVerified(pid)
							if _, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String()); true {
							}
							for _, info := range res.Learned {
								if info.ID == h.ID() {
									continue
								}
								_ = myhost.UpsertLearnedPeer(peerStore, policyBase.AttackMitigation, info.ID, info.Addrs, 0, "gossip")
							}
							metrics.AddGossipLearned(len(res.Learned))
							// Try suffix sync if remote is ahead
							if res.RemoteStateHeight > height {
								if remoteHead, err := cid.Decode(res.RemoteStateHead); err == nil {
									_, _, _, _ = mystore.SyncSuffix(context.Background(), stack.Datastore, stack.BlockSvc, remoteHead, res.RemoteStateHeight, mystore.SyncOptions{MaxDepth: 512, MaxBlockBytes: 1 << 20, Timeout: 5 * time.Second})
								}
							}
						}
					}
				}
			}
		}()

		// Wire message metrics for P2P message counting (put, get, lookup)
		stack.MessageSink = ctrl.NodeMetricsMessageSink(metrics)
		stack.HopSink = ctrl.NodeMetricsHopSink(metrics)

		// Start control server and write daemon file
		addr, _, err := ctrl.Start(ctx, h, stack, peerStore, metrics, func() {
			// trigger graceful stop
			cancelMain()
		}, dynamicRouter, repairProtocol, gateway, storePath)
		if err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Dir(controlPath), 0755)
		f, err := os.Create(controlPath)
		if err == nil {
			type daemonInfo struct {
				Addr string `json:"addr"`
			}
			_ = json.NewEncoder(f).Encode(daemonInfo{Addr: addr})
			_ = f.Close()
		}

		addrs := hostAddrsStrings(h)
		printBanner(h.ID().String(), addrs)
		printDerivedPublicAddrs(addrs)

		<-ctx.Done()
		return nil

	// "put" stores a block from --data or --file, optionally via a running daemon.
	case "put":
		fs := flag.NewFlagSet("put", flag.ExitOnError)
		var listenAddrs stringSlice
		var data string
		var filePath string
		var serve bool
		var controlPath string
		var daemon bool
		var httpDebug string
		fs.Var(&listenAddrs, "listen", "multiaddr to listen on (repeatable)")
		fs.StringVar(&data, "data", "", "inline data to store as a block")
		fs.StringVar(&filePath, "file", "", "path to file to store as a block")
		fs.BoolVar(&serve, "serve", false, "keep node running to serve inbound wants")
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		fs.BoolVar(&daemon, "daemon", false, "use a running daemon at --control instead of inline")
		fs.StringVar(&httpDebug, "http-debug", "", "optional host:port to serve /cid/<cid> debug handler")
		_ = fs.Parse(os.Args[2:])
		if len(listenAddrs) == 0 {
			listenAddrs = []string{
				"/ip4/0.0.0.0/tcp/2893",
				"/ip4/0.0.0.0/udp/2894/quic-v1",
			}
		}

		if data == "" && filePath == "" {
			return fmt.Errorf("put: either --data or --file is required")
		}
		if data != "" && filePath != "" {
			return fmt.Errorf("put: specify only one of --data or --file")
		}

		var payload []byte
		if filePath != "" {
			f, err := os.Open(filePath)
			if err != nil {
				return err
			}
			defer f.Close()
			b, err := io.ReadAll(f)
			if err != nil {
				return err
			}
			payload = b
		} else {
			payload = []byte(data)
		}

		ctx := context.Background()

		// If --daemon, use running daemon at controlPath
		if daemon {
			if b, err := os.ReadFile(controlPath); err == nil && len(b) > 0 {
				var info struct {
					Addr string `json:"addr"`
				}
				if json.Unmarshal(b, &info) == nil && info.Addr != "" {
					// send HTTP request to daemon
					client := &http.Client{Timeout: 15 * time.Second}
					var reqBody = struct {
						Data string `json:"data"`
					}{Data: string(payload)}
					buf, _ := json.Marshal(reqBody)
					resp, err := client.Post("http://"+info.Addr+"/put", "application/json", bytes.NewReader(buf))
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						body, _ := io.ReadAll(resp.Body)
						return fmt.Errorf("daemon put failed: %s", string(body))
					}
					var out struct {
						CID          string `json:"cid"`
						MultihashHex string `json:"multihash_hex"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
						return err
					}
					fmt.Println("CID:", out.CID)
					fmt.Printf("CID (multihash hex): %s\n", out.MultihashHex)
					return nil
				}
			}
		}
		// Inline mode
		h, err := myhost.NewHost(ctx, listenAddrs)
		if err != nil {
			return err
		}
		defer h.Close()

		// Install handshake hooks for inline mode.
		policyBase := getHandshakePolicyFromEnv(requireSNG40, pubsSNG40, tokenSNG40, 10*time.Second)
		stopAntiReplay := myhost.EnableAntiReplay(ctx, &policyBase)
		defer stopAntiReplay()
		_ = myhost.EnableAttackMitigation(ctx, &policyBase)

		stack, err := mystore.NewStack(ctx, h)
		if err != nil {
			return err
		}
		defer stack.Close()

		// Now register handshake with current state head/height (after stack is ready)
		head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		headStr := ""
		if head.Defined() {
			headStr = head.String()
		}
		myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height}, policyBase)
		_ = myhost.InstallHandshakeGateWithCallback(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, policyBase, func(pid peer.ID) {
			_, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String())
		})

		lockOpts := (*mystore.PutLockOpts)(nil)
		if stack.KeyLockManager != nil && stack.Host != nil {
			lockOpts = &mystore.PutLockOpts{Manager: stack.KeyLockManager, Holder: stack.Host.ID()}
		}
		key, c, err := mystore.PutRawBlockIndexed(ctx, stack.Datastore, stack.BlockSvc, payload, lockOpts)
		if err != nil {
			return err
		}
		// Auto-update routing table with replication vector on Put
		stack.UpdateRoutingTableOnPut(key, h.ID(), nil, c) // nil = use default replication vector

		fmt.Println("CID:", c.String())
		fmt.Printf("CID (multihash hex): %s\n", hex.EncodeToString(c.Hash()))

		addrs2 := hostAddrsStrings(h)
		printBanner(h.ID().String(), addrs2)
		printDerivedPublicAddrs(addrs2)

		if serve {
			go func() {
				t := time.NewTicker(5 * time.Minute)
				defer t.Stop()
				for range t.C {
					if err := myhost.VerifyECDHKeyDerivationUsed(h); err != nil {
						log.Printf("connection security: %v", err)
					}
					if err := myhost.EnsureAllTrafficEncrypted(h); err != nil {
						log.Printf("connection security: %v", err)
					}
				}
			}()
			select {}
		}
		return nil

	// "connect" dials a single peer explicitly by address and peer ID.
	case "connect":
		fs := flag.NewFlagSet("connect", flag.ExitOnError)
		var listenAddrs stringSlice
		var addr string
		var peerIDStr string
		var timeoutStr string
		var controlPath string
		var daemon bool
		fs.Var(&listenAddrs, "listen", "multiaddr to listen on (repeatable)")
		fs.StringVar(&addr, "addr", "", "remote peer multiaddr")
		fs.StringVar(&peerIDStr, "peer", "", "remote peer ID")
		fs.StringVar(&timeoutStr, "timeout", "10s", "dial timeout (e.g., 10s)")
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		fs.BoolVar(&daemon, "daemon", false, "use a running daemon at --control instead of inline")
		_ = fs.Parse(os.Args[2:])
		if len(listenAddrs) == 0 {
			listenAddrs = []string{
				"/ip4/0.0.0.0/tcp/2893",
				"/ip4/0.0.0.0/udp/2894/quic-v1",
			}
		}
		if addr == "" || peerIDStr == "" {
			return fmt.Errorf("connect: --addr and --peer are required")
		}
		dur, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return err
		}

		ctx := context.Background()

		// If --daemon, prefer daemon
		if daemon {
			if b, err := os.ReadFile(controlPath); err == nil && len(b) > 0 {
				var info struct {
					Addr string `json:"addr"`
				}
				if json.Unmarshal(b, &info) == nil && info.Addr != "" {
					var reqBody = struct {
						Addr    string `json:"addr"`
						Peer    string `json:"peer"`
						Timeout string `json:"timeout"`
					}{Addr: addr, Peer: peerIDStr, Timeout: timeoutStr}
					buf, _ := json.Marshal(reqBody)
					resp, err := http.Post("http://"+info.Addr+"/connect", "application/json", bytes.NewReader(buf))
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						body, _ := io.ReadAll(resp.Body)
						return fmt.Errorf("daemon connect failed: %s", string(body))
					}
					fmt.Println("Connected via daemon to:", peerIDStr)
					return nil
				}
			}
		}
		h, err := myhost.NewHost(ctx, listenAddrs)
		if err != nil {
			return err
		}
		defer h.Close()

		// Install handshake hooks for inline connect mode.
		policyBase := getHandshakePolicyFromEnv(requireSNG40, pubsSNG40, tokenSNG40, dur)
		stopAntiReplay := myhost.EnableAntiReplay(ctx, &policyBase)
		defer stopAntiReplay()
		_ = myhost.EnableAttackMitigation(ctx, &policyBase)

		stack, err := mystore.NewStack(ctx, h)
		if err != nil {
			return err
		}
		defer stack.Close()

		head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		headStr := ""
		if head.Defined() {
			headStr = head.String()
		}
		myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height}, policyBase)
		handshakeGate := myhost.InstallHandshakeGateWithCallback(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, policyBase, func(pid peer.ID) {
			_, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String())
		})

		maddr, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			return err
		}
		pid, err := peer.Decode(peerIDStr)
		if err != nil {
			return err
		}
		info := peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{maddr}}

		if err := dialWithTimeout(ctx, h, info, dur); err != nil {
			return err
		}

		// optional: register handshake responder for inbound peers
		myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, policyBase)
		// initiator-side handshake to validate remote
		policy := policyBase
		policy.Timeout = dur
		local := myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}
		res, err := myhost.PerformHandshakeWithState(ctx, h, pid, policy, local)
		if err != nil {
			return err
		}
		handshakeGate.MarkVerified(pid)
		// advance local state for the explicitly connected peer
		if _, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String()); true {
		}
		// If remote advertised higher height, try a short suffix sync
		if res.RemoteStateHeight > height {
			if remoteHead, err := cid.Decode(res.RemoteStateHead); err == nil {
				_, _, _, _ = mystore.SyncSuffix(context.Background(), stack.Datastore, stack.BlockSvc, remoteHead, res.RemoteStateHeight, mystore.SyncOptions{MaxDepth: 512, MaxBlockBytes: 1 << 20, Timeout: dur})
			}
		}

		if err := myhost.VerifyECDHKeyDerivationUsed(h); err != nil {
			log.Printf("connection security: %v", err)
		}
		if err := myhost.EnsureAllTrafficEncrypted(h); err != nil {
			log.Printf("connection security: %v", err)
		}
		fmt.Println("Connected to:", pid)
		for _, a := range h.Addrs() {
			fmt.Println("Our Addr:", a.String())
		}
		return nil

	// "get" fetches a single block from a specific known provider.
	case "get":
		fs := flag.NewFlagSet("get", flag.ExitOnError)
		var listenAddrs stringSlice
		var cidStr string
		var fromAddr string
		var fromPeer string
		var timeoutStr string
		var controlPath string
		var daemon bool
		var outFile string
		fs.Var(&listenAddrs, "listen", "multiaddr to listen on (repeatable)")
		fs.StringVar(&cidStr, "cid", "", "content ID to fetch")
		fs.StringVar(&fromAddr, "from-addr", "", "provider multiaddr")
		fs.StringVar(&fromPeer, "from-peer", "", "provider peer ID")
		fs.StringVar(&timeoutStr, "timeout", "20s", "fetch timeout (e.g., 20s)")
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		fs.BoolVar(&daemon, "daemon", false, "use a running daemon at --control instead of inline")
		fs.StringVar(&outFile, "out", "", "write fetched bytes to this file (optional)")
		_ = fs.Parse(os.Args[2:])
		if len(listenAddrs) == 0 {
			listenAddrs = []string{
				"/ip4/0.0.0.0/tcp/2893",
				"/ip4/0.0.0.0/udp/2894/quic-v1",
			}
		}
		if cidStr == "" || fromAddr == "" || fromPeer == "" {
			return fmt.Errorf("get: --cid, --from-addr, and --from-peer are required")
		}
		dur, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return err
		}

		ctx := context.Background()

		// If --daemon, prefer daemon
		if daemon {
			if b, err := os.ReadFile(controlPath); err == nil && len(b) > 0 {
				var info struct {
					Addr string `json:"addr"`
				}
				if json.Unmarshal(b, &info) == nil && info.Addr != "" {
					var reqBody = struct {
						CID     string `json:"cid"`
						Addr    string `json:"from_addr"`
						Peer    string `json:"from_peer"`
						Timeout string `json:"timeout"`
					}{CID: cidStr, Addr: fromAddr, Peer: fromPeer, Timeout: timeoutStr}
					buf, _ := json.Marshal(reqBody)
					resp, err := http.Post("http://"+info.Addr+"/get", "application/json", bytes.NewReader(buf))
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						body, _ := io.ReadAll(resp.Body)
						return fmt.Errorf("daemon get failed: %s", string(body))
					}
					var out struct {
						Bytes   int    `json:"bytes"`
						DataB64 string `json:"data_b64"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
						return err
					}
					decoded, err := base64.StdEncoding.DecodeString(out.DataB64)
					if err != nil {
						return err
					}
					if outFile != "" {
						if err := os.WriteFile(outFile, decoded, 0644); err != nil {
							return err
						}
						fmt.Printf("Fetched %d bytes -> %s\n", len(decoded), outFile)
					} else {
						fmt.Printf("Fetched %d bytes\n", len(decoded))
					}
					return nil
				}
			}
		}
		h, err := myhost.NewHost(ctx, listenAddrs)
		if err != nil {
			return err
		}
		defer h.Close()

		// Install handshake hooks for inline get mode.
		policyBase := getHandshakePolicyFromEnv(requireSNG40, pubsSNG40, tokenSNG40, dur)
		stopAntiReplay := myhost.EnableAntiReplay(ctx, &policyBase)
		defer stopAntiReplay()
		_ = myhost.EnableAttackMitigation(ctx, &policyBase)

		// stack is created below; handshake registration with state must occur after

		maddr, err := multiaddr.NewMultiaddr(fromAddr)
		if err != nil {
			return err
		}
		pid, err := peer.Decode(fromPeer)
		if err != nil {
			return err
		}
		info := peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{maddr}}

		staticRouter := &staticContentRouter{provider: info}
		stack, err := mystore.NewStackWithRouter(ctx, h, staticRouter)
		if err != nil {
			return err
		}
		defer stack.Close()

		// Now that stack exists, register handshake with current state and install gate
		head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		headStr := ""
		if head.Defined() {
			headStr = head.String()
		}
		myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height}, policyBase)
		_ = myhost.InstallHandshakeGateWithCallback(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, policyBase, func(pid peer.ID) {
			_, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String())
		})

		// Use the minimum of default dial (10s) and fetch timeout to avoid exceeding fetch budget
		dialDur := minDuration(dur, 10*time.Second)
		if err := dialWithTimeout(ctx, h, info, dialDur); err != nil {
			return err
		}

		c, err := cid.Decode(cidStr)
		if err != nil {
			return err
		}

		fetchCtx, cancel2 := context.WithTimeout(ctx, dur)
		defer cancel2()
		b, err := mystore.GetBlockIndexed(fetchCtx, stack.Datastore, stack.BlockSvc, c)
		if err != nil {
			return err
		}
		if err := myhost.VerifyECDHKeyDerivationUsed(h); err != nil {
			log.Printf("connection security: %v", err)
		}
		if err := myhost.EnsureAllTrafficEncrypted(h); err != nil {
			log.Printf("connection security: %v", err)
		}
		if outFile != "" {
			if err := os.WriteFile(outFile, b, 0644); err != nil {
				return err
			}
			fmt.Printf("Fetched %d bytes -> %s\n", len(b), outFile)
		} else {
			fmt.Printf("Fetched %d bytes\n", len(b))
		}
		return nil

	// "shutdown" signals a running daemon to stop gracefully.
	case "shutdown":
		fs := flag.NewFlagSet("shutdown", flag.ExitOnError)
		var controlPath string
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		_ = fs.Parse(os.Args[2:])
		b, err := os.ReadFile(controlPath)
		if err != nil || len(b) == 0 {
			return fmt.Errorf("cannot read control file: %v", err)
		}
		var info struct {
			Addr string `json:"addr"`
		}
		if err := json.Unmarshal(b, &info); err != nil || info.Addr == "" {
			return fmt.Errorf("invalid control file")
		}
		resp, err := http.Get("http://" + info.Addr + "/shutdown")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("shutdown failed: %s", string(body))
		}
		fmt.Println("Shutdown signal sent.")
		return nil

	// "restore" submits and polls a bulk CID restore job on a running daemon.
	case "restore":
		fs := flag.NewFlagSet("restore", flag.ExitOnError)
		var manifest string
		var concurrency int
		var timeoutStr string
		var controlPath string
		fs.StringVar(&manifest, "manifest", "", "path to file with CIDs (one per line) or a single CID")
		fs.IntVar(&concurrency, "concurrency", 4, "parallel fetches")
		fs.StringVar(&timeoutStr, "timeout", "20s", "per-CID timeout (e.g., 20s)")
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		_ = fs.Parse(os.Args[2:])
		if manifest == "" {
			return fmt.Errorf("restore: --manifest is required")
		}
		// read manifest
		var cids []string
		if fi, err := os.Stat(manifest); err == nil && !fi.IsDir() {
			b, err := os.ReadFile(manifest)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				cids = append(cids, line)
			}
		} else {
			cids = []string{manifest}
		}
		b, err := os.ReadFile(controlPath)
		if err != nil || len(b) == 0 {
			return fmt.Errorf("cannot read control file: %v", err)
		}
		var info struct {
			Addr string `json:"addr"`
		}
		if err := json.Unmarshal(b, &info); err != nil || info.Addr == "" {
			return fmt.Errorf("invalid control file")
		}
		// submit restore job with retries
		reqBody := struct {
			CIDs        []string `json:"cids"`
			Concurrency int      `json:"concurrency"`
			Timeout     string   `json:"timeout"`
			ByteBudget  int64    `json:"byte_budget"`
		}{CIDs: cids, Concurrency: concurrency, Timeout: timeoutStr, ByteBudget: 0}
		buf, _ := json.Marshal(&reqBody)
		var out struct {
			Job string `json:"job"`
		}
		maxRetries := 3
		retryDelay := 2 * time.Second
		for attempt := 1; attempt <= maxRetries; attempt++ {
			resp, err := http.Post("http://"+info.Addr+"/restore", "application/json", bytes.NewReader(buf))
			if err != nil {
				if attempt < maxRetries {
					fmt.Printf("Submit attempt %d failed: %v, retrying in %v...\n", attempt, err, retryDelay)
					time.Sleep(retryDelay)
					retryDelay *= 2
					continue
				}
				return fmt.Errorf("restore submit failed after %d attempts: %v", maxRetries, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				body, _ := io.ReadAll(resp.Body)
				if attempt < maxRetries {
					fmt.Printf("Submit attempt %d failed (status %d): %s, retrying in %v...\n", attempt, resp.StatusCode, string(body), retryDelay)
					time.Sleep(retryDelay)
					retryDelay *= 2
					continue
				}
				return fmt.Errorf("restore submit failed after %d attempts: %s", maxRetries, string(body))
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				if attempt < maxRetries {
					fmt.Printf("Submit attempt %d failed to decode response: %v, retrying in %v...\n", attempt, err, retryDelay)
					time.Sleep(retryDelay)
					retryDelay *= 2
					continue
				}
				return err
			}
			break
		}
		if out.Job == "" {
			return fmt.Errorf("restore submit failed: no job ID returned")
		}
		fmt.Println("Restore job:", out.Job)
		// poll status until done with timeout
		client := &http.Client{Timeout: 5 * time.Second}
		pollTimeout := 5 * time.Minute
		pollStart := time.Now()
		for {
			if time.Since(pollStart) > pollTimeout {
				return fmt.Errorf("restore poll timeout after %v", pollTimeout)
			}
			time.Sleep(1 * time.Second)
			u := "http://" + info.Addr + "/restore/status?id=" + out.Job
			r2, err := client.Get(u)
			if err != nil {
				continue
			}
			var st struct {
				OK     int   `json:"ok"`
				Failed int   `json:"failed"`
				Bytes  int64 `json:"bytes"`
				Done   bool  `json:"done"`
			}
			if json.NewDecoder(r2.Body).Decode(&st) == nil {
				fmt.Printf("status: ok=%d failed=%d bytes=%d done=%v\r", st.OK, st.Failed, st.Bytes, st.Done)
				if st.Done {
					fmt.Println()
					_ = r2.Body.Close()
					// Emit metrics snapshot after completion
					metricsResp, err := http.Get("http://" + info.Addr + "/metrics")
					if err == nil {
						var metrics map[string]interface{}
						if json.NewDecoder(metricsResp.Body).Decode(&metrics) == nil {
							fmt.Println("Final metrics:")
							fmt.Printf("  dials_attempted: %v\n", metrics["dials_attempted"])
							fmt.Printf("  dials_succeeded: %v\n", metrics["dials_succeeded"])
							fmt.Printf("  dials_failed: %v\n", metrics["dials_failed"])
							fmt.Printf("  restores_started: %v\n", metrics["restores_started"])
							fmt.Printf("  restores_ok: %v\n", metrics["restores_ok"])
							fmt.Printf("  restores_failed: %v\n", metrics["restores_failed"])
							fmt.Printf("  restore_bytes: %v\n", metrics["restore_bytes"])
						}
						_ = metricsResp.Body.Close()
					}
					break
				}
			}
			_ = r2.Body.Close()
		}
		return nil

	// "snapshot" proxies a running daemon's paginated CID listing to stdout.
	case "snapshot":
		fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
		var controlPath string
		var limit int
		var cursor string
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		fs.IntVar(&limit, "limit", 1000, "max CIDs to return")
		fs.StringVar(&cursor, "cursor", "", "optional cursor for pagination (startAfter)")
		_ = fs.Parse(os.Args[2:])
		b, err := os.ReadFile(controlPath)
		if err != nil || len(b) == 0 {
			return fmt.Errorf("cannot read control file: %v", err)
		}
		var info struct {
			Addr string `json:"addr"`
		}
		if err := json.Unmarshal(b, &info); err != nil || info.Addr == "" {
			return fmt.Errorf("invalid control file")
		}
		url := fmt.Sprintf("http://%s/snapshot?limit=%d", info.Addr, limit)
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		resp, err := http.Get(url)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("snapshot failed: %s", string(body))
		}
		_, err = io.Copy(os.Stdout, resp.Body)
		return err

	// "neighbors" proxies a running daemon's connected-peer listing to stdout.
	case "neighbors":
		fs := flag.NewFlagSet("neighbors", flag.ExitOnError)
		var controlPath string
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		_ = fs.Parse(os.Args[2:])
		b, err := os.ReadFile(controlPath)
		if err != nil || len(b) == 0 {
			return fmt.Errorf("cannot read control file: %v", err)
		}
		var info struct {
			Addr string `json:"addr"`
		}
		if err := json.Unmarshal(b, &info); err != nil || info.Addr == "" {
			return fmt.Errorf("invalid control file")
		}
		resp, err := http.Get("http://" + info.Addr + "/neighbors")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("neighbors failed: %s", string(body))
		}
		_, err = io.Copy(os.Stdout, resp.Body)
		return err

	// "keygen" generates or loads a persistent libp2p private key.
	case "keygen":
		fs := flag.NewFlagSet("keygen", flag.ExitOnError)
		var outPath string
		fs.StringVar(&outPath, "out", "", "path to write libp2p private key (PEM)")
		_ = fs.Parse(os.Args[2:])
		if outPath == "" {
			return fmt.Errorf("keygen: --out is required")
		}
		priv, err := myhost.LoadOrCreatePrivateKey(outPath)
		if err != nil {
			return err
		}
		pub := priv.GetPublic()
		pid, err := peer.IDFromPublicKey(pub)
		if err != nil {
			return err
		}
		fmt.Println("Wrote key:", outPath)
		fmt.Println("PeerID:", pid.String())
		return nil

	// "lookup-key" performs a one-off, stateless DHT lookup from a fresh node.
	case "lookup-key":
		fs := flag.NewFlagSet("lookup-key", flag.ExitOnError)
		var bootstrapAddr string
		var keyHex string
		var timeoutStr string
		fs.StringVar(&bootstrapAddr, "bootstrap", "", "bootstrap peer multiaddr(s), comma-separated (extra peers speed cold RT fill)")
		fs.StringVar(&keyHex, "key", "", "key (64 hex chars) to lookup")
		fs.StringVar(&timeoutStr, "timeout", "30s", "max duration for GetToken lookup only (connect and DHT bootstrap use separate budgets)")
		_ = fs.Parse(os.Args[2:])
		if bootstrapAddr == "" || keyHex == "" {
			return fmt.Errorf("lookup-key: --bootstrap and --key are required")
		}
		timeout, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return fmt.Errorf("lookup-key: invalid timeout: %w", err)
		}
		return runLookupKey(bootstrapAddr, keyHex, timeout)

	default:
		return fmt.Errorf("unknown subcommand: %s\nusage: %s <run|put|connect|get|shutdown|restore|snapshot|neighbors|keygen|lookup-key> [flags]", subcmd, os.Args[0])
	}
}

// parseLookupKeyBootstrapPeers parses a comma-separated list of p2p
// multiaddrs (each including a /p2p/<peerID> component) into peer.AddrInfo
// values for use as "lookup-key" bootstrap targets.
//
// Parameters:
//   - s (string): comma-separated multiaddrs, e.g. "/ip4/1.2.3.4/tcp/2893/p2p/Qm...".
//
// Returns:
//   - []peer.AddrInfo: the parsed bootstrap peers, in input order.
//   - error: non-nil if any entry fails to parse as a multiaddr or lacks a resolvable peer ID, or if s yields no entries.
func parseLookupKeyBootstrapPeers(s string) ([]peer.AddrInfo, error) {
	var out []peer.AddrInfo
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ma, err := multiaddr.NewMultiaddr(part)
		if err != nil {
			return nil, fmt.Errorf("bootstrap multiaddr %q: %w", part, err)
		}
		info, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			return nil, fmt.Errorf("bootstrap peer %q: %w", part, err)
		}
		out = append(out, *info)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no bootstrap addresses")
	}
	return out, nil
}

// lookupKeyBootstrapDialTimeout is separate from --timeout (GetToken budget). Connect + DHT init can exceed 30s under load.
const lookupKeyBootstrapDialTimeout = 120 * time.Second

// lookupKeyConnectTimeout returns the dial timeout used when connecting to
// "lookup-key" bootstrap peers: lookupKeyBootstrapDialTimeout by default, or
// the value of LOOKUP_KEY_CONNECT_TIMEOUT (parsed as a duration) if it is set
// to a valid positive duration.
//
// Returns:
//   - time.Duration: the bootstrap connect timeout to use.
func lookupKeyConnectTimeout() time.Duration {
	d := lookupKeyBootstrapDialTimeout
	if v := os.Getenv("LOOKUP_KEY_CONNECT_TIMEOUT"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil && parsed > 0 {
			d = parsed
		}
	}
	return d
}

// connectLookupKeyBootstrapPeers dials every bootstrap peer in infos,
// bounded per-dial by lookupKeyConnectTimeout. It does not stop at the first
// success: it attempts all entries (so alternate addresses for the same
// logical peer are also tried), logging failures at "bootstrap" severity
// before the first success and "optional bootstrap" severity afterward. It
// only returns an error if every dial in infos failed.
//
// Parameters:
//   - ctxBg (context.Context): parent context each per-peer dial timeout is derived from.
//   - h (host.Host): the libp2p host performing the dials.
//   - infos ([]peer.AddrInfo): bootstrap peers to connect to.
//
// Returns:
//   - error: non-nil if infos is empty or every dial attempt failed.
func connectLookupKeyBootstrapPeers(ctxBg context.Context, h host.Host, infos []peer.AddrInfo) error {
	if len(infos) == 0 {
		return fmt.Errorf("no bootstrap addresses")
	}
	dialTimeout := lookupKeyConnectTimeout()
	var lastErr error
	connected := false
	for i := range infos {
		ctxConn, cancelConn := context.WithTimeout(ctxBg, dialTimeout)
		connErr := h.Connect(ctxConn, infos[i])
		cancelConn()
		if connErr == nil {
			connected = true
			continue
		}
		lastErr = connErr
		if !connected {
			log.Printf("lookup-key: bootstrap %d: %v", i, connErr)
		} else {
			log.Printf("lookup-key: optional bootstrap %d: %v", i, connErr)
		}
	}
	if !connected {
		if lastErr != nil {
			return fmt.Errorf("connect to bootstrap: all %d dials failed: %w", len(infos), lastErr)
		}
		return fmt.Errorf("connect to bootstrap: all %d dials failed", len(infos))
	}
	return nil
}

// runLookupKey implements the "lookup-key" subcommand: it performs a single
// cold DHT lookup for key from a throwaway, otherwise-empty node, and prints
// a JSON result to stdout.
//
// It parses keyHex and bootstrapMultiaddrs, creates a TCP-only libp2p host
// (avoiding QUIC UDP buffer issues in short-lived container runs), installs a
// handshake responder using the SNG40_ENV/SNG40_CA_PUBS/SNG40_TOKEN env vars
// (via getHandshakePolicyFromEnv), and connects to every bootstrap peer
// (connectLookupKeyBootstrapPeers) before building a client-mode DHT seeded
// with those same peers. It waits (up to 45s) for the DHT routing table to
// become non-empty, then calls mystore.GetToken for key, counting
// routing.SendingQuery events during the call as the network hop count (the
// same accounting path used by the HTTP /lookup control endpoint, unlike
// GetClosestPeers which often reported 0 hops). Because this node has no
// local token state, GetToken is forced to traverse the DHT rather than
// short-circuiting on a local hit, making the hop count meaningful. The
// result (network_hops, lookup_latency_ms, found, and error/lookup_deadline
// if applicable) is JSON-encoded to stdout; lookup_latency_ms is omitted
// (null) when the lookup failed by hitting its deadline, since wall time in
// that case reflects timeout saturation rather than round-trip latency.
//
// Parameters:
//   - bootstrapMultiaddrs (string): comma-separated bootstrap peer multiaddrs (each with a /p2p/<peerID> component).
//   - keyHex (string): the 64-hex-char key to look up.
//   - lookupTimeout (time.Duration): budget for the GetToken call only (connect and DHT bootstrap use separate, larger budgets); values under 5s are raised to 30s.
//
// Returns:
//   - error: non-nil if the key or bootstrap addresses are invalid, the host/DHT cannot be created, bootstrap connection fails, or the routing table never becomes non-empty; a failed or timed-out lookup itself is reported in the JSON output, not as a returned error.
func runLookupKey(bootstrapMultiaddrs, keyHex string, lookupTimeout time.Duration) error {
	ctxBg := context.Background()
	if lookupTimeout < 5*time.Second {
		lookupTimeout = 30 * time.Second
	}

	key, err := mystore.ParseKey(keyHex)
	if err != nil {
		return fmt.Errorf("invalid key: %w", err)
	}

	infos, err := parseLookupKeyBootstrapPeers(bootstrapMultiaddrs)
	if err != nil {
		return err
	}

	// TCP-only avoids QUIC UDP buffer issues in short-lived docker runs; DHT uses the same token prefix as peers.
	h, err := myhost.NewHost(ctxBg, []string{"/ip4/0.0.0.0/tcp/0"})
	if err != nil {
		return err
	}
	defer h.Close()

	// Cluster peers use InstallHandshakeGate: non-handshake streams are reset until SNG40 handshake completes.
	// Respond to the bootstrap node's post-connect handshake so DHT streams are allowed.
	requireSNG40 := os.Getenv("SNG40_ENV") == "true"
	tokenSNG40 := os.Getenv("SNG40_TOKEN")
	pubsSNG40 := os.Getenv("SNG40_CA_PUBS")
	policyBase := getHandshakePolicyFromEnv(requireSNG40, pubsSNG40, tokenSNG40, 30*time.Second)
	stopAntiReplay := myhost.EnableAntiReplay(ctxBg, &policyBase)
	defer stopAntiReplay()
	_ = myhost.EnableAttackMitigation(ctxBg, &policyBase)
	localHS := myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}
	myhost.RegisterHandshake(h, localHS, policyBase)

	if err := connectLookupKeyBootstrapPeers(ctxBg, h, infos); err != nil {
		return err
	}
	// Short settle; first peer must have completed handshake before DHT dials.
	time.Sleep(1 * time.Second)

	dhtCfg := myhost.DHTConfig{
		Mode:           myhost.DHTModeClient,
		UseTokenDHT:    true,
		BootstrapPeers: infos,
	}
	ctxBootstrap, cancelBootstrap := context.WithTimeout(ctxBg, 90*time.Second)
	defer cancelBootstrap()
	d, err := myhost.NewDHT(ctxBootstrap, h, dhtCfg)
	if err != nil {
		return fmt.Errorf("create DHT: %w", err)
	}
	defer d.Close()

	rtWait, cancelRT := context.WithTimeout(ctxBg, 45*time.Second)
	defer cancelRT()
	for d.RoutingTable().Size() == 0 {
		select {
		case <-rtWait.Done():
			return fmt.Errorf("DHT routing table empty after 45s (bootstrap may have failed)")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	if os.Getenv("SNG40_LOG_LOOKUP_PATHS") == "1" {
		log.Printf("lookup-key: dht_rt_size=%d", d.RoutingTable().Size())
	}

	// Match control /lookup: count routing.SendingQuery during GetToken (same path as HTTP /lookup).
	// GetClosestPeers was a different DHT walk and often reported 0 hops while GetToken succeeded.
	ctxLookup, cancelLookup := context.WithTimeout(ctxBg, lookupTimeout)
	defer cancelLookup()
	evCtx, evCh := routing.RegisterForQueryEvents(ctxLookup)
	evCtx2, cancel2 := context.WithCancel(evCtx)
	defer cancel2()
	var hops int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range evCh {
			if ev != nil && ev.Type == routing.SendingQuery {
				hops++
			}
		}
	}()
	start := time.Now()
	_, err = mystore.GetToken(evCtx2, routing.ValueStore(d), key)
	cancel2()
	cancelLookup()
	<-done
	latencyMs := time.Since(start).Milliseconds()
	deadlineHit := errors.Is(err, context.DeadlineExceeded) || errors.Is(ctxLookup.Err(), context.DeadlineExceeded)
	if os.Getenv("SNG40_LOG_LOOKUP_PATHS") == "1" {
		log.Printf("lookup-key: network_hops=%d lookup_latency_ms=%d found=%v deadline_hit=%v err=%v", int(hops), latencyMs, err == nil, deadlineHit, err)
	}

	var latencyJSON interface{} = latencyMs
	if deadlineHit {
		// Wall time ~= lookupTimeout; reporting it as "latency" is misleading (timeout saturation, not RTT).
		latencyJSON = nil
	}

	out := map[string]interface{}{
		"network_hops":      int(hops),
		"lookup_latency_ms": latencyJSON,
		"found":             err == nil,
	}
	if deadlineHit {
		out["lookup_deadline"] = true
	}
	if err != nil {
		out["error"] = err.Error()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "")
	return enc.Encode(out)
}

// staticContentRouter is a minimal routing.ContentRouting implementation
// that always reports a single, fixed provider for any CID. It backs the
// "get" subcommand's inline mode, where the caller already knows exactly
// which peer to fetch from via --from-addr/--from-peer and does not need
// real content routing. This is a legacy stub; key-based token routing
// (via the DHT) is the primary lookup path elsewhere in the node.
type staticContentRouter struct {
	provider peer.AddrInfo // the single peer always returned as a "provider".
}

// Provide is a no-op; staticContentRouter never announces providership for
// content because it does not participate in real content routing.
//
// Parameters:
//   - ctx (context.Context): unused.
//   - c (cid.Cid): unused.
//   - b (bool): unused.
//
// Returns:
//   - error: always nil.
func (s *staticContentRouter) Provide(ctx context.Context, c cid.Cid, b bool) error { return nil }

// ProvideMany is a no-op, for the same reason as Provide.
//
// Parameters:
//   - ctx (context.Context): unused.
//   - keys ([]cid.Cid): unused.
//
// Returns:
//   - error: always nil.
func (s *staticContentRouter) ProvideMany(ctx context.Context, keys []cid.Cid) error { return nil }

// FindProvidersAsync returns a buffered channel that yields s.provider
// exactly once (for any CID, since this router has only the one configured
// provider) and then closes, or closes without yielding if ctx is canceled
// first.
//
// Parameters:
//   - ctx (context.Context): if canceled before the send completes, no value is sent.
//   - c (cid.Cid): unused; the same provider is returned regardless of which CID is requested.
//   - count (int): unused; at most one provider is ever produced.
//
// Returns:
//   - <-chan peer.AddrInfo: a channel yielding s.provider once, then closed.
func (s *staticContentRouter) FindProvidersAsync(ctx context.Context, c cid.Cid, count int) <-chan peer.AddrInfo {
	out := make(chan peer.AddrInfo, 1)
	go func() {
		defer close(out)
		select {
		case out <- s.provider:
		case <-ctx.Done():
			return
		}
	}()
	return out
}

// FindProviders synchronously returns a single-element slice containing
// s.provider, regardless of which CID is requested.
//
// Parameters:
//   - ctx (context.Context): unused.
//   - c (cid.Cid): unused.
//
// Returns:
//   - []peer.AddrInfo: always []peer.AddrInfo{s.provider}.
//   - error: always nil.
func (s *staticContentRouter) FindProviders(ctx context.Context, c cid.Cid) ([]peer.AddrInfo, error) {
	return []peer.AddrInfo{s.provider}, nil
}

// Ready always reports true, since staticContentRouter has no real
// initialization state to wait on.
//
// Returns:
//   - bool: always true.
func (s *staticContentRouter) Ready() bool { return true }
