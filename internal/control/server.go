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
	mytuplespace "github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

// no persistent server struct is required

// getRemoteOnlyQuery reports whether the client requested a network path for /get (skip local chunk
// index, local payload resolution, and gateway shortcut so stack.GetBlock runs). This is largely
// used in testing scripts to measure the network path.
//
// Parameters:
//   - r (*http.Request): the incoming request; the "remote_only" query parameter is inspected.
//
// Returns:
//   - (bool): true if remote_only is "1", "true", or "yes" (case-insensitive, trimmed).
func getRemoteOnlyQuery(r *http.Request) bool {
	v := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("remote_only")))
	return v == "1" || v == "true" || v == "yes"
}

type instrumentedTupleSpace interface {
	TsReadWithStats(string) ([]byte, mytuplespace.IndexedQueryStats, error)
	MutationSnapshot() mytuplespace.IndexMutationStats
}

type instrumentedTuplePutSpace interface {
	TsPutWithMutationStats(string, []byte) (int, mytuplespace.IndexMutationStats, error)
}

type tupleQueryResponse struct {
	Pattern       string                          `json:"pattern"`
	ValueBase64   string                          `json:"value_base64,omitempty"`
	QueryStats    mytuplespace.IndexedQueryStats  `json:"query_stats"`
	MutationStats mytuplespace.IndexMutationStats `json:"mutation_stats"`
}

type tuplePutRequest struct {
	Name        string   `json:"name"`
	Names       []string `json:"names,omitempty"`
	ValueBase64 string   `json:"value_base64"`
	Copies      int      `json:"copies,omitempty"`
	Concurrency int      `json:"concurrency,omitempty"`
}

type tuplePutResponse struct {
	Requested      int                             `json:"requested"`
	Succeeded      int                             `json:"succeeded"`
	Failed         int                             `json:"failed"`
	Retried        int                             `json:"retried,omitempty"`
	FailureReasons map[string]int                  `json:"failure_reasons,omitempty"`
	FailureSamples []tuplePutFailure               `json:"failure_samples,omitempty"`
	DurationNS     int64                           `json:"duration_ns"`
	MutationBefore mytuplespace.IndexMutationStats `json:"mutation_before"`
	MutationDelta  mytuplespace.IndexMutationStats `json:"mutation_delta"`
	MutationStats  mytuplespace.IndexMutationStats `json:"mutation_stats"`
}

type tuplePutFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

const tuplePutMaxAttempts = 4

// retryableTuplePutError identifies index-stage failures that occur before the
// authoritative tuple publication. The index is a repairable hint and its
// insertion is idempotent, so retrying these pre-publication failures cannot
// create an extra tuple instance. Exact tuple-owner failures are deliberately
// excluded because a lost acknowledgment there may follow a committed put.
func retryableTuplePutError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "index authority") ||
		strings.Contains(message, "index mutation") ||
		strings.Contains(message, "index overlay route") ||
		strings.Contains(message, "index-owner") ||
		strings.Contains(message, "pht")
}

func tuplePutFailureReason(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "stale index mutation authority"):
		return "stale_authority"
	case strings.Contains(message, "resolve index authority"):
		return "authority_resolution"
	case strings.Contains(message, "no index overlay route"),
		strings.Contains(message, "index-owner stream"),
		strings.Contains(message, "read index response"):
		return "index_route"
	case strings.Contains(message, "no tuple overlay route"),
		strings.Contains(message, "tuple-owner stream"),
		strings.Contains(message, "read tuple response"):
		return "tuple_route"
	case strings.Contains(message, "pht"):
		return "pht"
	case strings.Contains(message, "deadline"), strings.Contains(message, "timeout"):
		return "timeout"
	default:
		return "other"
	}
}

func addMutationStats(total *mytuplespace.IndexMutationStats, add mytuplespace.IndexMutationStats) {
	total.Total += add.Total
	total.Local += add.Local
	total.Remote += add.Remote
	total.Failures += add.Failures
	total.DurationNS += add.DurationNS
	total.AuthorityClaims += add.AuthorityClaims
	total.AuthorityTransitions += add.AuthorityTransitions
	total.AuthorityRenewals += add.AuthorityRenewals
	total.FenceRejections += add.FenceRejections
	if len(total.PerShard) < len(add.PerShard) {
		grown := make([]uint64, len(add.PerShard))
		copy(grown, total.PerShard)
		total.PerShard = grown
	}
	for shard, value := range add.PerShard {
		total.PerShard[shard] += value
	}
}

func registerTupleExperimentEndpoints(mux *http.ServeMux, gateway *mygateway.Gateway) {
	mux.HandleFunc("/tuple/query", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		pattern := strings.TrimSpace(r.URL.Query().Get("pattern"))
		if pattern == "" {
			http.Error(w, `{"error":"missing pattern"}`, http.StatusBadRequest)
			return
		}
		if gateway == nil || gateway.TupleSpace == nil {
			http.Error(w, `{"error":"tuple space unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		instrumented, ok := gateway.TupleSpace.(instrumentedTupleSpace)
		if !ok {
			http.Error(w, `{"error":"tuple-space instrumentation unavailable"}`, http.StatusNotImplemented)
			return
		}
		value, queryStats, err := instrumented.TsReadWithStats(pattern)
		response := tupleQueryResponse{
			Pattern:       pattern,
			QueryStats:    queryStats,
			MutationStats: instrumented.MutationSnapshot(),
		}
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(struct {
				Error    string             `json:"error"`
				Response tupleQueryResponse `json:"response"`
			}{Error: err.Error(), Response: response})
			return
		}
		response.ValueBase64 = base64.StdEncoding.EncodeToString(value)
		_ = json.NewEncoder(w).Encode(response)
	})

	mux.HandleFunc("/tuple/put", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if gateway == nil || gateway.TupleSpace == nil {
			http.Error(w, `{"error":"tuple space unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		instrumented, ok := gateway.TupleSpace.(instrumentedTupleSpace)
		if !ok {
			http.Error(w, `{"error":"tuple-space instrumentation unavailable"}`, http.StatusNotImplemented)
			return
		}
		putInstrumented, ok := gateway.TupleSpace.(instrumentedTuplePutSpace)
		if !ok {
			http.Error(w, `{"error":"tuple put instrumentation unavailable"}`, http.StatusNotImplemented)
			return
		}
		var request tuplePutRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
			http.Error(w, `{"error":"invalid JSON request"}`, http.StatusBadRequest)
			return
		}
		names := make([]string, 0, len(request.Names)+1)
		if name := strings.TrimSpace(request.Name); name != "" {
			names = append(names, name)
		}
		for _, rawName := range request.Names {
			if name := strings.TrimSpace(rawName); name != "" {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			http.Error(w, `{"error":"missing tuple name or names"}`, http.StatusBadRequest)
			return
		}
		if len(names) > 10000 {
			http.Error(w, `{"error":"at most 10000 names per request"}`, http.StatusBadRequest)
			return
		}
		value, err := base64.StdEncoding.DecodeString(request.ValueBase64)
		if err != nil || len(value) == 0 {
			http.Error(w, `{"error":"value_base64 must encode a non-empty value"}`, http.StatusBadRequest)
			return
		}
		if request.Copies == 0 {
			request.Copies = 1
		}
		if request.Copies < 1 || request.Copies > 10000 {
			http.Error(w, `{"error":"copies must be between 1 and 10000"}`, http.StatusBadRequest)
			return
		}
		if request.Concurrency == 0 {
			request.Concurrency = 1
		}
		if request.Concurrency < 1 || request.Concurrency > 64 {
			http.Error(w, `{"error":"concurrency must be between 1 and 64"}`, http.StatusBadRequest)
			return
		}

		requested := len(names) * request.Copies
		jobs := make(chan string)
		type putResult struct {
			name    string
			stats   mytuplespace.IndexMutationStats
			retried int
			err     error
		}
		results := make(chan putResult, requested)
		var workers sync.WaitGroup
		for worker := 0; worker < request.Concurrency; worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for name := range jobs {
					var stats mytuplespace.IndexMutationStats
					var err error
					retried := 0
					for attempt := 1; attempt <= tuplePutMaxAttempts; attempt++ {
						_, stats, err = putInstrumented.TsPutWithMutationStats(name, value)
						if err == nil || !retryableTuplePutError(err) || attempt == tuplePutMaxAttempts {
							break
						}
						retried++
						time.Sleep(time.Duration(100*(1<<(attempt-1))) * time.Millisecond)
					}
					results <- putResult{name: name, stats: stats, retried: retried, err: err}
				}
			}()
		}
		mutationBefore := instrumented.MutationSnapshot()
		started := time.Now()
		go func() {
			for _, name := range names {
				for copyIndex := 0; copyIndex < request.Copies; copyIndex++ {
					jobs <- name
				}
			}
			close(jobs)
			workers.Wait()
			close(results)
		}()
		succeeded := 0
		failed := 0
		retried := 0
		failureReasons := make(map[string]int)
		failureSamples := make([]tuplePutFailure, 0, 20)
		mutationDelta := mytuplespace.IndexMutationStats{}
		for result := range results {
			addMutationStats(&mutationDelta, result.stats)
			retried += result.retried
			if result.err == nil {
				succeeded++
			} else {
				failed++
				failureReasons[tuplePutFailureReason(result.err)]++
				if len(failureSamples) < cap(failureSamples) {
					failureSamples = append(failureSamples, tuplePutFailure{
						Name:  result.name,
						Error: result.err.Error(),
					})
				}
			}
		}
		mutationAfter := instrumented.MutationSnapshot()
		response := tuplePutResponse{
			Requested:      requested,
			Succeeded:      succeeded,
			Failed:         failed,
			Retried:        retried,
			FailureReasons: failureReasons,
			FailureSamples: failureSamples,
			DurationNS:     time.Since(started).Nanoseconds(),
			MutationBefore: mutationBefore,
			MutationDelta:  mutationDelta,
			MutationStats:  mutationAfter,
		}
		if failed > 0 {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(response)
	})
}

// ReplicationFactorR is the enforced minimum replicas per file (Near 40%, Midrange 30%, Far 30%).
const ReplicationFactorR = 7

// PutRequest is the JSON request body for POST /put when Content-Type is not
// application/octet-stream (the raw-bytes path bypasses this struct entirely).
type PutRequest struct {
	// Data is the block payload, either a raw string or base64-encoded bytes
	// depending on client convention; it is used verbatim as []byte(Data).
	Data string `json:"data"`
}

// PutResponse is the JSON response body for a successful POST /put.
type PutResponse struct {
	// CID is the IPFS-compatible content ID of the stored block, kept for
	// blockstore compatibility; new code should prefer MultihashHex (Key).
	CID string `json:"cid"`
	// MultihashHex is the 64-hex-char content Key (SHA256 of the stored
	// data), the primary identifier for subsequent /get, /lookup, and
	// /replication/status calls.
	MultihashHex string `json:"multihash_hex"`
	// NetworkHops is always 0 for /put (token DHT sync is asynchronous and
	// not instrumented); present for symmetry with GetResponse.
	NetworkHops *int `json:"network_hops,omitempty"`
}

// ConnectRequest is the JSON request body for POST /connect.
type ConnectRequest struct {
	// Addr is the multiaddr string of the peer to dial.
	Addr string `json:"addr"`
	// Peer is the base58/CID-encoded libp2p peer ID to connect to.
	Peer string `json:"peer"`
	// Timeout is an optional Go duration string (e.g. "10s") bounding the dial; defaults to 10s if empty or unparsable.
	Timeout string `json:"timeout"`
	// Protect retains this trusted control-plane edge when the connection
	// manager trims opportunistic peers. Operators must keep the protected
	// topology bounded; campaign trees protect at most three peers per node.
	Protect bool `json:"protect,omitempty"`
}

const explicitConnectionProtectionTag = "tarsus-explicit-anchor"

// connectExplicitPeer performs a direct operator-requested dial. The explicit
// address and deadline come from the local control plane, so a stale libp2p
// opportunistic-dial backoff must not suppress the attempt without touching
// the network.
func connectExplicitPeer(ctx context.Context, h host.Host, info peer.AddrInfo) error {
	if h.Network().Connectedness(info.ID) == network.Connected {
		return nil
	}
	return h.Connect(network.WithForceDirectDial(ctx, "explicit control-plane connect"), info)
}

func protectExplicitPeer(h host.Host, pid peer.ID) {
	h.ConnManager().Protect(pid, explicitConnectionProtectionTag)
}

// GetRequest is the JSON request body for POST /get.
type GetRequest struct {
	Key     string `json:"key"` // Key (hex string) - primary identifier for token-based routing
	CID     string `json:"cid"` // CID (deprecated, kept for backward compatibility)
	Addr    string `json:"from_addr"`
	Peer    string `json:"from_peer"`
	Timeout string `json:"timeout"`
}

// GetResponse is the JSON response body for a successful, non-raw POST /get.
type GetResponse struct {
	// Bytes is the length of the returned block payload.
	Bytes int `json:"bytes"`
	// DataB64 is the block payload, base64 standard-encoded.
	DataB64 string `json:"data_b64"`
	// NetworkHops is the DHT lookup hop count when the block was resolved via
	// GetToken/DirectFetch; 0 when served from the local store or gateway
	// shortcut; nil (omitted) if not tracked for the path taken.
	NetworkHops *int `json:"network_hops,omitempty"`
}

// wantsRawGetResponse reports whether /get should stream raw bytes
// (application/octet-stream) instead of the JSON GetResponse envelope.
//
// Parameters:
//   - r (*http.Request): the incoming request; the "format" query parameter and "Accept" header are inspected.
//
// Returns:
//   - (bool): true if format=raw (case-insensitive) or the Accept header contains "application/octet-stream".
func wantsRawGetResponse(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "raw") {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "application/octet-stream")
}

func waitForLocalTokenPublication(
	ctx context.Context,
	stack *mystore.Stack,
	key mystore.Key,
	c cid.Cid,
	ready <-chan error,
) error {
	var err error
	if ready != nil {
		select {
		case err = <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
	} else {
		err = stack.SyncLocalTokenLocation(ctx, key, c)
	}
	delay := 100 * time.Millisecond
	for err != nil {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		err = stack.SyncLocalTokenLocation(ctx, key, c)
		if delay < 5*time.Second {
			delay *= 2
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
		}
	}
	return nil
}

// DeleteRequest is the JSON request body for POST /delete.
type DeleteRequest struct {
	// CID is the IPFS-compatible content ID of the block to delete
	// (key-based delete is not currently supported; see docs/API.md).
	CID string `json:"cid"`
}

// DeleteResponse is the JSON response body for a successful POST /delete.
type DeleteResponse struct {
	// CID echoes the deleted block's content ID.
	CID string `json:"cid"`
	// Deleted is true if the delete succeeded.
	Deleted bool `json:"deleted"`
}

// simulatedRTTByIndex returns a deterministic RTT for a peer when simulate_distances=1.
// Uses index within sorted provider list to guarantee at least one Near, Midrange, Farflung.
// Caller must pass sorted provider IDs and index; returns 10ms/75ms/250ms by round-robin.
//
// Parameters:
//   - index (int): the peer's position in a sorted provider list.
//
// Returns:
//   - (time.Duration): 10ms if index%3==0 (Near), 75ms if index%3==1 (Midrange), 250ms otherwise (FarFlung).
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
// Returns first successful result or error if all fail. Every location is
// raced concurrently and the function waits for all of them to finish (even
// after a winner is found) before returning, so it does not race-cancel the
// slower attempts.
//
// Parameters:
//   - ctx (context.Context): context passed to each DirectFetch attempt.
//   - stack (*mystore.Stack): storage stack providing DirectFetch and the optional MessageSink for message-count metrics.
//   - token (mystore.Token): the token whose Locations are attempted in parallel.
//   - key (mystore.Key): the content key being fetched.
//
// Returns:
//   - ([]byte): the block bytes from the first location to succeed.
//   - (error): non-nil if stack is nil, token has no locations, or every location's DirectFetch failed (error aggregates all per-location failures).
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
// It wires up an http.ServeMux with every documented control endpoint
// (/health, /metrics, /storage/stats, /replication/status, /lookup, /has_key,
// /restore, /restore/status, /shutdown, /neighbors, /id, /events, /put,
// /delete, /peers, /connect, /get, /snapshot, plus the /namespace/* routes
// registered via registerNamespaceHandlers), registers libp2p stream handlers
// for repair and direct-fetch protocols when available, binds a TCP listener,
// and starts serving in a background goroutine. Each handler closure captures
// the parameters below directly rather than through a struct, since no
// persistent server struct is required.
//
// Parameters:
//   - ctx (context.Context): reserved for future use in the listen/setup path; not currently used to bound the server's lifetime (Shutdown does that).
//   - h (host.Host): the libp2p host; used for peer info, dialing, stream handler registration, and routing-table updates. Several endpoints degrade gracefully or are skipped if nil, but most assume non-nil.
//   - stack (*mystore.Stack): the storage/routing stack backing put/get/delete/snapshot/replication/lookup and namespace operations.
//   - peers (*mynet.PeerStore): supplies dial candidates for the /peers endpoint.
//   - metrics (*NodeMetrics): metrics sink updated by handlers and exposed via /metrics; a nil value will panic in handlers that call methods on it unconditionally (e.g. /put's putHops logic assumes non-nil metrics via NetworkHops).
//   - onShutdown (func()): optional callback invoked ~100ms after /shutdown responds, to trigger graceful node stop.
//   - explicitRouter (*DynamicRouter): optional; when non-nil, used for DynamicRouter fallback and composed with stack's router (e.g. via NewFallbackContentRouter). When nil, a fresh DynamicRouter is created internally.
//   - repairProtocol (*mystore.RepairProtocol): optional repair protocol for automatic repair on vector mismatch (nil disables repair and the repair stream handler).
//   - gateway (*mygateway.Gateway): optional; when non-nil, used for token routing and query operations in /get (Phase 5.3).
//   - storePath (string): optional path to persistent blockstore; when non-empty, /storage/stats returns disk_bytes for that dir; when empty, /storage/stats reports an ephemeral store.
//
// Returns:
//   - (string): the bound listen address (127.0.0.1:<port>), normalized from any 0.0.0.0 bind.
//   - (func(context.Context) error): a shutdown function that stops the HTTP server (wraps http.Server.Shutdown).
//   - (error): non-nil if the TCP listener could not be created.
func Start(ctx context.Context, h host.Host, stack *mystore.Stack, peers *mynet.PeerStore, metrics *NodeMetrics, onShutdown func(), explicitRouter *DynamicRouter, repairProtocol *mystore.RepairProtocol, gateway *mygateway.Gateway, storePath string) (string, func(context.Context) error, error) {
	mux := http.NewServeMux()
	registerTupleExperimentEndpoints(mux, gateway)
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
	//
	// restoreStats tracks the progress of one async /restore job: how many
	// blocks succeeded/failed, total bytes restored, and whether the job has
	// finished. Instances are shared via the jobs map and guarded by jobsMu.
	type restoreStats struct {
		// OK is the count of blocks restored successfully so far.
		OK int `json:"ok"`
		// Failed is the count of blocks that failed to restore (decode or fetch error).
		Failed int `json:"failed"`
		// Bytes is the total size of successfully restored blocks so far.
		Bytes int64 `json:"bytes"`
		// Done is true once all workers have finished processing the job's CID list.
		Done bool `json:"done"`
	}
	// jobsMu guards jobs against concurrent access from the HTTP handlers and
	// the background restore worker goroutines.
	var jobsMu sync.Mutex
	// jobs maps a restore job ID (from /restore) to its live restoreStats, so
	// /restore/status can report progress while the job runs asynchronously.
	jobs := make(map[string]*restoreStats)

	// GET /health is a liveness probe; always responds 200 "ok".
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes the literal body "ok".
	//   - r (*http.Request): unused.
	//
	// Returns: (none — writes directly to w)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	// GET /metrics returns the node's current metrics as JSON (see
	// NodeMetrics.Snapshot / MetricsSnapshot). Before encoding, it refreshes
	// ProviderRecordsCount from stack.ProviderRecords if both are available.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes the MetricsSnapshot JSON.
	//   - r (*http.Request): unused.
	//
	// Returns: (none — writes directly to w)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if metrics != nil && stack != nil && stack.ProviderRecords != nil {
			metrics.SetProviderRecordsCount(int64(stack.ProviderRecords.Len()))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metrics.Snapshot())
	})

	// GET /storage/stats returns disk_bytes for the persistent store path
	// (storage efficiency tests). If storePath is empty, reports an
	// ephemeral store (disk_bytes=null). If storePath is a single file, uses
	// its size directly; if a directory, walks it recursively summing file
	// sizes (walk errors are swallowed per-entry so partial results are
	// still returned).
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes JSON {"disk_bytes": ...} or {"reason": "ephemeral"} or {"error": ...}.
	//   - r (*http.Request): unused.
	//
	// Returns: (none — writes directly to w)
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

	// GET /replication/status returns the current replica count and
	// distance-class breakdown for a key by fetching its DHT token
	// (GetToken) and classifying each location's RTT (near/midrange/
	// farflung) via mystore.ClassifyDistanceByRTT. If GetToken fails, still
	// responds 200 with replica_count=0 and an error_reason/error_detail for
	// diagnostics rather than a non-2xx error. Not currently documented in
	// docs/API.md.
	//
	// Query parameters:
	//   - key (string, required): 64-hex-char content key to look up.
	//   - simulate_distances ("1" to enable): when set, provider RTTs are
	//     replaced with deterministic values from simulatedRTTByIndex (over
	//     providers sorted by peer ID string) so tests can exercise all three
	//     distance classes even with real RTT=0 locations.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes the JSON response described above.
	//   - r (*http.Request): must be a GET; key and simulate_distances are read from the query string.
	//
	// Returns: (none — writes directly to w)
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
		var near, midrange, farflung, unknown int
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
				unknown++
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
			"unknown_count":  unknown,
		})
	})

	// GET /lookup?key=... or POST /lookup with JSON {"key": ...} performs an
	// isolated token lookup (GetToken only, no block fetch). It registers for
	// routing.SendingQuery events during the lookup to count network_hops,
	// and times the call for lookup_latency_ms. If
	// SNG40_LOG_LOOKUP_PATHS=1, logs hops/latency/error per request.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes {"lookup_latency_ms", "network_hops", "found"} JSON on success.
	//   - r (*http.Request): must be GET (key via query string) or POST (key via JSON body); other methods are rejected.
	//
	// Returns: (none — writes directly to w)
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

	// GET /has_key?key=... returns whether this node holds the key locally
	// (for polling replica count across nodes by querying each node
	// individually). Not currently documented in docs/API.md.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes {"key", "has_key"} JSON.
	//   - r (*http.Request): must be a GET with "key" query parameter.
	//
	// Returns: (none — writes directly to w)
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
	//
	// restoreReq is the JSON request body for POST /restore.
	type restoreReq struct {
		// CIDs is the list of IPFS CID strings to restore (required, non-empty).
		CIDs []string `json:"cids"`
		// Concurrency is the number of parallel worker goroutines; defaults to 4 if <= 0.
		Concurrency int `json:"concurrency"`
		// Timeout is a Go duration string bounding each individual block fetch; defaults to 20s if empty or unparsable.
		Timeout string `json:"timeout"`
		// ByteBudget caps total restored bytes for the job; <= 0 means unbounded. Enforced approximately (checked between tasks, not preemptively).
		ByteBudget int64 `json:"byte_budget"`
	}
	// POST /restore starts an asynchronous job that fetches each CID in
	// req.CIDs (via stack.GetBlock BlockSvc) using a worker pool of
	// req.Concurrency goroutines, respecting req.ByteBudget as a soft cap
	// (workers stop pulling new tasks once accumulated bytes meet/exceed the
	// budget) and req.Timeout per fetch. Progress is tracked in the jobs map
	// under a generated job ID and can be polled via GET /restore/status.
	// Responds 202 Accepted immediately with {"job": "<job id>"}; the job
	// itself completes in the background.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes {"job": "<id>"} JSON with status 202 on success.
	//   - r (*http.Request): must be a POST with JSON body per restoreReq.
	//
	// Returns: (none — writes directly to w)
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

	// GET /restore/status?id=... returns the current restoreStats for a job
	// previously started via POST /restore (separate route since it's a
	// GET-only lookup rather than a mutating action).
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes the job's restoreStats JSON on success.
	//   - r (*http.Request): must be a GET with "id" query parameter naming a known job.
	//
	// Returns: (none — writes directly to w)
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

	// GET /shutdown triggers a graceful node stop. It responds 200
	// immediately, then invokes onShutdown (if non-nil) after a 100ms delay
	// on a separate goroutine so the HTTP response is flushed to the client
	// before the process begins tearing down (avoiding a client hang on a
	// connection that gets torn down mid-response).
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes status 200 with an empty body.
	//   - r (*http.Request): must be a GET.
	//
	// Returns: (none — writes directly to w)
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

	// GET /neighbors returns the peers this host is currently connected to
	// (via h.Network().Peers()), each with their known multiaddrs, excluding
	// the local host itself and deduplicated by peer ID. Despite the
	// docs/API.md description ("DHT neighbors"), this reflects live libp2p
	// connections, not a DHT routing-table view.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes a JSON array of {"peer", "addrs"} objects.
	//   - r (*http.Request): must be a GET.
	//
	// Returns: (none — writes directly to w)
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

	// GET /id returns this node's own PeerID and current listen addrs.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes {"peer", "addrs"} JSON.
	//   - r (*http.Request): must be a GET.
	//
	// Returns: (none — writes directly to w)
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

	// GET /events returns the most recent events recorded in the append-only
	// event log (walked backward from HEAD via ListRecentFromHead),
	// newest-first, up to "limit" entries (default 50, max 1000). Despite
	// docs/API.md listing this as an "Event stream (SSE)", the handler here
	// returns a single JSON array response, not a Server-Sent-Events stream
	// (no text/event-stream content type or chunked keep-alive).
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes a JSON array of {"cid", "type", "ts", "peer", "prev"} objects.
	//   - r (*http.Request): must be a GET; optional "limit" query parameter (1-1000).
	//
	// Returns: (none — writes directly to w)
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
	//
	// maxPutBodyBytes bounds the accepted raw-bytes /put body size (64 MiB);
	// larger bodies are rejected with 413.
	const maxPutBodyBytes = mystore.MaxTransferBlockSize
	// POST /put stores a block, deriving its content Key as SHA256(data).
	// Body is read as raw bytes when Content-Type is
	// application/octet-stream, otherwise as JSON PutRequest. A logical payload
	// is stored under one key/CID pair so the repair and direct-fetch protocols
	// transfer exactly the bytes identified by the returned key. After storing,
	// the routing table is updated
	// asynchronously (UpdateRoutingTableOnPutAsync) and, if repairProtocol
	// and h are available, replication to peers is scheduled on a background
	// goroutine with a 4-minute budget (independent of the request's/test's
	// deadline, since large clusters need time to pick connected peers and
	// complete sequential transfers). The HTTP response is written before
	// replication starts, matching Swarm's "return after first copy" timing
	// semantics. If SNG40_LOG_PUT_PHASES=1, logs the PutBlock vs
	// routing-table+mapping phase durations.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes PutResponse JSON on success.
	//   - r (*http.Request): must be a POST; body per Content-Type as described above (max 64 MiB for raw bytes).
	//
	// Returns: (none — writes directly to w)
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
		key, c, err := stack.PutPayload(r.Context(), blockData)
		t1 := time.Now()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		var tokenReady <-chan error
		if h != nil {
			tokenReady = stack.UpdateRoutingTableOnPutAsync(key, h.ID(), nil, c)
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
				if err := waitForLocalTokenPublication(
					ctxRepair,
					stack,
					key,
					c,
					tokenReady,
				); err != nil {
					log.Printf("publish source token for %s before replication: %v", key.String(), err)
					return
				}
				_ = repairProtocol.ReplicateToNPeers(ctxRepair, key, c, blockData, 6)
			}()
		}
	})

	// POST /delete removes a block (identified by CID, not yet by Key — see
	// docs/API.md note on a possible future key-based delete) from the local
	// store and routing table via stack.DeleteBlock, and clears any explicit
	// DynamicRouter provider hint for that CID.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes DeleteResponse JSON on success.
	//   - r (*http.Request): must be a POST with JSON body {"cid": "..."}.
	//
	// Returns: (none — writes directly to w)
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

	// GET /peers returns up to "limit" dial candidates from the PeerStore
	// (peers.GetDialCandidates), including each candidate's score and dial
	// history metadata.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes a JSON array of {"peer", "addrs", "score", "last_seen_unix", "last_tried_unix", "last_succ_unix", "failure_count", "source"} objects.
	//   - r (*http.Request): must be a GET; optional "limit" query parameter (1-200, default 20).
	//
	// Returns: (none — writes directly to w)
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

	// POST /connect dials a specific peer at a specific multiaddr
	// (h.Connect). If the target peer ID equals this host's own ID, it
	// short-circuits to success without dialing.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes status 200 on success (no body).
	//   - r (*http.Request): must be a POST with JSON body per ConnectRequest {"addr", "peer", "timeout"}.
	//
	// Returns: (none — writes directly to w)
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
		if err := connectExplicitPeer(ctxDial, h, info); err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		if req.Protect {
			// The control server is loopback-only and therefore a trusted
			// administrative surface. Protecting both endpoints of a bounded
			// configured edge prevents Kademlia trimming from partitioning the
			// sparse backbone while leaving learned peers eligible for pruning.
			protectExplicitPeer(h, pid)
		}
		w.WriteHeader(http.StatusOK)
	})

	// POST /get fetches a block by content Key (preferred) or CID (deprecated,
	// requires a routing-table entry to resolve to a Key). Resolution order,
	// unless remote_only is set: (1) chunk-indexed raw streaming shortcut when
	// format=raw and a ChunkIndex exists — flushes the first chunk immediately
	// then streams the rest; (2) local store via
	// ResolvePayloadByKeyLocal (networkHops=0); (3) gateway.Query → token →
	// fetchBlockFromToken (DirectFetch) when a Gateway is configured
	// (networkHops=0, DHT hops not tracked on this path); (4) stack.GetBlock,
	// which itself performs GetToken (DHT) + DirectFetch and reports real
	// networkHops. When remote_only=1/true/yes, steps (1)-(3) are skipped
	// entirely and stack.GetBlock is called with mystore.WithRemoteOnlyGet so
	// even its internal local-blockstore fast path is bypassed, giving a
	// cold-fetch timing measurement. Response is either a JSON GetResponse or,
	// when format=raw or Accept: application/octet-stream is requested, a raw
	// application/octet-stream body with Content-Length and X-Network-Hops
	// headers (chunked first-4KiB flush to reduce time-to-first-byte).
	//
	// After the response is sent, if the block was fetched remotely, a
	// detached background goroutine persists it locally (PutBlock), then
	// verifies replication health (VerifyKeyStateWithRepVector) and, if
	// under-replicated and repairProtocol is configured, triggers a repair
	// (TriggerRepair) — none of this delays the first byte written to the
	// client (curl time_starttransfer and other clients would otherwise
	// include this work in their measured latency).
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes either GetResponse JSON or a raw octet-stream body, per the rules above.
	//   - r (*http.Request): must be a POST with JSON body per GetRequest; recognizes "format=raw" and "remote_only" query parameters and the "Accept" header.
	//
	// Returns: (none — writes directly to w)
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
		remoteOnly := getRemoteOnlyQuery(r)
		if rawResponse && !remoteOnly {
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
		if !remoteOnly {
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
		}
		if b == nil && err == nil {
			var hops int
			gctx := ctxFetch
			if remoteOnly {
				gctx = mystore.WithRemoteOnlyGet(ctxFetch)
			}
			b, hops, err = stack.GetBlock(gctx, key)
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
			var measureRTT mystore.ProviderRTTMeasurer
			if repairRef != nil {
				measureRTT = repairRef.MeasureRTTAt
			}
			ctxVerify, cancelVerify := context.WithTimeout(context.Background(), 5*time.Second)
			verification, verifyErr := mystore.VerifyKeyStateWithRepVector(
				ctxVerify,
				keyCopy,
				rt,
				tokenStore,
				hostRef.ID(),
				measureRTT,
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

	// GET /snapshot returns locally indexed block identifiers
	// (mystore.ListIndexedCIDs), paginated by an opaque cursor. Storage is
	// Key-based internally but this endpoint surfaces CIDs for
	// IPFS-blockstore compatibility.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes {"cids", "next", "count"} JSON.
	//   - r (*http.Request): must be a GET; optional "limit" (1-100000, default 1000) and "cursor" (start-after) query parameters.
	//
	// Returns: (none — writes directly to w)
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
	registerNamedObjectHandlers(mux, stack, h, repairProtocol, gateway)

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
