# Tarsus claim/evidence audit

This file is an internal drafting aid, not part of the submitted manuscript.
Every visible paper claim should be supported by code, a test, an experiment,
or an explicitly stated assumption/limitation.

| Claim | Current evidence | Status | Minimum next action |
|---|---|---|---|
| Tarsus implements its own tuple space | `internal/tuplespace/native.go`, `distributed.go` | Supported | Retain regression tests |
| Tuple space is a multiset | `NativeTupleSpace.tuples`; duplicate-name test | Supported | None |
| `read` is non-consuming | implementation and repeated-read test | Supported | None |
| Concurrent `get` consumes one instance once at a stable owner | owner-side mutex; local and two-client concurrency tests | Supported under stated assumptions | Add higher-contention multi-peer experiment |
| Exact names have deterministic Kademlia-aligned owners | `DHTTupleOwnerResolver`, unit tests, and live-DHT owner/failover test | Implemented | Measure owner distribution in the campaign |
| General associative patterns work without external TSH | PHT catalog scan followed by exact fenced-owner verification; indexed regex regression | Supported with O(M) regex matching | Measure cost as catalog size grows |
| Prefix queries use sharded PHTs in production | shard stores, per-shard ownership keys, incremental PHT, production constructors, live DHT record test, direct fanout/node-fetch counters, and validated distributed smoke cell | Implemented and instrumented | Run planned 10/50/100-node catalog-size/selectivity campaign |
| Substring queries use Bloom pruning in production | live Bloom maintenance, parallel shard queries, indexed substring path, multi-peer integration test, branch/pruning counters, and selectable Bloom-off path | Implemented, instrumented, and ablation-ready | Run paired Bloom on/off campaign cells |
| Index mutation authority is distributed | full-name hash selects a configurable number of shards; each shard resolves a distinct overlay owner; request-scoped per-shard/local/remote/failure/service-time counters | Implemented and instrumented | Run mutation throughput and owner-distribution experiment at 1/4/16/64 shards |
| Tuple state survives owner restart | Versioned `/tuplestate/` record contains the multiset and successful mutation results; reconstruction regression | Supported | Add campaign restart cell |
| Exclusive consumption survives ownership change | Lease-bearing epoch/writer fence, persisted retry results, stale-writer rejection, 20/20 three-node live-DHT crash handoffs | Supported under DHT-convergence and bounded-clock assumptions | Add partition/reconciliation and campaign failure cells; do not claim consensus |
| Content tokens are separate from block bytes | token store plus `DirectFetch` path | Supported | Add end-to-end trace/measurement |
| Returned content is hash verified | `GetBlock`/direct-fetch verification path and tests | Supported | Cite exact test and report failure behavior |
| Upload creates a fixed number of opportunistic replicas | Exact largest-remainder target allocation and placement tests; default is 3/2/2 for seven total copies | Implemented | Measure achieved replica count and completion time |
| Default upload seeks RTT-diverse placement | active RTT probes, cached classification, exact near/mid/far targets, and opportunistic fallback | Implemented as a latency heuristic | Measure achieved classes; do not call them geographic domains |
| Repair can classify and fill RTT-category shortfalls | periodic liveness audit, stale-provider pruning, deterministic coordinator, live libp2p byte-for-byte repair regression | Implemented and tested at small scale | Run end-to-end controlled-latency recovery campaign |
| Replica placement survives regional failure | Docker data does not model independent regions | Unsupported | Multi-host/netem failure experiment; otherwise present only as motivation |
| Catalog growth compares fairly with Swarm | existing runs use potentially different retrieval/cache semantics | Not yet defensible | Rerun equivalent cold-remote workload |
| Lookup or propagation scales logarithmically | DHT theory supports expected lookup; existing counter is not a proof | Partially supported | Report measured routing work; avoid converting a reference curve into data |
| ACAN is realized by active metadata for discovery, placement, and repair | tokens, tuple coordination, repair paths | Architectural interpretation | Tie each ACAN responsibility to a protocol and experiment |
| SMC2 dynamically selects available resources | peer selection, RTT classification, storage offers, direct fetch | Partially implemented | Define inputs precisely and measure selection/redistribution; avoid universal-scale language |

## Reviewer-derived acceptance gates

1. The introduction must identify associative coordination—not provider/data
   decoupling—as the central problem and contribution.
2. Related work must directly compare Linda-style coordination, PHT/DHT query
   systems, IPFS, Bitswap, Filecoin where relevant, and Swarm.
3. The system model must identify crash/omission assumptions, stable ownership,
   content integrity, and properties not provided.
4. Every abstract and introduction result claim must point to a reported
   experiment.
5. The evaluation must contain real results, equivalent baselines, multiple
   trials, and variability.
6. Port numbers, opcodes, token supply, KYC, and legacy white-paper material do
   not belong in the research narrative.
7. Seven replicas must be described as a configurable durability policy, not a
   derivation of Byzantine fault tolerance.
