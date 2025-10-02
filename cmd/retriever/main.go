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

	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// staticContentRouter implements routing.ContentRouting and always returns
// the connected provider peer for any queried CID.
type staticContentRouter struct {
	provider peer.AddrInfo
}

func (s *staticContentRouter) Provide(ctx context.Context, c cid.Cid, b bool) error {
	return nil
}

func (s *staticContentRouter) ProvideMany(ctx context.Context, keys []cid.Cid) error {
	return nil
}

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

	// Build stack (bitswap) first with a static content router that
	// always returns the provider as the content source. This ensures
	// Bitswap observes subsequent connection events to the provider.
	staticRouter := &staticContentRouter{provider: info}
	stack, err := mystore.NewStackWithRouter(context.Background(), h, staticRouter)
	must(err)
	defer stack.Bitswap.Close()

	// Now connect to the provider
	connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	must(h.Connect(connectCtx, info))
	fmt.Println("Connected to provider:", pid)
	for _, a := range h.Addrs() {
		fmt.Println("Our Addr:", a.String())
	}

	// Decode CID and show both forms for sanity
	c, err := cid.Decode(cidStr)
	must(err)
	fmt.Println("Retrieving CID:   ", c.String())
	cV1, err := cid.Cast(c.Bytes())
	must(err)
	fmt.Println("Retrieving CIDv1: ", cV1.String())

	// Use a dedicated fetch timeout
	fetchCtx, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	data, err := mystore.GetBlock(fetchCtx, stack.BlockSvc, c)
	must(err)

	fmt.Printf("Fetched %d bytes: %q\n", len(data), string(data))
}
