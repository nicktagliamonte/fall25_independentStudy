// serves a single block via Bitswap using Boxo

package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	myhost "github.com/nicktagliamonte/fall25_independantStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independantStudy/internal/storage"
)

func main() {
	ctx := context.Background()

	h, err := myhost.NewHost(ctx, []string{
		"/ip4/0.0.0.0/tcp/0",
		"/ip4/0.0.0.0/udp/0/quic-v1",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()

	stack, err := mystore.NewStack(ctx, h)
	if err != nil {
		log.Fatal(err)
	}
	defer stack.Bitswap.Close()

	// Prep a demo payload
	payload := []byte("hello from provider " + time.Now().Format(time.RFC3339))
	c, err := mystore.PutRawBlock(ctx, stack.BlockSvc, payload)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("== Provider ==")
	fmt.Println("PeerID: ", h.ID().String())
	fmt.Println("CID: ", c.String())
	for _, a := range h.Addrs() {
		fmt.Println("Addr: ", a.String())
	}
	fmt.Printf("CID (multihash hex): %s\n", hex.EncodeToString(c.Hash()))

	// Keep running
	select {}
}
