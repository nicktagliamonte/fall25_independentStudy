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
7. [ ] Analyze and optimize the growing short-test latency and resource costs.
8. [ ] Run the full 100-node failure, repair, query-cost, and resource campaign.
9. [ ] Produce figures/results and finish `paper/final.tex`.
10. [ ] Remove obsolete results, dated harnesses, and abandoned planning files.
11. [ ] Emit a brief, parseable plain-English document describing the Tarsus
    rewrite for the research group.

## Immediate next work

The implementation/claim limitation gate is complete. The next step is the
scheduled latency and resource regression pass. Durable exact-name operations
now add DHT reads plus write/read confirmation, placement adds active RTT
probing, and periodic repair scans local content. Establish a reproducible
10-node baseline, attribute time and resource growth to specific operations,
then cache/coalesce or batch work without weakening fencing, confirmation, or
repair correctness.

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
> working tree first, then continue the latency/resource regression pass. The
> 50/50 gate and the distributed-filesystem implementation/claim gate are
> complete; do not rerun the large 50-node cell unless a relevant change
> invalidates that result.

If this conversation is unavailable, a new Codex conversation opened in the
repository can use the same prompt.
