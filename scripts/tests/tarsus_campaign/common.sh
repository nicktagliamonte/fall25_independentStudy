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
  local commit dirty
  commit=$(git -C "$REPO_ROOT" rev-parse HEAD)
  dirty=$(git -C "$REPO_ROOT" status --porcelain)
  jq -n \
    --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg commit "$commit" \
    --arg branch "$(git -C "$REPO_ROOT" branch --show-current)" \
    --arg dirty "$dirty" \
    --arg kernel "$(uname -srvmo)" \
    --arg cpus "$(getconf _NPROCESSORS_ONLN)" \
    --arg docker_version "$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)" \
    --arg compose_version "$(docker-compose version --short 2>/dev/null || true)" \
    '{captured_at:$captured_at,git:{commit:$commit,branch:$branch,dirty:$dirty},
      host:{kernel:$kernel,logical_cpus:($cpus|tonumber)},
      docker:{engine:$docker_version,compose:$compose_version}}' >"$output"
  free -b >"${output%.json}.memory.txt"
  df -B1 "$REPO_ROOT" >"${output%.json}.disk.txt"
}
