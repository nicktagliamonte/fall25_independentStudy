# Gateway: Token vs Data Routing

## Invariant: Gateway Handles Token Routing, Not Data Routing

The Gateway routes **tokens** (location metadata, tuple values) only. It never fetches or transfers block content.

- **Query**: Returns `Result{Key, Value}` where `Value` is token JSON (locations, provider IDs, addresses). Never block bytes.
- **TokenStore**: Put/Get for `/tokens/` keys only. Tokens contain provider locations, not data.
- No methods on Gateway fetch block data or stream content.

## Data Routing: Device-to-Device After Token Lookup

Block data retrieval is **not** routed through the Gateway:

1. Caller uses `Gateway.Query()` to obtain token (locations).
2. Caller uses `DirectFetch` to stream block bytes from providers via libp2p.
3. Data flows peer-to-peer; Gateway is not in the data path.

This separation is enforced in:

- `internal/control/server.go` `/get` handler: `Gateway.Query` → `fetchBlockFromToken` (DirectFetch).
- `internal/storage/store.go` `GetBlock`: `GetToken` → `DirectFetch` to each location in parallel.

================================================================================
GATEWAY AND QUERY OPTIMIZER
================================================================================
Per newReqs.txt and planTwo: gateway is stateless; routes tokens, not data. Query
optimizer breaks down queries involving multiple partitions and routes each part.

1. Gateway Invariant
   -----------------
   - Gateway routes tokens (location metadata, tuple values) only. Never fetches block data.
   - Result: {Key, Value} where Value is token JSON (locations, provider IDs). Never block bytes.
   - Data path: Caller uses Gateway.Query to get token, then DirectFetch to providers peer-to-peer.

2. Gateway Components
   ------------------
   - Router: routing.ContentRouting for token lookups.
   - TupleSpace: for TsRead/TsPut. When tuplespace.Router, routes by pattern type.
   - TokenStore(): returns routing.ValueStore delegating Put/Get to TupleSpace for /tokens/ keys.

2b. TupleSpace Implementations (internal/tuplespace)
    -------------------------------------------------
   - DHTTupleSpace (dht_ts.go): exact-match tuple names, backed by the Kademlia DHT.
     No permission checks. O(log N). "Consumption" (TsGet) is emulated via tombstone
     markers since the DHT has no native delete; cleanup relies on the 48h libp2p TTL.
   - P2PTupleSpace (p2p_ts.go): complex regex/wildcard patterns and admin/coordination
     ops (KYC, application management), speaking the legacy TSH wire protocol over TCP.
     Permissioned via an optional PermissionChecker (permission.go); O(log_20 k)-hop
     routing, O(N) messaging.
   - Router (router.go): the concrete TupleSpace most Gateways use. Dispatches by
     pattern shape: exact name -> DHTTupleSpace; simple wildcard (prefix*, *substring*)
     -> resolved via internal/pht to candidate names, then fetched from DHTTupleSpace;
     complex regex -> P2PTupleSpace. Falls back to P2PTupleSpace for wildcards if no
     pht.ValueStore was supplied at construction.
   - TokenFallbackTupleSpace (token_fallback_ts.go): wraps another TupleSpace (typically
     a Router). 64-hex-char tuple names are read/written directly under /tokens/ on a
     ValueStore; everything else delegates to the wrapped TupleSpace. This is the piece
     that lets exact-key Gateway.Query calls resolve to token JSON.
   - PermissionChecker (permission.go): pluggable auth hook consulted by P2PTupleSpace;
     nil means allow-all.

2c. PHT (internal/pht) - Prefix Hash Tree
    --------------------------------------
   - Backs Router's resolution of simple-wildcard (QueryPrefix) patterns to concrete
     tuple names before they're fetched from the DHT tuple space.
   - tree.go: PHT node/tree structure. query.go: ParseQuery/ExecutePrefixQuery (tree
     descent)/ExecuteSubstringQuery (Bloom-pruned substring search). bloom.go: Bloom
     filters that prune substring-query candidates before DHT lookups. dht.go: DHT-backed
     storage of PHT tree nodes.

3. Query Types (QueryOptimizer)
   ----------------------------
   QueryExact: No wildcards. Route -> DHT token lookup.
   QueryPrefix: Contains * (simple wildcard). Route -> PHT + DHT token lookup.
   QueryRegex: Contains .+?^$[]{}|()\. Route -> P2P tuple space.
   QueryMultiPartition: Contains | as OR separator. Break down, route each part.

4. Query Optimizer Pipeline
   ------------------------
   ParseQuery: Classify pattern, set Query.Type.
   OptimizeQuery: Trim whitespace, deduplicate OR parts (a|a|b -> a|b), collapse single part.
   BreakDownQuery: Split on |, dedupe sub-patterns, return []SubQuery.
   RouteForQuery: Returns routing target (DHT, PHT+DHT, P2P, multi-partition).

5. Multi-Partition Flow
   ---------------------
   QueryMultiPartition(queryStr, optimizer): ParseQuery -> OptimizeQuery -> BreakDownQuery ->
   ExecuteSubQueriesParallel. Sub-queries run in parallel via goroutines; results aggregated,
   deduplicated by key.

6. Integration (/get Handler)
   --------------------------
   Local first; then Gateway.Query(key) if gateway set; then stack.GetBlock (DHT token + DirectFetch).
   Gateway returns token; handler unmarshals, calls fetchBlockFromToken (DirectFetch to providers).

7. TupleSpace Routing (when Router)
   ---------------------------------
   Exact match -> DHT tuple space. Prefix/substring (*) -> PHT find matches, then DHT retrieve.
   Complex regex -> P2P tuple space. Storage queries use DHT; admin/coordination use P2P.

================================================================================
