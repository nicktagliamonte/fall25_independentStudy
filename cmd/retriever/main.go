// Connects to provider and fetches by CID

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	myhost "github.com/nicktagliamonte/fall25_independantStudy/internal/net"
	mystore "github.com/nictagliamonte/fall25_independantStudy/internal/storage"
)

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// Usage: retriever <provider_multiaddr> <provider_peer_id> <cid>
func main() {
	if len(os.Args) != 4 {
		log.Fatalf("usage: %s <provider_multiaddr> <provider_peer_id> <cid>", os.Args[0])
	}
	addrStr := os.Args[1]
	peerIDStr := os.Args[2]
	cidStr := os.Args[3]

	ctx := context.Background()

	h, err := myhost.NewHost(ctx, []string{
		"/ip4/0.0.0.0/tcp/0",
		"/ip4/0.0.0.0/udp/0/quic-v1",
	})
	must(err)
	defer h.Close()

	// Build AddrInfo for provider
	maddr, err := multiaddr.NewMultiaddr(addrStr)
	must(err)
	pid, err := peer.Decode(peerIDStr)
	must(err)
	info := peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{maddr}}

	// Connect
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	must(h.Connect(ctx, info))
	fmt.Println("Connected to provider: ", pid)

	// Fetch block by CID
	c, err := cid.Decode(cidStr)
	must(err)

	data, err := mystore.GetBlock(context.Background(), stack.BlockSvc, c)
	must(err)

	fmt.Printf("Fetched %d bytes: %q\n", len(data), string(data))
}
