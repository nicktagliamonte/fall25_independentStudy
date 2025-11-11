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

	"strconv"

	"os"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	mynet "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// no persistent server struct is required

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
func Start(ctx context.Context, h host.Host, stack *mystore.Stack, peers *mynet.PeerStore, metrics *NodeMetrics) (string, func(context.Context) error, error) {
	mux := http.NewServeMux()
	router := NewDynamicRouter()

	// Health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	// Metrics endpoint (JSON)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metrics.Snapshot())
	})

	// Events endpoint (recent peer_added events; newest-first)
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		limit := 50
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		entries, err := mystore.ListRecentFromHead(r.Context(), stack.Datastore, stack.BlockSvc, limit)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		type eventOut struct {
			CID  string  `json:"cid"`
			Type string  `json:"type"`
			Ts   int64   `json:"ts"`
			Peer string  `json:"peer"`
			Prev *string `json:"prev,omitempty"`
		}
		out := make([]eventOut, 0, len(entries))
		for _, e := range entries {
			if e.Event == nil {
				continue
			}
			out = append(out, eventOut{
				CID:  e.CID.String(),
				Type: e.Event.Type,
				Ts:   e.Event.Ts,
				Peer: e.Event.Peer,
				Prev: e.Event.Prev,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
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

	// Peers endpoint
	mux.HandleFunc("/peers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		limit := 20
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		infos, meta := peers.GetDialCandidates(limit, 0, nil)
		// shape response
		type peerOut struct {
			Peer   string   `json:"peer"`
			Addrs  []string `json:"addrs"`
			Score  float64  `json:"score"`
			Seen   int64    `json:"last_seen_unix"`
			Tried  int64    `json:"last_tried_unix"`
			Succ   int64    `json:"last_succ_unix"`
			Fails  int      `json:"failure_count"`
			Source string   `json:"source"`
		}
		out := make([]peerOut, 0, len(infos))
		for i, info := range infos {
			po := peerOut{Peer: info.ID.String(), Score: meta[i].Score, Seen: meta[i].LastSeenUnix, Tried: meta[i].LastTriedUnix, Succ: meta[i].LastSuccUnix, Fails: meta[i].FailureCount, Source: meta[i].Source}
			for _, a := range info.Addrs {
				po.Addrs = append(po.Addrs, a.String())
			}
			out = append(out, po)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
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
		// Verify peer before initiating any Bitswap traffic (token-based admission).
		caB64 := os.Getenv("SNG40_CA_PUB")
		token := os.Getenv("SNG40_TOKEN")
		if caB64 == "" || token == "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("missing token env: set SNG40_CA_PUB and SNG40_TOKEN"))
			return
		}
		caPub, err := base64.StdEncoding.DecodeString(caB64)
		if err != nil || len(caPub) != 32 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("invalid SNG40_CA_PUB"))
			return
		}
		pol := mynet.HandshakePolicy{Timeout: d, MinAgentVersion: "sng40/0.1.0", ServicesAllow: ^uint64(0), RequireCredential: true, AuthScheme: "token-ed25519-v1", CAPubKeys: [][]byte{caPub}, Token: token}
		// include our current state head/height in handshake
		hcid, hgt, _ := mystore.GetHead(r.Context(), stack.Datastore)
		local := mynet.HandshakeLocal{Agent: "sng40/0.1.0", Services: ^uint64(0), StartHeight: 0}
		if hcid.Defined() {
			local.StateHeadCID = hcid.String()
		}
		local.StateHeight = hgt
		if _, err := mynet.PerformHandshake(r.Context(), h, pid, pol, local); err != nil {
			// Drop the connection if handshake fails.
			h.Network().ClosePeer(pid)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		_, _, _, _ = mystore.AppendPeerAddedIfNew(r.Context(), stack.Datastore, stack.BlockSvc, pid.String())
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
