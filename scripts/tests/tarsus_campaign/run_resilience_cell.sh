#!/usr/bin/env bash
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

[[ $# -eq 5 ]] || {
  echo "usage: $0 CELL_DIR NODES PAYLOAD_BYTES TRIALS REPLICA_TARGET" >&2
  exit 2
}
cell_dir=$1
node_count=$2
payload_bytes=$3
trials=$4
replica_target=$5

if [[ -f "$cell_dir/COMPLETE" ]]; then
  campaign_log "skip complete resilience cell $cell_dir"
  exit 0
fi

mkdir -p "$cell_dir/trials"
kernel_diagnostics_since=$(date +%s)
campaign_capture_host_manifest "$cell_dir/host.json"
jq -n \
  --arg cell_id "$(basename "$cell_dir")" \
  --argjson node_count "$node_count" \
  --argjson payload_bytes "$payload_bytes" \
  --argjson trials "$trials" \
  --argjson replica_target "$replica_target" \
  --argjson min_outbound "${TARSUS_MIN_OUTBOUND:-3}" \
  '{cell_id:$cell_id,node_count:$node_count,payload_bytes:$payload_bytes,
    trials:$trials,replica_target:$replica_target,transport:"tcp",
    min_outbound:$min_outbound,
    failure_mode:"stopped proven replica holder",
    repair_mode:"automatic periodic audit"}' >"$cell_dir/cell.json"

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
  docker-compose -f "$COMPOSE_FILE" ps -a >"$cell_dir/docker-ps-final.txt" 2>/dev/null || true
  docker-compose -f "$COMPOSE_FILE" logs --no-color >"$cell_dir/docker.log" 2>&1 || true
  campaign_capture_kernel_network_diagnostics \
    "$kernel_diagnostics_since" "$cell_dir/kernel-network.log"
  artifacts_finalized=1
}
cleanup() {
  finalize_artifacts
  docker-compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

export TARSUS_NODE_COUNT="$node_count"
export TARSUS_INDEX_SHARDS="${TARSUS_INDEX_SHARDS:-16}"
export TARSUS_DISABLE_BLOOM_PRUNING="${TARSUS_DISABLE_BLOOM_PRUNING:-false}"
export TARSUS_MIN_OUTBOUND="${TARSUS_MIN_OUTBOUND:-3}"
export TARSUS_FRESH_VOLUMES=true

campaign_require_neighbor_capacity "$node_count" "$TARSUS_MIN_OUTBOUND"
campaign_log "resilience start nodes=$node_count payload_bytes=$payload_bytes trials=$trials target=$replica_target"
"$REPO_ROOT/scripts/docker/start.sh" "$node_count" >"$cell_dir/start.log" 2>&1
campaign_capture_kernel_network_diagnostics \
  "$kernel_diagnostics_since" "$cell_dir/kernel-network-startup.log"
if [[ -s "$cell_dir/kernel-network-startup.log" ]]; then
  echo "host kernel reported neighbor/conntrack exhaustion during startup; see $cell_dir/kernel-network-startup.log" >&2
  exit 1
fi
"$REPO_ROOT/scripts/utils/resource_monitor.sh" \
  --output "$cell_dir/resources.csv" \
  --interval "${RESOURCE_INTERVAL_SECONDS:-5}" &
monitor_pid=$!

docker-compose -f "$COMPOSE_FILE" ps >"$cell_dir/docker-ps-initial.txt"
docker stats --no-stream >"$cell_dir/docker-stats-initial.txt"

availability_deadline=$((SECONDS + ${AVAILABILITY_WAIT_SECONDS:-300}))
while true; do
  availability_response="$cell_dir/startup-availability.tmp"
  availability_matches=0
  if campaign_tuple_query bootstrap "storage-available:*" >"$availability_response"; then
    availability_matches=$(jq -r '.query_stats.index_matches // 0' "$availability_response")
  fi
  mv "$availability_response" "$cell_dir/startup-availability.json"
  if [[ "$availability_matches" -ge "$node_count" ]]; then
    break
  fi
  if [[ "$SECONDS" -ge "$availability_deadline" ]]; then
    echo "availability advertisements indexed=$availability_matches, want >=$node_count" >&2
    exit 1
  fi
  campaign_log "resilience availability indexed=$availability_matches/$node_count"
  sleep 2
done

jq -n '[]' >"$cell_dir/provider-map.json"
for ((ordinal = 1; ordinal <= node_count; ordinal++)); do
  service=$(campaign_service_name "$ordinal")
  identity=$(campaign_control_get "$service" /id)
  container="fall25-$service"
  jq --arg service "$service" --arg container "$container" \
    --argjson identity "$identity" \
    '. += [{service:$service,container:$container,peer:$identity.peer,
      addrs:$identity.addrs}]' \
    "$cell_dir/provider-map.json" >"$cell_dir/provider-map.tmp"
  mv "$cell_dir/provider-map.tmp" "$cell_dir/provider-map.json"
done

echo "trial,node_count_active,payload_bytes,key,initial_replicas,killed_service,killed_peer,survival_get_s,repair_s,repaired_replicas,replacement_peer,remote_get_service,remote_get_s" >"$cell_dir/results.csv"
: >"$cell_dir/status.ndjson"
declare -A stopped_services=()
active_nodes=$node_count

for ((trial = 1; trial <= trials; trial++)); do
  trial_dir=$(printf '%s/trials/trial-%03d' "$cell_dir" "$trial")
  mkdir -p "$trial_dir"
  dd if=/dev/zero of="$trial_dir/payload.bin" bs=1 count="$payload_bytes" status=none
  printf 'tarsus-resilience-trial-%08d\n' "$trial" |
    dd of="$trial_dir/payload.bin" conv=notrunc status=none
  payload_hash=$(sha256sum "$trial_dir/payload.bin" | awk '{print $1}')
  echo "$payload_hash" >"$trial_dir/payload.sha256"

  put_started=$(date +%s%N)
  campaign_content_put_file bootstrap "$trial_dir/payload.bin" >"$trial_dir/put.json"
  put_seconds=$(campaign_elapsed_seconds "$put_started")
  key=$(jq -r '.multihash_hex // empty' "$trial_dir/put.json")
  [[ "$key" == "$payload_hash" ]] || {
    echo "trial $trial put key=$key, payload hash=$payload_hash" >&2
    exit 1
  }

  initial_started=$(date +%s%N)
  initial_deadline=$((SECONDS + ${RESILIENCE_REPLICATION_TIMEOUT_SECONDS:-300}))
  initial_status=""
  while true; do
    if status=$(campaign_replication_status bootstrap "$key"); then
      elapsed=$(campaign_elapsed_seconds "$initial_started")
      jq -cn --arg phase initial --argjson trial "$trial" \
        --arg elapsed_s "$elapsed" --argjson status "$status" \
        '{phase:$phase,trial:$trial,elapsed_s:($elapsed_s|tonumber),status:$status}' \
        >>"$cell_dir/status.ndjson"
      count=$(jq -r '.replica_count // 0' <<<"$status")
      unique_count=$(jq -r '[.providers[]] | unique | length' <<<"$status")
      if [[ "$count" -eq "$replica_target" && "$unique_count" -eq "$replica_target" ]]; then
        initial_status=$status
        break
      fi
    fi
    if [[ "$SECONDS" -ge "$initial_deadline" ]]; then
      echo "trial $trial did not reach exactly $replica_target replicas" >&2
      exit 1
    fi
    sleep 2
  done
  jq . <<<"$initial_status" >"$trial_dir/initial-status.json"
  initial_providers=$(jq -c '.providers | unique' <<<"$initial_status")

  killed_service=""
  killed_peer=""
  while IFS= read -r candidate_peer; do
    candidate_service=$(jq -r --arg peer "$candidate_peer" \
      '.[] | select(.peer == $peer) | .service' "$cell_dir/provider-map.json")
    if [[ -n "$candidate_service" && "$candidate_service" != "bootstrap" &&
      -z "${stopped_services[$candidate_service]:-}" ]]; then
      killed_service=$candidate_service
      killed_peer=$candidate_peer
      break
    fi
  done < <(jq -r '.[]' <<<"$initial_providers")
  [[ -n "$killed_service" && -n "$killed_peer" ]] || {
    echo "trial $trial could not map a stoppable replica provider" >&2
    exit 1
  }

  survivor_service=""
  while IFS= read -r candidate_peer; do
    [[ "$candidate_peer" == "$killed_peer" ]] && continue
    candidate_service=$(jq -r --arg peer "$candidate_peer" \
      '.[] | select(.peer == $peer) | .service' "$cell_dir/provider-map.json")
    if [[ -n "$candidate_service" &&
      -z "${stopped_services[$candidate_service]:-}" ]]; then
      survivor_service=$candidate_service
      break
    fi
  done < <(jq -r '.[]' <<<"$initial_providers")
  [[ -n "$survivor_service" ]] || {
    echo "trial $trial has no mapped surviving provider" >&2
    exit 1
  }

  repair_started=$(date +%s%N)
  docker-compose -f "$COMPOSE_FILE" stop -t 5 "$killed_service" >/dev/null
  stopped_services[$killed_service]=1
  active_nodes=$((active_nodes - 1))

  survival_started=$(date +%s%N)
  campaign_content_get_file "$survivor_service" "$key" \
    "$trial_dir/survival.bin" false
  survival_seconds=$(campaign_elapsed_seconds "$survival_started")
  survival_hash=$(sha256sum "$trial_dir/survival.bin" | awk '{print $1}')
  echo "$survival_hash" >"$trial_dir/survival.sha256"
  [[ "$survival_hash" == "$payload_hash" ]] || {
    echo "trial $trial surviving-provider retrieval hash mismatch" >&2
    exit 1
  }

  repair_deadline=$((SECONDS + ${RESILIENCE_REPAIR_TIMEOUT_SECONDS:-300}))
  repaired_status=""
  replacement_peer=""
  while true; do
    if status=$(campaign_replication_status bootstrap "$key"); then
      repair_seconds=$(campaign_elapsed_seconds "$repair_started")
      jq -cn --arg phase repair --argjson trial "$trial" \
        --arg elapsed_s "$repair_seconds" --argjson status "$status" \
        '{phase:$phase,trial:$trial,elapsed_s:($elapsed_s|tonumber),status:$status}' \
        >>"$cell_dir/status.ndjson"
      count=$(jq -r '.replica_count // 0' <<<"$status")
      killed_present=$(jq -r --arg peer "$killed_peer" \
        '.providers | index($peer) != null' <<<"$status")
      replacement_peer=$(jq -r --argjson initial "$initial_providers" \
        '[.providers[] | select(. as $peer | ($initial | index($peer) | not))][0] // empty' \
        <<<"$status")
      if [[ "$count" -eq "$replica_target" &&
        "$killed_present" == "false" && -n "$replacement_peer" ]]; then
        repaired_status=$status
        break
      fi
    fi
    if [[ "$SECONDS" -ge "$repair_deadline" ]]; then
      echo "trial $trial did not repair to exactly $replica_target with a replacement" >&2
      exit 1
    fi
    sleep 2
  done
  jq . <<<"$repaired_status" >"$trial_dir/repaired-status.json"

  remote_service=""
  while IFS=$'\t' read -r candidate_service candidate_peer; do
    [[ -n "${stopped_services[$candidate_service]:-}" ]] && continue
    is_provider=$(jq -r --arg peer "$candidate_peer" \
      '.providers | index($peer) != null' <<<"$repaired_status")
    if [[ "$is_provider" == "false" ]]; then
      remote_service=$candidate_service
      break
    fi
  done < <(jq -r '.[] | [.service,.peer] | @tsv' "$cell_dir/provider-map.json")
  [[ -n "$remote_service" ]] || {
    echo "trial $trial has no running non-provider for cold retrieval" >&2
    exit 1
  }

  remote_started=$(date +%s%N)
  campaign_content_get_file "$remote_service" "$key" \
    "$trial_dir/remote.bin" true
  remote_seconds=$(campaign_elapsed_seconds "$remote_started")
  remote_hash=$(sha256sum "$trial_dir/remote.bin" | awk '{print $1}')
  echo "$remote_hash" >"$trial_dir/remote.sha256"
  [[ "$remote_hash" == "$payload_hash" ]] || {
    echo "trial $trial cold retrieval hash mismatch" >&2
    exit 1
  }

  jq -n \
    --argjson trial "$trial" \
    --argjson node_count_active "$active_nodes" \
    --argjson payload_bytes "$payload_bytes" \
    --arg key "$key" \
    --arg payload_hash "$payload_hash" \
    --arg put_s "$put_seconds" \
    --argjson initial_status "$initial_status" \
    --arg killed_service "$killed_service" \
    --arg killed_peer "$killed_peer" \
    --arg survivor_service "$survivor_service" \
    --arg survival_s "$survival_seconds" \
    --arg repair_s "$repair_seconds" \
    --argjson repaired_status "$repaired_status" \
    --arg replacement_peer "$replacement_peer" \
    --arg remote_service "$remote_service" \
    --arg remote_s "$remote_seconds" \
    '{trial:$trial,node_count_active:$node_count_active,
      payload_bytes:$payload_bytes,key:$key,payload_hash:$payload_hash,
      put_s:($put_s|tonumber),initial_status:$initial_status,
      killed_service:$killed_service,killed_peer:$killed_peer,
      survivor_service:$survivor_service,
      survival_get_s:($survival_s|tonumber),
      repair_s:($repair_s|tonumber),repaired_status:$repaired_status,
      replacement_peer:$replacement_peer,remote_get_service:$remote_service,
      remote_get_s:($remote_s|tonumber)}' >"$trial_dir/summary.json"

  jq -r '[
    .trial,.node_count_active,.payload_bytes,.key,
    .initial_status.replica_count,.killed_service,.killed_peer,
    .survival_get_s,.repair_s,.repaired_status.replica_count,
    .replacement_peer,.remote_get_service,.remote_get_s
  ] | @csv' "$trial_dir/summary.json" >>"$cell_dir/results.csv"
  campaign_log "resilience trial=$trial repaired in ${repair_seconds}s replacement=$replacement_peer"
done

finalize_artifacts
"$CAMPAIGN_DIR/validate_resilience_cell.sh" "$cell_dir"
touch "$cell_dir/COMPLETE"
campaign_log "resilience complete $cell_dir"
