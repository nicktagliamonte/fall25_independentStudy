# Tarsus work checkpoint — 2026-07-29

## Where work stopped

- Branch: `tarsus-paper-rewrite`
- The interrupted 50-node run and all Compose containers have been stopped.
- Generated `docker-compose.yml` has been restored.
- The current production work is preserved in a WIP checkpoint commit on this
  branch. Continue from it; do not reset to an earlier branch state.
- `git diff --check` passes.

The immediate correctness gate is strict discovery of all 50 live storage
advertisements in the Docker campaign. The best completed 50-node runs reached
49/50, not 50/50. Treat that as failure evidence, not a passing result.

## What was fixed during this investigation

- bounded-degree private TCP startup topology;
- reconnect handshake verification and cleanup;
- mutual responder acceptance in the handshake gate;
- removal of the low-level stale tagging path and centralized tagging;
- bounded parallel candidate verification;
- renewable storage-offer replacement without changing general Linda-style
  `TsPut` multiset semantics;
- expiration, staggered refresh, and anti-entropy reassertion;
- an owner-candidate quorum guard for index mutation.

Focused tests passed after the latest owner threshold change. The full suite had
passed before that final threshold adjustment; rerun it after settling the
ownership design.

## Current diagnosis

The remaining failure is not adequately explained as an ordinary startup delay.
Concurrent PHT/index mutation is serialized through an elected overlay owner, but
nodes can elect from inconsistent routing views and the DHT record/version
semantics do not provide a robust fencing boundary. This can leave a query with
only 14/16 or 15/16 index shards and/or 49 indexed storage advertisements.

The current minimum owner-candidate threshold is 16. That threshold is a
diagnostic guard, not a proof of single-owner safety. The interrupted run using
it is incomplete and must not be reported as a result:

`test_results/tarsus_campaign_smoke/n050-owner-quorum16/cells/smoke-n050-c000100-s016-bloom-on`

Useful completed failure evidence:

- `test_results/tarsus_campaign_smoke/n050-gated-tags`
- `test_results/tarsus_campaign_smoke/n050-owner-quorum`

## Recommended next move

Do not redefine 49/50 as success for the production-real system or paper. Begin
with an ownership-protocol review and implement an explicit mutation epoch plus
fencing token (or an equivalently strong single-writer mechanism). Required
properties:

1. one mutation authority per shard and epoch;
2. stale owners cannot commit after a newer epoch is visible;
3. authority transfer is recoverable and observable;
4. retries are idempotent or deduplicated;
5. failover tests demonstrate no lost index updates.

Then:

1. run focused ownership and tuple-space tests;
2. run `go test ./...` with the repository's `/tmp` Go/ccache settings;
3. rerun the strict 50/50 cell at 50 nodes;
4. repeat it enough times to distinguish a fix from a lucky run;
5. proceed to 100-node publication cells only after the correctness gate passes.

## Resume prompt

Open this same conversation and send:

> Resume the Tarsus work from `docs/TARSUS_RESUME.md`. Inspect the branch and
> working tree first. Do not accept 49/50 as a pass; review and implement
> epoch/fencing for PHT mutation authority, then rerun the strict 50-node gate.

If this conversation is unavailable, a new Codex conversation in the repository
can use the same prompt.
