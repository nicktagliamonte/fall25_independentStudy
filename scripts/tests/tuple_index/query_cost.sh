#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 CONTROL_URL PATTERNS_FILE OUTPUT_CSV [REPETITIONS]" >&2
  echo "patterns file: one exact, prefix (* suffix), or substring (*...*) query per line" >&2
  exit 2
}

[[ $# -ge 3 && $# -le 4 ]] || usage
command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

control_url=${1%/}
patterns_file=$2
output_csv=$3
repetitions=${4:-30}

[[ -r "$patterns_file" ]] || { echo "cannot read patterns file: $patterns_file" >&2; exit 1; }
[[ "$repetitions" =~ ^[1-9][0-9]*$ ]] || { echo "repetitions must be positive" >&2; exit 2; }

mkdir -p "$(dirname "$output_csv")"
echo "timestamp_utc,repetition,pattern,query_kind,shards_contacted,shards_succeeded,shards_failed,nodes_fetched,branches_considered,branches_pruned,index_candidates,index_matches,owner_attempts,verified_matches,duration_ns,mutation_total,mutation_local,mutation_remote,mutation_failures,mutation_duration_ns" >"$output_csv"

while IFS= read -r pattern || [[ -n "$pattern" ]]; do
  [[ -n "$pattern" && "${pattern:0:1}" != "#" ]] || continue
  for ((repetition = 1; repetition <= repetitions; repetition++)); do
    response=$(curl --fail-with-body --silent --show-error --get \
      --data-urlencode "pattern=$pattern" \
      "$control_url/tuple/query")
    jq -r \
      --arg timestamp "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      --argjson repetition "$repetition" \
      '[
        $timestamp,
        $repetition,
        .pattern,
        .query_stats.query_kind,
        .query_stats.shards_contacted,
        .query_stats.shards_succeeded,
        .query_stats.shards_failed,
        .query_stats.nodes_fetched,
        .query_stats.branches_considered,
        .query_stats.branches_pruned,
        .query_stats.index_candidates,
        .query_stats.index_matches,
        .query_stats.owner_attempts,
        .query_stats.verified_matches,
        .query_stats.duration_ns,
        .mutation_stats.total,
        .mutation_stats.local,
        .mutation_stats.remote,
        .mutation_stats.failures,
        .mutation_stats.duration_ns
      ] | @csv' <<<"$response" >>"$output_csv"
  done
done <"$patterns_file"

echo "wrote $output_csv"
