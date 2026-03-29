# Purpose: First-class directories (namespace) — schema, replication, and ACL hooks

## Model

A **directory** is a normal content-addressed block stored with the same `PutBlock` path as any other payload. Its key is `SHA256(encoded_bytes)` (64 hex). The wire format is JSON with discriminator `kind: "vnipfs-directory-v1"` and `entries`: a map from single-segment names (no `/`, no `..`) to **child object keys** (64 hex), each pointing at either another directory block or an arbitrary blob.

This mirrors IPLD / UnixFS-style directory nodes: the tree is a Merkle structure; listing and traversal are walks over immutable blocks.

## Relationship to Put/Get and provider records

- **Directory CID vs entry keys:** The block has one **CID** (IPFS-style) and one **Key** derived from raw bytes. Each **entry value** is a **Key** to a child block, not embedded CIDs. Clients resolve children via `Get`/`GetBlock` by key; provider records and DHT tokens are keyed the same way as for any block (see `docs/REPLICATION.md`).
- **Listing:** Load the directory block by key, then interpret `entries`. No separate index is required for correctness (only for performance at scale).
- **Paths:** A path is a `/`-separated suffix relative to a **root key** (often a user or policy root). Resolution loads each directory in sequence; the final key may refer to a file blob or another directory.

## Concurrent edits: single-writer vs CRDT

The implementation uses **copy-on-write (single-writer semantics)** per directory: each `link`, `unlink`, or `rename` produces a **new** block and thus a **new** key. Concurrent writers must coordinate out-of-band (or use a higher layer) to avoid lost updates; readers always see immutable snapshots. A **CRDT** directory (e.g. OR-Set of links) is not implemented here but could replace the `entries` map in a future version if mergeable namespace state is required.

## Unlink and tombstones

Removing a name allocates a new directory block **without** that name. Older directory keys remain valid immutable snapshots; they are not erased. Tombstone-style deletion at the data layer can be represented by replacing a child with a small marker block if needed; the default `unlink` only updates the directory map.

## Replication and routing

Directory blocks use the same replication path as other puts: after `PutBlock`, the control server updates the routing table and schedules async token sync and `ReplicateToNPeers` with the same replication factor policy as user content (`ReplicationFactorR` in `internal/control/server.go`). Resolving paths on a peer uses `GetBlock` so missing blocks can be fetched via token routing.

## ACL and policy roots

ACL attachment is modeled at the **policy root**: a root key identifies a namespace subtree. Enforcement (who may `link`/`unlink`) is not implemented in the HTTP handlers described here; the design allows attaching authorization checks to those operations once a token or capability system is wired. Paths resolve **under** that root for naming; object keys remain global content addresses.

## See also

- `docs/API.md` — `/namespace/*` endpoints.
- `docs/REPLICATION.md` — token routing and replication overview.
