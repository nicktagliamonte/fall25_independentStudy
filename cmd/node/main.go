// Purpose: Symmetric node CLI supporting run/put/connect/get primitives.

package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
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

type stringSlice []string

func (s *stringSlice) String() string {
	return fmt.Sprint([]string(*s))
}

func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func printBanner(hID string, addrs []string) {
	fmt.Println("PeerID:", hID)
	for _, a := range addrs {
		fmt.Println("Addr:", a)
	}
}

// bestPublicIPv4 returns the first non-loopback, non-private IPv4 found on the machine.
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
// Uses short timeouts and falls back across providers.
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

// importDirectory walks dirPath and stores each file as a block; creates a JSON manifest and stores it as the root block.
// Returns manifest CID, number of files, and total bytes across files.
func importDirectory(ctx context.Context, stack *mystore.Stack, dirPath string) (cid.Cid, int, int64, error) {
	type entry struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
		CID  string `json:"cid"`
		Type string `json:"type"` // "file" or "dir"
	}
	var manifest []entry
	var total int64
	var files int
	err := filepath.Walk(dirPath, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsDir() {
			rel, _ := filepath.Rel(dirPath, p)
			if rel != "." {
				// record directory to preserve empty dirs
				manifest = append(manifest, entry{Path: rel, Size: 0, CID: "", Type: "dir"})
			}
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		c, err := mystore.PutRawBlock(ctx, stack.BlockSvc, b)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dirPath, p)
		manifest = append(manifest, entry{Path: rel, Size: int64(len(b)), CID: c.String(), Type: "file"})
		total += int64(len(b))
		files++
		return nil
	})
	if err != nil {
		return cid.Cid{}, 0, 0, err
	}
	// write manifest block
	buf, _ := json.Marshal(manifest)
	mc, err := mystore.PutRawBlock(ctx, stack.BlockSvc, buf)
	if err != nil {
		return cid.Cid{}, 0, 0, err
	}
	return mc, files, total, nil
}

// exportDirectory fetches the manifest block, then fetches each file block and writes to outDir.
func exportDirectory(ctx context.Context, stack *mystore.Stack, root cid.Cid, outDir string) (int, int64, error) {
	type entry struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
		CID  string `json:"cid"`
		Type string `json:"type"`
	}
	b, err := mystore.GetBlock(ctx, stack.BlockSvc, root)
	if err != nil {
		return 0, 0, err
	}
	var manifest []entry
	if err := json.Unmarshal(b, &manifest); err != nil {
		return 0, 0, err
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return 0, 0, err
	}
	var total int64
	for _, e := range manifest {
		dst := filepath.Join(outDir, e.Path)
		if e.Type == "dir" {
			if err := os.MkdirAll(dst, 0755); err != nil {
				return 0, 0, err
			}
			continue
		}
		c, err := cid.Decode(e.CID)
		if err != nil {
			return 0, 0, err
		}
		data, err := mystore.GetBlock(ctx, stack.BlockSvc, c)
		if err != nil {
			return 0, 0, err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return 0, 0, err
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return 0, 0, err
		}
		total += int64(len(data))
	}
	return len(manifest), total, nil
}

func hostAddrsStrings(h host.Host) []string {
	addrs := make([]string, 0, len(h.Addrs()))
	for _, a := range h.Addrs() {
		addrs = append(addrs, a.String())
	}
	return addrs
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func dialWithTimeout(ctx context.Context, h host.Host, info peer.AddrInfo, d time.Duration) error {
	connectCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return h.Connect(connectCtx, info)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <run|put|connect|get> [flags]\n", os.Args[0])
		os.Exit(2)
	}

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
			// Re-exec ourselves without the --background flag and detach
			childArgs := []string{"run"}
			for _, a := range listenAddrs {
				childArgs = append(childArgs, "--listen", a)
			}
			cmd := exec.Command(os.Args[0], childArgs...)
			if logPath != "" {
				f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
				if err != nil {
					log.Fatal(err)
				}
				cmd.Stdout = f
				cmd.Stderr = f
			} else {
				// Default background log file to avoid hijacking the current terminal
				_ = os.MkdirAll("/tmp/fall25_node", 0755)
				f, err := os.OpenFile("/tmp/fall25_node/daemon.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
				if err == nil {
					cmd.Stdout = f
					cmd.Stderr = f
				}
			}
			cmd.Stdin = nil
			if err := cmd.Start(); err != nil {
				log.Fatal(err)
			}
			fmt.Println("Started node in background. PID:", cmd.Process.Pid)
			return
		}

		ctx := context.Background()
		// Optional persistent key
		var h host.Host
		if keyPath != "" {
			priv, err := myhost.LoadOrCreatePrivateKey(keyPath)
			if err != nil {
				log.Fatal(err)
			}
			h, err = myhost.NewHostWithPriv(ctx, listenAddrs, priv)
			if err != nil {
				log.Fatal(err)
			}
		} else {
			var err error
			h, err = myhost.NewHost(ctx, listenAddrs)
			if err != nil {
				log.Fatal(err)
			}
		}
		defer h.Close()

		// Install handshake responder and gate for inbound connections will happen after stack initialization (needs state head)

		// Optional persistent store
		var stack *mystore.Stack
		if storePath != "" {
			bs, d, err := mystore.NewPersistentBlockstore(storePath)
			if err != nil {
				log.Fatal(err)
			}
			// Router: DHT forbidden by policy; use null router here
			var router routing.ContentRouting = routinghelpers.Null{}
			stack, err = mystore.NewStackFromBlockstore(ctx, h, bs, d, router)
			if err != nil {
				log.Fatal(err)
			}
		} else {
			var err error
			stack, err = mystore.NewStack(ctx, h)
			if err != nil {
				log.Fatal(err)
			}
		}
		defer stack.Bitswap.Close()

		// Now that stack is initialized, install handshake responder and gate with state head/height
		head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		headStr := ""
		if head.Defined() {
			headStr = head.String()
		}
		myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height}, myhost.HandshakePolicy{Timeout: 10 * time.Second})
		_ = myhost.InstallHandshakeGate(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, myhost.HandshakePolicy{MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0), Timeout: 10 * time.Second})

		// Initialize PeerStore from the same datastore used by the stack
		peerStore, err := myhost.NewPeerStore(stack.Datastore)
		if err != nil {
			log.Fatal(err)
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

		// Register handshake responder for inbound peers with permissive policy and peer sample
		myhost.RegisterHandshakeWithPeers(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, ListenAddrs: hostAddrsStrings(h)}, myhost.HandshakePolicy{Timeout: 10 * time.Second}, func(max int) []peer.AddrInfo {
			infos, _ := peerStore.GetDialCandidates(max, 0, nil)
			return infos
		})

		// Dialer loop: maintain minOutbound connections with backoff
		dialTimeout, err := time.ParseDuration(dialTimeoutStr)
		if err != nil {
			log.Fatal(err)
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
					if learned, err := myhost.PerformHandshake(context.Background(), h, pid, myhost.HandshakePolicy{Timeout: dialTimeout}, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, WantPeerlist: true, ListenAddrs: hostAddrsStrings(h)}); err == nil {
						// advance state head for this peer (best effort)
						if _, _, err := mystore.AppendPeerAdded(context.Background(), stack.Datastore, stack.BlockSvc, pid.String()); err == nil {
							// no-op on error
						}
						for _, info2 := range learned {
							if info2.ID == h.ID() {
								continue
							}
							_ = peerStore.Upsert(info2.ID, info2.Addrs, 0, "handshake")
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
						if learned, err := myhost.PerformHandshake(context.Background(), h, pid, myhost.HandshakePolicy{Timeout: 5 * time.Second}, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, WantPeerlist: true, ListenAddrs: hostAddrsStrings(h)}); err == nil {
							if _, _, err := mystore.AppendPeerAdded(context.Background(), stack.Datastore, stack.BlockSvc, pid.String()); err == nil {
							}
							for _, info := range learned {
								if info.ID == h.ID() {
									continue
								}
								_ = peerStore.Upsert(info.ID, info.Addrs, 0, "gossip")
							}
							metrics.AddGossipLearned(len(learned))
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
		addr, _, err := ctrl.Start(ctx, h, stack, peerStore, metrics)
		if err != nil {
			log.Fatal(err)
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

		select {}

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
			log.Fatal("put: either --data or --file is required")
		}
		if data != "" && filePath != "" {
			log.Fatal("put: specify only one of --data or --file")
		}

		var payload []byte
		if filePath != "" {
			f, err := os.Open(filePath)
			if err != nil {
				log.Fatal(err)
			}
			defer f.Close()
			b, err := io.ReadAll(f)
			if err != nil {
				log.Fatal(err)
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
						log.Fatal(err)
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						body, _ := io.ReadAll(resp.Body)
						log.Fatalf("daemon put failed: %s", string(body))
					}
					var out struct {
						CID          string `json:"cid"`
						MultihashHex string `json:"multihash_hex"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
						log.Fatal(err)
					}
					fmt.Println("CID:", out.CID)
					fmt.Printf("CID (multihash hex): %s\n", out.MultihashHex)
					return
				}
			}
		}
		// Inline mode
		h, err := myhost.NewHost(ctx, listenAddrs)
		if err != nil {
			log.Fatal(err)
		}
		defer h.Close()

		// Install handshake hooks for inline mode.
		require := os.Getenv("SNG40_REQUIRE_TOKEN") == "true"
		var caPubs [][]byte
		if pubs := os.Getenv("SNG40_CA_PUBS"); pubs != "" {
			for _, s := range strings.Split(pubs, ",") {
				b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
				if err == nil && len(b) == 32 {
					caPubs = append(caPubs, b)
				}
			}
		}
		token := os.Getenv("SNG40_TOKEN")
		policyBase := myhost.HandshakePolicy{Timeout: 10 * time.Second, MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0)}
		if require || (len(caPubs) > 0 && token != "") {
			policyBase.RequireCredential = true
			policyBase.AuthScheme = "token-ed25519-v1"
			policyBase.CAPubKeys = caPubs
			policyBase.Token = token
		}
		_ = myhost.InstallHandshakeGate(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, policyBase)

		stack, err := mystore.NewStack(ctx, h)
		if err != nil {
			log.Fatal(err)
		}
		defer stack.Bitswap.Close()

		// Now register handshake with current state head/height (after stack is ready)
		head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		headStr := ""
		if head.Defined() {
			headStr = head.String()
		}
		myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height}, policyBase)

		c, err := mystore.PutRawBlock(ctx, stack.BlockSvc, payload)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println("CID:", c.String())
		fmt.Printf("CID (multihash hex): %s\n", hex.EncodeToString(c.Hash()))

		addrs2 := hostAddrsStrings(h)
		printBanner(h.ID().String(), addrs2)
		printDerivedPublicAddrs(addrs2)

		if serve {
			select {}
		}

		// case "putdir": removed
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
			log.Fatal("connect: --addr and --peer are required")
		}
		dur, err := time.ParseDuration(timeoutStr)
		if err != nil {
			log.Fatal(err)
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
						log.Fatal(err)
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						body, _ := io.ReadAll(resp.Body)
						log.Fatalf("daemon connect failed: %s", string(body))
					}
					fmt.Println("Connected via daemon to:", peerIDStr)
					return
				}
			}
		}
		h, err := myhost.NewHost(ctx, listenAddrs)
		if err != nil {
			log.Fatal(err)
		}
		defer h.Close()

		// Install handshake hooks for inline connect mode.
		require := os.Getenv("SNG40_REQUIRE_TOKEN") == "true"
		var caPubs [][]byte
		if pubs := os.Getenv("SNG40_CA_PUBS"); pubs != "" {
			for _, s := range strings.Split(pubs, ",") {
				b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
				if err == nil && len(b) == 32 {
					caPubs = append(caPubs, b)
				}
			}
		}
		token := os.Getenv("SNG40_TOKEN")
		base := myhost.HandshakePolicy{Timeout: dur, MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0)}
		if require || (len(caPubs) > 0 && token != "") {
			base.RequireCredential = true
			base.AuthScheme = "token-ed25519-v1"
			base.CAPubKeys = caPubs
			base.Token = token
		}
		stack, err := mystore.NewStack(ctx, h)
		if err != nil {
			log.Fatal(err)
		}
		defer stack.Bitswap.Close()
		head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		headStr := ""
		if head.Defined() {
			headStr = head.String()
		}
		myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height}, base)
		_ = myhost.InstallHandshakeGate(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, base)

		maddr, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			log.Fatal(err)
		}
		pid, err := peer.Decode(peerIDStr)
		if err != nil {
			log.Fatal(err)
		}
		info := peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{maddr}}

		if err := dialWithTimeout(ctx, h, info, dur); err != nil {
			log.Fatal(err)
		}

		// optional: register handshake responder for inbound peers
		myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, myhost.HandshakePolicy{Timeout: dur})
		// initiator-side handshake to validate remote
		policy := myhost.HandshakePolicy{MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0), Timeout: dur}
		local := myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}
		if _, err := myhost.PerformHandshake(ctx, h, pid, policy, local); err != nil {
			log.Fatal(err)
		}
		// advance local state for the explicitly connected peer
		if _, _, _ = mystore.AppendPeerAdded(context.Background(), stack.Datastore, stack.BlockSvc, pid.String()); true {
		}

		fmt.Println("Connected to:", pid)
		for _, a := range h.Addrs() {
			fmt.Println("Our Addr:", a.String())
		}

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
			log.Fatal("get: --cid, --from-addr, and --from-peer are required")
		}
		dur, err := time.ParseDuration(timeoutStr)
		if err != nil {
			log.Fatal(err)
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
						log.Fatal(err)
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						body, _ := io.ReadAll(resp.Body)
						log.Fatalf("daemon get failed: %s", string(body))
					}
					var out struct {
						Bytes   int    `json:"bytes"`
						DataB64 string `json:"data_b64"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
						log.Fatal(err)
					}
					decoded, err := base64.StdEncoding.DecodeString(out.DataB64)
					if err != nil {
						log.Fatal(err)
					}
					if outFile != "" {
						if err := os.WriteFile(outFile, decoded, 0644); err != nil {
							log.Fatal(err)
						}
						fmt.Printf("Fetched %d bytes -> %s\n", len(decoded), outFile)
					} else {
						fmt.Printf("Fetched %d bytes\n", len(decoded))
					}
					return
				}
			}
		}
		h, err := myhost.NewHost(ctx, listenAddrs)
		if err != nil {
			log.Fatal(err)
		}
		defer h.Close()

		// Install handshake hooks for inline get mode.
		require := os.Getenv("SNG40_REQUIRE_TOKEN") == "true"
		var caPubs [][]byte
		if pubs := os.Getenv("SNG40_CA_PUBS"); pubs != "" {
			for _, s := range strings.Split(pubs, ",") {
				b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
				if err == nil && len(b) == 32 {
					caPubs = append(caPubs, b)
				}
			}
		}
		token := os.Getenv("SNG40_TOKEN")
		base := myhost.HandshakePolicy{Timeout: dur, MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0)}
		if require || (len(caPubs) > 0 && token != "") {
			base.RequireCredential = true
			base.AuthScheme = "token-ed25519-v1"
			base.CAPubKeys = caPubs
			base.Token = token
		}
		// stack is created below; handshake registration with state must occur after

		maddr, err := multiaddr.NewMultiaddr(fromAddr)
		if err != nil {
			log.Fatal(err)
		}
		pid, err := peer.Decode(fromPeer)
		if err != nil {
			log.Fatal(err)
		}
		info := peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{maddr}}

		staticRouter := &staticContentRouter{provider: info}
		stack, err := mystore.NewStackWithRouter(ctx, h, staticRouter)
		if err != nil {
			log.Fatal(err)
		}
		defer stack.Bitswap.Close()

		// Now that stack exists, register handshake with current state and install gate
		head, height, _ := mystore.GetHead(ctx, stack.Datastore)
		headStr := ""
		if head.Defined() {
			headStr = head.String()
		}
		myhost.RegisterHandshake(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0, StateHeadCID: headStr, StateHeight: height}, base)
		_ = myhost.InstallHandshakeGate(h, myhost.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}, base)

		// Use the minimum of default dial (10s) and fetch timeout to avoid exceeding fetch budget
		dialDur := minDuration(dur, 10*time.Second)
		if err := dialWithTimeout(ctx, h, info, dialDur); err != nil {
			log.Fatal(err)
		}

		c, err := cid.Decode(cidStr)
		if err != nil {
			log.Fatal(err)
		}

		fetchCtx, cancel2 := context.WithTimeout(ctx, dur)
		defer cancel2()
		b, err := mystore.GetBlock(fetchCtx, stack.BlockSvc, c)
		if err != nil {
			log.Fatal(err)
		}
		if outFile != "" {
			if err := os.WriteFile(outFile, b, 0644); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("Fetched %d bytes -> %s\n", len(b), outFile)
		} else {
			fmt.Printf("Fetched %d bytes\n", len(b))
		}

		// case "getdir": removed

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", subcmd)
		fmt.Fprintf(os.Stderr, "usage: %s <run|put|connect|get> [flags]\n", os.Args[0])
		os.Exit(2)
	}
}

// staticContentRouter implements routing.ContentRouting and always returns
// the connected provider peer for any queried CID.
type staticContentRouter struct {
	provider peer.AddrInfo
}

func (s *staticContentRouter) Provide(ctx context.Context, c cid.Cid, b bool) error  { return nil }
func (s *staticContentRouter) ProvideMany(ctx context.Context, keys []cid.Cid) error { return nil }

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

func (s *staticContentRouter) FindProviders(ctx context.Context, c cid.Cid) ([]peer.AddrInfo, error) {
	return []peer.AddrInfo{s.provider}, nil
}

func (s *staticContentRouter) Ready() bool { return true }
