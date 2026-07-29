#!/usr/bin/env bash
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

[[ $# -eq 7 ]] || {
  echo "usage: $0 CELL_DIR NODES CATALOG SHARDS BLOOM_PRUNING REPETITIONS CLIENT_COUNT" >&2
  exit 2
}
cell_dir=$1
node_count=$2
catalog_size=$3
shard_count=$4
bloom_pruning=$5
repetitions=$6
client_count=$7

if [[ -f "$cell_dir/COMPLETE" ]]; then
  campaign_log "skip complete cell $cell_dir"
  exit 0
fi

mkdir -p "$cell_dir/workload" "$cell_dir/batches"
campaign_capture_host_manifest "$cell_dir/host.json"
"$CAMPAIGN_DIR/generate_workload.sh" "$catalog_size" "$cell_dir/workload"

jq -n \
  --arg cell_id "$(basename "$cell_dir")" \
  --argjson node_count "$node_count" \
  --argjson catalog_size "$catalog_size" \
  --argjson index_shards "$shard_count" \
  --argjson bloom_pruning "$bloom_pruning" \
  --argjson query_repetitions "$repetitions" \
  --argjson client_count "$client_count" \
  --argjson min_outbound "${TARSUS_MIN_OUTBOUND:-4}" \
  --arg transport "tcp" \
  '{cell_id:$cell_id,node_count:$node_count,catalog_size:$catalog_size,
    index_shards:$index_shards,bloom_pruning:$bloom_pruning,
    query_repetitions:$query_repetitions,client_count:$client_count,
    min_outbound:$min_outbound,transport:$transport}' >"$cell_dir/cell.json"

monitor_pid=""
artifacts_finalized=0
finalize_artifacts() {
  if [[ "$artifacts_finalized" -eq 1 ]]; then
    return
  fi
  if [[ -n "$monitor_pid" ]]; then
    kill "$monitor_pid" 2>/dev/null || true
    wait "$monitor_pid" 2>/dev/null || true
    monitor_pid=""
  fi
  docker-compose -f "$COMPOSE_FILE" logs --no-color >"$cell_dir/docker.log" 2>&1 || true
  artifacts_finalized=1
}
cleanup() {
  finalize_artifacts
  docker-compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

export TARSUS_NODE_COUNT="$node_count"
export TARSUS_INDEX_SHARDS="$shard_count"
if [[ "$bloom_pruning" == "true" ]]; then
  export TARSUS_DISABLE_BLOOM_PRUNING=false
else
  export TARSUS_DISABLE_BLOOM_PRUNING=true
fi

campaign_log "start nodes=$node_count shards=$shard_count bloom=$bloom_pruning"
"$REPO_ROOT/scripts/docker/start.sh" "$node_count" >"$cell_dir/start.log" 2>&1
"$REPO_ROOT/scripts/utils/resource_monitor.sh" \
  --output "$cell_dir/resources.csv" \
  --interval "${RESOURCE_INTERVAL_SECONDS:-5}" &
monitor_pid=$!

docker-compose -f "$COMPOSE_FILE" ps >"$cell_dir/docker-ps.txt"
docker stats --no-stream >"$cell_dir/docker-stats.txt"
campaign_log "settling routing state for ${SETTLE_SECONDS:-30}s"
sleep "${SETTLE_SECONDS:-30}"
availability_deadline=$((SECONDS + ${AVAILABILITY_WAIT_SECONDS:-300}))
while true; do
  availability_response="$cell_dir/startup-availability.tmp"
  availability_ready=0
  if campaign_tuple_query bootstrap "storage-available:*" >"$availability_response"; then
    availability_matches=$(jq -r '.query_stats.index_matches // 0' "$availability_response")
    if [[ "$availability_matches" -ge "$node_count" ]]; then
      availability_ready=1
    fi
  else
    # A 404 means the distributed index has no currently verifiable match;
    # a curl timeout means it did not answer within the per-request budget.
    # Both are readiness states until the overall availability deadline.
    availability_matches=$(jq -r '.response.query_stats.index_matches // 0' \
      "$availability_response" 2>/dev/null || echo 0)
  fi
  mv "$availability_response" "$cell_dir/startup-availability.json"
  if [[ "$availability_ready" -eq 1 ]]; then
    break
  fi
  if [[ "$SECONDS" -ge "$availability_deadline" ]]; then
    echo "availability advertisements indexed=$availability_matches, want >=$node_count" >&2
    exit 1
  fi
  campaign_log "availability advertisements indexed=$availability_matches/$node_count; awaiting refresh"
  sleep 2
done

split -d -a 5 -l "${POPULATE_BATCH_SIZE:-2500}" \
  "$cell_dir/workload/names.txt" "$cell_dir/batches/names-"
: >"$cell_dir/populate.ndjson"
for batch in "$cell_dir"/batches/names-*; do
  request="$batch.json"
  jq -Rn \
    --arg value_base64 "dGFyc3VzLWV4cGVyaW1lbnQtdG9rZW4=" \
    --argjson concurrency "${POPULATE_CONCURRENCY:-16}" \
    '[inputs] | {names:.,value_base64:$value_base64,copies:1,concurrency:$concurrency}' \
    <"$batch" >"$request"
  campaign_tuple_put_file bootstrap "$request" | tee -a "$cell_dir/populate.ndjson"
done
jq -s '{
  requested:(map(.requested)|add),succeeded:(map(.succeeded)|add),
  failed:(map(.failed)|add),duration_ns:(map(.duration_ns)|add),
  mutation_delta:{
    total:(map(.mutation_delta.total)|add),
    local:(map(.mutation_delta.local)|add),
    remote:(map(.mutation_delta.remote)|add),
    failures:(map(.mutation_delta.failures)|add),
    duration_ns:(map(.mutation_delta.duration_ns)|add)
  },
  final_mutation_stats:.[-1].mutation_stats
}' "$cell_dir/populate.ndjson" >"$cell_dir/populate-summary.json"

jq -n '{nodes:[]}' >"$cell_dir/topology.json"
for ((ordinal = 1; ordinal <= node_count; ordinal++)); do
  service=$(campaign_service_name "$ordinal")
  neighbors=$(campaign_control_get "$service" /neighbors)
  jq --arg service "$service" --argjson neighbors "$neighbors" \
    '.nodes += [{service:$service,neighbor_count:($neighbors|length),neighbors:$neighbors}]' \
    "$cell_dir/topology.json" >"$cell_dir/topology.tmp"
  mv "$cell_dir/topology.tmp" "$cell_dir/topology.json"
done

echo "cell_id,node_count,catalog_size,index_shards,bloom_pruning,client,trial,label,selectivity,pattern,query_kind,shards_contacted,shards_succeeded,shards_failed,nodes_fetched,branches_considered,branches_pruned,index_candidates,index_matches,owner_attempts,verified_matches,duration_ns,mutation_total,mutation_failures" >"$cell_dir/queries.csv"
for ((client_index = 0; client_index < client_count; client_index++)); do
  ordinal=$((1 + client_index * (node_count - 1) / (client_count > 1 ? client_count - 1 : 1)))
  service=$(campaign_service_name "$ordinal")
  while IFS=$'\t' read -r label kind selectivity pattern; do
    for ((trial = 1; trial <= repetitions; trial++)); do
      response=$(campaign_tuple_query "$service" "$pattern")
      jq -r \
        --arg cell_id "$(basename "$cell_dir")" \
        --argjson node_count "$node_count" \
        --argjson catalog_size "$catalog_size" \
        --argjson index_shards "$shard_count" \
        --argjson bloom_pruning "$bloom_pruning" \
        --arg client "$service" --argjson trial "$trial" \
        --arg label "$label" --arg selectivity "$selectivity" \
        '[$cell_id,$node_count,$catalog_size,$index_shards,$bloom_pruning,
          $client,$trial,$label,$selectivity,.pattern,.query_stats.query_kind,
          .query_stats.shards_contacted,.query_stats.shards_succeeded,
          .query_stats.shards_failed,.query_stats.nodes_fetched,
          .query_stats.branches_considered,.query_stats.branches_pruned,
          .query_stats.index_candidates,.query_stats.index_matches,
          .query_stats.owner_attempts,.query_stats.verified_matches,
          .query_stats.duration_ns,.mutation_stats.total,
          .mutation_stats.failures] | @csv' <<<"$response" >>"$cell_dir/queries.csv"
    done
  done <"$cell_dir/workload/patterns.tsv"
done

finalize_artifacts
"$CAMPAIGN_DIR/validate_cell.sh" "$cell_dir"
touch "$cell_dir/COMPLETE"
campaign_log "complete $cell_dir"
