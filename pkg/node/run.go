// Purpose: Library entrypoint and CLI implementation for the symmetric node.

package node

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ipfs/go-cid"
	routinghelpers "github.com/libp2p/go-libp2p-routing-helpers"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multiaddr"
	ctrl "github.com/nicktagliamonte/fall25_independentStudy/internal/control"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// stringSlice implements flag.Value to support repeatable string flags
// (e.g. "-listen" or "-seed" passed multiple times, each appended in order).
type stringSlice []string

// String implements flag.Value. It returns the Go-syntax representation of
// the accumulated slice (via fmt.Sprint), primarily used by the flag package
// for help/usage output and default-value display.
func (s *stringSlice) String() string { return fmt.Sprint([]string(*s)) }

// Set implements flag.Value. It is called once per occurrence of the flag on
// the command line and appends v to the slice. It never returns a non-nil
// error (any string value is accepted as-is).
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// printBanner writes the node's PeerID and each of its listen/advertised
// addresses to stdout, one "PeerID: <id>" line followed by one "Addr: <a>"
// line per address in addrs. Used by the "run" subcommand at startup.
func printBanner(hID string, addrs []string) {
	fmt.Println("PeerID:", hID)
	for _, a := range addrs {
		fmt.Println("Addr:", a)
	}
}

// bestPublicIPv4 attempts to determine this machine's public-facing IPv4
// address, for display purposes (printDerivedPublicAddrs) and as a fallback
// host-IP source for the tuple-space client (newTupleSpaceClient). It takes
// no parameters.
//
// It first enumerates local network interfaces (net.Interfaces) and returns
// the first IPv4 address found that is up, not a loopback interface, not a
// loopback/private/link-local address (RFC1918 and link-local ranges are
// skipped). If no such local address is found, it falls back to
// fetchPublicIPv4, which queries an external "what is my IP" service.
//
// Returns the string form of the discovered IPv4 address, or "" if neither
// the local-interface scan nor the external fallback yields one (e.g. no
// network access). This is a best-effort heuristic — it can be wrong on
// machines with multiple interfaces, complex NAT, or IPv6-only egress.
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

// printDerivedPublicAddrs emits derived public addrs by replacing the /ip4 component
// in the host addrs with the detected public IPv4. This does not change what the
// node listens on; it only prints human-usable remote addresses.
//
// Parameters:
//   - addrs: the node's own listen/advertised multiaddr strings (as from
//     hostAddrsStrings). Only entries containing "/ip4/" are considered;
//     others (e.g. pure /ip6 addrs) are silently skipped.
//
// If bestPublicIPv4 cannot determine a public IPv4 (returns ""), this
// function does nothing. Otherwise, for each matching addr it prints a
// "Public Addr: /ip4/<publicIP><remainder>" line to stdout, where
// <remainder> is everything in the original addr after the IP component
// (e.g. "/tcp/2893"). No value is returned; this is purely for operator
// convenience when sharing a dialable address with peers behind NAT.
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

// fetchPublicIPv4 contacts a simple web service to discover the public IPv4.
// Uses short timeouts and falls back across providers. Takes no parameters.
//
// It tries each endpoint in turn (currently api.ipify.org and
// checkip.amazonaws.com, both of which return the caller's IPv4 as plain
// text) with a 1.5-second HTTP client timeout, stopping at the first
// endpoint that returns a parseable, non-private, non-loopback IPv4 address.
//
// Returns the discovered IPv4 as a string, or "" if every endpoint fails to
// respond, times out, or returns an unusable address (network errors are
// silently ignored and the next endpoint is tried).
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

// hostAddrsStrings returns the string encoding of every multiaddr the given
// libp2p host h is currently listening on/advertising (h.Addrs()), in the
// same order. Used to populate handshake advertisements and CLI banner
// output. Returns an empty (non-nil) slice if h has no addresses.
func hostAddrsStrings(h host.Host) []string {
	addrs := make([]string, 0, len(h.Addrs()))
	for _, a := range h.Addrs() {
		addrs = append(addrs, a.String())
	}
	return addrs
}

// minDuration returns the smaller of a and b (time.Duration comparison).
// Used to cap a fetch/dial timeout so it never exceeds a hardcoded ceiling
// (e.g. dialDur := minDuration(dur, 10*time.Second) in the commented-out
// inline "get" flow).
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// dialWithTimeout attempts to connect host h to the peer described by info,
// aborting the attempt if it does not complete within d. ctx is the parent
// context; a child context with a d timeout is derived from it via
// context.WithTimeout and always canceled before returning (deferred).
// Returns the error from h.Connect, which is non-nil on timeout, refused
// connection, unreachable address, or handshake/security-transport failure
// at the libp2p layer; nil on a successful connection.
func dialWithTimeout(ctx context.Context, h host.Host, info peer.AddrInfo, d time.Duration) error {
	connectCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return h.Connect(connectCtx, info)
}

// getHandshakePolicyFromEnv builds a myhost.HandshakePolicy for the inline
// (non-"run", non-daemon) CLI subcommands ("connect", and the now-dead
// inline branches of "put"/"get") from CLI/env-derived settings.
//
// Parameters:
//   - require: if true, forces RequireCredential on the resulting policy
//     regardless of pubs/token (mirrors Options.RequireToken /
//     SNG40_ENV=true).
//   - pubs: a comma-separated list of base64-standard-encoded 32-byte
//     Ed25519 CA public keys (mirrors Options.CAPubKeysB64 /
//     SNG40_CA_PUBS). Entries that fail to base64-decode or do not decode
//     to exactly 32 bytes are silently skipped (unlike Options-based Start,
//     this function does not error on a malformed entry).
//   - token: the shared credential string (mirrors Options.Token /
//     SNG40_TOKEN).
//   - timeout: the handshake timeout to set on the returned policy.
//
// The base policy always sets MinAgentVersion to "sng40/0.1.0" and
// ServicesAllow to all bits set (^uint64(0)). RequireCredential (with
// AuthScheme "token-ed25519-v1") is enabled when require is true, or when
// both at least one valid CA key was parsed from pubs AND token is
// non-empty — matching the same "either flag, or both key+token" rule used
// by Start for Options.RequireToken/CAPubKeysB64/Token.
//
// Returns the constructed myhost.HandshakePolicy; never errors.
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

// Run executes the CLI behavior of the node binary. It reads os.Args
// directly (os.Args[0] is the program name, os.Args[1] is the subcommand,
// and each subcommand parses its own flag.FlagSet from os.Args[2:]), so it
// is intended to be called once from main() and takes no parameters.
//
// Three environment variables configure the token-gated admission policy
// for the inline (non-daemon) subcommands ("connect", and the dead inline
// branches of "put"/"get"), via getHandshakePolicyFromEnv:
//   - SNG40_ENV: if "true", forces RequireCredential on.
//   - SNG40_TOKEN: the shared credential token.
//   - SNG40_CA_PUBS: comma-separated base64 CA public keys.
//
// Subcommands (os.Args[1]):
//   - "run": starts a long-running node. Supports "-daemon" to re-exec
//     itself detached (Setsid) with the same flags translated back onto the
//     child's argv and stdout/stderr redirected to a log file (either
//     "-log" or a default under /tmp/fall25_node), returning immediately
//     after spawning the child. In foreground mode, it constructs the host
//     and storage stack (optionally persistent via "-key"/"-store"),
//     installs the handshake responder/gate, starts the same three
//     background loops as Start (pruning, dial-maintenance, gossip — here
//     duplicated as an inline implementation rather than calling Start;
//     see refactor note), loads bootstrap seeds from "-seed"/"-seed-file"/
//     the SNG40_SEEDS env var, starts the control server and writes its
//     address as JSON to the "-control" path (default
//     /tmp/fall25_node/daemon.json) for other subcommands/processes to
//     discover, prints the PeerID/address banner, and then blocks until the
//     context is canceled (e.g. via the control server's /shutdown
//     endpoint).
//   - "put": submits a tuple-space PUT via a myhost.TupleSpaceClient
//     (requires "-name" and one of "-data"/"-file"; talks to a TSH daemon
//     at "-tsh", default 127.0.0.1:2890). NOTE: despite the "put" name,
//     this no longer performs the P2P block-put behavior described in
//     docs/FOR_NEXT_WEEK.txt's node CLI reference; the original inline
//     libp2p/blockstore "put" implementation is present but commented out
//     below (dead code) — see refactor notes.
//   - "connect": dials a remote peer directly by "-addr"/"-peer" (bypassing
//     the daemon unless "-daemon" is passed, in which case it POSTs to the
//     running daemon's /connect endpoint instead), performs a handshake,
//     and attempts a bounded suffix-sync if the remote's chain height is
//     ahead of the local one.
//   - "get": submits a tuple-space GET via TsRead (requires "-name"; despite
//     the flag set being named "ts-read" this is the "get" case). Like
//     "put", the original inline P2P content-fetch implementation is
//     present but commented out (dead code).
//   - "shutdown": reads the daemon's control address from "-control" and
//     issues an HTTP GET to its /shutdown endpoint.
//   - "restore": reads a manifest (a file of one-CID-per-line, or a single
//     CID literal if the path does not exist as a file) and POSTs a restore
//     job to the daemon's /restore endpoint (retrying up to 3 times with
//     exponential backoff on submit failure), then polls
//     /restore/status?id=<job> once per second (bounded by a 5-minute total
//     poll timeout) until the job reports done, printing progress and a
//     final /metrics snapshot.
//   - "snapshot": GETs the daemon's /snapshot endpoint (with "-limit" and
//     optional "-cursor" for pagination) and copies the response body
//     verbatim to stdout.
//   - "neighbors": GETs the daemon's /neighbors endpoint and copies the
//     response body verbatim to stdout.
//   - "keygen": generates (or loads, if one already exists at "-out") a
//     persistent libp2p private key and prints the resulting PeerID.
//   - "ts-get" / "ts-read": duplicate tuple-space GET/READ implementations
//     (functionally identical to each other and to the "get" case above);
//     see refactor notes about consolidating these three near-duplicate
//     branches.
//
// Returns nil on success. Returns a non-nil error for: fewer than 2
// os.Args (missing subcommand), an unrecognized subcommand, missing
// required flags for a subcommand, failures parsing user-supplied
// multiaddrs/peer IDs/durations/CIDs, I/O failures (reading files, HTTP
// requests to a daemon), or propagated errors from the underlying
// libp2p/storage/control layers. Most subcommands write human-readable
// progress/results to stdout as a side effect regardless of the return
// value.
func Run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: %s <run|put|connect|get> [flags]", os.Args[0])
	}

	requireSNG40 := os.Getenv("SNG40_ENV") == "true"
	tokenSNG40 := os.Getenv("SNG40_TOKEN")
	pubsSNG40 := os.Getenv("SNG40_CA_PUBS")

	subcmd := os.Args[1]
	switch subcmd {
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
		var dialTimeoutStr string
		var staleAgeStr string
		var maxFailures int
		var maxKnown int
		var perIPDialLimit int
		fs.Var(&listenAddrs, "listen", "multiaddr to listen on (repeatable)")
		fs.BoolVar(&daemon, "daemon", false, "run the node in the background and return immediately")
		fs.StringVar(&logPath, "log", "", "when backgrounding, write logs to this file (appended)")
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to write control endpoint info")
		fs.StringVar(&keyPath, "key", "", "path to persistent private key (optional)")
		fs.StringVar(&storePath, "store", "", "path to persistent blockstore (optional)")
		fs.Var(&seedAddrs, "seed", "seed peer multiaddr (repeatable)")
		fs.StringVar(&seedFile, "seed-file", "", "path to file with seed multiaddrs (one per line)")
		fs.IntVar(&minOutbound, "min-outbound", 4, "target minimum outbound peer connections")
		fs.StringVar(&dialTimeoutStr, "dial-timeout", "10s", "dial timeout, e.g. 10s")
		fs.StringVar(&staleAgeStr, "stale-age", "24h", "consider peers stale after this duration")
		fs.IntVar(&maxFailures, "max-fail", 8, "evict peers after this many consecutive failures")
		fs.IntVar(&maxKnown, "max-known", 5000, "soft cap on tracked peers in PeerStore")
		fs.IntVar(&perIPDialLimit, "per-ip-dial-limit", 3, "maximum outbound dials per unique IP")
		_ = fs.Parse(os.Args[2:])
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
			childArgs = append(childArgs, "--dial-timeout", dialTimeoutStr)
			childArgs = append(childArgs, "--stale-age", staleAgeStr)
			childArgs = append(childArgs, "--max-fail", fmt.Sprintf("%d", maxFailures))
			childArgs = append(childArgs, "--max-known", fmt.Sprintf("%d", maxKnown))
			childArgs = append(childArgs, "--per-ip-dial-limit", fmt.Sprintf("%d", perIPDialLimit))

			cmd := exec.Command(os.Args[0], childArgs...)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

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

		// Optional persistent store
		var stack *mystore.Stack
		if storePath != "" {
			bs, d, err := mystore.NewPersistentBlockstore(storePath)
			if err != nil {
				return err
			}
			// Router: DHT forbidden by policy; use null router here
			var router routing.ContentRouting = routinghelpers.Null{}
			stack, err = mystore.NewStackFromBlockstore(ctx, h, bs, d, router)
			if err != nil {
				return err
			}
		} else {
			var err error
			stack, err = mystore.NewStack(ctx, h)
			if err != nil {
				return err
			}
		}
		defer stack.Bitswap.Close()

		// Now that stack is initialized, install handshake responder and gate with state head/height
		head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		headStr := ""
		if head.Defined() {
			headStr = head.String()
		}
		_ = myhost.InstallHandshakeGateWithCallback(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, myhost.HandshakePolicy{MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0), Timeout: 10 * time.Second}, func(pid peer.ID) {
			_, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String())
		})

		// Initialize PeerStore from the same datastore used by the stack
		peerStore, err := myhost.NewPeerStore(stack.Datastore)
		if err != nil {
			return err
		}
		// Metrics
		metrics := &ctrl.NodeMetrics{}
		// Apply pruning policy from flags
		if d, err := time.ParseDuration(staleAgeStr); err == nil {
			peerStore.SetPolicy(d, maxFailures)
		}
		if maxKnown > 0 {
			peerStore.SetMaxKnown(maxKnown)
		}
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

		// Register handshake responder for inbound peers with state summary AND peer sample
		// This combines state head/height with peer discovery functionality
		// Peer provider returns connected peers from network (not just peerstore) so new nodes learn about each other
		myhost.RegisterHandshakeWithPeers(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height, ListenAddrs: hostAddrsStrings(h)}, myhost.HandshakePolicy{Timeout: 10 * time.Second}, func(max int) []peer.AddrInfo {
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
		})

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
				if outbound >= minOutbound {
					time.Sleep(2 * time.Second)
					continue
				}
				needed := minOutbound - outbound
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
					if res, err := myhost.PerformHandshakeWithState(context.Background(), h, pid, myhost.HandshakePolicy{Timeout: dialTimeout}, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, WantPeerlist: true, ListenAddrs: hostAddrsStrings(h)}); err == nil {
						// advance state head for this peer (best effort)
						if _, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String()); true {
							// no-op
						}
						learnedToConnect := make([]peer.AddrInfo, 0)
						for _, info2 := range res.Learned {
							if info2.ID == h.ID() {
								continue
							}
							_ = peerStore.Upsert(info2.ID, info2.Addrs, 0, "handshake")
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
					if outbound >= minOutbound {
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
						if res, err := myhost.PerformHandshakeWithState(context.Background(), h, pid, myhost.HandshakePolicy{Timeout: 5 * time.Second}, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, WantPeerlist: true, ListenAddrs: hostAddrsStrings(h)}); err == nil {
							if _, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String()); true {
							}
							for _, info := range res.Learned {
								if info.ID == h.ID() {
									continue
								}
								_ = peerStore.Upsert(info.ID, info.Addrs, 0, "gossip")
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

		// Load seeds from CLI/env/file and upsert into PeerStore
		var seeds []string
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
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					seeds = append(seeds, line)
				}
			}
		}
		// Normalize and insert
		seenSeeds := make(map[string]struct{})
		for _, s := range seeds {
			if _, ok := seenSeeds[s]; ok {
				continue
			}
			seenSeeds[s] = struct{}{}
			maddr, err := multiaddr.NewMultiaddr(s)
			if err != nil {
				continue
			}
			if info, err := peer.AddrInfoFromP2pAddr(maddr); err == nil {
				if info.ID == h.ID() {
					continue
				}
				_ = peerStore.Upsert(info.ID, info.Addrs, 0, "seed")
			}
		}

		// Start control server and write daemon file
		addr, _, err := ctrl.Start(ctx, h, stack, peerStore, metrics, func() {
			// trigger graceful stop
			cancelMain()
		})
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

	case "put":

		fs := flag.NewFlagSet("ts-put", flag.ExitOnError)
		var tshAddr, appId, name, data, filePath string
		fs.StringVar(&tshAddr, "tsh", "127.0.0.1:2890", "TSH daemon address (host:port)")
		fs.StringVar(&appId, "app", "defaultApp", "Application ID")
		fs.StringVar(&name, "name", "", "Tuple name")
		fs.StringVar(&data, "data", "", "Tuple value (string)")
		fs.StringVar(&filePath, "file", "", "Tuple value (file)")
		_ = fs.Parse(os.Args[2:])

		if name == "" {
			return fmt.Errorf("ts-put: --name is required")
		}
		if data == "" && filePath == "" {
			return fmt.Errorf("ts-put: either --data or --file is required")
		}
		var val []byte
		if filePath != "" {
			b, err := os.ReadFile(filePath)
			if err != nil {
				return err
			}
			val = b
		} else {
			val = []byte(data)
		}

		client, err := newTupleSpaceClient(tshAddr, appId)
		if err != nil {
			return err
		}

		status, err := client.TsPut(name, val)
		if err != nil {
			return err
		}
		fmt.Printf("TsPut: Success (Status: %d)\n", status)
		return nil

		// fs := flag.NewFlagSet("put", flag.ExitOnError)
		// var listenAddrs stringSlice
		// var data string
		// var filePath string
		// var serve bool
		// var controlPath string
		// var daemon bool
		// var httpDebug string
		// fs.Var(&listenAddrs, "listen", "multiaddr to listen on (repeatable)")
		// fs.StringVar(&data, "data", "", "inline data to store as a block")
		// fs.StringVar(&filePath, "file", "", "path to file to store as a block")
		// fs.BoolVar(&serve, "serve", false, "keep node running to serve inbound wants")
		// fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		// fs.BoolVar(&daemon, "daemon", false, "use a running daemon at --control instead of inline")
		// fs.StringVar(&httpDebug, "http-debug", "", "optional host:port to serve /cid/<cid> debug handler")
		// _ = fs.Parse(os.Args[2:])
		// if len(listenAddrs) == 0 {
		// 	listenAddrs = []string{
		// 		"/ip4/0.0.0.0/tcp/2893",
		// 		"/ip4/0.0.0.0/udp/2894/quic-v1",
		// 	}
		// }

		// if data == "" && filePath == "" {
		// 	return fmt.Errorf("put: either --data or --file is required")
		// }
		// if data != "" && filePath != "" {
		// 	return fmt.Errorf("put: specify only one of --data or --file")
		// }

		// var payload []byte
		// if filePath != "" {
		// 	f, err := os.Open(filePath)
		// 	if err != nil {
		// 		return err
		// 	}
		// 	defer f.Close()
		// 	b, err := io.ReadAll(f)
		// 	if err != nil {
		// 		return err
		// 	}
		// 	payload = b
		// } else {
		// 	payload = []byte(data)
		// }

		// ctx := context.Background()

		// // If --daemon, use running daemon at controlPath
		// if daemon {
		// 	if b, err := os.ReadFile(controlPath); err == nil && len(b) > 0 {
		// 		var info struct {
		// 			Addr string `json:"addr"`
		// 		}
		// 		if json.Unmarshal(b, &info) == nil && info.Addr != "" {
		// 			// send HTTP request to daemon
		// 			client := &http.Client{Timeout: 15 * time.Second}
		// 			var reqBody = struct {
		// 				Data string `json:"data"`
		// 			}{Data: string(payload)}
		// 			buf, _ := json.Marshal(reqBody)
		// 			resp, err := client.Post("http://"+info.Addr+"/put", "application/json", bytes.NewReader(buf))
		// 			if err != nil {
		// 				return err
		// 			}
		// 			defer resp.Body.Close()
		// 			if resp.StatusCode != http.StatusOK {
		// 				body, _ := io.ReadAll(resp.Body)
		// 				return fmt.Errorf("daemon put failed: %s", string(body))
		// 			}
		// 			var out struct {
		// 				CID          string `json:"cid"`
		// 				MultihashHex string `json:"multihash_hex"`
		// 			}
		// 			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		// 				return err
		// 			}
		// 			fmt.Println("CID:", out.CID)
		// 			fmt.Printf("CID (multihash hex): %s\n", out.MultihashHex)
		// 			return nil
		// 		}
		// 	}
		// }
		// // Inline mode
		// h, err := myhost.NewHost(ctx, listenAddrs)
		// if err != nil {
		// 	return err
		// }
		// defer h.Close()

		// // Install handshake hooks for inline mode.
		// policyBase := getHandshakePolicyFromEnv(requireSNG40, pubsSNG40, tokenSNG40, 10*time.Second)

		// stack, err := mystore.NewStack(ctx, h)
		// if err != nil {
		// 	return err
		// }
		// defer stack.Bitswap.Close()

		// // Now register handshake with current state head/height (after stack is ready)
		// head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		// headStr := ""
		// if head.Defined() {
		// 	headStr = head.String()
		// }
		// myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height}, policyBase)
		// _ = myhost.InstallHandshakeGateWithCallback(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, policyBase, func(pid peer.ID) {
		// 	_, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String())
		// })

		// c, err := mystore.PutRawBlockIndexed(ctx, stack.Datastore, stack.BlockSvc, payload)
		// if err != nil {
		// 	return err
		// }

		// fmt.Println("CID:", c.String())
		// fmt.Printf("CID (multihash hex): %s\n", hex.EncodeToString(c.Hash()))

		// addrs2 := hostAddrsStrings(h)
		// printBanner(h.ID().String(), addrs2)
		// printDerivedPublicAddrs(addrs2)

		// if serve {
		// 	select {}
		// }
		// return nil

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

		stack, err := mystore.NewStack(ctx, h)
		if err != nil {
			return err
		}
		defer stack.Bitswap.Close()

		head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		headStr := ""
		if head.Defined() {
			headStr = head.String()
		}
		myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height}, policyBase)
		_ = myhost.InstallHandshakeGateWithCallback(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, policyBase, func(pid peer.ID) {
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
		myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, myhost.HandshakePolicy{Timeout: dur})
		// initiator-side handshake to validate remote
		policy := myhost.HandshakePolicy{MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0), Timeout: dur}
		local := myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}
		res, err := myhost.PerformHandshakeWithState(ctx, h, pid, policy, local)
		if err != nil {
			return err
		}
		// advance local state for the explicitly connected peer
		if _, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String()); true {
		}
		// If remote advertised higher height, try a short suffix sync
		if res.RemoteStateHeight > height {
			if remoteHead, err := cid.Decode(res.RemoteStateHead); err == nil {
				_, _, _, _ = mystore.SyncSuffix(context.Background(), stack.Datastore, stack.BlockSvc, remoteHead, res.RemoteStateHeight, mystore.SyncOptions{MaxDepth: 512, MaxBlockBytes: 1 << 20, Timeout: dur})
			}
		}

		fmt.Println("Connected to:", pid)
		for _, a := range h.Addrs() {
			fmt.Println("Our Addr:", a.String())
		}
		return nil

	case "get":
		fs := flag.NewFlagSet("ts-read", flag.ExitOnError)
		var tshAddr, appId, name string
		fs.StringVar(&tshAddr, "tsh", "127.0.0.1:2890", "TSH daemon address (host:port)")
		fs.StringVar(&appId, "app", "defaultApp", "Application ID")
		fs.StringVar(&name, "name", "", "Tuple name")
		_ = fs.Parse(os.Args[2:])

		if name == "" {
			return fmt.Errorf("ts-read: --name is required")
		}

		client, err := newTupleSpaceClient(tshAddr, appId)
		if err != nil {
			return err
		}

		val, err := client.TsRead(name)
		if err != nil {
			return err
		}
		if _, err := os.Stdout.Write(val); err != nil {
			return err
		}
		return nil

		// fs := flag.NewFlagSet("get", flag.ExitOnError)
		// var listenAddrs stringSlice
		// var cidStr string
		// var fromAddr string
		// var fromPeer string
		// var timeoutStr string
		// var controlPath string
		// var daemon bool
		// var outFile string
		// fs.Var(&listenAddrs, "listen", "multiaddr to listen on (repeatable)")
		// fs.StringVar(&cidStr, "cid", "", "content ID to fetch")
		// fs.StringVar(&fromAddr, "from-addr", "", "provider multiaddr")
		// fs.StringVar(&fromPeer, "from-peer", "", "provider peer ID")
		// fs.StringVar(&timeoutStr, "timeout", "20s", "fetch timeout (e.g., 20s)")
		// fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		// fs.BoolVar(&daemon, "daemon", false, "use a running daemon at --control instead of inline")
		// fs.StringVar(&outFile, "out", "", "write fetched bytes to this file (optional)")
		// _ = fs.Parse(os.Args[2:])
		// if len(listenAddrs) == 0 {
		// 	listenAddrs = []string{
		// 		"/ip4/0.0.0.0/tcp/2893",
		// 		"/ip4/0.0.0.0/udp/2894/quic-v1",
		// 	}
		// }
		// if cidStr == "" || fromAddr == "" || fromPeer == "" {
		// 	return fmt.Errorf("get: --cid, --from-addr, and --from-peer are required")
		// }
		// dur, err := time.ParseDuration(timeoutStr)
		// if err != nil {
		// 	return err
		// }

		// ctx := context.Background()

		// // If --daemon, prefer daemon
		// if daemon {
		// 	if b, err := os.ReadFile(controlPath); err == nil && len(b) > 0 {
		// 		var info struct {
		// 			Addr string `json:"addr"`
		// 		}
		// 		if json.Unmarshal(b, &info) == nil && info.Addr != "" {
		// 			var reqBody = struct {
		// 				CID     string `json:"cid"`
		// 				Addr    string `json:"from_addr"`
		// 				Peer    string `json:"from_peer"`
		// 				Timeout string `json:"timeout"`
		// 			}{CID: cidStr, Addr: fromAddr, Peer: fromPeer, Timeout: timeoutStr}
		// 			buf, _ := json.Marshal(reqBody)
		// 			resp, err := http.Post("http://"+info.Addr+"/get", "application/json", bytes.NewReader(buf))
		// 			if err != nil {
		// 				return err
		// 			}
		// 			defer resp.Body.Close()
		// 			if resp.StatusCode != http.StatusOK {
		// 				body, _ := io.ReadAll(resp.Body)
		// 				return fmt.Errorf("daemon get failed: %s", string(body))
		// 			}
		// 			var out struct {
		// 				Bytes   int    `json:"bytes"`
		// 				DataB64 string `json:"data_b64"`
		// 			}
		// 			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		// 				return err
		// 			}
		// 			decoded, err := base64.StdEncoding.DecodeString(out.DataB64)
		// 			if err != nil {
		// 				return err
		// 			}
		// 			if outFile != "" {
		// 				if err := os.WriteFile(outFile, decoded, 0644); err != nil {
		// 					return err
		// 				}
		// 				fmt.Printf("Fetched %d bytes -> %s\n", len(decoded), outFile)
		// 			} else {
		// 				fmt.Printf("Fetched %d bytes\n", len(decoded))
		// 			}
		// 			return nil
		// 		}
		// 	}
		// }
		// h, err := myhost.NewHost(ctx, listenAddrs)
		// if err != nil {
		// 	return err
		// }
		// defer h.Close()

		// // Install handshake hooks for inline get mode.
		// policyBase := getHandshakePolicyFromEnv(requireSNG40, pubsSNG40, tokenSNG40, dur)

		// // stack is created below; handshake registration with state must occur after

		// maddr, err := multiaddr.NewMultiaddr(fromAddr)
		// if err != nil {
		// 	return err
		// }
		// pid, err := peer.Decode(fromPeer)
		// if err != nil {
		// 	return err
		// }
		// info := peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{maddr}}

		// staticRouter := &staticContentRouter{provider: info}
		// stack, err := mystore.NewStackWithRouter(ctx, h, staticRouter)
		// if err != nil {
		// 	return err
		// }
		// defer stack.Bitswap.Close()

		// // Now that stack exists, register handshake with current state and install gate
		// head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		// headStr := ""
		// if head.Defined() {
		// 	headStr = head.String()
		// }
		// myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height}, policyBase)
		// _ = myhost.InstallHandshakeGateWithCallback(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, policyBase, func(pid peer.ID) {
		// 	_, _, _, _ = mystore.AppendPeerAddedIfNew(context.Background(), stack.Datastore, stack.BlockSvc, pid.String())
		// })

		// // Use the minimum of default dial (10s) and fetch timeout to avoid exceeding fetch budget
		// dialDur := minDuration(dur, 10*time.Second)
		// if err := dialWithTimeout(ctx, h, info, dialDur); err != nil {
		// 	return err
		// }

		// c, err := cid.Decode(cidStr)
		// if err != nil {
		// 	return err
		// }

		// fetchCtx, cancel2 := context.WithTimeout(ctx, dur)
		// defer cancel2()
		// b, err := mystore.GetBlockIndexed(fetchCtx, stack.Datastore, stack.BlockSvc, c)
		// if err != nil {
		// 	return err
		// }
		// if outFile != "" {
		// 	if err := os.WriteFile(outFile, b, 0644); err != nil {
		// 		return err
		// 	}
		// 	fmt.Printf("Fetched %d bytes -> %s\n", len(b), outFile)
		// } else {
		// 	fmt.Printf("Fetched %d bytes\n", len(b))
		// }
		// return nil

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

	case "ts-get":
		fs := flag.NewFlagSet("ts-get", flag.ExitOnError)
		var tshAddr, appId, name string
		fs.StringVar(&tshAddr, "tsh", "127.0.0.1:2890", "TSH daemon address (host:port)")
		fs.StringVar(&appId, "app", "defaultApp", "Application ID")
		fs.StringVar(&name, "name", "", "Tuple name")
		_ = fs.Parse(os.Args[2:])

		if name == "" {
			return fmt.Errorf("ts-get: --name is required")
		}

		client, err := newTupleSpaceClient(tshAddr, appId)
		if err != nil {
			return err
		}

		val, err := client.TsGet(name)
		if err != nil {
			return err
		}
		if _, err := os.Stdout.Write(val); err != nil {
			return err
		}
		return nil

	case "ts-read":
		fs := flag.NewFlagSet("ts-read", flag.ExitOnError)
		var tshAddr, appId, name string
		fs.StringVar(&tshAddr, "tsh", "127.0.0.1:2890", "TSH daemon address (host:port)")
		fs.StringVar(&appId, "app", "defaultApp", "Application ID")
		fs.StringVar(&name, "name", "", "Tuple name")
		_ = fs.Parse(os.Args[2:])

		if name == "" {
			return fmt.Errorf("ts-read: --name is required")
		}

		client, err := newTupleSpaceClient(tshAddr, appId)
		if err != nil {
			return err
		}

		val, err := client.TsRead(name)
		if err != nil {
			return err
		}
		if _, err := os.Stdout.Write(val); err != nil {
			return err
		}
		return nil

	default:
		return fmt.Errorf("unknown subcommand: %s\nusage: %s <run|put|connect|get|shutdown|restore|snapshot|neighbors|keygen> [flags]", subcmd, os.Args[0])
	}
}

// newTupleSpaceClient constructs a myhost.TupleSpaceClient for talking to a
// TSH (tuple-space handler) daemon, used by the "put"/"get"/"ts-get"/
// "ts-read" CLI subcommands.
//
// Parameters:
//   - tshAddr: the TSH daemon's "host:port" address to connect to.
//   - appId: the application ID namespace to use for tuple operations.
//
// It determines this machine's IPv4 (via bestPublicIPv4, falling back to
// "127.0.0.1" if that yields nothing) to use as a callback/identifying host
// IP embedded in the client, encoding it as a big-endian uint32
// (HostIP) for the TupleSpaceClient.
//
// Returns the constructed *myhost.TupleSpaceClient, or a non-nil error if
// the determined IP string fails to parse as an IP (should not normally
// happen given bestPublicIPv4's own validation) or is not an IPv4 address
// (e.g. an IPv6-only result).
func newTupleSpaceClient(tshAddr, appId string) (*myhost.TupleSpaceClient, error) {
	// determine host IP for callback
	ipStr := bestPublicIPv4()
	if ipStr == "" {
		ipStr = "127.0.0.1"
	}
	parsed := net.ParseIP(ipStr)
	if parsed == nil {
		return nil, fmt.Errorf("failed to parse host IP: %s", ipStr)
	}
	ipv4 := parsed.To4()
	if ipv4 == nil {
		return nil, fmt.Errorf("host IP is not IPv4: %s", ipStr)
	}
	hostIP := binary.BigEndian.Uint32(ipv4)

	return &myhost.TupleSpaceClient{
		TshAddr: tshAddr,
		HostIP:  hostIP,
		AppId:   appId,
	}, nil
}

// staticContentRouter implements routing.ContentRouting and always returns
// the connected provider peer for any queried CID.
//
// It is used wherever a caller already knows exactly which peer to fetch
// content from (GetRawFrom in service.go, and the commented-out inline
// "get" implementation in Run) and wants Bitswap to route all wants to that
// single peer without doing real content discovery (which is out of scope
// for this repo — routing/coordination is expected to live in the separate
// SNG tuple-space system). It effectively turns off content routing in
// favor of a fixed, single provider.
type staticContentRouter struct {
	// provider is the single peer (with its known addresses) returned for
	// every FindProviders/FindProvidersAsync query, regardless of the CID
	// requested.
	provider peer.AddrInfo
}

// Provide implements routing.ContentRouting.Provide as a no-op: this router
// never announces content to a wider network (there is no DHT/routing
// table to announce to). c and b (the "broadcast" flag) are ignored. Always
// returns nil.
func (s *staticContentRouter) Provide(ctx context.Context, c cid.Cid, b bool) error { return nil }

// ProvideMany implements routing.ContentRouting's batch-provide extension as
// a no-op, mirroring Provide. keys is ignored. Always returns nil.
func (s *staticContentRouter) ProvideMany(ctx context.Context, keys []cid.Cid) error { return nil }

// FindProvidersAsync implements routing.ContentRouting.FindProvidersAsync.
// It ignores c and count and always yields exactly s.provider on the
// returned channel (buffered, capacity 1), regardless of which CID was
// queried — this router does not actually discover providers, it just
// asserts a single fixed one. The channel is closed after the single send
// (or immediately, without sending, if ctx is canceled first). The
// goroutine that performs the send exits either after sending or when ctx
// is done, so it never leaks.
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

// FindProviders implements routing.ContentRouting.FindProviders (the
// synchronous variant). It ignores c and always returns a single-element
// slice containing s.provider. The error return is always nil.
func (s *staticContentRouter) FindProviders(ctx context.Context, c cid.Cid) ([]peer.AddrInfo, error) {
	return []peer.AddrInfo{s.provider}, nil
}

// Ready implements routing.ContentRouting.Ready. It always reports true:
// this router has no warm-up state and is usable immediately upon
// construction.
func (s *staticContentRouter) Ready() bool { return true }
