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
| Exact names have deterministic Kademlia-aligned owners | `DHTTupleOwnerResolver` and unit test | Implemented | Test agreement using multiple live DHT nodes |
| General associative patterns work without external TSH | peer scan plus passing multi-peer wildcard/regex integration test | Supported for reachable peers | Measure cost as peer count grows |
| Prefix queries use sharded PHTs in production | shard stores, per-shard ownership keys, incremental PHT, production constructors, live DHT record test, direct fanout/node-fetch counters, and validated distributed smoke cell | Implemented and instrumented | Run planned 10/50/100-node catalog-size/selectivity campaign |
| Substring queries use Bloom pruning in production | live Bloom maintenance, parallel shard queries, indexed substring path, multi-peer integration test, branch/pruning counters, and selectable Bloom-off path | Implemented, instrumented, and ablation-ready | Run paired Bloom on/off campaign cells |
| Index mutation authority is distributed | full-name hash selects a configurable number of shards; each shard resolves a distinct overlay owner; request-scoped per-shard/local/remote/failure/service-time counters | Implemented and instrumented | Run mutation throughput and owner-distribution experiment at 1/4/16/64 shards |
| Tuple state survives owner restart | Native owner state is memory-resident | Unsupported | Add persistence or state clearly as a prototype limitation |
| Exclusive consumption survives ownership change | No ownership-transfer protocol | Unsupported | Do not claim; future work unless required by experiments |
| Content tokens are separate from block bytes | token store plus `DirectFetch` path | Supported | Add end-to-end trace/measurement |
| Returned content is hash verified | `GetBlock`/direct-fetch verification path and tests | Supported | Cite exact test and report failure behavior |
| Upload creates a fixed number of opportunistic replicas | `ReplicateToNPeers`; replication tests | Implemented | Measure achieved replica count and completion time |
| Default upload enforces RTT-diverse 40/30/30 placement | ordinary replication labels selected peers midrange | Unsupported | Do not claim for upload; either integrate category-aware repair or narrow wording |
| Repair can classify and fill RTT-category shortfalls | verification and category-aware repair code; availability advertisements retry transient startup failures and campaign preflight requires one indexed offer per node | Implemented in components with validated discovery startup | Run end-to-end controlled-latency recovery test |
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
