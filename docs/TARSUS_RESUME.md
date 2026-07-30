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

## Current plan

1. [x] Preserve the branch/checkpoint and baseline 49/50 evidence.
2. [x] Implement and test fenced index authority, retry deduplication, and
   failover.
3. [x] Fix the native authority timing stall and bound overlay routing.
4. [x] Pass the strict 50/50 distributed correctness gate.
5. [x] Diagnose and fix the pre-existing and distributed failures exposed so
   far.
6. [ ] Address the manuscript's remaining distributed-filesystem limitations
   in implementation and claims.
7. [ ] Analyze and optimize the growing short-test latency and resource costs.
8. [ ] Run the full 100-node failure, repair, query-cost, and resource campaign.
9. [ ] Produce figures/results and finish `paper/final.tex`.
10. [ ] Remove obsolete results, dated harnesses, and abandoned planning files.
11. [ ] Emit a brief, parseable plain-English document describing the Tarsus
    rewrite for the research group.

## Immediate next limitation

Exact tuple state still exists only in the deterministic owner's in-memory
`NativeTupleSpace`. The new authority fencing protects PHT/index mutations, not
tuple values. A tuple owner crash can therefore lose tuple state, and request
deduplication is also owner-memory-local.

The next production-real slice is durable, fenced exact-tuple state:

1. persist tuple state and operation IDs before acknowledging mutations;
2. identify the current tuple authority with an epoch/fence;
3. reject commits from stale owners after authority transfer;
4. load committed state when a successor assumes ownership;
5. prove retry and failover behavior for `put`, `read`, `get`, and `replace`;
6. preserve the Linda-style interface without claiming that Tarsus uses Linda.

Do not update the manuscript to claim tuple-owner failover until these tests
pass. RTT diversity also remains a latency heuristic, not proof of geographic
or administrative failure-domain independence.

## Resume prompt

Open this conversation and send:

> Resume the Tarsus work from `docs/TARSUS_RESUME.md`. Inspect the branch and
> working tree first, then continue the durable fenced exact-tuple ownership
> slice. The 50/50 gate is complete; do not rerun it unless a relevant change
> invalidates that result.

If this conversation is unavailable, a new Codex conversation opened in the
repository can use the same prompt.
