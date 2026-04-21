# Purpose: Replication and routing semantics (theory vs measured comparison metrics)

## Token routing and DHT

vn-IPFS uses a key-based token stored in the DHT (`TokenNamespace` + key hash) with provider locations. PUT triggers local persistence first; token sync and peer replication can proceed asynchronously (see `docs/API.md`).

**Dial maintenance:** The node targets **20** minimum outbound libp2p connections by default (`--min-outbound`). The effective target is capped at **N−1** when `CLUSTER_NODE_COUNT` or `--cluster-nodes` is set to **N**, and otherwise capped by the number of distinct peers known in the peer store with addresses (so small clusters do not chase an unreachable target).

**Directory blocks:** Namespace directories are stored as ordinary blocks (see `docs/NAMESPACE.md`). Each directory key is announced and replicated like any other put; path resolution walks directory keys to child keys using the same Get/token path.

## Lookup hop count vs ideal O(log N)

- **Theory (Kademlia-style)**: In a mature table with many peers, iterative lookups often visit **O(log N)** peers in expectation.
- **What we measure**: `network_hops` in the control API and `lookup-key` counts **`routing.SendingQuery`** events during the instrumented window (e.g. `GetToken` on `/lookup`, or the `GetClosestPeers` phase in `lookup-key`). That count correlates with routing work but is **not** a full formal proof of asymptotic behavior.
- **Comparison suite**: `lookup_complexity_test.sh` writes **`hops`** as an **O(log N) reference** in cluster size (for complexity plots) and **`hops_raw`** as the measured cold `lookup-key` counter when available.
- **Fig06 reference curve** (`scripts/analysis/matrix_paper_plots.py`): smooth **k·log₂(N)** uses **k_ls = (1−w)·k_network + w·k_reference** with **k_network** / **k_reference** from least-squares through the origin on per-**N** means of **`hops_raw`** and **`hops`**, and **w = LOOKUP_PLOT_K_BLEND_REF** (default **0.5**). Use **`LOOKUP_PLOT_K_FACTOR`** (default **1**) as an extra scalar on **k_ls** for the plotted line only.
- **Upload latency at large N** reflects replication load, disk, and HTTP path — **not** the same axis as hop count.

## Further reading

- `docs/SWARM_COMPARISON_TESTS.md` — lookup complexity, catalog-growth upload/download vs object count, and interpretation. Catalog **download** for both stacks defaults to **host-wall** timing (`CATALOG_GROWTH_HOST_WALL_GET`, default **1**) so vn-IPFS and Swarm CSVs are comparable in magnitude (docker exec + request). Swarm **`CATALOG_GROWTH_SWARM_FETCH`**: **`latest`** vs **`first`** + pinning tradeoffs as documented there.
- `docs/API.md` — `network_hops`, PUT semantics, `/lookup`.
