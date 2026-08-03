# Key-Based API Endpoints

Control server HTTP API. Base URL: `http://127.0.0.1:<port>` (port from daemon control file).

**Key format:** 64-character hex string (SHA256 hash of data). Primary identifier for storage and token routing.

---

## PUT /put

Stores one immutable block. Key is derived from data (`SHA256(data)`). Token is
synced to the DHT for discovery. This compatibility endpoint does not chunk a
logical object; the named-object client streams objects into 1 MiB immutable
blocks before calling it.

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

Named-object publication has different semantics: a strict signed name head is
rejected until its manifest and every chunk meet that record's replica and
placement counts.

---

## Mutable named objects (v1)

All mutating requests contain either `record_cbor` (base64 in JSON) or a
`record` object. Nodes re-encode record objects as canonical DAG-CBOR and
verify the Ed25519 signature. `NameID` is 64 lower-case hex characters derived
from the namespace and normalized path. See `docs/MUTABLE_NAMES.md` for the
signed schemas and authorization rules.

| Endpoint | Method | Purpose |
|---|---|---|
| `/v1/names` | POST | Commit a signed generation-zero name |
| `/v1/names/{NameID}` | GET | Resolve and verify the newest generation |
| `/v1/names/{NameID}` | PUT | Expected-generation signed update |
| `/v1/names/{NameID}` | DELETE | Expected-generation signed tombstone |
| `/v1/names/search` | GET | Prefix/suffix search of current searchable heads |
| `/v1/locks/acquire` | POST | Acquire a signed exact-name or subtree lease |
| `/v1/locks/renew` | POST | Renew the same holder/fencing lease |
| `/v1/locks/release` | POST | Release a signed lease without resetting its fence |

Create request:

```json
{"record_cbor":"<base64 canonical DAG-CBOR>"}
```

Update or delete request:

```json
{"expected_generation":4,"record_cbor":"<base64 canonical DAG-CBOR>"}
```

A stale generation returns `409 Conflict`. Invalid identifiers, signatures,
capabilities, policies, tombstones, encodings, or predecessor hashes return
`400`. Failure to satisfy strict prepublication replication returns `503` and
does not expose the proposed head.

Search accepts `prefix` and `suffix`. Results include only verified,
non-tombstoned current records and report `fanout_attempted`,
`fanout_completed`, `index_repairs_pending`, and `complete`. A failed shard or
pending idempotent index repair sets `complete=false` and supplies
`incomplete_cause`; exact NameID resolution remains available.

The supported client workflow is:

```bash
go build -o bin/tarsusctl ./cmd/tarsusctl
bin/tarsusctl object put --api http://127.0.0.1:2892 \
  --file ./a.dat --path /projects/a.dat --signing-key "$ED25519_PRIVATE_HEX"
bin/tarsusctl object get --api http://127.0.0.1:2892 \
  --name-id "$NAME_ID" --reader-private "$X25519_PRIVATE_HEX" --output ./a.dat
```

`object put` and `object update` stream, chunk, encrypt, wrap keys, sign, stage
blocks, wait for the requested copy count, and only then submit the mutable
head. `object get` verifies and reconstructs every chunk. `object delete` signs
a tombstone; `object search` exposes completeness metadata.

**Key-based usage:** Use `multihash_hex` as the key for subsequent GET operations.

**Errors:** `400` invalid request; `413` body too large; `500` storage error (e.g. lock contention).

**Diagnostics:** Set `SNG40_LOG_PUT_PHASES=1` to log server-side durations for `PutBlock` vs routing-table + local mapping (token DHT + replication remain async).

---

## Namespace (first-class directories)

Directory blocks are JSON (`kind: "tarsus-directory-v1"`) stored via the same `PutBlock` pipeline as opaque bytes; each mutating call returns a **new** `dir_key` (copy-on-write). The decoder retains read compatibility with the pre-rename discriminator. See `docs/NAMESPACE.md` for semantics.

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

## Other Endpoints

| Endpoint       | Method | Purpose                    |
|----------------|--------|----------------------------|
| /health        | GET    | Liveness (returns "ok")    |
| /dht/status    | GET    | DHT table and live-peer counts |
| /metrics       | GET    | Node metrics (JSON)        |
| /storage/stats | GET    | Disk bytes for blockstore  |
| /restore       | POST   | Restore blocks by CID list |
| /restore/status| GET    | Restore job status         |
| /shutdown      | POST   | Graceful node stop         |
| /peers         | GET    | Dial candidates            |
| /connect       | POST   | Connect to peer            |
| /neighbors     | GET    | Live libp2p neighbors      |
| /id            | GET    | Peer ID                    |
| /events        | GET    | Event stream (SSE)         |
| /lookup        | GET/POST | Token lookup only (hops + ms) |
