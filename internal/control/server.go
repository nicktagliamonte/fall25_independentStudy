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
	"sync"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	mynet "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// no persistent server struct is required

// PutRequest is the JSON request body accepted by POST /put.
type PutRequest struct {
	// Data is the raw block content to store, as a plain (non-base64)
	// string. It is converted directly to []byte before being written,
	// so any encoding of Data is the caller's responsibility.
	Data string `json:"data"`
}

// PutResponse is the JSON response body returned by POST /put on success
// (HTTP 200).
type PutResponse struct {
	// CID is the string form of the content identifier computed for the
	// stored block (c.String()).
	CID string `json:"cid"`
	// MultihashHex is the lowercase hex encoding of the block's
	// multihash (c.Hash(), formatted with "%x").
	MultihashHex string `json:"multihash_hex"`
}

// ConnectRequest is the JSON request body accepted by POST /connect.
type ConnectRequest struct {
	// Addr is a single multiaddr string (e.g. "/ip4/1.2.3.4/tcp/4001")
	// identifying the network address to dial. Must parse via
	// multiaddr.NewMultiaddr.
	Addr string `json:"addr"`
	// Peer is the target's PeerID, base58/CID-encoded as accepted by
	// peer.Decode.
	Peer string `json:"peer"`
	// Timeout is an optional Go duration string (e.g. "10s") parsed via
	// time.ParseDuration. If empty or unparsable, a 10s default is used.
	Timeout string `json:"timeout"`
}

// GetRequest is the JSON request body accepted by POST /get.
type GetRequest struct {
	// CID is the string form of the content identifier to fetch, parsed
	// via cid.Decode.
	CID string `json:"cid"`
	// Addr is the multiaddr of the peer to fetch from, used only when
	// Peer is not this node's own ID. Must parse via
	// multiaddr.NewMultiaddr.
	Addr string `json:"from_addr"`
	// Peer is the PeerID of the node believed to hold the block, parsed
	// via peer.Decode. If it equals this node's own ID, the block is
	// read from the local store instead of being fetched remotely.
	Peer string `json:"from_peer"`
	// Timeout is an optional Go duration string used both as the dial
	// timeout and the fetch timeout when fetching from a remote peer
	// (default 20s if empty/unparsable). Unused for local (self) reads.
	Timeout string `json:"timeout"`
}

// GetResponse is the JSON response body returned by POST /get on success
// (HTTP 200), for both the local-read and remote-fetch code paths.
type GetResponse struct {
	// Bytes is the length of the fetched block, in bytes (len(data)).
	Bytes int `json:"bytes"`
	// DataB64 is the block's raw bytes, standard base64-encoded.
	DataB64 string `json:"data_b64"`
}

// Start launches the loopback-only ("127.0.0.1:0", OS-assigned ephemeral
// port) HTTP control server for this node and registers every control
// endpoint (see the per-handler comments below) on a fresh
// http.ServeMux. The server is served in a background goroutine; Start
// itself returns as soon as the listener is bound.
//
// Parameters:
//   - ctx: accepted for interface consistency with the rest of the
//     codebase; not currently used to cancel the server itself (shutdown
//     is instead performed via the returned shutdown func or the
//     /shutdown endpoint). Per-request contexts (r.Context()) are used for
//     individual handler operations and deadlines.
//   - h: the libp2p host used to answer /id and /neighbors, to dial peers
//     for /connect and /get, and as the connection target for handshake
//     verification in /get.
//   - stack: the node's local storage stack (datastore + block service),
//     used for /put, /get (local reads and persisting remote fetches),
//     /events, /restore (as the source of blocks to restore), and
//     /snapshot.
//   - peers: the node's peer store, used by /peers to list dial
//     candidates with their scoring metadata.
//   - metrics: shared counters updated by /restore (RestoresStarted,
//     RestoresOK/Failed, RestoreBytes) and reported verbatim by /metrics.
//   - onShutdown: optional callback invoked (after a short delay, from a
//     background goroutine) when GET /shutdown is called, to let the
//     caller trigger a graceful node stop. May be nil, in which case
//     /shutdown still returns 200 but performs no shutdown action.
//
// Returns:
//   - string: the bound address of the listener (e.g. "127.0.0.1:54321"),
//     as reported by net.Listener.Addr().String(). This is the address
//     scripts/SNG should use to reach the control endpoints.
//   - func(context.Context) error: a shutdown function that calls
//     http.Server.Shutdown with the given context, gracefully stopping
//     the HTTP server (waiting for in-flight requests). Returns whatever
//     error http.Server.Shutdown returns (e.g. context deadline
//     exceeded), or nil on clean shutdown.
//   - error: non-nil only if the TCP listener could not be created (e.g.
//     port/permission issue); in that case the first two return values
//     are "" and nil and no server is started.
func Start(ctx context.Context, h host.Host, stack *mystore.Stack, peers *mynet.PeerStore, metrics *NodeMetrics, onShutdown func()) (string, func(context.Context) error, error) {
	mux := http.NewServeMux()
	router := NewDynamicRouter()
	// restore job manager (in-memory)

	// restoreStats is the JSON-serializable status of a single async
	// restore job, both stored in the jobs map and returned verbatim by
	// GET /restore/status.
	type restoreStats struct {
		// OK is the count of CIDs successfully restored so far.
		OK int `json:"ok"`
		// Failed is the count of CIDs that failed to decode or fetch so far.
		Failed int `json:"failed"`
		// Bytes is the total size, in bytes, of successfully restored blocks so far.
		Bytes int64 `json:"bytes"`
		// Done is true once every worker goroutine for this job has
		// finished processing (whether the job ran to completion or
		// stopped early due to ByteBudget).
		Done bool `json:"done"`
	}
	// jobsMu guards all reads/writes to the jobs map (including the
	// restoreStats values it points to), since jobs are mutated
	// concurrently by multiple per-job worker goroutines as well as read
	// by the /restore/status handler.
	var jobsMu sync.Mutex
	// jobs maps a job ID (as returned by POST /restore) to its
	// in-progress/completed status. Entries are never removed, so this
	// map grows for the lifetime of the process (one entry per restore
	// job ever started).
	jobs := make(map[string]*restoreStats)

	// Health endpoint: GET /health. Always responds with HTTP 200 and a
	// plain-text body of "ok" regardless of method or request content;
	// used as a liveness check by scripts/SNG.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	// Metrics endpoint: GET /metrics (though method is not actually
	// checked). Responds HTTP 200 with a JSON-encoded MetricsSnapshot
	// (see metrics.go) reflecting the current values of every counter on
	// metrics.
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metrics.Snapshot())
	})

	// restoreReq is the JSON request body accepted by POST /restore.
	type restoreReq struct {
		// CIDs is the list of content identifiers (string form, decoded
		// via cid.Decode per-item) to restore. Must be non-empty.
		CIDs []string `json:"cids"`
		// Concurrency is the number of worker goroutines used to fetch
		// blocks in parallel. If <= 0, defaults to 4.
		Concurrency int `json:"concurrency"`
		// Timeout is a Go duration string applied as the per-block fetch
		// timeout (time.ParseDuration). If empty/unparsable, defaults to
		// 20s.
		Timeout string `json:"timeout"`
		// ByteBudget, if > 0, caps the total bytes restored for this job;
		// once the running total reaches or exceeds ByteBudget, workers
		// stop pulling new CIDs from the queue and the feeder goroutine
		// stops enqueueing more (any CIDs not yet started are simply
		// left unprocessed, not counted as failed).
		ByteBudget int64 `json:"byte_budget"`
	}
	// Restore endpoint: POST /restore starts an asynchronous restore job
	// that fetches the given CIDs' blocks into stack's local store via a
	// worker pool, and reports HTTP 202 with {"job": "<job-id>"} so the
	// caller can poll GET /restore/status?id=<job-id>. Any other HTTP
	// method returns 405. See the inner goroutine below for the job's
	// execution model.
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
			//
			// Job execution model: a bounded worker pool of `conc`
			// goroutines consumes CID tasks from the unbuffered `todo`
			// channel. A single feeder goroutine pushes every CID in
			// `cids` onto `todo` in order, checking the job's
			// accumulated Bytes against `budget` after each send and
			// stopping early (without closing early — close still
			// happens via defer) once the budget is met or exceeded.
			// Each worker also re-checks the budget before starting a
			// new task, decodes the CID, fetches the block via
			// mystore.GetBlock with a fresh per-block `timeout`
			// deadline, and updates both the job's restoreStats (OK/
			// Failed/Bytes, guarded by jobsMu) and the shared `metrics`
			// counters. Once the feeder has closed `todo` and every
			// worker has drained it (wg.Wait), the job's Done flag is
			// set to true. Note: `mu` below is redundant with jobsMu —
			// every critical section that takes `mu` also immediately
			// takes jobsMu, so `mu` provides no additional protection
			// (see refactor notes).
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
							b, err := mystore.GetBlock(ctx2, stack.BlockSvc, c)
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

	// Restore status endpoint: GET /restore/status?id=<job-id> reports
	// the current restoreStats for a previously-started restore job as
	// JSON (HTTP 200): {"ok": N, "failed": N, "bytes": N, "done": bool}.
	// Returns 405 for non-GET methods, 400 if the "id" query param is
	// missing, and 404 if no job with that ID is known (including jobs
	// from before a process restart, since job state is in-memory only).
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

	// Shutdown endpoint: GET /shutdown responds HTTP 200 with an empty
	// body immediately, then (from a background goroutine, after a
	// 100ms delay to let the HTTP response actually flush to the client
	// before the process potentially exits) invokes onShutdown if it is
	// non-nil. Non-GET methods get 405. Note this uses GET despite being
	// a state-changing/irreversible operation; there is no
	// confirmation step or idempotency guard beyond onShutdown's own
	// behavior.
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

	// Neighbors endpoint: GET /neighbors returns HTTP 200 with a JSON
	// array of currently-connected peers, deduplicated by PeerID and
	// excluding this node's own ID:
	// [{"peer": "<peer-id>", "addrs": ["<multiaddr>", ...]}, ...].
	// Addrs are read from the host's peerstore (h.Peerstore().Addrs),
	// which may include stale/unreachable addresses in addition to the
	// live connection's address. Non-GET methods get 405.
	mux.HandleFunc("/neighbors", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// neighbor is one entry in the /neighbors JSON array response.
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

	// ID endpoint: GET /id returns HTTP 200 with this node's PeerID and
	// its currently-advertised listen addresses (h.Addrs()):
	// {"peer": "<peer-id>", "addrs": ["<multiaddr>", ...]}. Per
	// docs/SCENARIO_STATUS.txt, scripts read this to seed
	// SNG40_SEEDS for other nodes. Non-GET methods get 405.
	mux.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// self is the JSON shape returned by /id.
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

	// Events endpoint: GET /events?limit=N returns HTTP 200 with a JSON
	// array of up to `limit` (default 50, clamped to (0,1000]) most
	// recent "peer_added" events, walking backward from the local
	// state-chain head (mystore.ListRecentFromHead) so results are
	// newest-first. Returns [] if there is no chain head yet. Returns
	// 500 with the error text as the body if the underlying chain walk
	// fails (e.g. datastore error); 405 for non-GET methods.
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
		// eventOut is one entry in the /events JSON array response. Prev
		// is the CID (string form) of the previous chain entry, or
		// omitted/null for the first event in the chain.
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

	// Put endpoint: POST /put stores req.Data (raw bytes of the JSON
	// "data" string) as a content-addressed, indexed block via
	// mystore.PutRawBlockIndexed and responds HTTP 200 with a
	// PutResponse ({"cid": "...", "multihash_hex": "..."}). Returns 400
	// for an unparsable JSON body, 500 if the underlying store write
	// fails, 405 for non-POST methods.
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
		c, err := mystore.PutRawBlockIndexed(r.Context(), stack.Datastore, stack.BlockSvc, []byte(req.Data))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		resp := PutResponse{CID: c.String(), MultihashHex: fmt.Sprintf("%x", c.Hash())}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Peers endpoint: GET /peers?limit=N returns HTTP 200 with a JSON
	// array of up to `limit` (default 20, clamped to (0,200]) known dial
	// candidates from the node's PeerStore (peers.GetDialCandidates,
	// called with wantServices=0 meaning "any services" and no
	// exclusion set), sorted by that store's internal score/recency
	// ordering. This endpoint is NOT listed in
	// docs/FOR_NEXT_WEEK.txt / docs/SCENARIO_STATUS.txt's documented
	// endpoint set. Non-GET methods get 405.
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
		// peerOut is one entry in the /peers JSON array response,
		// combining a peer's address info with its PeerStore scoring
		// metadata (see net.PeerRecord).
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

	// Connect endpoint: POST /connect parses req.Addr as a multiaddr and
	// req.Peer as a PeerID, then dials that peer via h.Connect with a
	// timeout (req.Timeout, Go duration string, default 10s). Responds
	// HTTP 200 on success (including the special case where req.Peer is
	// this node's own ID, which short-circuits without dialing) or when
	// already connected. Returns 400 for a malformed body, bad multiaddr,
	// or bad PeerID; 502 (StatusBadGateway) if the dial itself fails
	// (with the dial error as the response body); 405 for non-POST
	// methods. No response body is written on success.
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

	// Get endpoint: POST /get fetches the block for req.CID and responds
	// HTTP 200 with a GetResponse ({"bytes": N, "data_b64": "..."}).
	// Two code paths:
	//
	//  1. Local read: if req.Peer (from_peer) decodes to this node's own
	//     ID, the block is read straight from stack via
	//     mystore.GetBlockIndexed. Returns 404 if not found locally.
	//
	//  2. Remote fetch: otherwise, the handler registers req.Peer/
	//     req.Addr as the sole provider for the CID on the package-level
	//     `router` (DynamicRouter), builds a throwaway Bitswap-backed
	//     Stack around it (mystore.NewStackWithRouter, closed via
	//     st.Bitswap.Close() when the handler returns), dials the peer
	//     (timeout: req.Timeout, default 20s), then performs a
	//     token-based handshake (mynet.PerformHandshake) before allowing
	//     any Bitswap traffic. The handshake requires both SNG40_CA_PUB
	//     (a base64-encoded 32-byte ed25519 public key) and SNG40_TOKEN
	//     environment variables to be set; if either is missing, or
	//     SNG40_CA_PUB does not decode to exactly 32 bytes, the handler
	//     returns 500 without attempting the handshake or any dial-time
	//     side effects beyond the already-established connection. If the
	//     handshake fails, the peer connection is force-closed
	//     (h.Network().ClosePeer) and 502 is returned. On handshake
	//     success, the peer is recorded via
	//     mystore.AppendPeerAddedIfNew (best-effort; errors ignored),
	//     then the block is fetched with a fresh timeout via
	//     mystore.GetBlockIndexed against the *local* datastore (for
	//     indexing) but the *ephemeral* remote-routed BlockService (for
	//     the actual Bitswap transfer); a 404 is returned if the fetch
	//     fails. On success the fetched bytes are also best-effort
	//     persisted into the local store (mystore.PutRawBlockIndexed,
	//     errors ignored) before the response is written, so a
	//     successful remote /get has the side effect of durably caching
	//     the block locally.
	//
	// Returns 400 for a malformed body, bad CID, bad multiaddr, or bad
	// PeerID; 502 for dial or handshake failure; 500 for stack
	// construction failure or missing/invalid token env vars; 404 if the
	// block cannot be fetched; 405 for non-POST methods.
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
			b, err := mystore.GetBlockIndexed(r.Context(), stack.Datastore, stack.BlockSvc, c)
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
		b, err := mystore.GetBlockIndexed(ctxFetch, stack.Datastore, st.BlockSvc, c)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		// Persist fetched block into the daemon's local store for durability/indexing.
		// Best-effort: ignore error to still serve the response body to the client.
		_, _ = mystore.PutRawBlockIndexed(r.Context(), stack.Datastore, stack.BlockSvc, b)
		resp := GetResponse{Bytes: len(b), DataB64: base64.StdEncoding.EncodeToString(b)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&resp)
	})

	// Snapshot endpoint: GET /snapshot?limit=N&cursor=<cid> returns HTTP
	// 200 with the node's locally-indexed CIDs:
	// {"cids": [...], "count": N, "next": ""}, matching the shape
	// documented in docs/SCENARIO_STATUS.txt. limit defaults to 1000
	// (clamped to (0,100000]); cursor is passed through as startAfter to
	// mystore.ListIndexedCIDs to resume after a given CID. Note: "next"
	// is always the empty string in this implementation regardless of
	// whether more results exist beyond `limit` — callers cannot detect
	// truncation from the response alone; they would need to compare
	// len(cids) to limit and re-query with cursor=<last cid> themselves.
	// Returns 500 with the error text as the body if the datastore query
	// fails; 405 for non-GET methods.
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

	// Bind to an OS-assigned loopback-only port; the control server is
	// never intended to be reachable from outside the host.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}

	s := &http.Server{Handler: mux}
	go func() {
		// Serve blocks until the listener is closed (e.g. via
		// shutdown() below) or errors; the error is intentionally
		// discarded since http.ErrServerClosed is the expected
		// outcome on graceful shutdown and there is no caller left
		// to report other errors to.
		_ = s.Serve(ln)
	}()

	shutdown := func(ctx context.Context) error { return s.Shutdown(ctx) }
	return ln.Addr().String(), shutdown, nil
}
