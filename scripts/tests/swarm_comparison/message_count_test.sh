#!/usr/bin/env bash
set -euo pipefail

# Purpose: Message count test for vn-IPFS vs Swarm comparison.
# Performs upload/download, queries message counts from both systems, writes CSV:
#   system,operation,message_count
# Usage: ./scripts/tests/swarm_comparison/message_count_test.sh [options]
#   --our-api <addr>     Our system API (default: auto-detect bootstrap)
#   --swarm-api <addr>   Swarm API (default: http://172.20.0.200:8500)
#   --output <file>      Output CSV (default: message_counts.csv)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "$SCRIPT_DIR/api.sh"
source "$ROOT_DIR/scripts/utils/error_handler.sh" 2>/dev/null || true

OUR_API=""
SWARM_API="http://172.20.0.200:8500"
OUTPUT_FILE="message_counts.csv"
PAYLOAD_SIZE=10240

while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)   OUR_API="$2";   shift 2 ;;
    --swarm-api) SWARM_API="$2"; shift 2 ;;
    --output)    OUTPUT_FILE="$2"; shift 2 ;;
    --help)
      echo "Usage: $0 [options]"
      echo "  --our-api <addr>   Our system API (default: auto-detect)"
      echo "  --swarm-api <addr> Swarm API (default: http://172.20.0.200:8500)"
      echo "  --output <file>    Output CSV (default: message_counts.csv)"
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
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
      [[ ! -f "$compose" ]] && continue
      if docker-compose -f "$compose" ps bootstrap 2>/dev/null | grep -q "Up"; then
        OUR_CONTAINER="bootstrap"
        OUR_API_ADDR=$(docker-compose -f "$compose" exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
        [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && break
      fi
    done
  fi
  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
    echo -e "${RED}Error: Could not detect our system API. Specify --our-api.${NC}" >&2
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

echo "Message count test: our=$OUR_API swarm=$SWARM_API"

TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

generate_test_file() {
  dd if=/dev/urandom of="$1" bs=1 count="$2" 2>/dev/null
}

# Get our system metrics (JSON)
get_our_metrics() {
  if [[ -n "$OUR_CONTAINER" && -n "$OUR_API_ADDR" ]]; then
    docker exec "$OUR_CONTAINER" curl -sSf "http://$OUR_API_ADDR/metrics" 2>/dev/null || echo "{}"
  else
    curl -sSf "$OUR_API/metrics" 2>/dev/null || echo "{}"
  fi
}

our_put_total() {
  local j="$1"
  echo "$j" | jq -r '((.["put_messages_in"]//0)+(.["put_messages_out"]//0)+(.["lookup_messages_in"]//0)+(.["lookup_messages_out"]//0)) | floor' 2>/dev/null || echo "0"
}

our_get_total() {
  local j="$1"
  echo "$j" | jq -r '((.["get_messages_in"]//0)+(.["get_messages_out"]//0)+(.["lookup_messages_in"]//0)+(.["lookup_messages_out"]//0)) | floor' 2>/dev/null || echo "0"
}

# Upload to our system, return CID
upload_our() {
  local f="$1"
  local data_b64=$(base64 -w 0 < "$f" 2>/dev/null || base64 < "$f" | tr -d '\n')
  local json="$TEMP_DIR/put_$$.json"
  echo "{\"data\":\"$data_b64\"}" > "$json"
  docker cp "$json" "${OUR_CONTAINER}:/tmp/put_$$.json" >/dev/null 2>&1
  local resp=$(docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" -d @/tmp/put_$$.json "http://$OUR_API_ADDR/put" 2>/dev/null || echo "{}")
  docker exec "$OUR_CONTAINER" rm -f /tmp/put_$$.json >/dev/null 2>&1 || true
  echo "$resp" | jq -r '.cid // .key // empty'
}

# Provider info for /get provider hint
# Our /put returns {"cid":"...", "multihash_hex":"..."}. For /get we need key or cid.
# From docs: key is primary. Put returns cid. Get accepts key or cid. If we only have cid,
# we need from_peer and from_addr for provider hint. Download_test gets provider from /id.
get_provider_info() {
  local id_json=$(docker exec "$OUR_CONTAINER" curl -sSf "http://$OUR_API_ADDR/id" 2>/dev/null || echo "{}")
  local peer=$(echo "$id_json" | jq -r '.peer // empty')
  local addr=$(echo "$id_json" | jq -r '.addrs[0] // empty')
  echo "${peer}|${addr}"
}

# Swarm: try Prometheus /metrics and sum relevant counters, else N/A
get_swarm_metrics_raw() {
  curl -sSf -m 5 "$SWARM_API/metrics" 2>/dev/null || echo ""
}

# Metric name patterns: Bee (newer) and legacy Swarm v0.5.8 (provider announcements, retrieval)
# Legacy ethersphere/swarm may use swarm_*, chunk_*, retrieval_*, provider_*, announce_*
swarm_parse_prometheus_delta() {
  local before="$1"
  local after="$2"
  if [[ -z "$before" || -z "$after" ]]; then
    echo "N/A"
    return
  fi
  local sum_before=0 sum_after=0
  local patterns="bee_swap_cheques_received bee_swap_cheques_sent bee_retrieval bee_chunk"
  patterns="$patterns swarm_chunk swarm_retrieval swarm_provider swarm_announce chunk_delivery retrieval_request"
  patterns="$patterns provider_announce retrieval"
  for pat in $patterns; do
    local v_b v_a
    v_b=$(echo "$before" | grep -E "^${pat}[_{a-zA-Z0-9]*" | awk '{gsub(/[^0-9.eE+-]/,"",$NF); sum+=$NF+0} END {printf "%.0f", sum+0}')
    v_a=$(echo "$after"  | grep -E "^${pat}[_{a-zA-Z0-9]*" | awk '{gsub(/[^0-9.eE+-]/,"",$NF); sum+=$NF+0} END {printf "%.0f", sum+0}')
    sum_before=$((sum_before + ${v_b:-0}))
    sum_after=$((sum_after + ${v_a:-0}))
  done
  if [[ $sum_after -gt $sum_before ]]; then
    echo $((sum_after - sum_before))
  else
    echo "N/A"
  fi
}

test_file="$TEMP_DIR/test.bin"
generate_test_file "$test_file" "$PAYLOAD_SIZE"

echo "system,operation,message_count" > "$OUTPUT_FILE"

# --- Our system ---
echo -e "\n${GREEN}Our system...${NC}"

m0=$(get_our_metrics)
our_put_0=$(our_put_total "$m0")
our_put_0=${our_put_0:-0}

OUR_CID=$(upload_our "$test_file")
if [[ -z "$OUR_CID" ]]; then
  echo -e "${RED}Our upload failed${NC}"
  echo "our_system,put,FAILED" >> "$OUTPUT_FILE"
  echo "our_system,get,FAILED" >> "$OUTPUT_FILE"
else
  m1=$(get_our_metrics)
  p1=$(our_put_total "$m1")
  put_delta=$((p1 - our_put_0))
  echo "our_system,put,$put_delta" >> "$OUTPUT_FILE"

  PROVIDER=$(get_provider_info)
  PEER=$(echo "$PROVIDER" | cut -d'|' -f1)
  ADDR=$(echo "$PROVIDER" | cut -d'|' -f2)
  get_req="$TEMP_DIR/get_req.json"
  if [[ -n "$PEER" && -n "$ADDR" ]]; then
    echo "{\"cid\":\"$OUR_CID\",\"from_peer\":\"$PEER\",\"from_addr\":\"$ADDR\",\"timeout\":\"30s\"}" > "$get_req"
  else
    echo "{\"key\":\"$OUR_CID\",\"timeout\":\"30s\"}" > "$get_req"
  fi
  docker cp "$get_req" "${OUR_CONTAINER}:/tmp/get_req.json" >/dev/null 2>&1
  docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" -d @/tmp/get_req.json "http://$OUR_API_ADDR/get" >/dev/null 2>&1 || true

  m2=$(get_our_metrics)
  g1=$(our_get_total "$m1")
  g2=$(our_get_total "$m2")
  get_delta=$((g2 - g1))
  echo "our_system,get,$get_delta" >> "$OUTPUT_FILE"
  echo "  put messages: $put_delta, get messages: $get_delta"
fi

# --- Swarm ---
echo -e "\n${GREEN}Swarm...${NC}"

swarm_before=$(get_swarm_metrics_raw)
SWARM_HASH=$(upload_file "$SWARM_API" "$test_file" 2>/dev/null || echo "")
swarm_after_put=$(get_swarm_metrics_raw)

if [[ -n "$SWARM_HASH" && ${#SWARM_HASH} -ge 64 ]]; then
  curl -sSfL -o /dev/null "$SWARM_API/bzz:/$SWARM_HASH/" 2>/dev/null || true
fi
swarm_after_get=$(get_swarm_metrics_raw)

if [[ -n "$swarm_before" && -n "$swarm_after_put" ]]; then
  put_swarm=$(swarm_parse_prometheus_delta "$swarm_before" "$swarm_after_put")
  get_swarm=$(swarm_parse_prometheus_delta "$swarm_after_put" "$swarm_after_get")
  echo "swarm,put,$put_swarm" >> "$OUTPUT_FILE"
  echo "swarm,get,$get_swarm" >> "$OUTPUT_FILE"
  echo "  put messages: $put_swarm, get messages: $get_swarm"
else
  echo "swarm,put,N/A" >> "$OUTPUT_FILE"
  echo "swarm,get,N/A" >> "$OUTPUT_FILE"
  echo -e "  ${YELLOW}Swarm /metrics not available (v0.5.8 may not expose Prometheus metrics)${NC}"
fi

echo ""
echo "Results: $OUTPUT_FILE"
cat "$OUTPUT_FILE"
