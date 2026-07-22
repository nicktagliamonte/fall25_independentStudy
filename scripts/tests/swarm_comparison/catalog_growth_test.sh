#!/usr/bin/env bash
set -euo pipefail

# Purpose: Measure upload and download as catalog grows (1..N). Upload: host curl, same as Swarm, but
#          -w time_starttransfer (ms to first response byte) — not full time_total. GET: remote_only worker.
# Download timing: default CATALOG_GROWTH_HOST_WALL_GET=1 — host date +%s%N around the full docker exec + curl
# (same order of magnitude as Swarm catalog host-wall GET). Set CATALOG_GROWTH_HOST_WALL_GET=0 for in-container curl time_total only.
# Usage: ./catalog_growth_test.sh [--node-count 50] [--max-files 256] [--payload-size bytes] [--trials T] [--output file.csv] [--our-api container]
# Multi-trial: CATALOG_GROWTH_TRIALS>1 runs the sweep that many times and writes row-wise arithmetic means (expects a clean store per trial for valid results).

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

NODE_COUNT="${CATALOG_GROWTH_NODE_COUNT:-50}"
MAX_FILES="${CATALOG_GROWTH_MAX_OBJECTS:-256}"
PAYLOAD_SIZE="${CATALOG_GROWTH_PAYLOAD_BYTES:-262144}"
TRIALS="${CATALOG_GROWTH_TRIALS:-1}"
OUTPUT_FILE="catalog_growth_results.csv"
OUR_API=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --node-count) NODE_COUNT="$2"; shift 2 ;;
    --max-files) MAX_FILES="$2"; shift 2 ;;
    --payload-size) PAYLOAD_SIZE="$2"; shift 2 ;;
    --trials) TRIALS="$2"; shift 2 ;;
    --output) OUTPUT_FILE="$2"; shift 2 ;;
    --our-api) OUR_API="$2"; shift 2 ;;
    --help)
      echo "Usage: $0 [--node-count N] [--max-files M] [--payload-size bytes] [--trials T] [--output file.csv] [--our-api container]"
      echo "Env: CATALOG_GROWTH_NODE_COUNT, CATALOG_GROWTH_MAX_OBJECTS (default 256), CATALOG_GROWTH_PAYLOAD_BYTES (default 262144),"
      echo "     CATALOG_GROWTH_TRIALS (default 1; mean upload_ms and download_total_ms per row across trials),"
      echo "     CATALOG_GROWTH_HOST_WALL_GET=0|1 (default 1: host wall around docker exec GET)"
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

if ! [[ "$TRIALS" =~ ^[1-9][0-9]*$ ]]; then
  echo "Error: --trials / CATALOG_GROWTH_TRIALS must be a positive integer (got: $TRIALS)" >&2
  exit 1
fi

if ! command -v bc >/dev/null 2>&1; then
  echo "Error: bc required" >&2
  exit 1
fi

source "$SCRIPT_DIR/comparison_system_env.sh"
cmp_resolve_system_flags

if [[ "${CMP_INCLUDE_OUR:-1}" != "1" ]]; then
  echo "catalog_growth_test is vn-IPFS only; CMP_INCLUDE_OUR is off." >&2
  exit 0
fi

OUR_CONTAINER=""
OUR_API_ADDR=""
if [[ -z "$OUR_API" ]]; then
  if docker ps --format '{{.Names}}' | grep -q "^fall25-bootstrap$"; then
    OUR_CONTAINER="fall25-bootstrap"
    OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
  fi
  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
    for compose in "$ROOT_DIR/docker-compose.vnipfs.yml" "$ROOT_DIR/docker-compose.yml"; do
      [[ ! -f "$compose" ]] || ! command -v docker-compose >/dev/null 2>&1 && continue
      if docker-compose -f "$compose" ps bootstrap 2>/dev/null | grep -q "Up"; then
        OUR_CONTAINER=$(docker-compose -f "$compose" ps -q bootstrap 2>/dev/null | xargs -r docker inspect -f '{{.Name}}' 2>/dev/null | sed 's|^/||' || echo "fall25-bootstrap")
        [[ -z "$OUR_CONTAINER" ]] && OUR_CONTAINER="fall25-bootstrap"
        OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
        [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && break
      fi
    done
  fi
else
  if [[ "$OUR_API" =~ ^[a-zA-Z0-9_-]+$ ]]; then
    OUR_CONTAINER="$OUR_API"
    OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
  fi
fi

if [[ -z "$OUR_CONTAINER" || -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
  echo "Error: could not resolve vn-IPFS bootstrap container / API" >&2
  echo "  Start the stack first, e.g.: ./scripts/docker/start_vnipfs.sh 50" >&2
  echo "  Or pass --our-api <container> if bootstrap.json lives elsewhere." >&2
  echo "  Check: docker ps --format '{{.Names}}' | grep fall25-bootstrap" >&2
  exit 1
fi

GET_CONTAINER="$OUR_CONTAINER"
GET_API_ADDR="$OUR_API_ADDR"
while IFS= read -r c; do
  [[ -z "$c" || "$c" == "$OUR_CONTAINER" ]] && continue
  ctrl_path="/app/logs/$(echo "$c" | sed 's/^fall25-//').json"
  addr=$(docker exec "$c" jq -r '.addr // .Addr' "$ctrl_path" 2>/dev/null || echo "")
  if [[ -n "$addr" && "$addr" != "null" ]]; then
    GET_CONTAINER="$c"
    GET_API_ADDR="$addr"
  fi
done < <(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-node' || true)

TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' EXIT

get_provider_info() {
  local id_json
  id_json=$(docker exec "$OUR_CONTAINER" curl -sSf "http://$OUR_API_ADDR/id" 2>/dev/null || echo "{}")
  local peer_id addrs
  peer_id=$(echo "$id_json" | jq -r '.peer' 2>/dev/null || echo "")
  addrs=$(echo "$id_json" | jq -r '.addrs[0]' 2>/dev/null || echo "")
  if [[ -z "$peer_id" || "$peer_id" == "null" || -z "$addrs" || "$addrs" == "null" ]]; then
    return 1
  fi
  echo "$peer_id|$addrs"
}

PROVIDER_INFO=$(get_provider_info) || { echo "Error: provider id" >&2; exit 1; }
PROVIDER_PEER_ID=$(echo "$PROVIDER_INFO" | cut -d'|' -f1)
PROVIDER_ADDR=$(echo "$PROVIDER_INFO" | cut -d'|' -f2)

generate_file() {
  local out="$1"
  local seq="$2"
  local sz="$PAYLOAD_SIZE"
  if [[ "$sz" -lt 8 ]]; then sz=8; fi
  printf '%08d' "$seq" | dd of="$out" bs=1 count=8 2>/dev/null
  dd if=/dev/urandom bs=1 count=$((sz - 8)) >>"$out" 2>/dev/null
}

# stdout: latency_ms|multihash_hex; upload = curl time_starttransfer (ms to first response byte)
our_put_timed_key() {
  local fp="$1"
  local rid="$RANDOM"
  local body_out="$TEMP_DIR/put_resp_${rid}.json"
  local line port put_url
  line=$(docker port "$OUR_CONTAINER" 9400 2>/dev/null | head -1) || true
  [[ -z "$line" ]] && line=$(docker port "$OUR_CONTAINER" 9400/tcp 2>/dev/null | head -1) || true
  port=$(echo "$line" | grep -oE '[0-9]+$' | head -1)
  put_url="http://127.0.0.1:${port:-9400}/put"
  local http_sec response key
  http_sec=$(curl -sSf -m 180 -X POST \
    -H "Content-Type: application/octet-stream" \
    --data-binary "@$fp" \
    -o "$body_out" \
    -w '%{time_starttransfer}' \
    "$put_url" 2>&1) || true
  response=$(cat "$body_out" 2>/dev/null || echo "")
  rm -f "$body_out" 2>/dev/null || true
  key=$(echo "$response" | jq -r '.multihash_hex // empty' 2>/dev/null || echo "")
  if [[ -z "$key" || "$key" == "null" || ${#key} -ne 64 ]]; then
    return 1
  fi
  http_sec=$(echo "$http_sec" | tr -d ' \n\r')
  [[ "$http_sec" =~ ^[0-9]+\.?[0-9]*$ ]] || return 1
  local latency_ms
  latency_ms=$(echo "scale=6; $http_sec * 1000" | bc -l)
  echo "${latency_ms}|${key}"
}

download_total_ms() {
  local key="$1"
  local rid="$RANDOM"
  local get_req="$TEMP_DIR/get_${rid}.json"
  local remote_body="/tmp/get_body_catalog_${rid}.bin"
  echo "{\"key\":\"$key\",\"timeout\":\"${SNG40_GET_JSON_TIMEOUT:-90s}\"}" > "$get_req"
  docker cp "$get_req" "${GET_CONTAINER}:/tmp/get_req_catalog.json" >/dev/null 2>&1

  local use_host_wall=true
  case "${CATALOG_GROWTH_HOST_WALL_GET:-1}" in
    0|false|FALSE|no|NO) use_host_wall=false ;;
  esac

  local rc curl_output total_curl t0 t1
  if [[ "$use_host_wall" == "true" ]]; then
    t0=$(date +%s%N)
    set +e
    docker exec "$GET_CONTAINER" curl -sSf -m "${SNG40_GET_CURL_MAX_SEC:-120}" -X POST \
      -H "Content-Type: application/json" \
      -H "Accept: application/octet-stream" \
      -d @/tmp/get_req_catalog.json \
      -o "$remote_body" \
      "http://$GET_API_ADDR/get?format=raw&remote_only=1" 2>/dev/null
    rc=$?
    set -e
    t1=$(date +%s%N)
    [[ "$rc" -eq 0 ]] || {
      docker exec "$GET_CONTAINER" rm -f /tmp/get_req_catalog.json "$remote_body" >/dev/null 2>&1 || true
      rm -f "$get_req"
      return 1
    }
    docker exec "$GET_CONTAINER" test -s "$remote_body" 2>/dev/null || {
      docker exec "$GET_CONTAINER" rm -f /tmp/get_req_catalog.json "$remote_body" >/dev/null 2>&1 || true
      rm -f "$get_req"
      return 1
    }
    docker exec "$GET_CONTAINER" rm -f /tmp/get_req_catalog.json "$remote_body" >/dev/null 2>&1 || true
    rm -f "$get_req"
    echo "scale=6; ($t1 - $t0) / 1000000" | bc -l
    return 0
  fi

  set +e
  curl_output=$(docker exec "$GET_CONTAINER" curl -sSf -m "${SNG40_GET_CURL_MAX_SEC:-120}" -w "\n%{time_total}" -X POST \
    -H "Content-Type: application/json" \
    -H "Accept: application/octet-stream" \
    -d @/tmp/get_req_catalog.json \
    -o "$remote_body" \
    "http://$GET_API_ADDR/get?format=raw&remote_only=1" 2>&1)
  rc=$?
  set -e
  docker exec "$GET_CONTAINER" rm -f /tmp/get_req_catalog.json "$remote_body" >/dev/null 2>&1 || true
  rm -f "$get_req"
  [[ "$rc" -eq 0 ]] || return 1
  total_curl=$(echo "$curl_output" | tail -n 1)
  [[ "$total_curl" =~ ^[0-9] ]] || return 1
  echo "scale=6; $total_curl * 1000" | bc -l
}

MERGE_SH="$SCRIPT_DIR/catalog_growth_merge.sh"
[[ -x "$MERGE_SH" ]] || MERGE_SH="bash $MERGE_SH"

run_one_catalog_pass() {
  local out="$1"
  echo "system,node_count,files_on_network,payload_size,upload_ms,download_total_ms" > "$out"
  local FIRST_KEY=""
  local f fp up lat kh dl_ms
  for f in $(seq 1 "$MAX_FILES"); do
    fp="$TEMP_DIR/blob_$f.bin"
    generate_file "$fp" "$f"
    up=$(our_put_timed_key "$fp" 2>/dev/null) || true
    if [[ -z "$up" || "$up" != *"|"* ]]; then
      echo "our_system,$NODE_COUNT,$f,$PAYLOAD_SIZE,ERROR,ERROR" >> "$out"
      echo "  stop: upload failed at files_on_network=$f" >&2
      break
    fi
    lat=$(echo "$up" | cut -d'|' -f1)
    kh=$(echo "$up" | cut -d'|' -f2)
    [[ -z "$FIRST_KEY" ]] && FIRST_KEY="$kh"
    dl_ms="ERROR"
    if [[ -n "$FIRST_KEY" ]]; then
      dl_ms=$(download_total_ms "$FIRST_KEY" 2>/dev/null) || dl_ms="ERROR"
    fi
    echo "our_system,$NODE_COUNT,$f,$PAYLOAD_SIZE,$lat,$dl_ms" >> "$out"
    if (( f % 25 == 0 )) || [[ "$f" -eq 1 ]]; then
      echo "  files_on_network=$f upload_ms=$lat download_total_ms=$dl_ms" >&2
    fi
  done
}

echo "Catalog growth test: node_count=$NODE_COUNT (label), max_files=$MAX_FILES, payload=$PAYLOAD_SIZE, trials=$TRIALS, GET remote_only via $GET_CONTAINER"
if [[ "$TRIALS" -gt 1 ]]; then
  echo "  (trials>1: each trial should use an empty store; use fresh volumes or separate benchmark invocations.)" >&2
  PASS_FILES=()
  for ((t = 1; t <= TRIALS; t++)); do
    echo "  --- trial $t/$TRIALS ---" >&2
    pf="$TEMP_DIR/catalog_pass_${t}.csv"
    run_one_catalog_pass "$pf"
    PASS_FILES+=("$pf")
  done
  "$MERGE_SH" "$OUTPUT_FILE" "${PASS_FILES[@]}"
else
  run_one_catalog_pass "$OUTPUT_FILE"
fi

echo "Wrote $OUTPUT_FILE"
