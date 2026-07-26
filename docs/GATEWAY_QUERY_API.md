# Gateway Query API

The Gateway is stateless and routes **tokens** (location metadata) only. It never fetches or transfers block content. After a query returns a token, callers use DirectFetch to retrieve block data peer-to-peer.

---

## Invariant

- **Query** returns `Result{Key, Value}` where `Value` is token JSON (locations, provider IDs). Never block bytes.
- **Data path**: `Gateway.Query` → token → `DirectFetch` to providers.

---

## Gateway

```go
type Gateway struct {
    Router     routing.ContentRouting
    TupleSpace tuplespace.TupleSpace
}

gateway := gateway.NewGateway(router, tupleSpace)
```

---

## Query

Execute a query by pattern. Returns tokens/metadata, not block data.

```go
results, err := gateway.Query(ctx, gateway.Query{Pattern: key.String()})
```

**Parameters:**
- `Pattern`: Key (64 hex chars for exact), or pattern with wildcards/OR.

**Returns:** `[]Result` where each `Result` has:
- `Key`: Matched key/pattern.
- `Value`: Token JSON (locations, provider IDs, addresses).

**Example (exact key):**
```go
results, err := gateway.Query(ctx, gateway.Query{Pattern: "a1b2c3..."})
// results[0].Value = token JSON
```

**Note:** `Query` is a thin wrapper around `QueryMultiPartition` (with a default `QueryOptimizer`) — a single pattern takes the synchronous single-sub-query path, and an OR-separated pattern is optimized, broken down, and run in parallel exactly as `QueryMultiPartition` describes below.

---

## QueryMultiPartition

Break down OR-separated patterns, run sub-queries in parallel, aggregate and deduplicate.

```go
optimizer := gateway.NewQueryOptimizer()
results, err := gateway.QueryMultiPartition(ctx, "key1|key2|key3", optimizer)
```

**Pipeline:**
1. `ParseQuery`: Classify pattern type.
2. `OptimizeQuery`: Trim whitespace, deduplicate OR parts.
3. `BreakDownQuery`: Split on `|`, produce `[]SubQuery`.
4. `ExecuteSubQueriesParallel`: Run each sub-query, aggregate by key.

---

## Result

```go
type Result struct {
    Key   string  // Matched key or pattern
    Value []byte  // Token JSON (locations, etc.)
}
```

`Value` is serialized `storage.Token` (see TOKEN_PROTOCOL.md).

---

## Query Types and Routing

| Type               | Pattern              | Routing target   | Description                    |
|--------------------|----------------------|------------------|--------------------------------|
| QueryExact         | `a1b2c3...`          | DHT              | Exact key, DHT token lookup    |
| QueryPrefix        | `prefix_*`           | PHT+DHT          | Wildcard `*`, PHT then DHT     |
| QueryRegex         | `a.b`, `.+`          | P2P              | Regex metachars → P2P tuple space |
| QueryMultiPartition| `a\|b\|c`            | multi-partition  | OR-separated, route each part  |

**Pattern rules:**
- `|`: OR separator. `a|b|c` → multi-partition.
- `*`: Simple wildcard. Trailing or surrounding.
- Regex metachars (`.+?^$[]{}|()\`): Complex regex → P2P.

---

## QueryOptimizer

```go
optimizer := gateway.NewQueryOptimizer()
```

| Method          | Description                                         |
|-----------------|-----------------------------------------------------|
| ParseQuery(s)   | Classify pattern, return `Query{Pattern, Type}`     |
| OptimizeQuery(q)| Trim, dedupe OR parts, collapse single part         |
| BreakDownQuery(q)| Split on `|`, return `[]SubQuery`                      |
| RouteForQuery(q)| Routing target string (DHT, PHT+DHT, P2P, multi-partition) |

**OptimizeQuery behavior:**
- Trim whitespace.
- Deduplicate OR parts: `a|a|b` → `a|b`.
- Collapse single OR: `x` when type is multi-partition → single sub-query.

---

## TokenStore

Gateway exposes a `ValueStore` for token Put/Get (used by `SyncTokenOnPut`):

```go
tokenStore := gateway.TokenStore()
```

- **Keys:** `/tokens/` prefix only.
- **PutValue/GetValue:** Delegates to TupleSpace TsPut/TsRead.
- Used when Gateway handles token routing instead of raw DHT.

---

## TupleSpace implementations (`internal/tuplespace`)

`Gateway.TupleSpace` is typically a `tuplespace.Router` (`internal/tuplespace/router.go`), which does the actual dispatch that `RouteForQuery` describes above:

| Implementation | File | Backs | Notes |
|---|---|---|---|
| `DHTTupleSpace` | `dht_ts.go` | Exact-match tuple names (QueryExact) | Open storage layer, **no permission checks**, O(log N) DHT operations. "Consumption" (`TsGet`) is emulated with tombstone markers since the DHT has no native delete; cleanup relies on the standard 48h libp2p TTL. |
| `P2PTupleSpace` | `p2p_ts.go` | Complex regex/wildcard patterns (QueryRegex) and admin/coordination ops | Speaks the legacy TSH wire protocol over TCP. Permissioned via an optional `PermissionChecker`; O(log₂₀ k)-hop routing with O(N) messaging. |
| `Router` | `router.go` | Dispatch by pattern shape | Exact names → `DHTTupleSpace`. Simple wildcards (`prefix*`, `*substring*`) → resolved via the PHT (`internal/pht`) to candidate tuple names, then fetched from `DHTTupleSpace`. Complex regex → `P2PTupleSpace`. |
| `TokenFallbackTupleSpace` | `token_fallback_ts.go` | Exact-key token reads/writes | Wraps another `TupleSpace` (typically a `Router`). 64-character hex tuple names are read/written directly under `/tokens/` on a `ValueStore`; everything else delegates to the wrapped `TupleSpace`. This is what makes `Gateway.TokenStore()` and exact-key `Query` calls resolve to token JSON without every backend needing to know about tokens. |
| `PermissionChecker` | `permission.go` | Authorization hook for `P2PTupleSpace` | Consulted before `TsPut`/`TsGet`/`TsRead` on the P2P layer; absent (`nil`) is treated as allow-all. |

None of these types fetch or transfer block content — only tuples/tokens (locations, metadata). Retrieving actual block bytes is always the caller's responsibility via DirectFetch.

## PHT (Prefix Hash Tree) — `internal/pht`

`Router.TsGet`/`TsRead` resolve simple-wildcard patterns (`QueryPrefix` in the routing-target table above) through the PHT before falling back to the DHT tuple space:

- `tree.go` — the PHT node/tree structure (prefix-keyed trie stored across the DHT).
- `query.go` — `pht.ParseQuery`, `pht.ExecutePrefixQuery` (tree descent), `pht.ExecuteSubstringQuery` (Bloom-filter-pruned substring search).
- `bloom.go` — Bloom filters used to prune substring-query candidates before consulting the DHT, avoiding unnecessary lookups.
- `dht.go` — DHT-backed storage/retrieval of PHT tree nodes.

A `Router` is constructed with a `pht.ValueStore` (`phtStore`); if it's `nil`, `Router` falls back to `P2PTupleSpace` for any wildcard pattern instead of attempting PHT resolution.

---

## Integration with /get

The control server `/get` handler uses Gateway when configured:

1. Local: `GetBlockByKey` (local store).
2. Gateway: `gateway.Query(ctx, Query{Pattern: key.String()})` → token → `fetchBlockFromToken` (DirectFetch).
3. Fallback: `stack.GetBlock` (DHT token + DirectFetch).

---

## Errors

| Condition          | Result                         |
|--------------------|--------------------------------|
| TupleSpace nil     | `tuple space required for query` |
| Pattern empty      | `nil`, no error                |
| TsRead error       | Sub-query skipped (no result)  |
