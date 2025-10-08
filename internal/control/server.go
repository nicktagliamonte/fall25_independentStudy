// Purpose: Local control server for the running node (HTTP on 127.0.0.1).

package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"encoding/base64"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// no persistent server struct is required for this simple control plane

type PutRequest struct {
	Data string `json:"data"`
}

type PutResponse struct {
	CID          string `json:"cid"`
	MultihashHex string `json:"multihash_hex"`
}

type ConnectRequest struct {
	Addr    string `json:"addr"`
	Peer    string `json:"peer"`
	Timeout string `json:"timeout"`
}

type GetRequest struct {
	CID     string `json:"cid"`
	Addr    string `json:"from_addr"`
	Peer    string `json:"from_peer"`
	Timeout string `json:"timeout"`
}

type GetResponse struct {
	Bytes   int    `json:"bytes"`
	DataB64 string `json:"data_b64"`
}

// Start launches the control server and returns the bound address and a shutdown func.
func Start(ctx context.Context, h host.Host, stack *mystore.Stack) (string, func(context.Context) error, error) {
	mux := http.NewServeMux()
	router := NewDynamicRouter()

	// Health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	// Put endpoint
	mux.HandleFunc("/put", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req PutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		c, err := mystore.PutRawBlock(r.Context(), stack.BlockSvc, []byte(req.Data))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		resp := PutResponse{CID: c.String(), MultihashHex: fmt.Sprintf("%x", c.Hash())}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Connect endpoint
	mux.HandleFunc("/connect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req ConnectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		maddr, err := multiaddr.NewMultiaddr(req.Addr)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		pid, err := peer.Decode(req.Peer)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		info := peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{maddr}}
		// If attempting to connect to self, treat as success without dialing
		if pid == h.ID() {
			w.WriteHeader(http.StatusOK)
			return
		}
		d := 10 * time.Second
		if req.Timeout != "" {
			if parsed, err := time.ParseDuration(req.Timeout); err == nil {
				d = parsed
			}
		}
		ctxDial, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		if err := h.Connect(ctxDial, info); err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// Get endpoint
	mux.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req GetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		c, err := cid.Decode(req.CID)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		maddr, err := multiaddr.NewMultiaddr(req.Addr)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		pid, err := peer.Decode(req.Peer)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		info := peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{maddr}}
		// If the provider is this node, read directly from the existing stack
		if pid == h.ID() {
			b, err := mystore.GetBlock(r.Context(), stack.BlockSvc, c)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(err.Error()))
				return
			}
			resp := GetResponse{Bytes: len(b), DataB64: base64.StdEncoding.EncodeToString(b)}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&resp)
			return
		}

		// Else, use a router-equipped ephemeral stack to fetch from remote
		router.SetProviderForCID(c, info)
		st, err := mystore.NewStackWithRouter(r.Context(), h, router)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		defer st.Bitswap.Close()

		d := 20 * time.Second
		if req.Timeout != "" {
			if parsed, err := time.ParseDuration(req.Timeout); err == nil {
				d = parsed
			}
		}
		ctxDial, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		if err := h.Connect(ctxDial, info); err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		ctxFetch, cancel2 := context.WithTimeout(r.Context(), d)
		defer cancel2()
		b, err := mystore.GetBlock(ctxFetch, st.BlockSvc, c)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		resp := GetResponse{Bytes: len(b), DataB64: base64.StdEncoding.EncodeToString(b)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&resp)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}

	s := &http.Server{Handler: mux}
	go func() {
		_ = s.Serve(ln)
	}()

	shutdown := func(ctx context.Context) error { return s.Shutdown(ctx) }
	return ln.Addr().String(), shutdown, nil
}
