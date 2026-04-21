#!/usr/bin/env bash
set -euo pipefail

# Purpose: Swarm catalog growth — CSV columns match vn-IPFS catalog_growth_test.sh.
#   - PUT always on swarm-bootstrap (host SWARM_API), same role as vn-IPFS bootstrap PUT. Worker-only
#     uploads break peer retrieval in this mesh (“no suitable peer” on the getter).
#   - Timed GET: curl inside GET_CONTAINER. Default URL base is swarm-bootstrap on the Docker
#     overlay (inspect fall25_independentstudy_node-network): worker localhost often returns 404 with
#     pinning / --store.cache.size 0 / mesh. Set CATALOG_GROWTH_SWARM_GET_LOCALHOST=1 to force
#     http://127.0.0.1:8500, or CATALOG_GROWTH_SWARM_BZZ_HTTP for a full base (no trailing slash).
#   - CATALOG_GROWTH_SWARM_FETCH=latest (default): time GET of the hash just uploaded in that row so the
#     getter has not cached that root yet (avoids absurd sub-ms warm-cache rows after the first fetch).
#   - CATALOG_GROWTH_SWARM_FETCH=first: same fixed root as vn-IPFS (first upload); before each GET run
#     best-effort bzz-pin + bzz DELETE to evict local state — use SWARM_ENABLE_PINNING=true when
#     generating the stack (see swarm/start.sh) or eviction is mostly ineffective.
#   - CATALOG_GROWTH_HOST_WALL_GET=1 (default): measure GET as host wall time (docker exec + curl), same
#     as vn-IPFS catalog. Set to 0 for in-container curl time_total only. CATALOG_GROWTH_SWARM_HOST_WALL_GET=1
#     still forces host wall if you only set the Swarm-specific name.
#   - GET container: last healthy swarm-node*, else swarm-bootstrap.
# Usage: ./catalog_growth_swarm_test.sh [--node-count 50] [--max-files 256] [--payload-size 8192] [--output file.csv] [--append]

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "$SCRIPT_DIR/comparison_system_env.sh"
source "$SCRIPT_DIR/swarm_publish_url.sh"

NODE_COUNT="${CATALOG_GROWTH_NODE_COUNT:-50}"
MAX_FILES="${CATALOG_GROWTH_MAX_OBJECTS:-256}"
PAYLOAD_SIZE="${CATALOG_GROWTH_PAYLOAD_BYTES:-8192}"
FETCH_MODE="${CATALOG_GROWTH_SWARM_FETCH:-latest}"
OUTPUT_FILE="catalog_growth_results.csv"
APPEND=false
SWARM_API="${SWARM_API:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --node-count) NODE_COUNT="$2"; shift 2 ;;
    --max-files) MAX_FILES="$2"; shift 2 ;;
    --payload-size) PAYLOAD_SIZE="$2"; shift 2 ;;
    --output) OUTPUT_FILE="$2"; shift 2 ;;
    --append) APPEND=true; shift ;;
    --help)
      echo "Usage: $0 [--node-count N] [--max-files M] [--payload-size bytes] [--output file.csv] [--append]"
      echo "Env: CATALOG_GROWTH_NODE_COUNT, CATALOG_GROWTH_MAX_OBJECTS, CATALOG_GROWTH_PAYLOAD_BYTES,"
      echo "     SWARM_API, CATALOG_GROWTH_SWARM_FETCH=latest|first,"
      echo "     CATALOG_GROWTH_HOST_WALL_GET=0|1 (default 1), CATALOG_GROWTH_SWARM_HOST_WALL_GET, CATALOG_GROWTH_SWARM_EVICT_SLEEP_SEC,"
      echo "     CATALOG_GROWTH_SWARM_GET_LOCALHOST=0|1, CATALOG_GROWTH_SWARM_BZZ_HTTP (override GET base)"
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

case "$FETCH_MODE" in
  first|latest) ;;
  *)
    echo "Error: CATALOG_GROWTH_SWARM_FETCH must be first or latest (got: $FETCH_MODE)" >&2
    exit 1
    ;;
esac

if ! command -v bc >/dev/null 2>&1; then
  echo "Error: bc required" >&2
  exit 1
fi

cmp_resolve_system_flags
if [[ "${CMP_INCLUDE_SWARM:-1}" != "1" ]]; then
  echo "catalog_growth_swarm_test is Swarm only; CMP_INCLUDE_SWARM is off." >&2
  exit 0
fi

if [[ -z "$SWARM_API" ]]; then
  SWARM_API="$(swarm_publish_base_url)"
fi

if ! docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^swarm-bootstrap$'; then
  echo "Error: swarm-bootstrap container not running" >&2
  exit 1
fi

SWARM_WORKERS=()
while IFS= read -r c; do
  [[ -z "$c" ]] && continue
  if docker exec "$c" curl -sSf -m 3 "http://127.0.0.1:8500/" >/dev/null 2>&1; then
    SWARM_WORKERS+=("$c")
  fi
done < <(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^swarm-node[0-9]+$' | sort -V || true)

nw=${#SWARM_WORKERS[@]}
GET_CONTAINER=""
if [[ "$nw" -ge 1 ]]; then
  GET_CONTAINER="${SWARM_WORKERS[$((nw - 1))]}"
else
  GET_CONTAINER="swarm-bootstrap"
  echo "Catalog growth (Swarm): no swarm-node workers; GET on bootstrap only (single-node)" >&2
fi

SWARM_NODE_NETWORK_NAME="${SWARM_NODE_NETWORK_NAME:-fall25_independentstudy_node-network}"

swarm_bootstrap_overlay_http() {
  local ip
  ip=$(docker inspect -f "{{range \$k, \$v := .NetworkSettings.Networks}}{{if eq \$k \"${SWARM_NODE_NETWORK_NAME}\"}}{{\$v.IPAddress}}{{end}}{{end}}" swarm-bootstrap 2>/dev/null | tr -d '\n\r')
  [[ -n "$ip" ]] || ip="172.20.0.200"
  echo "http://${ip}:8500"
}

BZZ_GET_BASE=""
if [[ -n "${CATALOG_GROWTH_SWARM_BZZ_HTTP:-}" ]]; then
  BZZ_GET_BASE="${CATALOG_GROWTH_SWARM_BZZ_HTTP%/}"
elif [[ "${CATALOG_GROWTH_SWARM_GET_LOCALHOST:-0}" == "1" ]]; then
  BZZ_GET_BASE="http://127.0.0.1:8500"
elif [[ "$GET_CONTAINER" == "swarm-bootstrap" ]]; then
  BZZ_GET_BASE="http://127.0.0.1:8500"
else
  BZZ_GET_BASE="$(swarm_bootstrap_overlay_http)"
fi

TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' EXIT

generate_file() {
  local out="$1"
  local seq="$2"
  local sz="$PAYLOAD_SIZE"
  if [[ "$sz" -lt 8 ]]; then sz=8; fi
  printf '%08d' "$seq" | dd of="$out" bs=1 count=8 2>/dev/null
  dd if=/dev/urandom bs=1 count=$((sz - 8)) >>"$out" 2>/dev/null
}

# stdout: ms|hash64 — POST to bootstrap only.
swarm_put_ms_hash() {
  local fp="$1"
  local body_tmp="$TEMP_DIR/sw_put_$$.bin"
  local http_sec rc hash
  set +e
  http_sec=$(curl -sSf -m 180 -X POST \
    -H "Content-Type: application/octet-stream" \
    --data-binary "@$fp" \
    -o "$body_tmp" \
    -w '%{time_total}' \
    "${SWARM_API%/}/bzz:/" 2>&1)
  rc=$?
  set -e
  if [[ "$rc" -ne 0 ]]; then
    rm -f "$body_tmp"
    return 1
  fi
  http_sec=$(echo "$http_sec" | tr -d ' \n\r')
  [[ "$http_sec" =~ ^[0-9]+\.?[0-9]*$ ]] || { rm -f "$body_tmp"; return 1; }
  hash=$(tr -d '\n\r' < "$body_tmp")
  rm -f "$body_tmp"
  if [[ ! "$hash" =~ ^[a-fA-F0-9]{64,}$ ]]; then
    return 1
  fi
  hash="${hash:0:64}"
  local ms
  ms=$(echo "scale=6; $http_sec * 1000" | bc -l)
  echo "${ms}|${hash}"
}

swarm_evict_local_best_effort() {
  local hash="$1"
  [[ -z "$hash" ]] && return 0
  local _c="$GET_CONTAINER"
  local _pass
  for _pass in 1 2; do
    docker exec "$_c" sh -c \
      "curl -sS -m 15 -o /dev/null -X DELETE 'http://127.0.0.1:8500/bzz-pin:/${hash}'" 2>/dev/null || true
    docker exec "$_c" sh -c \
      "curl -sS -m 15 -o /dev/null -X DELETE 'http://127.0.0.1:8500/bzz:/${hash}/'" 2>/dev/null || true
    docker exec "$_c" sh -c \
      "curl -sS -m 15 -o /dev/null -X DELETE 'http://127.0.0.1:8500/bzz:/${hash}'" 2>/dev/null || true
    docker exec "$_c" sh -c \
      "curl -sS -m 15 -o /dev/null -X DELETE 'http://127.0.0.1:8500/bzz-raw:/${hash}'" 2>/dev/null || true
  done
  if [[ "$FETCH_MODE" == "first" ]]; then
    sleep "${CATALOG_GROWTH_SWARM_EVICT_SLEEP_SEC:-0.1}"
  fi
}

# One attempt: stdout is latency in ms (CATALOG_GROWTH_HOST_WALL_GET / SWARM_HOST_WALL_GET).
_swarm_cg_get_try() {
  local remote_out="$1"
  local url_path="$2"
  local tt rc t0 t1
  local use_hw=false
  case "${CATALOG_GROWTH_HOST_WALL_GET:-1}" in
    0|false|FALSE|no|NO) ;;
    *) use_hw=true ;;
  esac
  [[ "${CATALOG_GROWTH_SWARM_HOST_WALL_GET:-0}" == "1" ]] && use_hw=true
  [[ "${CATALOG_GROWTH_SWARM_HOST_WALL_GET:-}" == "0" ]] && use_hw=false
  if [[ "$use_hw" == "true" ]]; then
    t0=$(date +%s%N)
    set +e
    docker exec "$GET_CONTAINER" curl -sSfL -m 120 -o "$remote_out" \
      "${BZZ_GET_BASE}/${url_path}" 2>/dev/null
    rc=$?
    set -e
    t1=$(date +%s%N)
    [[ "$rc" -eq 0 ]] || return 1
    docker exec "$GET_CONTAINER" test -s "$remote_out" 2>/dev/null || return 1
    if docker exec "$GET_CONTAINER" grep -q "<a href=" "$remote_out" 2>/dev/null; then
      return 1
    fi
    echo "scale=6; ($t1 - $t0) / 1000000" | bc -l
    return 0
  fi
  set +e
  tt=$(docker exec "$GET_CONTAINER" curl -sSfL -m 120 -o "$remote_out" -w "%{time_total}" \
    "${BZZ_GET_BASE}/${url_path}" 2>/dev/null)
  rc=$?
  set -e
  [[ "$rc" -eq 0 ]] || return 1
  [[ -n "$tt" && "$tt" =~ ^[0-9] ]] || return 1
  docker exec "$GET_CONTAINER" test -s "$remote_out" 2>/dev/null || return 1
  if docker exec "$GET_CONTAINER" grep -q "<a href=" "$remote_out" 2>/dev/null; then
    return 1
  fi
  echo "scale=6; $tt * 1000" | bc -l
  return 0
}

# stdout: ms — GET via getter localhost.
swarm_get_total_ms() {
  local hash="$1"
  local rid="$RANDOM"
  local remote_out="/tmp/swarm_cg_get_${rid}.bin"
  local attempt max_attempts=25 sleep_sec=2
  local p ms_line

  for ((attempt = 1; attempt <= max_attempts; attempt++)); do
    for p in "bzz:/${hash}/" "bzz:/${hash}" "bzz-raw:/${hash}"; do
      if ms_line=$(_swarm_cg_get_try "$remote_out" "$p"); then
        docker exec "$GET_CONTAINER" rm -f "$remote_out" >/dev/null 2>&1 || true
        echo "$ms_line"
        return 0
      fi
    done
    sleep "$sleep_sec"
  done
  docker exec "$GET_CONTAINER" rm -f "$remote_out" >/dev/null 2>&1 || true
  return 1
}

if [[ "$APPEND" != true ]]; then
  echo "system,node_count,files_on_network,payload_size,upload_ms,download_total_ms" > "$OUTPUT_FILE"
fi

echo "Catalog growth (Swarm): PUT=swarm-bootstrap GET=$GET_CONTAINER bzz_base=$BZZ_GET_BASE fetch=$FETCH_MODE host_wall_get=${CATALOG_GROWTH_HOST_WALL_GET:-1} workers_ok=$nw"
echo "  node_count=$NODE_COUNT (label), max_files=$MAX_FILES, payload=$PAYLOAD_SIZE"

FIRST_HASH=""
for f in $(seq 1 "$MAX_FILES"); do
  fp="$TEMP_DIR/blob_$f.bin"
  generate_file "$fp" "$f"
  up=$(swarm_put_ms_hash "$fp" 2>/dev/null) || true
  if [[ -z "$up" || "$up" != *"|"* ]]; then
    echo "swarm,$NODE_COUNT,$f,$PAYLOAD_SIZE,ERROR,ERROR" >> "$OUTPUT_FILE"
    echo "  stop: Swarm upload failed at files_on_network=$f" >&2
    break
  fi
  lat=$(echo "$up" | cut -d'|' -f1)
  rh=$(echo "$up" | cut -d'|' -f2)
  [[ -z "$FIRST_HASH" ]] && FIRST_HASH="$rh"

  gkey="$rh"
  if [[ "$FETCH_MODE" == "first" ]]; then
    gkey="$FIRST_HASH"
    swarm_evict_local_best_effort "$gkey"
  fi

  dl_ms="ERROR"
  if [[ -n "$gkey" ]]; then
    dl_ms=$(swarm_get_total_ms "$gkey" 2>/dev/null) || dl_ms="ERROR"
  fi
  echo "swarm,$NODE_COUNT,$f,$PAYLOAD_SIZE,$lat,$dl_ms" >> "$OUTPUT_FILE"
  if (( f % 25 == 0 )) || [[ "$f" -eq 1 ]]; then
    echo "  files_on_network=$f upload_ms=$lat download_total_ms=$dl_ms" >&2
  fi
done

echo "Wrote $OUTPUT_FILE"
