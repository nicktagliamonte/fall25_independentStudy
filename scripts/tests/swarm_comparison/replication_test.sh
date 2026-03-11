#!/usr/bin/env bash
set -euo pipefail

# Purpose: Replication speed test - put one payload, poll replica count until R reached.
# Output: system,payload_size,nodes,replicas_target,time_to_R_s
# Usage: ./scripts/tests/swarm_comparison/replication_test.sh [options]

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "$ROOT_DIR/scripts/utils/error_handler.sh"
source "$SCRIPT_DIR/api.sh"

RUN_ID="${RUN_ID:-$(date +%s)}"
ERROR_LOG_DIR="artifacts/swarm_tests/$RUN_ID"
export RUN_ID ERROR_LOG_DIR
mkdir -p "$ERROR_LOG_DIR"

OUR_API=""
SWARM_API="http://172.20.0.200:8500"
PAYLOAD_SIZE=65536
REPLICAS_TARGET=2
TIMEOUT_S=120
POLL_INTERVAL_S=2
OUTPUT_FILE="replication_results.csv"
NODE_COUNT=""
APPEND=false
RECORD_OVERHEAD=false

while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)         OUR_API="$2"; shift 2 ;;
    --swarm-api)       SWARM_API="$2"; shift 2 ;;
    --payload-size)    PAYLOAD_SIZE="$2"; shift 2 ;;
    --replicas-target) REPLICAS_TARGET="$2"; shift 2 ;;
    --timeout)         TIMEOUT_S="$2"; shift 2 ;;
    --poll-interval)   POLL_INTERVAL_S="$2"; shift 2 ;;
    --node-count)      NODE_COUNT="$2"; shift 2 ;;
    --output)          OUTPUT_FILE="$2"; shift 2 ;;
    --append)          APPEND=true; shift ;;
    --record-overhead) RECORD_OVERHEAD=true; shift ;;
    --help)
      echo "Usage: $0 [options]"
      echo "  --our-api <addr>         Our system (default: auto-detect)"
      echo "  --swarm-api <addr>       Swarm API (default: http://172.20.0.200:8500)"
      echo "  --payload-size <n>       Payload bytes (default: 65536)"
      echo "  --replicas-target <R>    Replicas to reach (default: 2)"
      echo "  --timeout <s>            Max wait seconds (default: 120)"
      echo "  --poll-interval <s>      Poll interval seconds (default: 2)"
      echo "  --node-count <n>         Node count for output (default: from containers)"
      echo "  --output <file>          Output CSV"
      echo "  --append                 Append rows (skip header)"
      echo "  --record-overhead        Record network bytes during replication"
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

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

if [[ "$OUR_API" =~ ^[a-zA-Z0-9_-]+$ ]]; then
  OUR_CONTAINER="$OUR_API"
  OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
  [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && OUR_API="http://$OUR_API_ADDR"
fi

if [[ -z "$OUR_CONTAINER" ]] || [[ "$OUR_CONTAINER" == "bootstrap" ]]; then
  resolved=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-bootstrap$|bootstrap' | head -1)
  [[ -n "$resolved" ]] && OUR_CONTAINER="$resolved"
fi

if [[ -z "$NODE_COUNT" ]]; then
  vn_count=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -c -E '^fall25-(bootstrap|node)' || echo "0")
  swarm_count=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -c -E '^swarm-(bootstrap|node)' || echo "0")
  NODE_COUNT=$((vn_count > swarm_count ? vn_count : swarm_count))
  [[ "$NODE_COUNT" -lt 1 ]] && NODE_COUNT=1
fi

TEMP_DIR=$(mktemp -d)
trap "rm -rf '$TEMP_DIR'" EXIT

get_network_bytes_for_pattern() {
  local pattern="$1"
  local sum=0
  for c in $(docker ps --format '{{.Names}}' 2>/dev/null | grep -E "$pattern" || true); do
    local r t
    r=$(docker exec "$c" cat /sys/class/net/eth0/statistics/rx_bytes 2>/dev/null || echo "0")
    t=$(docker exec "$c" cat /sys/class/net/eth0/statistics/tx_bytes 2>/dev/null || echo "0")
    sum=$((sum + ${r:-0} + ${t:-0}))
  done
  echo "$sum"
}

if [[ "$APPEND" != "true" ]]; then
  if [[ "$RECORD_OVERHEAD" == "true" ]]; then
    echo "system,payload_size,nodes,replicas_target,time_to_R_s,replication_bytes" > "$OUTPUT_FILE"
  else
    echo "system,payload_size,nodes,replicas_target,time_to_R_s" > "$OUTPUT_FILE"
  fi
fi

echo -e "${BLUE}Replication Speed Test${NC}"
echo "  Payload size: $PAYLOAD_SIZE bytes"
echo "  Replicas target: $REPLICAS_TARGET"
echo "  Timeout: ${TIMEOUT_S}s, poll interval: ${POLL_INTERVAL_S}s"
echo ""

# Generate test file
dd if=/dev/urandom of="$TEMP_DIR/payload.bin" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null

# --- Our system ---
echo -e "${GREEN}Our system: put and poll /replication/status until R >= $REPLICAS_TARGET${NC}"
data_b64=$(base64 -w 0 < "$TEMP_DIR/payload.bin" 2>/dev/null || base64 < "$TEMP_DIR/payload.bin" | tr -d '\n')
payload_file="$TEMP_DIR/put_req.json"
echo "{\"data\":\"$data_b64\"}" > "$payload_file"
docker cp "$payload_file" "${OUR_CONTAINER}:/tmp/put_req_rep_$$.json" >/dev/null 2>&1
resp=$(docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" \
  -d @/tmp/put_req_rep_$$.json "http://$OUR_API_ADDR/put" 2>/dev/null || echo "{}")
docker exec "$OUR_CONTAINER" rm -f /tmp/put_req_rep_$$.json >/dev/null 2>&1 || true

KEY=$(echo "$resp" | jq -r '.multihash_hex // .cid // empty')
if [[ -z "$KEY" || "$KEY" == "null" ]]; then
  if [[ -n "$(echo "$resp" | jq -r '.cid // empty')" ]]; then
    cid_val=$(echo "$resp" | jq -r '.cid')
    KEY=$(echo "$cid_val" | grep -oE '[a-fA-F0-9]{64}' || echo "$cid_val" | sed 's/.*Qm//' | head -c 64)
  fi
fi
if [[ -z "$KEY" || ${#KEY} -lt 32 ]]; then
  echo -e "  ${RED}Upload failed, no key. Response: $resp${NC}"
  echo "our_system,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,FAILED" >> "$OUTPUT_FILE"
else
  echo "  Key: $KEY"
  start=$(date +%s)
  time_to_r=""
  while true; do
    now=$(date +%s)
    elapsed=$((now - start))
    if [[ $elapsed -ge $TIMEOUT_S ]]; then
      echo -e "  ${YELLOW}Timeout after ${TIMEOUT_S}s (last count: ${count:-0})${NC}"
      time_to_r="TIMEOUT"
      break
    fi
    count=$(docker exec "$OUR_CONTAINER" curl -sSf "http://$OUR_API_ADDR/replication/status?key=$KEY" 2>/dev/null | jq -r '.replica_count // 0' || echo "0")
    [[ -z "$count" || "$count" == "null" ]] && count=0
    echo -n "  Poll: replica_count=$count (${elapsed}s)... "
    if [[ "$count" -ge "$REPLICAS_TARGET" ]]; then
      time_to_r="$elapsed"
      echo -e "${GREEN}reached R=$REPLICAS_TARGET in ${elapsed}s${NC}"
      break
    fi
    echo ""
    sleep "$POLL_INTERVAL_S"
  done
  overhead=""
  if [[ "$RECORD_OVERHEAD" == "true" ]]; then
    bytes_after=$(get_network_bytes_for_pattern '^fall25-|^bootstrap$|^node[0-9]+$' 2>/dev/null || echo "0")
    overhead=$((bytes_after - bytes_before))
    echo "our_system,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,$time_to_r,$overhead" >> "$OUTPUT_FILE"
  else
    echo "our_system,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,$time_to_r" >> "$OUTPUT_FILE"
  fi
fi

# --- Swarm ---
echo -e "\n${GREEN}Swarm: put and poll HEAD /chunks/{ref} on each node until R >= $REPLICAS_TARGET${NC}"
compose_file="$ROOT_DIR/docker-compose.swarm.yml"
[[ ! -f "$compose_file" ]] && compose_file="$ROOT_DIR/docker-compose.yml"
container_name="swarm-bootstrap"
docker cp "$TEMP_DIR/payload.bin" "${container_name}:/tmp/swarm_rep_$$.bin" 2>/dev/null || container_name=""
if [[ -z "$container_name" ]]; then
  if docker ps --format '{{.Names}}' | grep -q "^swarm-bootstrap$"; then
    container_name="swarm-bootstrap"
    docker cp "$TEMP_DIR/payload.bin" "${container_name}:/tmp/swarm_rep_$$.bin" 2>/dev/null || container_name=""
  fi
fi

if [[ -z "$container_name" ]]; then
  echo -e "  ${YELLOW}Swarm containers not running, skipping${NC}"
  if [[ "$RECORD_OVERHEAD" == "true" ]]; then
    echo "swarm,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,SKIP," >> "$OUTPUT_FILE"
  else
    echo "swarm,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,SKIP" >> "$OUTPUT_FILE"
  fi
else
  compose_cmd="docker-compose"
  command -v docker-compose >/dev/null 2>&1 || compose_cmd="docker compose"
  hash=$($compose_cmd -f "$compose_file" exec -T "$container_name" /app/swarm up /tmp/swarm_rep_$$.bin 2>&1 | grep -oE '[a-fA-F0-9]{64}' | head -1 || echo "")
  $compose_cmd -f "$compose_file" exec -T "$container_name" rm -f /tmp/swarm_rep_$$.bin 2>/dev/null || true

  if [[ -z "$hash" || ${#hash} -lt 32 ]]; then
    echo -e "  ${RED}Upload failed, no hash${NC}"
    echo "swarm,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,FAILED" >> "$OUTPUT_FILE"
  else
    echo "  Hash: $hash"
    swarm_containers=($(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^swarm-(bootstrap|node)' || true))
    start=$(date +%s)
    time_to_r=""
    while true; do
      now=$(date +%s)
      elapsed=$((now - start))
      if [[ $elapsed -ge $TIMEOUT_S ]]; then
        echo -e "  ${YELLOW}Timeout after ${TIMEOUT_S}s (last count: ${count:-0})${NC}"
        time_to_r="TIMEOUT"
        break
      fi
      count=0
      for c in "${swarm_containers[@]}"; do
        code=$(docker exec "$c" curl -sI -o /dev/null -w "%{http_code}" "http://localhost:8500/chunks/$hash" 2>/dev/null || echo "000")
        [[ "$code" == "200" ]] && ((count++)) || true
      done
      echo -n "  Poll: nodes_with_chunk=$count (${elapsed}s)... "
      if [[ "$count" -ge "$REPLICAS_TARGET" ]]; then
        time_to_r="$elapsed"
        echo -e "${GREEN}reached R=$REPLICAS_TARGET in ${elapsed}s${NC}"
        break
      fi
      echo ""
      sleep "$POLL_INTERVAL_S"
    done
    if [[ "$RECORD_OVERHEAD" == "true" ]]; then
      bytes_after=$(get_network_bytes_for_pattern '^swarm-' 2>/dev/null || echo "0")
      overhead=$((bytes_after - bytes_before))
      echo "swarm,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,$time_to_r,$overhead" >> "$OUTPUT_FILE"
    else
      echo "swarm,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,$time_to_r" >> "$OUTPUT_FILE"
    fi
  fi
fi

echo ""
echo "Results: $OUTPUT_FILE"
cat "$OUTPUT_FILE"
