# Purpose: Replication and routing semantics (theory vs measured comparison metrics)

## Token routing and DHT

vn-IPFS uses a key-based token stored in the DHT (`TokenNamespace` + key hash) with provider locations. PUT triggers local persistence first; token sync and peer replication can proceed asynchronously (see `docs/API.md`).

**Directory blocks:** Namespace directories are stored as ordinary blocks (see `docs/NAMESPACE.md`). Each directory key is announced and replicated like any other put; path resolution walks directory keys to child keys using the same Get/token path.

## Lookup hop count vs ideal O(log N)

- **Theory (Kademlia-style)**: In a mature table with many peers, iterative lookups often visit **O(log N)** peers in expectation.
- **What we measure**: `network_hops` in the control API and `lookup-key` counts **`routing.SendingQuery`** events during the instrumented window (e.g. `GetToken` on `/lookup`, or the `GetClosestPeers` phase in `lookup-key`). That count correlates with routing work but is **not** a full formal proof of asymptotic behavior.
- **Comparison suite reality**: `lookup_complexity_results.csv` frequently shows **0 / N/A / flat** `lookup` hops vs `node_count` because of cold-start timing, retries, partial failures, or saturation at modest N. **Do not** infer O(log N) validation from a single run.
- **Upload latency at large N** reflects replication load, disk, and HTTP path — **not** the same axis as hop count.

## Further reading

- `docs/SWARM_COMPARISON_TESTS.md` — lookup complexity test and interpretation.
- `docs/API.md` — `network_hops`, PUT semantics, `/lookup`.
