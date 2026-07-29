#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 1 ]] || { echo "usage: $0 CELL_DIR" >&2; exit 2; }
cell_dir=$1
required=(cell.json host.json workload/names.txt workload/patterns.tsv populate.ndjson queries.csv topology.json docker-ps.txt docker-stats.txt docker.log)
for path in "${required[@]}"; do
  [[ -s "$cell_dir/$path" ]] || { echo "missing or empty artifact: $cell_dir/$path" >&2; exit 1; }
done

jq -e '.node_count > 1 and .catalog_size > 0 and .index_shards > 0' "$cell_dir/cell.json" >/dev/null
jq -e '.git.commit != "" and .host.logical_cpus > 0' "$cell_dir/host.json" >/dev/null
jq -e '.requested > 0 and .failed == 0 and .requested == .succeeded
  and .mutation_delta.total == .requested
  and .mutation_delta.failures == 0' "$cell_dir/populate-summary.json" >/dev/null

expected=$(( $(wc -l <"$cell_dir/workload/patterns.tsv") * $(jq -r .query_repetitions "$cell_dir/cell.json") * $(jq -r .client_count "$cell_dir/cell.json") ))
actual=$(( $(wc -l <"$cell_dir/queries.csv") - 1 ))
[[ "$actual" -eq "$expected" ]] || { echo "query rows=$actual, expected=$expected" >&2; exit 1; }

awk -F, 'NR == 1 {next} NF != 24 {bad++} END {exit bad != 0}' "$cell_dir/queries.csv"
echo "validated $cell_dir"
