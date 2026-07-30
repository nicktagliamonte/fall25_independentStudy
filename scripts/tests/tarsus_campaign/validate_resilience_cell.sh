#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 1 ]] || { echo "usage: $0 CELL_DIR" >&2; exit 2; }
cell_dir=$1
required=(
  cell.json host.json provider-map.json startup-availability.json
  results.csv status.ndjson resources.csv docker-ps-initial.txt
  docker-ps-final.txt docker-stats-initial.txt docker.log
)
for path in "${required[@]}"; do
  [[ -s "$cell_dir/$path" ]] || {
    echo "missing or empty resilience artifact: $cell_dir/$path" >&2
    exit 1
  }
done
[[ -f "$cell_dir/kernel-network.log" ]] || {
  echo "missing resilience artifact: $cell_dir/kernel-network.log" >&2
  exit 1
}
[[ ! -s "$cell_dir/kernel-network.log" ]] || {
  echo "host kernel network exhaustion recorded in $cell_dir/kernel-network.log" >&2
  exit 1
}

jq -e '.node_count > 1 and .payload_bytes > 4194304 and
  .trials > 0 and .replica_target > 1 and .min_outbound > 0 and
  .max_connections >= .min_outbound' "$cell_dir/cell.json" >/dev/null
jq -e '.git.commit != "" and .host.logical_cpus > 0 and
  .host.ipv4_neighbor_gc.thresh3 > 0' "$cell_dir/host.json" >/dev/null
jq -e --slurpfile cell "$cell_dir/cell.json" \
  'length == $cell[0].node_count and
   ([.[].peer] | unique | length) == $cell[0].node_count' \
  "$cell_dir/provider-map.json" >/dev/null
jq -e --slurpfile cell "$cell_dir/cell.json" \
  '.query_stats.index_matches >= $cell[0].node_count' \
  "$cell_dir/startup-availability.json" >/dev/null

trials=$(jq -r .trials "$cell_dir/cell.json")
target=$(jq -r .replica_target "$cell_dir/cell.json")
rows=$(( $(wc -l <"$cell_dir/results.csv") - 1 ))
[[ "$rows" -eq "$trials" ]] || {
  echo "resilience result rows=$rows, expected=$trials" >&2
  exit 1
}
awk -F, 'NR == 1 {next} NF != 13 {bad++} END {exit bad != 0}' \
  "$cell_dir/results.csv"

for ((trial = 1; trial <= trials; trial++)); do
  trial_dir=$(printf '%s/trials/trial-%03d' "$cell_dir" "$trial")
  for path in payload.sha256 put.json initial-status.json survival.sha256 \
    repaired-status.json remote.sha256 summary.json; do
    [[ -s "$trial_dir/$path" ]] || {
      echo "missing trial artifact: $trial_dir/$path" >&2
      exit 1
    }
  done
  payload_hash=$(tr -d '[:space:]' <"$trial_dir/payload.sha256")
  [[ "$(tr -d '[:space:]' <"$trial_dir/survival.sha256")" == "$payload_hash" ]]
  [[ "$(tr -d '[:space:]' <"$trial_dir/remote.sha256")" == "$payload_hash" ]]
  jq -e --argjson target "$target" --arg hash "$payload_hash" '
    . as $summary |
    .key == $hash and .payload_hash == $hash and
    .initial_status.replica_count == $target and
    .repaired_status.replica_count == $target and
    (.initial_status.providers | unique | length) == $target and
    (.repaired_status.providers | unique | length) == $target and
    (.repaired_status.providers | index($summary.killed_peer)) == null and
    (.initial_status.providers | index($summary.replacement_peer)) == null and
    (.repaired_status.providers | index($summary.replacement_peer)) != null and
    .repair_s > 0 and .survival_get_s >= 0 and .remote_get_s > 0
  ' "$trial_dir/summary.json" >/dev/null
done

jq -s -e --argjson trials "$trials" '
  ([.[] | select(.phase == "initial") | .trial] | unique | length) == $trials and
  ([.[] | select(.phase == "repair") | .trial] | unique | length) == $trials
' "$cell_dir/status.ndjson" >/dev/null
echo "validated resilience $cell_dir"
