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

## Replica selection algorithm

Replica placement (`internal/storage/replication.go`) targets a distribution across three RTT-based distance categories rather than picking peers arbitrarily:

- **`ReplicationVector`**: desired percentage split across `Near`/`Midrange`/`FarFlung`. `DefaultReplicationVector()` is 40% / 30% / 30%; a high-performance/reliable-network deployment might instead use ~90% / 10% / 0%.
- **`DistanceCategory`** classification: `ClassifyDistanceByRTT(rtt, thresholds)` buckets a peer using `RTTThresholds` (`DefaultRTTThresholds()`: Near `< 50ms`, Midrange `50–200ms`, Far-flung `>= 200ms`).
- **`PeerCandidate`**: a peer's `RTT`, `DistanceCategory`, `CommittedStake` (0 if the network isn't tokenized), `StorageAvailability`, and `ReputationScore` (0.0–1.0).
- **`SelectionCriteria`** / `DefaultSelectionCriteria(tokenized)`: per-factor weights, normalized to sum to 1.0. Tokenized networks default to Stake 30% / RTT 20% / Storage 20% / Reputation 30%; non-tokenized networks redistribute the stake weight to Stake 0% / RTT 25% / Storage 25% / Reputation 50%.
- **`ScoreCandidate(candidate, criteria, maxStake, maxRTT, maxStorage)`**: normalizes each factor to `[0.0, 1.0]` against the supplied pool maxima (RTT is inverted — lower is better; a candidate with RTT 0 gets the maximum RTT score), then combines them via the criteria's weights into one score.
- **`SelectReplicaCandidates(candidates, desiredCategory, criteria, count)`**: filters to the desired distance category, computes per-pool maxima, scores every candidate, sorts descending (ties broken by higher reputation, then lower RTT), and returns the top `count`. Returns `nil` if no candidate matches the desired category.

This scoring/selection logic is distinct from — and runs before — the token-locations bookkeeping described above; token sync just records *where* replicas ended up after selection and placement.

## Catalog reconciliation (IBLT)

Independent of per-key replication, every node periodically reconciles its whole content catalog against each connected neighbor using an **Invertible Bloom Lookup Table (IBLT)** (`internal/sync/{catalog,iblt,reconcile}.go`, wired to the libp2p host in `pkg/node/iblt_catalog.go`'s `InstallCatalogIBLT`). This lets two peers compute the *symmetric difference* of their key sets by exchanging one compact, fixed-size sketch, instead of transferring full catalogs.

- **Cadence**: every connected neighbor is IBLT-exchanged with once per `catalogIBLTInterval` (default 5 minutes), each exchange bounded by a 30s timeout.
- **Protocols**: `mysync.IBLTProtocolID` (exchange local vs. remote IBLT snapshots) and `mysync.IBLTFetchProtocolID` (resolve key hashes the exchange revealed as missing into CIDs, on request).
- **Flow**: build a local IBLT from `stack.ProviderRecords.Snapshot()` (`BuildIBLTFromCIDs`, default 256 cells) → open a stream to a neighbor → exchange IBLTs → `Peel` the difference to recover which key hashes each side has that the other doesn't → for hashes the *neighbor* has and *we* don't, request CID resolution over `IBLTFetchProtocolID`, then fetch each resolved block via the block service (landing it in the local store and provider records).
- **Best-effort**: `RequestFetch` (the active fetch path) swallows all errors and simply returns early on failure — this is a background repair mechanism, not a path callers can observe failures through.
- Larger `catalogIBLTCellCount` tolerates bigger set differences before `Peel` fails to fully recover them, at the cost of a larger sketch per exchange.

This subsystem runs unconditionally on every node (both the CLI and embedded-library startup paths) whenever `stack.ProviderRecords` is configured; it is a continuously-running eventual-consistency mechanism layered on top of the per-put replication and lookup paths described above.

## Further reading

- `docs/SWARM_COMPARISON_TESTS.md` — lookup complexity, catalog-growth upload/download vs object count, and interpretation. Catalog **download** for both stacks defaults to **host-wall** timing (`CATALOG_GROWTH_HOST_WALL_GET`, default **1**) so vn-IPFS and Swarm CSVs are comparable in magnitude (docker exec + request). Default catalog **payload** is **262144** bytes; multi-trial runs use **`catalog_growth_merge.sh`** for row-wise averages when **`CATALOG_GROWTH_TRIALS` > 1** with a clean store per trial. Swarm **`CATALOG_GROWTH_SWARM_FETCH`**: **`latest`** vs **`first`** + pinning tradeoffs as documented there.
- `docs/API.md` — `network_hops`, PUT semantics, `/lookup`.
