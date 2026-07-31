TARSUS REWRITE CHANGE SUMMARY

|AREA|CHANGE|WHY|
|-----------|------------|:--------:|
NAME | Renamed the current system and paper from vnIPFS to Tarsus. | Separate this architecture from IPFS and the rejected design.
TUPLE MODEL | Replaced the two-space design with one native tuple space. | Give applications and storage metadata one coordination interface without depending on Linda.
TUPLE OPERATIONS | Put adds one tuple, Read observes one tuple, and Get atomically removes one tuple. | Support publication, non-consuming observation, and exclusive work claims.
TUPLE MULTISET | Store duplicate names and values as distinct instances. | Preserve tuple-space semantics and allow repeated work or advertisements.
EXACT OWNERSHIP | Route each exact tuple name to one deterministic Kademlia owner. | Avoid catalog scans and provide one serialization point.
EXCLUSIVE GET | Select and remove a tuple in one owner critical section. | Prevent two successful reachable clients from claiming the same instance.
CONFIRMED STATE | Store tuple instances, fence, version, and retained mutation results in one DHT record. | Let state and retry outcomes move together after an owner crash.
RETRY SAFETY | Return a retained result when a request ID repeats. | Prevent a lost acknowledgement from turning one Put or Get into two mutations.
OWNER HANDOFF | Add lease expiry and epoch/writer fences for successor adoption. | Recover committed state while rejecting a converged stale owner.
FAILURE SCOPE | State the clock, lease, DHT convergence, crash, and omission assumptions. | Avoid presenting fenced crash recovery as partition-tolerant consensus.
ASSOCIATIVE SEARCH | Add Prefix Hash Trees for prefix and substring candidate discovery. | Find tuples whose exact names are not known without broadcasting to every peer.
BLOOM SEARCH | Add per-subtree Bloom summaries for substring n-grams. | Skip branches that cannot match selective substring queries.
AUTHORITATIVE MATCH | Verify every index candidate at its exact owner. | Make stale or false-positive index entries cost work instead of producing an invalid result.
INDEX UPDATES | Replace full catalog rebuilding with incremental PHT insertion and deletion. | Keep mutation cost proportional to the changed path.
INDEX SHARDS | Hash full tuple names across configurable PHT shards. | Parallelize mutations and spread common human-readable prefixes.
MUTATION AUTHORITY | Elect one overlay owner to serialize each shard's PHT writes. | Prevent concurrent read-modify-write updates from losing names.
INDEX FENCES | Version PHT nodes and fence stale shard writers. | Make DHT selection converge on the current mutation authority's state.
QUERY FANOUT | Read index shards in parallel and report partial failure. | Bound fanout by shard count and expose when a result is not an exhaustive search.
QUERY METRICS | Record shard, PHT, pruning, candidate, owner, match, and latency counters. | Explain query time with actual system work instead of wall time alone.
MUTATION METRICS | Record local, remote, failed, per-shard, authority, and service-time counters. | Measure the mutation bottleneck and the benefit of sharding.
CONTENT IDENTITY | Address immutable bytes with a SHA-256-derived key. | Detect altered content without trusting providers.
PROVIDER METADATA | Route content keys and provider locations separately from bytes. | Let providers change without changing content identity and keep bulk data out of tuple coordination.
DIRECT FETCH | Fetch bytes directly from providers and verify the requested key. | Avoid tuple-space data broadcast and reject corrupt responses.
DIRECTORY NAME | Encode new directories as tarsus-directory-v1 and read the old discriminator. | Complete the rename without orphaning pre-rename content-addressed blocks.
REPLICA POLICY | Target seven opportunistic replicas with a three-near, two-middle, two-far RTT goal. | Combine availability count with easy best-effort latency diversity.
REPAIR | Probe providers, require repeated failure evidence, elect one coordinator, and fill deficits. | Repair one lost provider without pruning healthy replicas or over-replicating.
PRIVATE OVERLAY | Disable public bootstrap peers and create a protected bounded-degree campaign topology. | Prevent unrelated public traffic and same-host connection storms from contaminating results.
CONNECTION BOUNDS | Bound startup and background overlay connections. | Stay inside host neighbor capacity while preserving a connected routing graph.
CAMPAIGN | Add versioned resumable cells with fresh state, source, workload, topology, resources, and logs. | Make long experiments auditable and safe to resume.
CAMPAIGN VALIDATION | Require topology, population, query rows, returned matches, and clean kernel checks. | Reject empty, partial, contaminated, or host-failed experiments.
CAMPAIGN RESUME | Skip COMPLETE cells and recreate interrupted population batches. | Preserve accepted work and prevent request JSON from becoming tuple names.
CAMPAIGN ANALYSIS | Merge only validated cells and generate request-level summaries and figures. | Keep manuscript claims tied to accepted raw evidence.
LEGACY TSH | Retain the old TSH client only as an optional compatibility adapter. | Avoid breaking historical integrations while keeping it out of the default Tarsus path.
