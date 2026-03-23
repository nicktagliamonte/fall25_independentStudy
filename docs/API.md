# Key-Based API Endpoints

Control server HTTP API. Base URL: `http://127.0.0.1:<port>` (port from daemon control file).

**Key format:** 64-character hex string (SHA256 hash of data). Primary identifier for storage and token routing.

---

## PUT /put

Stores a block. Key is derived from data (`SHA256(data)`). Token is synced to DHT for discovery.

**Method:** `POST`

**Request:**
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

PUT returns immediately after the first successful store (local + routing table). Replication to peers runs asynchronously, matching Swarm's behavior for fair comparison.

**Key-based usage:** Use `multihash_hex` as the key for subsequent GET operations.

**Errors:** `400` invalid request; `500` storage error (e.g. lock contention).

---

## POST /get

Fetches a block by key. Prefer `key` over `cid`. Resolves via local store, then token routing (GetToken → DirectFetch), then stack fallback.

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

**Errors:** `400` invalid key/cid, key or cid required, key not found in routing table; `404` block not found.

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
| /metrics       | GET    | Node metrics (JSON)        |
| /storage/stats | GET    | Disk bytes for blockstore  |
| /restore       | POST   | Restore blocks by CID list |
| /restore/status| GET    | Restore job status         |
| /shutdown      | POST   | Graceful node stop         |
| /peers         | GET    | Dial candidates            |
| /connect       | POST   | Connect to peer            |
| /neighbors     | GET    | DHT neighbors              |
| /id            | GET    | Peer ID                    |
| /events        | GET    | Event stream (SSE)         |
