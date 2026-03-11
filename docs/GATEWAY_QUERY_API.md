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
