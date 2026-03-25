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
SWARM_API="${SWARM_API:-http://127.0.0.1:8500}"
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
      echo "  --swarm-api <addr>       Swarm API (default: http://127.0.0.1:8500)"
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

bytes_before=0
[[ "$RECORD_OVERHEAD" == "true" ]] && bytes_before=$(get_network_bytes_for_pattern '^fall25-|^bootstrap$|^node[0-9]+$' 2>/dev/null || echo "0")

# --- Our system ---
# Timer starts at PUT so we measure actual replication time (not time from first poll)
echo -e "${GREEN}Our system: put and poll /replication/status until R >= $REPLICAS_TARGET${NC}"
data_b64=$(base64 -w 0 < "$TEMP_DIR/payload.bin" 2>/dev/null || base64 < "$TEMP_DIR/payload.bin" | tr -d '\n')
payload_file="$TEMP_DIR/put_req.json"
echo "{\"data\":\"$data_b64\"}" > "$payload_file"
docker cp "$payload_file" "${OUR_CONTAINER}:/tmp/put_req_rep_$$.json" >/dev/null 2>&1
start=$(date +%s.%N 2>/dev/null || date +%s)
resp=$(docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" \
  -d @/tmp/put_req_rep_$$.json "http://$OUR_API_ADDR/put" 2>/dev/null || echo "{}")
docker exec "$OUR_CONTAINER" rm -f /tmp/put_req_rep_$$.json >/dev/null 2>&1 || true

KEY=$(echo "$resp" | jq -r '.multihash_hex // .key // empty')
if [[ -z "$KEY" || "$KEY" == "null" ]]; then
  KEY=$(echo "$resp" | jq -r '.cid // empty')
  if [[ -n "$KEY" && "$KEY" != "null" ]]; then
    KEY=$(echo "$KEY" | grep -oE '[a-fA-F0-9]{64}' || echo "")
  fi
fi
# Require key: 64 hex chars (multihash_hex). ParseKey fails otherwise.
if [[ -z "$KEY" || ${#KEY} -ne 64 ]]; then
  echo -e "  ${RED}Upload failed: key must be 64 hex chars (multihash_hex). Got: len=${KEY:+${#KEY}} key=${KEY:0:20}... Response: $resp${NC}"
  echo "our_system,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,FAILED" >> "$OUTPUT_FILE"
elif ! [[ "$KEY" =~ ^[a-fA-F0-9]{64}$ ]]; then
  echo -e "  ${RED}Upload failed: key must be hex (64 chars). Got: $KEY${NC}"
  echo "our_system,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,FAILED" >> "$OUTPUT_FILE"
else
  echo "  Key: $KEY"
  # Option C: GET from worker node (cold) triggers VerifyKeyState+TriggerRepair; then poll replication
  WORKER_CONTAINER=""
  WORKER_API=""
  for c in $(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-node[0-9]+$' || true); do
    addr=$(docker exec "$c" jq -r '.addr // .Addr' /app/logs/"${c#fall25-}".json 2>/dev/null || echo "")
    if [[ -n "$addr" && "$addr" != "null" ]]; then
      WORKER_CONTAINER="$c"
      WORKER_API="http://$addr"
      break
    fi
  done
  if [[ -n "$WORKER_CONTAINER" && -n "$WORKER_API" ]]; then
    echo "  Triggering repair: GET from $WORKER_CONTAINER (cold)..."
    get_json=$(echo "{\"key\":\"$KEY\",\"timeout\":\"30s\"}" | docker exec -i "$WORKER_CONTAINER" tee /tmp/get_rep_$$.json >/dev/null 2>&1)
    docker exec "$WORKER_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" \
      -d @/tmp/get_rep_$$.json "$WORKER_API/get" >/dev/null 2>&1 || true
    docker exec "$WORKER_CONTAINER" rm -f /tmp/get_rep_$$.json 2>/dev/null || true
  else
    echo "  No worker node found for GET-triggered repair; polling bootstrap token only"
  fi
  time_to_r=""
  fast_poll=0.25
  while true; do
    now=$(date +%s.%N 2>/dev/null || date +%s)
    elapsed=$(awk "BEGIN {printf \"%.2f\", $now - $start}" 2>/dev/null || echo "$(( $(date +%s) - ${start%%.*} ))")
    [[ "$elapsed" == .* ]] && elapsed="0$elapsed"
    elapsed_int=${elapsed%%.*}
    [[ -z "$elapsed_int" ]] && elapsed_int=0
    if [[ "$elapsed_int" -ge "$TIMEOUT_S" ]]; then
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
    if [[ "${elapsed_int:-0}" -lt 15 ]]; then
      sleep "$fast_poll"
    else
      sleep "$POLL_INTERVAL_S"
    fi
  done
  overhead=""
  if [[ "$RECORD_OVERHEAD" == "true" ]]; then
    bytes_after=$(get_network_bytes_for_pattern '^fall25-|^bootstrap$|^node[0-9]+$' 2>/dev/null || echo "0")
    overhead=$((bytes_after - bytes_before))
    echo "our_system,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,$time_to_r,$overhead" >> "$OUTPUT_FILE"
  else
    echo "our_system,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,$time_to_r" >> "$OUTPUT_FILE"
  fi
  # C.3 verification: assert time_to_R reaches target or replica_count>=REPLICAS_TARGET
  if [[ "$time_to_r" == "TIMEOUT" || "$time_to_r" == "FAILED" ]]; then
    echo -e "  ${RED}C.3 verification failed: time_to_R=$time_to_r (expected numeric or target reached)${NC}" >&2
    exit 1
  fi
  if [[ "$count" -lt "$REPLICAS_TARGET" ]]; then
    echo -e "  ${RED}C.3 verification failed: replica_count=$count < target=$REPLICAS_TARGET${NC}" >&2
    exit 1
  fi
fi

# --- Swarm (skipped: Swarm v0.5.8 does not replicate chunks out-of-band; test would hang) ---
echo -e "\n${YELLOW}Swarm: skipped (no OOB replication; benchmark our system only)${NC}"
if [[ "$RECORD_OVERHEAD" == "true" ]]; then
  echo "swarm,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,SKIP," >> "$OUTPUT_FILE"
else
  echo "swarm,$PAYLOAD_SIZE,$NODE_COUNT,$REPLICAS_TARGET,SKIP" >> "$OUTPUT_FILE"
fi

echo ""
echo "Results: $OUTPUT_FILE"
cat "$OUTPUT_FILE"
