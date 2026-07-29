# Tuple-index query-cost experiment

`query_cost.sh` records system-reported work for exact, prefix, and substring
tuple reads. It queries a running node's local control API and writes one CSV
row per trial.

Create a pattern file containing one query per line:

```text
experiment/run-0001/output
experiment/run-*
*temperature*
```

Then run:

```sh
scripts/tests/tuple_index/query_cost.sh \
  http://127.0.0.1:8080 \
  patterns.txt \
  test_results/tuple_index/query_cost.csv \
  30
```

The node must already contain tuples matching every pattern. The harness uses
successful reads only so missing data cannot be mistaken for low query cost.
The CSV distinguishes PHT traversal, Bloom-filter pruning, index candidates,
authoritative owner verification, shard fanout, elapsed time, and cumulative
index-mutation activity.

For a defensible experiment, vary tuple count, name distribution, match
selectivity, live node count, and query class independently. Record the commit,
node configuration, host inventory, and pattern-generation seed beside each
CSV. Discard warm-up trials and report distributions rather than only means.
