# Token Protocol

Tokens route **physical locations** of data through the DHT. The DHT does not store block content; it stores tokens that tell clients where to fetch blocks via DirectFetch.

---

## Token Structure

```json
{
  "key": "<64 hex chars, SHA256 hash of data>",
  "locations": [
    {
      "provider_id": "<peer.ID>",
      "address": "<multiaddr>",
      "rtt_ns": 0
    }
  ],
  "timestamp": 1234567890000000000,
  "version": 1
}
```

| Field      | Type     | Description                                                |
|-----------|----------|------------------------------------------------------------|
| key       | string   | 64 hex chars = Key (SHA256 of data). Primary identifier.  |
| locations | array    | Physical locations (ProviderID + Address) where data lives. |
| timestamp | int64    | Unix nanoseconds. Creation or last update.                 |
| version   | int      | Incremented on updates; used for conflict resolution.      |

**Location:**
| Field       | Type   | Description                          |
|------------|--------|--------------------------------------|
| provider_id| string | Peer ID of the provider.             |
| address    | string | Multiaddr for dialing the provider.  |
| rtt_ns     | int64  | Round-trip time in nanoseconds (opt).|

Token is **stateless**: no internal mutable state; safe to serialize/deserialize and read concurrently.

---

## DHT Key Format

- **Namespace:** `/tokens/`
- **Full key:** `/tokens/` + hex(key)
- **Example:** `/tokens/a1b2c3d4...` (64 hex chars)

---

## Token Lifecycle

### Put (SyncTokenOnPut)

When a block is stored locally:
1. Key = KeyFromData(blockData)
2. Create or update token in DHT
3. If token exists: add current peer to Locations (deduplicated)
4. If token absent: create token with current peer as sole location

### Delete (SyncTokenOnDelete)

When a block is deleted locally:
1. Remove current peer from token Locations
2. If Locations empty after removal: token stays (DHT TTL handles expiry)

### Replication (SyncTokenOnReplication)

When a replica is created:
1. Add new replica peer to token Locations
2. Uses conflict resolution to merge with concurrent updates

---

## DHT Operations

| Operation | Description |
|-----------|-------------|
| PutToken | Store token at DHT key `/tokens/<hex(key)`. TTL: 48h (libp2p default). |
| GetToken | Retrieve token by key. Returns Locations for DirectFetch. |
| UpdateTokenLocations | Replace Locations (for replication). Uses conflict resolution. |

---

## Conflict Resolution

Concurrent updates use **optimistic concurrency** and **location merge**:

- **PutTokenWithConflictResolution**: Read-modify-write with retry (default 3 attempts)
- **UpdateTokenWithConflictResolution**: Same pattern for update functions
- **ResolveTokenConflict**: Merge strategy:
  - Combine Locations from both versions
  - Deduplicate by ProviderID
  - Use higher version and later timestamp
  - Increment version for merged result

---

## Direct Fetch Protocol

**Protocol ID:** `/sng40/direct-fetch/1.0.0`

After GetToken, the client fetches block data directly from providers.

### Client (Fetcher)

1. Connect to provider (if not connected)
2. Open stream with protocol `/sng40/direct-fetch/1.0.0`
3. Send: `key (64 hex chars) + "\n"`
4. Read status: `"OK\n"` or `"ERROR: ...\n"`
5. If OK: read block size (int + "\n"), then block bytes
6. Verify: KeyFromData(blockData) == key

### Server (Provider)

1. Read key (hex string + newline)
2. Parse key; lookup block by key locally
3. Write `"OK\n"`
4. Write block size (int + "\n")
5. Write block data
6. Close stream

### Limits

- Block size max: 10 MB

---

## Get Flow (End-to-End)

1. **Local:** GetBlockByKey (Key→CID→blockstore)
2. **Token lookup:** GetToken(key) from DHT → Locations
3. **DirectFetch:** Open streams to each Location in parallel
4. **Verify:** KeyFromData(blockData) == key
5. Return first successful block

---

## Gateway Integration

When Gateway is configured, TokenStore routes `/tokens/` keys through the tuple space. Query by key pattern returns token JSON (Locations); caller performs DirectFetch. Gateway never fetches or transfers block data.
