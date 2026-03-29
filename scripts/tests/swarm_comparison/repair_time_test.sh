#!/usr/bin/env bash
set -euo pipefail

# Purpose: Measure repair time after node failure.
# Put content, wait for R replicas, stop one node holding a replica, measure time until R restored.
# Output: system,node_count,repair_time_s
# Usage: ./scripts/tests/swarm_comparison/repair_time_test.sh [options]

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "$ROOT_DIR/scripts/utils/error_handler.sh"
source "$SCRIPT_DIR/api.sh"

RUN_ID="${RUN_ID:-$(date +%s)}"
ERROR_LOG_DIR="artifacts/swarm_tests/$RUN_ID"
export RUN_ID ERROR_LOG_DIR
mkdir -p "$ERROR_LOG_DIR"

OUR_API=""
PAYLOAD_SIZE=65536
REPLICAS_TARGET=2
TIMEOUT_S=180
POLL_INTERVAL_S=2
FAST_POLL_S=0.25
OUTPUT_FILE="repair_time_results.csv"
NODE_COUNT=""
APPEND=false

while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)         OUR_API="$2"; shift 2 ;;
    --payload-size)    PAYLOAD_SIZE="$2"; shift 2 ;;
    --replicas-target) REPLICAS_TARGET="$2"; shift 2 ;;
    --timeout)         TIMEOUT_S="$2"; shift 2 ;;
    --poll-interval)   POLL_INTERVAL_S="$2"; shift 2 ;;
    --fast-poll)       FAST_POLL_S="$2"; shift 2 ;;
    --node-count)      NODE_COUNT="$2"; shift 2 ;;
    --output)          OUTPUT_FILE="$2"; shift 2 ;;
    --append)          APPEND=true; shift ;;
    --help)
      echo "Usage: $0 [options]"
      echo "  --our-api <addr>         Our system (default: auto-detect)"
      echo "  --payload-size <n>       Payload bytes (default: 65536)"
      echo "  --replicas-target <R>    Replicas to restore (default: 2)"
      echo "  --timeout <s>            Max wait for repair (default: 180)"
      echo "  --poll-interval <s>      Poll interval after fast phase (default: 2)"
    echo "  --fast-poll <s>         Poll interval for first 20s (default: 0.25)"
      echo "  --node-count <n>         Node count"
      echo "  --output <file>          Output CSV"
      echo "  --append                 Append rows"
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

source "$SCRIPT_DIR/comparison_system_env.sh"
cmp_resolve_system_flags
if [[ "${CMP_INCLUDE_OUR:-1}" != "1" ]]; then
  echo "repair_time is vn-IPFS only; skipping."
  exit 0
fi

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

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
        OUR_CONTAINER="bootstrap"
        OUR_API_ADDR=$(docker-compose -f "$compose" exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
        [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && break
      fi
    done
  fi
  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
    echo -e "${RED}Error: Could not detect our system API.${NC}" >&2
    exit 1
  fi
  OUR_API="http://$OUR_API_ADDR"
fi

if [[ -z "$OUR_CONTAINER" ]]; then
  OUR_CONTAINER=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-bootstrap$|^bootstrap$' | head -1)
fi

if [[ -z "$NODE_COUNT" ]]; then
  vn_count=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -c -E '^fall25-(bootstrap|node)' || echo "0")
  swarm_count=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -c -E '^swarm-(bootstrap|node)' || echo "0")
  NODE_COUNT=$((vn_count > swarm_count ? vn_count : swarm_count))
  [[ "$NODE_COUNT" -lt 1 ]] && NODE_COUNT=1
fi

TEMP_DIR=$(mktemp -d)
trap "rm -rf '$TEMP_DIR'" EXIT

compose_cmd="docker-compose"
command -v docker-compose >/dev/null 2>&1 || compose_cmd="docker compose"
vnipfs_compose=$( [[ -f "$ROOT_DIR/docker-compose.vnipfs.yml" ]] && echo "$ROOT_DIR/docker-compose.vnipfs.yml" || echo "$ROOT_DIR/docker-compose.yml" )
swarm_compose="$ROOT_DIR/docker-compose.swarm.yml"

if [[ "$APPEND" != "true" ]]; then
  echo "system,node_count,repair_time_s" > "$OUTPUT_FILE"
fi

echo -e "${BLUE}Repair Time Test (after node failure)${NC}"
echo "  Target: measure time until R=$REPLICAS_TARGET replicas restored after stopping one node"
echo ""

# --- Our system ---
echo -e "${GREEN}Our system: put, wait R, stop one replica node, measure repair time${NC}"
dd if=/dev/urandom of="$TEMP_DIR/payload.bin" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
data_b64=$(base64 -w 0 < "$TEMP_DIR/payload.bin" 2>/dev/null || base64 < "$TEMP_DIR/payload.bin" | tr -d '\n')
echo "{\"data\":\"$data_b64\"}" > "$TEMP_DIR/put_req.json"
docker cp "$TEMP_DIR/put_req.json" "${OUR_CONTAINER}:/tmp/put_repair_$$.json" >/dev/null 2>&1
resp=$(docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" \
  -d @/tmp/put_repair_$$.json "http://$OUR_API_ADDR/put" 2>/dev/null || echo "{}")
docker exec "$OUR_CONTAINER" rm -f /tmp/put_repair_$$.json >/dev/null 2>&1 || true

KEY=$(echo "$resp" | jq -r '.multihash_hex // .cid // empty')
[[ -z "$KEY" || "$KEY" == "null" ]] && KEY=$(echo "$resp" | jq -r '.cid' | grep -oE '[a-fA-F0-9]{64}' || true)

if [[ -z "$KEY" || ${#KEY} -lt 32 ]]; then
  echo -e "  ${RED}Upload failed${NC}"
  echo "our_system,$NODE_COUNT,FAILED" >> "$OUTPUT_FILE"
else
  echo "  Key: $KEY"
  start=$(date +%s.%N 2>/dev/null || date +%s)
  while true; do
    now=$(date +%s.%N 2>/dev/null || date +%s)
    elapsed=$(awk "BEGIN {printf \"%.2f\", $now - $start}" 2>/dev/null || echo "$(( $(date +%s) - ${start%%.*} ))")
    [[ "$elapsed" == .* ]] && elapsed="0$elapsed"
    elapsed_int=${elapsed%%.*}
    [[ -z "$elapsed_int" ]] && elapsed_int=0
    [[ $elapsed_int -ge 90 ]] && break
    count=$(docker exec "$OUR_CONTAINER" curl -sSf "http://$OUR_API_ADDR/replication/status?key=$KEY" 2>/dev/null | jq -r '.replica_count // 0' || echo "0")
    [[ "$count" -ge "$REPLICAS_TARGET" ]] && break
    sleep "$POLL_INTERVAL_S"
  done
  count=$(docker exec "$OUR_CONTAINER" curl -sSf "http://$OUR_API_ADDR/replication/status?key=$KEY" 2>/dev/null | jq -r '.replica_count // 0' || echo "0")
  if [[ "$count" -lt "$REPLICAS_TARGET" ]]; then
    echo -e "  ${YELLOW}Did not reach R=$REPLICAS_TARGET before failure step, skipping repair${NC}"
    echo "our_system,$NODE_COUNT,SKIP" >> "$OUTPUT_FILE"
  else
    node_to_stop=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-node' | head -1)
    if [[ -n "$node_to_stop" ]]; then
      echo "  Stopping $node_to_stop..."
      repair_start=$(date +%s.%N 2>/dev/null || date +%s)
      docker stop "$node_to_stop" >/dev/null 2>&1 || true
      sleep 0.5
      repair_time="TIMEOUT"
      while true; do
        now=$(date +%s.%N 2>/dev/null || date +%s)
        elapsed=$(awk "BEGIN {printf \"%.3f\", $now - $repair_start}" 2>/dev/null || echo "$(( $(date +%s) - ${repair_start%%.*} ))")
        [[ "$elapsed" == .* ]] && elapsed="0$elapsed"
        elapsed_int=${elapsed%%.*}
        [[ -z "$elapsed_int" ]] && elapsed_int=0
        [[ $elapsed_int -ge $TIMEOUT_S ]] && break
        count=$(docker exec "$OUR_CONTAINER" curl -sSf "http://$OUR_API_ADDR/replication/status?key=$KEY" 2>/dev/null | jq -r '.replica_count // 0' || echo "0")
        if [[ "$count" -ge "$REPLICAS_TARGET" ]]; then
          repair_time="$elapsed"
          echo -e "  ${GREEN}Repair complete in ${elapsed}s${NC}"
          break
        fi
        if [[ "$elapsed_int" -lt 20 ]]; then
          sleep "$FAST_POLL_S"
        else
          sleep "$POLL_INTERVAL_S"
        fi
      done
      docker start "$node_to_stop" >/dev/null 2>&1 || true
      echo "our_system,$NODE_COUNT,$repair_time" >> "$OUTPUT_FILE"
    else
      echo -e "  ${YELLOW}No worker node to stop (only bootstrap?)${NC}"
      echo "our_system,$NODE_COUNT,SKIP" >> "$OUTPUT_FILE"
    fi
  fi
fi

# --- Swarm (skipped: no OOB replication; benchmark our system only) ---
echo -e "\n${YELLOW}Swarm: skipped (no OOB replication; benchmark our system only)${NC}"
echo "swarm,$NODE_COUNT,SKIP" >> "$OUTPUT_FILE"

echo ""
echo "Results: $OUTPUT_FILE"
cat "$OUTPUT_FILE"
