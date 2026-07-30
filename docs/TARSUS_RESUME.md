# Tarsus work checkpoint — 2026-07-30

## Current state

- Branch: `tarsus-paper-rewrite`
- The strict 50-node discovery/correctness gate is complete at 50/50.
- Passing artifact:
  `test_results/tarsus_campaign_smoke/n050-authority-cache/cells/smoke-n050-c0000100-s016-bloom-on`
- That run reached readiness on all 50 nodes and completed 100/100 writes with
  zero failures in 112.188 seconds.
- Generated `docker-compose.yml` is restored and no campaign is running.
- The full race suite, normal suite, shuffled suite, `go vet`, shell syntax
  checks, Python compilation checks, repeated distributed integration tests,
  and focused network/storage repetitions passed after the changes below.

## Completed implementation

- Added leased, epoch-fenced PHT mutation authority.
- Persisted write fences in PHT nodes so stale writers cannot overwrite state
  adopted by a newer authority epoch.
- Added mutation request IDs, retry deduplication, bounded routing, authority
  failover, and authority metrics.
- Corrected DHT validator ordering and typed-nil value-store handling.
- Fixed the default token-DHT construction path and several pre-existing test
  fixture/race failures.
- Corrected whole-payload storage for objects larger than 4 MiB and aligned
  transfer limits.
- Made the default replication target exactly seven total copies: three near,
  two mid-distance, and two far.
- Added active RTT/liveness checks, RTT-aware placement, unreachable-provider
  pruning, periodic crash auditing, deterministic repair coordination, and
  automatic replication repair.
- Corrected token-location removal/convergence and added a live libp2p
  durability/repair regression.
- Added versioned durable exact-name tuple records containing the multiset,
  ownership fence, and successful mutation results.
- Added lease-expiry crash handoff, higher-epoch stale-owner fencing, durable
  retry idempotence, and a three-node live-DHT failover test that passed 20/20.
- Routed general regex matches through the PHT catalog and then exact fenced
  owners, retaining the honest O(M) matching boundary.
- Added injected partial-write tests proving index-first publication cannot
  create an undiscoverable live tuple and stale hints cannot fabricate data or
  duplicate a consume.
- Added bounded lease-safe caches for exact-name tuple fences and owner state.
  Warm reads no longer repeat client and owner DHT lookups, while mutations
  still require replicated write/read confirmation and expired leases still
  advance the epoch before a request is retried.
- Replaced the resource monitor's per-container Docker calls with one batched
  sample per interval. The monitor now remains O(1) Docker daemon calls per
  sample at 50--100 nodes.
- Raised the campaign population default from 16 to 32 workers after a scaling
  probe; 64 workers did not improve wall time and increased summed work.
- Made replica count the repair invariant and RTT-class quotas a best-effort
  placement objective. Repair prefers missing classes, then fills the remaining
  shortfall from another healthy storage advertiser without exceeding seven.
- Serialized content-token publication: the source location is confirmed
  before replication, and the sending coordinator alone publishes each
  acknowledged replica. This removed a live race that lost the source from the
  token and left six advertised copies after a nominal seven-copy upload.
- Added and validated a production-current resilience campaign cell. A 10-node
  smoke run stored 5 MiB, reached exactly seven replicas, stopped a proven
  holder, hash-verified a surviving copy, repaired to a new seventh provider in
  30.590 seconds, and hash-verified a cold non-provider fetch.
- Made private campaign cells reproducible and network-isolated. Every cell now
  removes its declared Docker volumes before startup, so peer identities,
  peerstore records, tuples, and blocks cannot leak across cells.
  `--no-default-bootstrap` also removes the five public libp2p bootstrap
  identities from persistent and live peerstores, rather than merely declining
  to add them on the current invocation.
- Hardened binary-tree construction: every requested edge must remain connected
  after asynchronous Tarsus handshake verification, failed child-to-parent
  dials are retried in the reverse direction, and the last HTTP/dial error plus
  both node logs is preserved on terminal failure.
- Explicit `/connect` requests now force a bounded direct libp2p dial. This
  prevents an earlier opportunistic failure from turning every control-plane
  retry into an immediate local `dial backoff` rejection without a network
  attempt. A focused regression creates exactly that cached-backoff state and
  proves the explicit dial reaches the newly available peer.
- Topology construction now treats every binary-tree edge as a required,
  bounded administrative anchor. `/connect` can protect a trusted configured
  edge from opportunistic connection-manager trimming; the harness installs
  that tag at both endpoints, verifies both endpoints after the asynchronous
  handshake, and fails startup if any of the N-1 edges is absent. The tree
  protects at most three peers per node.
- Revalidated the hardened harness with
  `test_results/tarsus_campaign_smoke/resilience-n010-fresh-volumes-v2`.
  The validator-compliant 5 MiB run reached and restored exactly seven copies,
  repaired in 28.458 seconds, passed both hash checks, contained none of the
  public bootstrap IDs, emitted `COMPLETE`, and left zero containers running.
- Diagnosed the apparent 100-node Docker pair-connectivity failures as host
  IPv4 neighbor-table exhaustion, not Tarsus routing or public-IPFS traffic.
  The failed `resilience-n100-a1d761c` pilot produced 2,150 kernel
  `arp_cache: neighbor table overflow` messages against Fedora's default
  `gc_thresh3=1024`; its 100 nodes averaged 10.29 live neighbors and three
  nodes consequently became isolated. The strict availability gate correctly
  rejected the run at 0/100 after ten minutes.
- Removed a CLI-only dialer path that opened up to two opportunistic
  connections after every successful target dial and therefore bypassed the
  configured outbound bound. Learned authenticated addresses remain in the
  peerstore for later bounded maintenance.
- Set same-host campaign topology to three minimum outbound connections and
  added a host neighbor-capacity preflight. Host manifests record all three
  neighbor thresholds; startup and final artifacts capture kernel
  neighbor/conntrack exhaustion; validators reject any run that records it.
- Revalidated the bounded dialer at
  `test_results/tarsus_campaign_smoke/resilience-n010-bounded-outbound-v3`.
  The fresh 10-node, 5 MiB run indexed all ten availability tuples, reached
  exactly seven replicas, stopped a proven holder, repaired to a distinct
  seventh provider in 35.046 seconds, passed both retrieval hash checks,
  contained no public bootstrap identities, and left zero containers.
- The clean-commit `resilience-n100-c248025` pilot confirmed that removing
  opportunistic dials delayed but did not eliminate host exhaustion: topology
  progressed without degradation from node 100 through node 20, then the
  kernel recorded 60 neighbor-table overflows and the first dial failure at
  node 15. The run was stopped before its workload, its diagnostic artifact
  was preserved, and all containers were removed. The preflight reserve now
  accounts for the additional Kademlia/control-plane entries.
- The temporarily tuned `resilience-n100-52416e3` pilot completed all requested
  tree edges without degradation, proving the earlier pair failures were
  caused by host exhaustion. It then exposed a separate overlay-bound defect:
  nodes retained 16--74 live peers (mean 37.31) because libp2p's default
  connection-manager watermarks are 160/192. The startup guard rejected the
  resulting 100 kernel overflows before any workload.
- Added explicit Tarsus connection-manager watermarks. Campaign nodes use
  low/high 3/8, trim once per second with no grace interval, and back
  failed startup advertisements off from 500 milliseconds to 30 seconds.
  Kademlia's two nearest k=8 buckets are intentionally protected, so the host
  preflight budgets up to 16 protected DHT peers and three protected topology
  anchors in addition to the configured high watermark.
- Revalidated the capped overlay at
  `test_results/tarsus_campaign_smoke/resilience-n010-connection-cap-v1`.
  The validator-compliant 5 MiB run again indexed all nodes, maintained and
  repaired exactly seven copies in 35.051 seconds, passed both retrieval hash
  checks, and recorded no kernel exhaustion.
- The clean `resilience-n100-017054b` pilot proved the connection cap fixed
  host exhaustion: all 100 nodes started with 7--21 live peers (mean 15.87,
  versus the prior 37.31 mean and 74 maximum) and zero kernel events. It also
  exposed a distinct overlay-partition defect. All nodes successfully
  committed/refreshed their availability tuple, but the strict gate remained
  at 37/100. Live graph inspection found exactly three components of 31, 32,
  and 37 nodes, matching the three indexed query views; 27 requested tree
  edges had been pruned, including both root edges. The run timed out
  unchanged, preserved its artifact, restored host thresholds, and removed all
  containers.
- Added the protected-anchor topology described above and revalidated it at
  `test_results/tarsus_campaign_smoke/resilience-n010-protected-anchors-v1`.
  The fresh 5 MiB run retained all nine tree edges, reached exactly seven
  replicas, stopped a proven holder, fetched from a survivor in 0.117 seconds,
  repaired to a distinct seventh provider in 41.261 seconds, and completed a
  cold non-provider fetch in 0.139 seconds. Both hashes passed, both kernel
  diagnostics were empty, no public bootstrap identity appeared, and teardown
  left zero containers.
- The protected `resilience-n100-f2c044b` pilot retained all 99 tree edges,
  reached 100/100 indexed availability across all 16 shards, stored 8 MiB at
  exactly seven providers, survived a proven-holder stop, returned to seven in
  153.603 seconds, passed both hash checks, and recorded no kernel exhaustion.
  Its trace nevertheless invalidated it as final evidence: advertised replicas
  ranged from 1 to 14, 41 providers appeared, and none of the six original
  survivors remained in the final set. The old final-snapshot validator had
  accepted this churn; its `COMPLETE` marker was removed and the artifact is
  retained only as a diagnostic.
- Fixed the resulting bounded-overlay liveness defect. Replica verification
  now uses each token location's advertised address, forces a bounded direct
  dial when the provider is not already connected, and retries ping while the
  authenticated Tarsus handshake gate completes. An earlier no-address failure
  cannot suppress this address-aware attempt. Repeated live-libp2p and race
  tests cover a disconnected provider that is known only through its token.
- An initial address-aware 10-node run still exposed an intermittent
  single-probe false negative: after initially holding the six surviving
  replicas, the system eventually pruned healthy providers and amplified
  repair. That interrupted artifact is explicitly marked invalid at
  `test_results/tarsus_campaign_smoke/resilience-n010-address-aware-liveness-v1`.
  Two subsequent diagnostic runs passed strictly, so this was intermittent
  rather than a deterministic address-resolution failure.
- Added a conservative repeated-observation failure detector for replica
  pruning. The first failed address-aware probe retains the provider as a
  liveness suspicion; removal requires a second failed observation at least
  ten seconds later. A successful probe clears the evidence. This trades one
  additional audit interval of crash-repair latency for protection against
  destructive one-shot false positives. Focused normal tests passed 20
  repetitions, focused race tests passed five, and the full normal and race
  suites passed.
- Revalidated that detector in two fresh, strict 10-node cells:
  `resilience-n010-repeated-liveness-9b3104d-v1` repaired in 67.068 seconds
  and `resilience-n010-repeated-liveness-4fc1ccb-v2` repaired in 139.555
  seconds. Both retained all six healthy originals, admitted exactly one new
  provider, stayed between six and seven post-failure replicas, passed both
  content hashes, and left clean host/repository state.
- The second cell exposed why repair latency could vary by more than a minute:
  a failed node's two-minute storage-availability offer could remain eligible
  after its replica location was correctly pruned, causing repair to redial
  the dead node as a prospective replacement. Repair now excludes both
  existing providers and current-audit failures before candidate measurement,
  discovers the distributed offer pool once rather than once per RTT class,
  and reuses that pool for preferred placement and fixed-count fallback.
  A regression asserts that a stale failed-node offer receives only its single
  verification probe and is never attempted as its own replacement.
- Strengthened resilience validation for the single-holder failure experiment:
  every repair observation must remain between six and seven providers, the
  six live originals must survive, no over-replication is accepted, and only
  one new provider may enter the trace. The churned 100-node pilot is rejected
  under these rules while the clean protected 10-node artifact still passes.

## Current plan

1. [x] Preserve the branch/checkpoint and baseline 49/50 evidence.
2. [x] Implement and test fenced index authority, retry deduplication, and
   failover.
3. [x] Fix the native authority timing stall and bound overlay routing.
4. [x] Pass the strict 50/50 distributed correctness gate.
5. [x] Diagnose and fix the pre-existing and distributed failures exposed so
   far.
6. [x] Address the manuscript's remaining distributed-filesystem limitations
   in implementation and claims.
7. [x] Analyze and optimize the growing short-test latency and resource costs.
8. [ ] Run the full 100-node failure, repair, query-cost, and resource campaign.
9. [ ] Produce figures/results and finish `paper/final.tex`.
10. [ ] Remove obsolete results, dated harnesses, and abandoned planning files.
11. [ ] Emit a brief, parseable plain-English document describing the Tarsus
    rewrite for the research group.

## Performance checkpoint

The 10-node, 100-name regression used identical workload cells with one query
repetition. Before the tuple caches, exact reads took 2.28--2.76 ms and pattern
reads took 11.85--17.81 ms. Across three post-cache cells, exact reads took
0.42--1.47 ms and pattern reads took 3.65--15.12 ms; all writes and queries
were correct. Warm exact reads perform zero tuple-state DHT calls after the
confirmed mutation.

Population at 16 workers remained bimodal at 2.15--4.11 seconds rather than
showing monotonic degradation. A 32-worker probe completed in 2.09 seconds
with 100/100 successful writes. A 64-worker probe also took 2.09 seconds but
used 18.76 seconds of summed mutation service time versus 16.86 seconds at 32,
so 32 is the new default.

The old resource monitor issued one blocking `docker stats` call per
container, which makes instrumentation itself scale with node count. The
replacement issues one batched call and successfully captured four complete
10-node samples in the regression cells.

## Immediate next work

The production-current failure/repair harness has passed its fresh-volume
10-node end-to-end smoke gates. Successive 100-node pilots exposed and fixed
discarded connection diagnostics, stale libp2p dial backoff, unbounded
post-handshake over-connection, and connection-manager pruning of the sparse
backbone. What initially appeared to be arbitrary Docker pair failure is
explained by contemporaneous kernel neighbor-table overflow; what looked like
a lost-index-update plateau was proved to be three disconnected overlay
components. Both failure modes now have explicit startup guards. The first
protected 100-node workload then exposed false provider pruning and repair
amplification. Address-aware probes fixed missing route knowledge but one
intermittent false negative remained, so pruning now requires two separated
failures and the stricter trace validator rejects any recurrence. Two detector
smokes passed and exposed a stale-offer retry responsible for the slower run.
The immediate next step is one fresh 10-node validation of that candidate-pool
fix, followed by a 100-node, one-trial pilot using the default 8 MiB payload and
protected tree, with temporary host neighbor limits of 2048/4096/8192 and
restoration of the Fedora defaults 128/512/1024 afterward. If that validates,
run the full query-cost/shard/Bloom matrix and five-trial resilience cell,
validate every artifact, and only then generate manuscript figures. Do not use
the dated vnIPFS/Swarm repair scripts as paper evidence.

The remaining manuscript boundaries are deliberate or experimental: DHT
convergence is not consensus under arbitrary partitions; retry results and
exact-name records are bounded; index shards trade write parallelism for query
fanout; regex is O(M); RTT is not a geographic-domain oracle; and one-host
Docker does not prove regional resilience.

`paper/final.tex` has been updated to state the durable handoff assumptions and
proof obligations. Local PDF compilation is currently blocked because this
machine's TeX installation does not contain `IEEEtran.cls`, not because of a
reported TeX syntax error.

## Resume prompt

Open this conversation and send:

> Resume the Tarsus work from `docs/TARSUS_RESUME.md`. Inspect the branch and
> working tree first, then continue the 100-node resilience pilot. The
> 50/50 gate and the distributed-filesystem implementation/claim gate are
> complete; do not rerun the large 50-node cell unless a relevant change
> invalidates that result.

If this conversation is unavailable, a new Codex conversation opened in the
repository can use the same prompt.
