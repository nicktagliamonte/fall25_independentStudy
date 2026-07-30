# Tarsus distributed experiment campaign

This framework runs the real node binary and tuple/index protocols in fresh
Docker deployments. It records immutable per-cell configuration, Git and host
metadata, deterministic workload checksums, topology, population outcomes,
query-level work counters, container resources, and raw logs.

Plan a campaign without starting Docker:

```sh
scripts/tests/tarsus_campaign/run_campaign.sh \
  --config scripts/tests/tarsus_campaign/config.env.example
```

The command prints the generated run directory. Inspect `plan.tsv`, then launch
or resume it explicitly:

```sh
scripts/tests/tarsus_campaign/run_campaign.sh \
  --resume test_results/tarsus_campaign/<run-id> \
  --execute
```

A cell is skipped only after `validate_cell.sh` succeeds and writes
`COMPLETE`. Interrupted cells are recreated with fresh Docker volumes. The
campaign also invokes the node with `--no-default-bootstrap`; that mode purges
the known public libp2p bootstrap identities from reused persistent and live
peerstores. Thus a cell cannot inherit public bootstrap candidates, peer
identities, tuples, metadata, or blocks from an older deployment. The default
plan covers 10, 50, and 100 nodes; two catalog sizes; 1, 4, 16, and 64
mutation shards at the largest scale; and an otherwise-identical Bloom-off
ablation. A configurable settling interval precedes population so the reported
stable-ownership epoch does not begin while the routing tables are still
forming.

Run a small smoke campaign first by copying the example config and setting:

```text
NODE_COUNTS="3"
CATALOG_SIZES="100"
SHARD_COUNTS="2"
LARGE_NODE_COUNT=3
LARGE_CATALOG_SIZE=100
QUERY_REPETITIONS=2
CLIENT_COUNT=2
```

Docker evidence does not establish independent regional failure behavior.
Multi-host or controlled-delay experiments must be reported separately.

When `RUN_RESILIENCE=true`, the campaign also runs one production-current
resilience deployment. Each trial writes an 8 MiB deterministic payload, waits
for exactly seven token-advertised replicas, stops a mapped replica holder,
verifies a surviving copy, waits for automatic periodic repair to replace the
failed provider while returning to exactly seven, and verifies a cold fetch
from a non-provider. The cell preserves every status poll, peer/service map,
content hashes, timings, resource samples, and logs. Success demonstrates
single-host process-failure repair, not geographic or partition tolerance.

After every planned cell validates, `validate_campaign.sh` merges raw query and
population data, computes medians, p95 latency, standard deviation, and a
deterministic bootstrap confidence interval, and emits a generated LaTeX table
under the run's `analysis/` directory.
