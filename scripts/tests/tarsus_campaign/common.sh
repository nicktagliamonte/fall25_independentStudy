#!/usr/bin/env bash
set -euo pipefail

CAMPAIGN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$CAMPAIGN_DIR/../../.." && pwd)"
COMPOSE_FILE="$REPO_ROOT/docker-compose.yml"

campaign_log() {
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"
}

campaign_require_tools() {
  local missing=0
  for tool in docker-compose docker curl jq awk sed git; do
    if ! command -v "$tool" >/dev/null; then
      echo "missing required command: $tool" >&2
      missing=1
    fi
  done
  [[ "$missing" -eq 0 ]]
}

campaign_require_neighbor_capacity() {
  local node_count=$1
  local min_outbound=${2:-3}
  local threshold_file=/proc/sys/net/ipv4/neigh/default/gc_thresh3
  local threshold required per_node_budget recommended

  if [[ ! "$node_count" =~ ^[0-9]+$ || ! "$min_outbound" =~ ^[0-9]+$ ]]; then
    echo "neighbor-capacity inputs must be non-negative integers: nodes=$node_count min_outbound=$min_outbound" >&2
    return 1
  fi

  # Every connection occupies one neighbor-cache entry at each endpoint.
  # Reserve four additional entries per node for Kademlia/control-plane
  # transients plus 128 entries for the host and unrelated namespaces. The
  # prior two-entry reserve admitted a 100-node run that crossed gc_thresh3
  # as soon as its DHT routing views filled.
  per_node_budget=$((2 * min_outbound + 4))
  required=$((node_count * per_node_budget + 128))
  recommended=1024
  while [[ "$recommended" -lt $((required * 2)) ]]; do
    recommended=$((recommended * 2))
  done

  if [[ ! -r "$threshold_file" ]]; then
    campaign_log "neighbor-capacity preflight unavailable: $threshold_file is not readable"
    return
  fi
  threshold=$(<"$threshold_file")
  if [[ ! "$threshold" =~ ^[0-9]+$ ]]; then
    echo "invalid IPv4 neighbor-table limit in $threshold_file: $threshold" >&2
    return 1
  fi
  if [[ "$threshold" -lt "$required" ]]; then
    cat >&2 <<EOF
host IPv4 neighbor-table capacity is too small for this campaign:
  gc_thresh3=$threshold
  estimated_required=$required
  nodes=$node_count
  min_outbound=$min_outbound
Reduce TARSUS_MIN_OUTBOUND or raise the host limits before retrying, for example:
  sudo sysctl -w net.ipv4.neigh.default.gc_thresh1=$((recommended / 4))
  sudo sysctl -w net.ipv4.neigh.default.gc_thresh2=$((recommended / 2))
  sudo sysctl -w net.ipv4.neigh.default.gc_thresh3=$recommended
EOF
    return 1
  fi
  campaign_log "neighbor-capacity preflight passed: gc_thresh3=$threshold estimated_required=$required"
}

campaign_capture_kernel_network_diagnostics() {
  local since_epoch=$1
  local output=$2
  if ! command -v journalctl >/dev/null; then
    : >"$output"
    return
  fi
  journalctl -k --since "@$since_epoch" --no-pager 2>/dev/null |
    awk 'tolower($0) ~ /(neighbor table overflow|nf_conntrack.*table full)/' \
      >"$output" || true
}

campaign_service_name() {
  local ordinal=$1
  if [[ "$ordinal" -eq 1 ]]; then
    echo bootstrap
  else
    echo "node$ordinal"
  fi
}

campaign_control_get() {
  local service=$1
  local path=$2
  docker-compose -f "$COMPOSE_FILE" exec -T "$service" sh -c \
    'addr=$(jq -r .addr /app/logs/'"$service"'.json); curl --max-time "$1" --fail-with-body --silent --show-error "http://$addr'"$path"'"' \
    sh "${CAMPAIGN_HTTP_TIMEOUT_SECONDS:-30}" </dev/null
}

campaign_tuple_query() {
  local service=$1
  local pattern=$2
  docker-compose -f "$COMPOSE_FILE" exec -T "$service" sh -c \
    'addr=$(jq -r .addr /app/logs/'"$service"'.json); curl --max-time "$2" --fail-with-body --silent --show-error --get --data-urlencode "pattern=$1" "http://$addr/tuple/query"' \
    sh "$pattern" "${CAMPAIGN_QUERY_TIMEOUT_SECONDS:-30}" </dev/null
}

campaign_tuple_put_file() {
  local service=$1
  local request_file=$2
  docker-compose -f "$COMPOSE_FILE" exec -T "$service" sh -c \
    'addr=$(jq -r .addr /app/logs/'"$service"'.json); curl --max-time "$1" --fail-with-body --silent --show-error -H "Content-Type: application/json" --data-binary @- "http://$addr/tuple/put"' \
    sh "${CAMPAIGN_PUT_TIMEOUT_SECONDS:-300}" <"$request_file"
}

campaign_content_put_file() {
  local service=$1
  local payload_file=$2
  docker-compose -f "$COMPOSE_FILE" exec -T "$service" sh -c \
    'addr=$(jq -r .addr /app/logs/'"$service"'.json); curl --max-time "$1" --fail-with-body --silent --show-error -H "Content-Type: application/octet-stream" --data-binary @- "http://$addr/put"' \
    sh "${CAMPAIGN_CONTENT_TIMEOUT_SECONDS:-300}" <"$payload_file"
}

campaign_content_get_file() {
  local service=$1
  local key=$2
  local output_file=$3
  local remote_only=${4:-false}
  local query="format=raw"
  if [[ "$remote_only" == "true" ]]; then
    query="format=raw&remote_only=1"
  fi
  jq -cn --arg key "$key" \
    '{key:$key,timeout:"120s"}' |
    docker-compose -f "$COMPOSE_FILE" exec -T "$service" sh -c \
      'addr=$(jq -r .addr /app/logs/'"$service"'.json); curl --max-time "$1" --fail-with-body --silent --show-error -H "Content-Type: application/json" -H "Accept: application/octet-stream" --data-binary @- "http://$addr/get?'"$query"'"' \
      sh "${CAMPAIGN_CONTENT_TIMEOUT_SECONDS:-300}" >"$output_file"
}

campaign_replication_status() {
  local service=$1
  local key=$2
  campaign_control_get "$service" "/replication/status?key=$key"
}

campaign_elapsed_seconds() {
  local started_ns=$1
  local now_ns
  now_ns=$(date +%s%N)
  awk -v start="$started_ns" -v now="$now_ns" \
    'BEGIN {printf "%.3f", (now-start)/1000000000}'
}

campaign_capture_host_manifest() {
  local output=$1
  local commit dirty neighbor_gc_thresh1 neighbor_gc_thresh2 neighbor_gc_thresh3
  commit=$(git -C "$REPO_ROOT" rev-parse HEAD)
  dirty=$(git -C "$REPO_ROOT" status --porcelain)
  neighbor_gc_thresh1=""
  neighbor_gc_thresh2=""
  neighbor_gc_thresh3=""
  if [[ -r /proc/sys/net/ipv4/neigh/default/gc_thresh1 ]]; then
    neighbor_gc_thresh1=$(</proc/sys/net/ipv4/neigh/default/gc_thresh1)
  fi
  if [[ -r /proc/sys/net/ipv4/neigh/default/gc_thresh2 ]]; then
    neighbor_gc_thresh2=$(</proc/sys/net/ipv4/neigh/default/gc_thresh2)
  fi
  if [[ -r /proc/sys/net/ipv4/neigh/default/gc_thresh3 ]]; then
    neighbor_gc_thresh3=$(</proc/sys/net/ipv4/neigh/default/gc_thresh3)
  fi
  jq -n \
    --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg commit "$commit" \
    --arg branch "$(git -C "$REPO_ROOT" branch --show-current)" \
    --arg dirty "$dirty" \
    --arg kernel "$(uname -srvmo)" \
    --arg cpus "$(getconf _NPROCESSORS_ONLN)" \
    --arg neighbor_gc_thresh1 "$neighbor_gc_thresh1" \
    --arg neighbor_gc_thresh2 "$neighbor_gc_thresh2" \
    --arg neighbor_gc_thresh3 "$neighbor_gc_thresh3" \
    --arg docker_version "$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)" \
    --arg compose_version "$(docker-compose version --short 2>/dev/null || true)" \
    '{captured_at:$captured_at,git:{commit:$commit,branch:$branch,dirty:$dirty},
      host:{kernel:$kernel,logical_cpus:($cpus|tonumber),
        ipv4_neighbor_gc:{
          thresh1:(if $neighbor_gc_thresh1 == "" then null else ($neighbor_gc_thresh1|tonumber) end),
          thresh2:(if $neighbor_gc_thresh2 == "" then null else ($neighbor_gc_thresh2|tonumber) end),
          thresh3:(if $neighbor_gc_thresh3 == "" then null else ($neighbor_gc_thresh3|tonumber) end)}},
      docker:{engine:$docker_version,compose:$compose_version}}' >"$output"
  free -b >"${output%.json}.memory.txt"
  df -B1 "$REPO_ROOT" >"${output%.json}.disk.txt"
}
