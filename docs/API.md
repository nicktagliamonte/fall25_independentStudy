# Key-Based API Endpoints

Control server HTTP API. Base URL: `http://127.0.0.1:<port>` (port from daemon control file).

**Key format:** 64-character hex string (SHA256 hash of data). Primary identifier for storage and token routing.

---

## PUT /put

Stores a block. Key is derived from data (`SHA256(data)`). Token is synced to DHT for discovery.
Payloads larger than 4 KiB are stored as fixed 4 KiB content chunks with a per-key chunk index.

**Method:** `POST`

**Request (choose one):**

1. **`Content-Type: application/octet-stream`** — body is raw bytes (same pattern as Swarm `POST /bzz:/`). Max body 64 MiB.
2. **`Content-Type: application/json`** (default):
```json
{
  "data": "<base64 or raw string>"
}
```

**Response:** `200 OK`
```json
{
  "cid": "<IPFS CID, for compatibility>",
  "multihash_hex": "<64 hex chars = Key>",
  "network_hops": 0
}
```
`network_hops`: DHT lookup hops for this operation (0 for put; token sync is not instrumented).

PUT returns immediately after the first successful store (local + routing table). Token DHT sync and replication to peers run asynchronously, matching Swarm's "return after local accept" semantics for fair comparison.

**Key-based usage:** Use `multihash_hex` as the key for subsequent GET operations.

**Errors:** `400` invalid request; `413` body too large; `500` storage error (e.g. lock contention).

**Diagnostics:** Set `SNG40_LOG_PUT_PHASES=1` to log server-side durations for `PutBlock` vs routing-table + local mapping (token DHT + replication remain async).

---

## Namespace (first-class directories)

Directory blocks are JSON (`kind: "vnipfs-directory-v1"`) stored via the same `PutBlock` pipeline as opaque bytes; each mutating call returns a **new** `dir_key` (copy-on-write). See `docs/NAMESPACE.md` for semantics.

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/namespace/mkdir` | POST | Create an empty directory block |
| `/namespace/link` | POST | Add or replace `name` → `child_key` under `dir_key` |
| `/namespace/unlink` | POST | Remove `name` from `dir_key` (404 if missing) |
| `/namespace/rename` | POST | Move `old_name` → `new_name` within `dir_key` |
| `/namespace/ls` | POST | List `names` and `entries` for `dir_key` |
| `/namespace/resolve` | POST | Resolve `path` from `root_key` (uses `GetBlock` per segment) |

**POST /namespace/mkdir**

Request body may be `{}`. Response:
```json
{ "dir_key": "<64 hex>", "cid": "<cid>" }
```

**POST /namespace/link**

```json
{ "dir_key": "<64 hex>", "name": "<single segment>", "child_key": "<64 hex>" }
```

Response: `{ "dir_key": "<new dir key>", "cid": "<cid>" }`.

**POST /namespace/unlink**

```json
{ "dir_key": "<64 hex>", "name": "<segment>" }
```

**POST /namespace/rename**

```json
{ "dir_key": "<64 hex>", "old_name": "<segment>", "new_name": "<segment>" }
```

**POST /namespace/ls**

```json
{ "dir_key": "<64 hex>" }
```

Response includes `names` (sorted) and `entries` (name → child key).

**POST /namespace/resolve**

```json
{ "root_key": "<64 hex>", "path": "a/b/c" }
```

Response: `{ "key": "<64 hex>" }` — key of the final object (file or directory). Empty `path` returns `root_key`. Nested trees require updating parent `link` calls after child directories change (copy-on-write).

**Errors:** `400` validation; `404` missing directory or resolve path; `500` storage errors.

---

## POST /get

Fetches a block by key. Prefer `key` over `cid`. Resolves via local store, then token routing (GetToken → DirectFetch), then stack fallback.
For chunk-indexed payloads, GET reassembles bytes in key order before key validation (`SHA256(data) == key`).

**Method:** `POST`

**Request:**
```json
{
  "key": "<64 hex chars>",
  "timeout": "20s"
}
```

**Backward compatibility (deprecated):**
```json
{
  "cid": "<IPFS CID>",
  "timeout": "20s"
}
```
CID lookup requires routing table entry; use key when available.

**Response:** `200 OK`
```json
{
  "bytes": 123,
  "data_b64": "<base64-encoded block bytes>",
  "network_hops": 3
}
```
`network_hops`: DHT lookup hop count (peers queried during GetToken). 0 when served from local store or gateway; omitted when unknown.

**Early-response streaming mode (raw bytes):**

- Add query parameter `format=raw`, or send `Accept: application/octet-stream`.
- Response body is raw bytes (`Content-Type: application/octet-stream`), with:
  - `Content-Length: <bytes>`
  - `X-Network-Hops: <count>`
- On local chunk-index hits, the server flushes the first 4 KiB chunk immediately, then streams remaining chunks.

**Measurement / network path (`remote_only`):**

- Query parameter `remote_only=1` (or `true` / `yes`) skips the local chunk index, local payload resolution, gateway shortcut, and the **local blockstore fast path inside `GetBlock`**. The handler resolves the key via DHT (`GetToken`) and fetches payload from a provider (`DirectFetch`), so wall-clock time reflects lookup + transfer comparable to a cold replica fetch. Default behavior (no flag) still prefers local replicas for speed.

**Errors:** `400` invalid key/cid, key or cid required, key not found in routing table; `404` block not found.

---

## POST /lookup (and GET /lookup?key=)

Isolated **GetToken** only (no block fetch). Returns lookup wall-clock and `network_hops` (count of `routing.SendingQuery` events during the query). Used by comparison tests; not the same metric as PUT latency.

**Diagnostics:** `SNG40_LOG_LOOKUP_PATHS=1` logs hop count, latency, and token error (if any) per request.

---

## POST /delete

Removes a block from local store and routing table. Currently accepts CID.

**Method:** `POST`

**Request:**
```json
{
  "cid": "<IPFS CID>"
}
```

**Response:** `200 OK`
```json
{
  "cid": "<CID>",
  "deleted": true
}
```

**Note:** Key-based delete may be added in a future revision. Clients with key can obtain CID from routing table or from a prior PUT response.

**Errors:** `400` invalid CID; `500` delete failed.

---

## GET /snapshot

Returns locally indexed block identifiers. Uses Key→CID mapping; returned as CIDs for compatibility.

**Method:** `GET`

**Query params:** `limit` (default 1000, max 100000), `cursor` (start after, for pagination)

**Response:** `200 OK`
```json
{
  "cids": ["<cid1>", "<cid2>", ...],
  "next": "",
  "count": 2
}
```

---

## GET /storage/stats

Returns disk usage for the node's persistent blockstore directory (when started with `--store`). Used for storage efficiency tests.

**Response (persistent store):** `200 OK`
```json
{"disk_bytes": 123456}
```

**Response (ephemeral store):** `200 OK`
```json
{"disk_bytes": null, "reason": "ephemeral"}
```

**Errors:** When path exists but walk fails: `{"error": "..."}`.

---

## GET /replication/status

Reports how many replicas of a key's token are currently known, broken down by RTT distance class.

**Query params:**
- `key` (required): 64 hex chars.
- `simulate_distances` (`1` to enable): replaces zero-RTT locations with deterministic simulated values (sorted by provider ID string) so tests can exercise all three distance classes even with real RTT=0 locations.

**Response:** `200 OK`
```json
{
  "key": "<64 hex>",
  "replica_count": 3,
  "providers": ["<peer.ID>", "..."],
  "timestamp": 1234567890000000000,
  "near_count": 1,
  "midrange_count": 1,
  "farflung_count": 1
}
```

If the token cannot be found (or the key is invalid), still responds `200 OK` with `replica_count: 0` and diagnostic fields:
```json
{ "key": "<64 hex>", "replica_count": 0, "providers": [], "error_reason": "token_not_found", "error_detail": "..." }
```

**Errors:** `400` missing/invalid key; `503` no token store (DHT/Gateway) available.

---

## GET /has_key

Returns whether this node holds the given key locally. Intended for polling replica placement by querying each node individually.

**Query params:** `key` (required, 64 hex chars).

**Response:** `200 OK`
```json
{ "key": "<64 hex>", "has_key": true }
```

**Errors:** `400` missing/invalid key.

---

## Other Endpoints

| Endpoint            | Method   | Purpose                                                        |
|---------------------|----------|------------------------------------------------------------------|
| /health             | GET      | Liveness (returns "ok")                                        |
| /metrics            | GET      | Node metrics (JSON)                                            |
| /storage/stats      | GET      | Disk bytes for blockstore                                      |
| /replication/status | GET      | Replica count and near/midrange/farflung breakdown for a key   |
| /has_key            | GET      | Whether this node holds a key locally                           |
| /restore            | POST     | Restore blocks by CID list                                     |
| /restore/status     | GET      | Restore job status                                              |
| /shutdown           | POST     | Graceful node stop                                              |
| /peers              | GET      | Dial candidates                                                 |
| /connect            | POST     | Connect to peer                                                 |
| /neighbors          | GET      | Live libp2p connections (**not** a DHT routing-table view — see note below) |
| /id                 | GET      | Peer ID                                                         |
| /events             | GET      | Most recent append-only-log events, newest-first (single JSON array response — **not** an SSE stream; see note below) |
| /lookup             | GET/POST | Token lookup only (hops + ms)                                  |

**Note on `/neighbors`:** returns `h.Network().Peers()` — the peers this host currently has an open libp2p connection to — deduplicated, each with known multiaddrs. This is a live connection list, not a walk of the DHT routing table.

**Note on `/events`:** despite the name, this is a single JSON-array response (`Content-Type: application/json`), not a Server-Sent-Events stream — there is no `text/event-stream` content type or chunked/keep-alive behavior. Accepts an optional `limit` query parameter (default 50, max 1000) and returns entries `{"cid", "type", "ts", "peer", "prev"}` walked backward from the log HEAD.
