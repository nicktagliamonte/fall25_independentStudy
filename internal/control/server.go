// Purpose: Local control server for the running node (HTTP on 127.0.0.1).

package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multiaddr"
	mygateway "github.com/nicktagliamonte/fall25_independentStudy/internal/gateway"
	mynet "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// no persistent server struct is required

// ReplicationFactorR is the enforced minimum replicas per file (Near 40%, Midrange 30%, Far 30%).
const ReplicationFactorR = 7

type PutRequest struct {
	Data string `json:"data"`
}

type PutResponse struct {
	CID          string `json:"cid"`
	MultihashHex string `json:"multihash_hex"`
	NetworkHops  *int   `json:"network_hops,omitempty"`
}

type ConnectRequest struct {
	Addr    string `json:"addr"`
	Peer    string `json:"peer"`
	Timeout string `json:"timeout"`
}

type GetRequest struct {
	Key     string `json:"key"` // Key (hex string) - primary identifier for token-based routing
	CID     string `json:"cid"` // CID (deprecated, kept for backward compatibility)
	Addr    string `json:"from_addr"`
	Peer    string `json:"from_peer"`
	Timeout string `json:"timeout"`
}

type GetResponse struct {
	Bytes       int    `json:"bytes"`
	DataB64     string `json:"data_b64"`
	NetworkHops *int   `json:"network_hops,omitempty"`
}

func wantsRawGetResponse(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "raw") {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "application/octet-stream")
}

type DeleteRequest struct {
	CID string `json:"cid"`
}

type DeleteResponse struct {
	CID     string `json:"cid"`
	Deleted bool   `json:"deleted"`
}

// simulatedRTTForPeer returns a deterministic RTT for a peer when simulate_distances=1.
// Uses index within sorted provider list to guarantee at least one Near, Midrange, Farflung.
// Caller must pass sorted provider IDs and index; returns 10ms/75ms/250ms by round-robin.
func simulatedRTTByIndex(index int) time.Duration {
	switch index % 3 {
	case 0:
		return 10 * time.Millisecond
	case 1:
		return 75 * time.Millisecond
	default:
		return 250 * time.Millisecond
	}
}

// fetchBlockFromToken fetches block data from token locations in parallel.
// Returns first successful result or error if all fail.
func fetchBlockFromToken(ctx context.Context, stack *mystore.Stack, token mystore.Token, key mystore.Key) ([]byte, error) {
	if stack == nil || len(token.Locations) == 0 {
		return nil, fmt.Errorf("stack and token locations required")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var result []byte
	var fetchErrors []error
	success := false
	for _, loc := range token.Locations {
		loc := loc
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, fetchErr := mystore.DirectFetch(ctx, stack, loc, key)
			if fetchErr == nil && stack.MessageSink != nil {
				stack.MessageSink.AddGetMessagesOut(1)
				stack.MessageSink.AddGetMessagesIn(1)
			}
			if fetchErr != nil {
				mu.Lock()
				fetchErrors = append(fetchErrors, fmt.Errorf("peer %s: %w", loc.ProviderID, fetchErr))
				mu.Unlock()
				return
			}
			mu.Lock()
			if !success && data != nil {
				result = data
				success = true
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if !success {
		return nil, fmt.Errorf("direct fetch failed from all %d locations: %v", len(token.Locations), fetchErrors)
	}
	return result, nil
}

// Start launches the control server and returns the bound address and a shutdown func.
// onShutdown: optional callback to trigger graceful node stop when /shutdown is called.
// explicitRouter: optional; when non-nil, used for DynamicRouter fallback and composed with stack's router (e.g. via NewFallbackContentRouter).
// repairProtocol: optional repair protocol for automatic repair on vector mismatch (nil disables repair).
// gateway: optional; when non-nil, used for token routing and query operations (Phase 5.3).
// storePath: optional path to persistent blockstore; when non-empty, /storage/stats returns disk_bytes for that dir.
func Start(ctx context.Context, h host.Host, stack *mystore.Stack, peers *mynet.PeerStore, metrics *NodeMetrics, onShutdown func(), explicitRouter *DynamicRouter, repairProtocol *mystore.RepairProtocol, gateway *mygateway.Gateway, storePath string) (string, func(context.Context) error, error) {
	mux := http.NewServeMux()
	router := explicitRouter
	if router == nil {
		router = NewDynamicRouter()
	}
	if stack != nil && metrics != nil {
		stack.OnAnnounce = func() { metrics.IncProviderAnnounceCount() }
	}
	// Register repair stream handler if repair protocol is available
	if repairProtocol != nil && h != nil {
		h.SetStreamHandler(mystore.RepairProtocolID, func(stream network.Stream) {
			_ = repairProtocol.HandleRepairStream(stream)
		})
	}
	// Register direct fetch stream handler for token-based routing
	if stack != nil && h != nil {
		h.SetStreamHandler(mystore.DirectFetchProtocolID, func(stream network.Stream) {
			_ = mystore.HandleDirectFetchStream(stream, stack)
		})
	}
	// restore job manager (in-memory)
	type restoreStats struct {
		OK     int   `json:"ok"`
		Failed int   `json:"failed"`
		Bytes  int64 `json:"bytes"`
		Done   bool  `json:"done"`
	}
	var jobsMu sync.Mutex
	jobs := make(map[string]*restoreStats)

	// Health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	// Metrics endpoint (JSON)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if metrics != nil && stack != nil && stack.ProviderRecords != nil {
			metrics.SetProviderRecordsCount(int64(stack.ProviderRecords.Len()))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metrics.Snapshot())
	})

	// Storage stats endpoint: returns disk_bytes for persistent store path (storage efficiency tests).
	mux.HandleFunc("/storage/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if storePath == "" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"disk_bytes": nil, "reason": "ephemeral"})
			return
		}
		info, err := os.Stat(storePath)
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if !info.IsDir() {
			_ = json.NewEncoder(w).Encode(map[string]int64{"disk_bytes": info.Size()})
			return
		}
		var total int64
		_ = filepath.Walk(storePath, func(_ string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi != nil && !fi.IsDir() {
				total += fi.Size()
			}
			return nil
		})
		_ = json.NewEncoder(w).Encode(map[string]int64{"disk_bytes": total})
	})

	// Replication status: returns replica count for a key (polls DHT token for locations).
	mux.HandleFunc("/replication/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		keyHex := r.URL.Query().Get("key")
		if keyHex == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("missing key (query param)"))
			return
		}
		key, err := mystore.ParseKey(keyHex)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(fmt.Sprintf("invalid key: %v", err)))
			return
		}
		var tokenStore routing.ValueStore
		if stack != nil {
			tokenStore = stack.TokenStore
			if tokenStore == nil && stack.DHT != nil {
				tokenStore = stack.DHT
			}
		}
		if tokenStore == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("token store not available"))
			return
		}
		ctxGet, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		token, err := mystore.GetToken(ctxGet, tokenStore, key)
		if err != nil {
			// Return 200 with replica_count=0 and error_reason for diagnostics (surfaces why GetToken fails)
			errorReason := "token_not_found"
			if strings.Contains(err.Error(), "invalid") {
				errorReason = "invalid_key"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"key":           keyHex,
				"replica_count": 0,
				"providers":     []string{},
				"error_reason":  errorReason,
				"error_detail":  err.Error(),
			})
			return
		}
		providers := make([]string, 0, len(token.Locations))
		var near, midrange, farflung int
		thresholds := mystore.DefaultRTTThresholds()
		simulateDistances := r.URL.Query().Get("simulate_distances") == "1"
		locs := token.Locations
		if simulateDistances {
			locs = make([]mystore.Location, len(token.Locations))
			copy(locs, token.Locations)
			sort.Slice(locs, func(i, j int) bool { return locs[i].ProviderID.String() < locs[j].ProviderID.String() })
		}
		for i, loc := range locs {
			providers = append(providers, loc.ProviderID.String())
			rtt := loc.RTT
			if simulateDistances && rtt == 0 {
				rtt = simulatedRTTByIndex(i)
			}
			switch mystore.ClassifyDistanceByRTT(rtt, &thresholds) {
			case mystore.DistanceNear:
				near++
			case mystore.DistanceMidrange:
				midrange++
			case mystore.DistanceFarFlung:
				farflung++
			default:
				midrange++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"key":            keyHex,
			"replica_count":  len(token.Locations),
			"providers":      providers,
			"timestamp":      token.Timestamp,
			"near_count":     near,
			"midrange_count": midrange,
			"farflung_count": farflung,
		})
	})

	// Lookup: isolated token lookup (GetToken only, no fetch). Returns lookup_latency_ms and network_hops for comparison.
	mux.HandleFunc("/lookup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		keyHex := ""
		if r.Method == http.MethodGet {
			keyHex = r.URL.Query().Get("key")
		} else {
			var req struct {
				Key string `json:"key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(err.Error()))
				return
			}
			keyHex = req.Key
		}
		if keyHex == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("missing key"))
			return
		}
		key, err := mystore.ParseKey(keyHex)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(fmt.Sprintf("invalid key: %v", err)))
			return
		}
		var tokenStore routing.ValueStore
		if stack != nil {
			tokenStore = stack.TokenStore
			if tokenStore == nil && stack.DHT != nil {
				tokenStore = stack.DHT
			}
		}
		if tokenStore == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("token store not available"))
			return
		}
		ctxLookup, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
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
		_, err = mystore.GetToken(evCtx2, tokenStore, key)
		cancel2()
		cancel()
		<-done
		latencyMs := time.Since(start).Milliseconds()
		if os.Getenv("SNG40_LOG_LOOKUP_PATHS") == "1" {
			log.Printf("control /lookup: hops=%d latency_ms=%d token_err=%v", int(hops), latencyMs, err)
		}
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"lookup_latency_ms": latencyMs,
			"network_hops":      int(hops),
			"found":             true,
		})
	})

	// HasKey: returns whether this node holds the key locally (for polling replica count across nodes).
	mux.HandleFunc("/has_key", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		keyHex := r.URL.Query().Get("key")
		if keyHex == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("missing key (query param)"))
			return
		}
		key, err := mystore.ParseKey(keyHex)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(fmt.Sprintf("invalid key: %v", err)))
			return
		}
		hasKey := false
		if stack != nil {
			ctxGet, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			data, err := mystore.GetBlockByKey(ctxGet, stack.Datastore, stack.BlockSvc, key)
			hasKey = err == nil && data != nil && len(data) > 0
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"key":     keyHex,
			"has_key": hasKey,
		})
	})

	// Restore endpoints
	type restoreReq struct {
		CIDs        []string `json:"cids"`
		Concurrency int      `json:"concurrency"`
		Timeout     string   `json:"timeout"`
		ByteBudget  int64    `json:"byte_budget"`
	}
	mux.HandleFunc("/restore", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var req restoreReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(err.Error()))
				return
			}
			if len(req.CIDs) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("missing cids"))
				return
			}
			concurrency := req.Concurrency
			if concurrency <= 0 {
				concurrency = 4
			}
			to := 20 * time.Second
			if req.Timeout != "" {
				if d, err := time.ParseDuration(req.Timeout); err == nil {
					to = d
				}
			}
			// create job
			jobID := fmt.Sprintf("r-%d", time.Now().UnixNano())
			jobsMu.Lock()
			jobs[jobID] = &restoreStats{}
			jobsMu.Unlock()
			metrics.IncRestoresStarted()
			// run async
			go func(job string, cids []string, conc int, timeout time.Duration, budget int64) {
				// execute similar to Service.RestoreFromManifest using local stack
				type task struct{ c string }
				todo := make(chan task)
				var wg sync.WaitGroup
				var mu sync.Mutex
				for i := 0; i < conc; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for t := range todo {
							// budget check
							jobsMu.Lock()
							st0 := jobs[job]
							curBytes := st0.Bytes
							jobsMu.Unlock()
							if budget > 0 && curBytes >= budget {
								return
							}
							c, err := cid.Decode(t.c)
							if err != nil {
								mu.Lock()
								jobsMu.Lock()
								st1 := jobs[job]
								st1.Failed++
								jobsMu.Unlock()
								mu.Unlock()
								continue
							}
							ctx2, cancel2 := context.WithTimeout(context.Background(), timeout)
							b, err := mystore.GetBlockByCID(ctx2, stack.BlockSvc, c)
							cancel2()
							mu.Lock()
							jobsMu.Lock()
							st2 := jobs[job]
							if err != nil {
								st2.Failed++
								metrics.AddRestoresFailed(1)
							} else {
								st2.OK++
								sz := int64(len(b))
								st2.Bytes += sz
								metrics.AddRestoresOK(1)
								metrics.AddRestoreBytes(sz)
							}
							jobsMu.Unlock()
							mu.Unlock()
						}
					}()
				}
				go func() {
					defer close(todo)
					for _, s := range cids {
						todo <- task{c: s}
						jobsMu.Lock()
						st3 := jobs[job]
						over := budget > 0 && st3.Bytes >= budget
						jobsMu.Unlock()
						if over {
							return
						}
					}
				}()
				wg.Wait()
				jobsMu.Lock()
				if st, ok := jobs[job]; ok {
					st.Done = true
				}
				jobsMu.Unlock()
			}(jobID, req.CIDs, concurrency, to, req.ByteBudget)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"job": jobID})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	// Restore status endpoint (separate route for GET requests)
	mux.HandleFunc("/restore/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("missing id"))
			return
		}
		jobsMu.Lock()
		js, ok := jobs[id]
		jobsMu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("unknown job"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(js)
	})

	// Shutdown endpoint (graceful stop)
	mux.HandleFunc("/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		// Trigger shutdown after responding to avoid client hang.
		go func() {
			time.Sleep(100 * time.Millisecond)
			if onShutdown != nil {
				onShutdown()
			}
		}()
	})

	// Neighbors endpoint: returns currently connected peers (IDs and addrs)
	mux.HandleFunc("/neighbors", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		type neighbor struct {
			Peer  string   `json:"peer"`
			Addrs []string `json:"addrs"`
		}
		seen := make(map[string]struct{})
		var out []neighbor
		for _, pid := range h.Network().Peers() {
			if pid == h.ID() {
				continue
			}
			idStr := pid.String()
			if _, ok := seen[idStr]; ok {
				continue
			}
			seen[idStr] = struct{}{}
			var addrs []string
			for _, a := range h.Peerstore().Addrs(pid) {
				addrs = append(addrs, a.String())
			}
			out = append(out, neighbor{Peer: idStr, Addrs: addrs})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	// ID endpoint: returns this node's PeerID and current addrs
	mux.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		type self struct {
			Peer  string   `json:"peer"`
			Addrs []string `json:"addrs"`
		}
		var addrs []string
		for _, a := range h.Addrs() {
			addrs = append(addrs, a.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(self{Peer: h.ID().String(), Addrs: addrs})
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
	// Synchronous (included in upload latency): JSON decode or raw body read, PutBlock (local blockstore),
	// in-memory routing table Set. Asynchronous (not included): Key→provider datastore mapping,
	// SyncTokenOnPut (DHT), ReplicateToNPeers.
	const maxPutBodyBytes = 64 << 20
	mux.HandleFunc("/put", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var blockData []byte
		var err error
		ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
		if strings.HasPrefix(ct, "application/octet-stream") {
			limited := io.LimitReader(r.Body, maxPutBodyBytes+1)
			blockData, err = io.ReadAll(limited)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(err.Error()))
				return
			}
			if len(blockData) > maxPutBodyBytes {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				_, _ = w.Write([]byte("body too large"))
				return
			}
		} else {
			var req PutRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(err.Error()))
				return
			}
			blockData = []byte(req.Data)
		}
		t0 := time.Now()
		var key mystore.Key
		var c cid.Cid
		if len(blockData) > mystore.DefaultContentChunkSize {
			key = mystore.KeyFromData(blockData)
			chunks := mystore.SplitPayloadChunks(blockData, mystore.DefaultContentChunkSize)
			chunkKeys := make([]string, 0, len(chunks))
			for i := range chunks {
				chunkKey, chunkCID, putErr := mystore.PutRawBlockIndexed(r.Context(), stack.Datastore, stack.BlockSvc, chunks[i], nil)
				if putErr != nil {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(putErr.Error()))
					return
				}
				if !c.Defined() {
					c = chunkCID
				}
				chunkKeys = append(chunkKeys, chunkKey.String())
			}
			idx := mystore.ChunkIndex{
				Version:    1,
				ChunkSize:  mystore.DefaultContentChunkSize,
				TotalBytes: len(blockData),
				ChunkKeys:  chunkKeys,
			}
			if idxErr := mystore.StoreChunkIndex(r.Context(), stack.Datastore, key, idx); idxErr != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(idxErr.Error()))
				return
			}
		} else {
			key, c, err = stack.PutBlock(r.Context(), blockData)
		}
		t1 := time.Now()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		if h != nil {
			stack.UpdateRoutingTableOnPutAsync(key, h.ID(), nil, c)
		}
		t2 := time.Now()
		if os.Getenv("SNG40_LOG_PUT_PHASES") == "1" {
			log.Printf("put phases: putblock=%v routing_table+mapping=%v (token+replicate async)", t1.Sub(t0), t2.Sub(t1))
		}
		putHops := 0
		// multihash_hex must be 64 hex chars (Key) for /replication/status and /get
		keyHex := key.String()
		resp := PutResponse{CID: c.String(), MultihashHex: keyHex, NetworkHops: &putHops}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)

		// Replicate asynchronously (matches Swarm: return after first copy; replication is background).
		// Budget is per-run (not test timeout): large clusters need time to pick connected peers and
		// complete sequential transfers; a short deadline caused replica_count=1 at higher N.
		if repairProtocol != nil && h != nil && len(blockData) > 0 {
			go func() {
				ctxRepair, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
				defer cancel()
				_ = repairProtocol.ReplicateToNPeers(ctxRepair, key, c, blockData, 6)
			}()
		}
	})

	// Delete endpoint
	mux.HandleFunc("/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req DeleteRequest
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
		err = stack.DeleteBlock(r.Context(), c)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		// Clear explicit provider hint (if dynamic router is used)
		if router != nil {
			router.ClearProviderForCID(c)
		}
		resp := DeleteResponse{CID: c.String(), Deleted: true}
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
		if metrics != nil {
			metrics.AddGetMessagesIn(1)
		}
		var req GetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(err.Error()))
			return
		}

		// Extract key from request (primary identifier for token-based routing)
		var key mystore.Key
		if req.Key != "" {
			// Key provided - use token-based routing
			var err error
			key, err = mystore.ParseKey(req.Key)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(fmt.Sprintf("invalid key: %v", err)))
				return
			}
		} else if req.CID != "" {
			// CID provided (backward compatibility) - convert to Key via routing table
			c, err := cid.Decode(req.CID)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(fmt.Sprintf("invalid identifier: %v", err)))
				return
			}
			// Get Key from routing table entry (if available)
			if stack.RoutingTable != nil {
				entry := stack.RoutingTable.GetByCID(c)
				if entry != nil && !entry.Key.IsZero() {
					key = entry.Key
				} else {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte("key not found in routing table"))
					return
				}
			} else {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("routing table not available"))
				return
			}
		} else {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("key or cid required"))
			return
		}

		d := 20 * time.Second
		if req.Timeout != "" {
			if parsed, err := time.ParseDuration(req.Timeout); err == nil {
				d = parsed
			}
		}

		ctxFetch, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		rawResponse := wantsRawGetResponse(r)
		if rawResponse {
			if idx, idxErr := mystore.GetChunkIndex(ctxFetch, stack.Datastore, key); idxErr == nil && idx != nil {
				zeroHops := 0
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", strconv.Itoa(idx.TotalBytes))
				w.Header().Set("X-Network-Hops", strconv.Itoa(zeroHops))
				w.WriteHeader(http.StatusOK)
				flusher, _ := w.(http.Flusher)
				for i := range idx.ChunkKeys {
					chunkKey, perr := mystore.ParseKey(idx.ChunkKeys[i])
					if perr != nil {
						return
					}
					chunkData, gerr := mystore.GetBlockByKey(ctxFetch, stack.Datastore, stack.BlockSvc, chunkKey)
					if gerr != nil || chunkData == nil {
						return
					}
					_, _ = w.Write(chunkData)
					if i == 0 && flusher != nil {
						flusher.Flush()
					}
				}
				return
			}
		}
		start := time.Now()
		var b []byte
		var err error
		var networkHops *int
		zeroHops := 0
		fetchedFromRemote := false
		// B.3: networkHops populated in all code paths:
		// - Local: GetBlockByKey hit → networkHops = &zeroHops (correct; no DHT lookup)
		// - Gateway: Query hit, fetchBlockFromToken success → networkHops = &zeroHops (gateway path; DHT hops not tracked)
		// - stack.GetBlock: DHT GetToken + DirectFetch → networkHops = &hops from GetBlock return (DHT lookup hops)
		if localData, localErr := mystore.ResolvePayloadByKeyLocal(ctxFetch, stack.Datastore, stack.BlockSvc, key); localErr == nil && localData != nil {
			b = localData
			networkHops = &zeroHops
		} else if gateway != nil {
			results, qErr := gateway.Query(ctxFetch, mygateway.Query{Pattern: key.String()})
			if qErr == nil && len(results) > 0 {
				var token mystore.Token
				if token.Unmarshal(results[0].Value) == nil && token.Validate() == nil && len(token.Locations) > 0 {
					b, err = fetchBlockFromToken(ctxFetch, stack, token, key)
					if err == nil {
						networkHops = &zeroHops
						fetchedFromRemote = true
					}
				}
			}
		}
		if b == nil && err == nil {
			var hops int
			b, hops, err = stack.GetBlock(ctxFetch, key)
			if err == nil {
				networkHops = &hops
				fetchedFromRemote = true
			}
		}
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(err.Error()))
			return
		}

		if metrics != nil {
			metrics.SetProviderDiscoveryLatencyNs(time.Since(start).Nanoseconds())
		}

		if rawResponse {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			if networkHops != nil {
				w.Header().Set("X-Network-Hops", strconv.Itoa(*networkHops))
			}
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			firstBytes := len(b)
			if firstBytes > mystore.DefaultContentChunkSize {
				firstBytes = mystore.DefaultContentChunkSize
			}
			if firstBytes > 0 {
				_, _ = w.Write(b[:firstBytes])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if firstBytes < len(b) {
				_, _ = w.Write(b[firstBytes:])
			}
		} else {
			resp := GetResponse{Bytes: len(b), DataB64: base64.StdEncoding.EncodeToString(b), NetworkHops: networkHops}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(&resp); err != nil {
				return
			}
		}

		// Post-response: persist replica, verify replication vector, repair — must not delay first byte
		// (curl time_starttransfer and clients otherwise include PutBlock + VerifyKeyStateWithRepVector).
		keyCopy := key
		bCopy := append([]byte(nil), b...)
		fetched := fetchedFromRemote
		stackRef := stack
		hostRef := h
		repairRef := repairProtocol
		go func() {
			if fetched && stackRef != nil && len(bCopy) > 0 {
				ctxStore, cancelStore := context.WithTimeout(context.Background(), 10*time.Second)
				_, _, _ = stackRef.PutBlock(ctxStore, bCopy)
				cancelStore()
			}
			if stackRef == nil {
				return
			}
			c, err := mystore.GetCIDFromKey(context.Background(), stackRef.Datastore, keyCopy)
			if err != nil || !c.Defined() {
				return
			}
			tokenStore := stackRef.TokenStore
			if tokenStore == nil && stackRef.DHT != nil {
				tokenStore = stackRef.DHT
			}
			rt := stackRef.RoutingTable
			if rt == nil || tokenStore == nil || hostRef == nil {
				return
			}
			ctxVerify, cancelVerify := context.WithTimeout(context.Background(), 5*time.Second)
			verification, verifyErr := mystore.VerifyKeyStateWithRepVector(
				ctxVerify,
				keyCopy,
				rt,
				tokenStore,
				hostRef.ID(),
				nil,
				ReplicationFactorR,
				nil,
			)
			cancelVerify()
			if verifyErr != nil || verification == nil || verification.IsSynchronized {
				return
			}
			if repairRef != nil && len(bCopy) > 0 {
				repairB := append([]byte(nil), bCopy...)
				go func() {
					ctxRepair, cancelRepair := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancelRepair()
					_, _ = repairRef.TriggerRepair(ctxRepair, keyCopy, verification, repairB)
				}()
			}
		}()
	})

	// Snapshot endpoint: returns local indexed Keys/CIDs (Key-based storage; CID for compatibility)
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		limit := 1000
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 100000 {
				limit = n
			}
		}
		startAfter := r.URL.Query().Get("cursor")
		cids, err := mystore.ListIndexedCIDs(r.Context(), stack.Datastore, limit, startAfter)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cids":  cids,
			"next":  "",
			"count": len(cids),
		})
	})

	registerNamespaceHandlers(mux, stack, h, repairProtocol)

	listenAddr := "127.0.0.1:0"
	if a := os.Getenv("SNG40_CONTROL_LISTEN"); a != "" {
		listenAddr = a
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return "", nil, err
	}

	s := &http.Server{Handler: mux}
	go func() {
		_ = s.Serve(ln)
	}()

	shutdown := func(ctx context.Context) error { return s.Shutdown(ctx) }
	addr := ln.Addr().String()
	if strings.HasPrefix(addr, "0.0.0.0:") {
		addr = "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return addr, shutdown, nil
}
