// serves a single block via Bitswap using Boxo

package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"time"

	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
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

	// Also expose a trivial HTTP debug endpoint to fetch by CID for sanity
	http.HandleFunc("/cid/", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		cidStr := r.URL.Path[len("/cid/"):]
		if cidStr == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("missing cid"))
			return
		}
		data, err := mystore.GetBlock(ctx, stack.BlockSvc, c)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		_, _ = w.Write(data)
	})
	go func() { _ = http.ListenAndServe("127.0.0.1:0", nil) }()

	// Keep running
	select {}
}
