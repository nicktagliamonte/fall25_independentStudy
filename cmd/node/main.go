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
	"net/http"
	"path/filepath"

	"github.com/ipfs/go-cid"
	routinghelpers "github.com/libp2p/go-libp2p-routing-helpers"
	"github.com/libp2p/go-libp2p/core/host"
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
		var background bool
		var logPath string
		var controlPath string
		var keyPath string
		var storePath string
		fs.Var(&listenAddrs, "listen", "multiaddr to listen on (repeatable)")
		fs.BoolVar(&background, "background", false, "run the node in the background and return immediately")
		fs.StringVar(&logPath, "log", "", "when backgrounding, write logs to this file (appended)")
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to write control endpoint info")
		fs.StringVar(&keyPath, "key", "", "path to persistent private key (optional)")
		fs.StringVar(&storePath, "store", "", "path to persistent blockstore (optional)")
		_ = fs.Parse(os.Args[2:])
		if len(listenAddrs) == 0 {
			listenAddrs = []string{
				"/ip4/0.0.0.0/tcp/0",
				"/ip4/0.0.0.0/udp/0/quic-v1",
			}
		}

		if background {
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
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
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

		// Start control server and write daemon file
		addr, _, err := ctrl.Start(ctx, h, stack)
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

		printBanner(h.ID().String(), hostAddrsStrings(h))

		select {}

	case "put":
		fs := flag.NewFlagSet("put", flag.ExitOnError)
		var listenAddrs stringSlice
		var data string
		var filePath string
		var serve bool
		var controlPath string
		var noDaemon bool
		var httpDebug string
		fs.Var(&listenAddrs, "listen", "multiaddr to listen on (repeatable)")
		fs.StringVar(&data, "data", "", "inline data to store as a block")
		fs.StringVar(&filePath, "file", "", "path to file to store as a block")
		fs.BoolVar(&serve, "serve", false, "keep node running to serve inbound wants")
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		fs.BoolVar(&noDaemon, "no-daemon", false, "do not use a running daemon; perform inline")
		fs.StringVar(&httpDebug, "http-debug", "", "optional host:port to serve /cid/<cid> debug handler")
		_ = fs.Parse(os.Args[2:])
		if len(listenAddrs) == 0 {
			listenAddrs = []string{
				"/ip4/0.0.0.0/tcp/0",
				"/ip4/0.0.0.0/udp/0/quic-v1",
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

		// If a daemon control file exists and not disabled, use it
		if !noDaemon {
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
					// In daemon mode, serve flag is ignored; the daemon is already serving
					return
				}
			}
		}
		h, err := myhost.NewHost(ctx, listenAddrs)
		if err != nil {
			log.Fatal(err)
		}
		defer h.Close()

		stack, err := mystore.NewStack(ctx, h)
		if err != nil {
			log.Fatal(err)
		}
		defer stack.Bitswap.Close()

		c, err := mystore.PutRawBlock(ctx, stack.BlockSvc, payload)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println("CID:", c.String())
		fmt.Printf("CID (multihash hex): %s\n", hex.EncodeToString(c.Hash()))

		printBanner(h.ID().String(), hostAddrsStrings(h))

		if serve {
			select {}
		}

	case "connect":
		fs := flag.NewFlagSet("connect", flag.ExitOnError)
		var listenAddrs stringSlice
		var addr string
		var peerIDStr string
		var timeoutStr string
		var controlPath string
		var noDaemon bool
		fs.Var(&listenAddrs, "listen", "multiaddr to listen on (repeatable)")
		fs.StringVar(&addr, "addr", "", "remote peer multiaddr")
		fs.StringVar(&peerIDStr, "peer", "", "remote peer ID")
		fs.StringVar(&timeoutStr, "timeout", "10s", "dial timeout (e.g., 10s)")
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		fs.BoolVar(&noDaemon, "no-daemon", false, "do not use a running daemon; perform inline")
		_ = fs.Parse(os.Args[2:])
		if len(listenAddrs) == 0 {
			listenAddrs = []string{
				"/ip4/0.0.0.0/tcp/0",
				"/ip4/0.0.0.0/udp/0/quic-v1",
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

		// Prefer daemon if available
		if !noDaemon {
			if b, err := os.ReadFile(controlPath); err == nil && len(b) > 0 {
				var info struct {
					Addr string `json:"addr"`
				}
				if json.Unmarshal(b, &info) == nil && info.Addr != "" {
					// Allow enough time for the daemon to dial and complete the operation
					client := &http.Client{Timeout: 2*dur + 5*time.Second}
					var reqBody = struct {
						Addr    string `json:"addr"`
						Peer    string `json:"peer"`
						Timeout string `json:"timeout"`
					}{Addr: addr, Peer: peerIDStr, Timeout: timeoutStr}
					buf, _ := json.Marshal(reqBody)
					resp, err := client.Post("http://"+info.Addr+"/connect", "application/json", bytes.NewReader(buf))
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
		var noDaemon bool
		var outFile string
		fs.Var(&listenAddrs, "listen", "multiaddr to listen on (repeatable)")
		fs.StringVar(&cidStr, "cid", "", "content ID to fetch")
		fs.StringVar(&fromAddr, "from-addr", "", "provider multiaddr")
		fs.StringVar(&fromPeer, "from-peer", "", "provider peer ID")
		fs.StringVar(&timeoutStr, "timeout", "20s", "fetch timeout (e.g., 20s)")
		fs.StringVar(&controlPath, "control", "/tmp/fall25_node/daemon.json", "path to daemon control file")
		fs.BoolVar(&noDaemon, "no-daemon", false, "do not use a running daemon; perform inline")
		fs.StringVar(&outFile, "out", "", "write fetched bytes to this file (optional)")
		_ = fs.Parse(os.Args[2:])
		if len(listenAddrs) == 0 {
			listenAddrs = []string{
				"/ip4/0.0.0.0/tcp/0",
				"/ip4/0.0.0.0/udp/0/quic-v1",
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

		// Prefer daemon if available
		if !noDaemon {
			if b, err := os.ReadFile(controlPath); err == nil && len(b) > 0 {
				var info struct {
					Addr string `json:"addr"`
				}
				if json.Unmarshal(b, &info) == nil && info.Addr != "" {
					// Allow enough time for the daemon to dial and fetch before sending headers
					client := &http.Client{Timeout: 2*dur + 5*time.Second}
					var reqBody = struct {
						CID     string `json:"cid"`
						Addr    string `json:"from_addr"`
						Peer    string `json:"from_peer"`
						Timeout string `json:"timeout"`
					}{CID: cidStr, Addr: fromAddr, Peer: fromPeer, Timeout: timeoutStr}
					buf, _ := json.Marshal(reqBody)
					resp, err := client.Post("http://"+info.Addr+"/get", "application/json", bytes.NewReader(buf))
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
