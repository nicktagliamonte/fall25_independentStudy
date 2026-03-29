#!/usr/bin/env bash
set -euo pipefail

# Purpose: Storage efficiency test for vn-IPFS vs Swarm.
# Uploads a known payload, measures disk delta across nodes, computes efficiency ratio.
# Usage: ./scripts/tests/swarm_comparison/storage_efficiency_test.sh [options]
#   --our-api <addr>       Our system API (default: auto-detect from bootstrap)
#   --swarm-api <addr>     Swarm API (default: http://127.0.0.1:8500)
#   --payload-size <n>     Payload size in bytes (default: 65536)
#   --replication-count <n> Nominal replica count for efficiency formula (default: 1)
#   --output <file>        Output CSV (default: storage_efficiency_results.csv)

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
REPLICATION_COUNT=1
OUTPUT_FILE="storage_efficiency_results.csv"

while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)      OUR_API="$2"; shift 2 ;;
    --swarm-api)    SWARM_API="$2"; shift 2 ;;
    --payload-size) PAYLOAD_SIZE="$2"; shift 2 ;;
    --replication-count) REPLICATION_COUNT="$2"; shift 2 ;;
    --output)       OUTPUT_FILE="$2"; shift 2 ;;
    --help)
      echo "Usage: $0 [options]"
      echo "  --our-api <addr>        Our system API"
      echo "  --swarm-api <addr>      Swarm API (default: http://127.0.0.1:8500)"
      echo "  --payload-size <n>      Payload bytes (default: 65536)"
      echo "  --replication-count <n> Nominal replicas for formula (default: 1)"
      echo "  --output <file>         Output CSV"
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

source "$SCRIPT_DIR/comparison_system_env.sh"
cmp_resolve_system_flags

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

OUR_CONTAINER=""
OUR_API_ADDR=""

if [[ "${CMP_INCLUDE_OUR:-1}" == "1" ]]; then
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
    echo -e "${RED}Error: Could not detect our system API. Start vn-IPFS containers.${NC}" >&2
    exit 1
  fi
  OUR_API="http://$OUR_API_ADDR"
else
if [[ "$OUR_API" =~ ^[a-zA-Z0-9_-]+$ ]]; then
  OUR_CONTAINER="$OUR_API"
  OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
  [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && OUR_API="http://$OUR_API_ADDR"
fi
fi
fi

if [[ "${CMP_INCLUDE_OUR:-1}" == "1" ]] && ([[ -z "$OUR_CONTAINER" ]] || [[ "$OUR_CONTAINER" == "bootstrap" ]]); then
  resolved=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-bootstrap$|bootstrap' | head -1)
  [[ -n "$resolved" ]] && OUR_CONTAINER="$resolved" || OUR_CONTAINER="fall25-bootstrap"
elif [[ "${CMP_INCLUDE_OUR:-1}" != "1" ]]; then
  OUR_CONTAINER=""
  OUR_API_ADDR=""
fi

TEMP_DIR=$(mktemp -d)
trap "rm -rf '$TEMP_DIR'" EXIT

generate_test_file() {
  local output="$1"
  local size="$2"
  dd if=/dev/urandom of="$output" bs=1 count="$size" 2>/dev/null
}

sum_disk_bytes() {
  local pattern="$1"
  local path="$2"
  local total=0
  for c in $(docker ps --format '{{.Names}}' 2>/dev/null | grep -E "$pattern" || true); do
    local b
    b=$(docker exec "$c" du -sb "$path" 2>/dev/null | awk '{print $1}' || echo "0")
    total=$((total + b))
  done
  echo "$total"
}

upload_our() {
  local f="$1"
  local data_b64
  data_b64=$(base64 -w 0 < "$f" 2>/dev/null || base64 < "$f" | tr -d '\n')
  local json="$TEMP_DIR/put_$$.json"
  echo "{\"data\":\"$data_b64\"}" > "$json"
  docker cp "$json" "${OUR_CONTAINER}:/tmp/put_$$.json" >/dev/null 2>&1 || return 1
  local resp
  resp=$(docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" \
    -d @/tmp/put_$$.json "http://$OUR_API_ADDR/put" 2>/dev/null || echo "{}")
  docker exec "$OUR_CONTAINER" rm -f /tmp/put_$$.json >/dev/null 2>&1 || true
  echo "$resp" | jq -r '.multihash_hex // .cid // empty' 2>/dev/null || echo ""
}

echo "Storage efficiency test: payload=$PAYLOAD_SIZE bytes, replication=$REPLICATION_COUNT"
echo "Output: $OUTPUT_FILE"
echo ""

echo "system,payload_size,nodes,disk_bytes,efficiency_ratio" > "$OUTPUT_FILE"

# --- Our system ---
if [[ "${CMP_INCLUDE_OUR:-1}" == "1" ]]; then
echo -e "${GREEN}Our system...${NC}"
OUR_CONTAINERS=($(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-' || true))
OUR_NODES=${#OUR_CONTAINERS[@]}
if [[ $OUR_NODES -eq 0 ]]; then
  OUR_CONTAINERS=($(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^bootstrap$|^node[0-9]+$' || true))
  OUR_NODES=${#OUR_CONTAINERS[@]}
fi

if [[ $OUR_NODES -gt 0 ]]; then
  test_file="$TEMP_DIR/our_test.bin"
  generate_test_file "$test_file" "$PAYLOAD_SIZE"

  disk_before=$(sum_disk_bytes '^fall25-|^bootstrap$|^node[0-9]+$' "/app/data")
  [[ -z "$disk_before" ]] && disk_before=0

  key=$(upload_our "$test_file")
  if [[ -z "$key" || "$key" == "null" ]]; then
    echo -e "${RED}Our upload failed${NC}"
    echo "our_system,$PAYLOAD_SIZE,$OUR_NODES,," >> "$OUTPUT_FILE"
  else
    sleep 2
    disk_after=$(sum_disk_bytes '^fall25-|^bootstrap$|^node[0-9]+$' "/app/data")
    [[ -z "$disk_after" ]] && disk_after=0
    disk_delta=$((disk_after - disk_before))
    [[ $disk_delta -le 0 ]] && disk_delta=1
    eff=$(echo "scale=4; ($PAYLOAD_SIZE * $REPLICATION_COUNT) / $disk_delta" | bc -l 2>/dev/null || echo "0")
    echo "our_system,$PAYLOAD_SIZE,$OUR_NODES,$disk_delta,$eff" >> "$OUTPUT_FILE"
    echo "  nodes=$OUR_NODES disk_delta=$disk_delta bytes efficiency_ratio=$eff"
  fi
else
  echo -e "${YELLOW}No our-system containers found, skipping${NC}"
  echo "our_system,$PAYLOAD_SIZE,0,," >> "$OUTPUT_FILE"
fi
fi

# --- Swarm ---
if [[ "${CMP_INCLUDE_SWARM:-1}" == "1" ]]; then
echo -e "\n${GREEN}Swarm...${NC}"
SWARM_CONTAINERS=($(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^swarm-' || true))
SWARM_NODES=${#SWARM_CONTAINERS[@]}

if [[ $SWARM_NODES -gt 0 ]]; then
  test_file="$TEMP_DIR/swarm_test.bin"
  generate_test_file "$test_file" "$PAYLOAD_SIZE"

  disk_before=$(sum_disk_bytes '^swarm-' "/app/data")
  [[ -z "$disk_before" ]] && disk_before=0

  hash=$(upload_file "$SWARM_API" "$test_file" 2>/dev/null || echo "")
  if [[ -z "$hash" || ${#hash} -lt 64 ]]; then
    echo -e "${RED}Swarm upload failed${NC}"
    echo "swarm,$PAYLOAD_SIZE,$SWARM_NODES,," >> "$OUTPUT_FILE"
  else
    sleep 2
    disk_after=$(sum_disk_bytes '^swarm-' "/app/data")
    [[ -z "$disk_after" ]] && disk_after=0
    disk_delta=$((disk_after - disk_before))
    [[ $disk_delta -le 0 ]] && disk_delta=1
    eff=$(echo "scale=4; ($PAYLOAD_SIZE * $REPLICATION_COUNT) / $disk_delta" | bc -l 2>/dev/null || echo "0")
    echo "swarm,$PAYLOAD_SIZE,$SWARM_NODES,$disk_delta,$eff" >> "$OUTPUT_FILE"
    echo "  nodes=$SWARM_NODES disk_delta=$disk_delta bytes efficiency_ratio=$eff"
  fi
else
  echo -e "${YELLOW}No Swarm containers found, skipping${NC}"
  echo "swarm,$PAYLOAD_SIZE,0,," >> "$OUTPUT_FILE"
fi
fi

echo ""
echo "Results: $OUTPUT_FILE"
cat "$OUTPUT_FILE"
