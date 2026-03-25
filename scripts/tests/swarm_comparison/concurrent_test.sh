#!/usr/bin/env bash
set -euo pipefail

# Purpose: Concurrent read/write performance - spawn N parallel uploads and M parallel downloads.
# Output: system,concurrent_writes,concurrent_reads,throughput_mbps,p99_latency_ms
# Usage: ./scripts/tests/swarm_comparison/concurrent_test.sh [options]
#   --our-api <container>      Our system container (default: auto-detect)
#   --swarm-api <addr>         Swarm API (default: http://127.0.0.1:8500)
#   --concurrent-writes <n>    Parallel uploads (default: 5)
#   --concurrent-reads <n>     Parallel downloads (default: 5)
#   --payload-size <bytes>     Payload size (default: 65536)
#   --output <file>            Output CSV (default: concurrent_results.csv)
#   --system <our_system|swarm> Test one system (default: both)
#   --append                   Append to output; do not overwrite header (for matrix runs)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
source "$SCRIPT_DIR/api.sh"

OUR_API=""
SWARM_API="${SWARM_API:-http://127.0.0.1:8500}"
CONCURRENT_WRITES=5
CONCURRENT_READS=5
PAYLOAD_SIZE=65536
OUTPUT_FILE="concurrent_results.csv"
SYSTEM_FILTER=""
APPEND=false

while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)           OUR_API="$2"; shift 2 ;;
    --swarm-api)         SWARM_API="$2"; shift 2 ;;
    --concurrent-writes) CONCURRENT_WRITES="$2"; shift 2 ;;
    --concurrent-reads)  CONCURRENT_READS="$2"; shift 2 ;;
    --payload-size)      PAYLOAD_SIZE="$2"; shift 2 ;;
    --output)            OUTPUT_FILE="$2"; shift 2 ;;
    --system)            SYSTEM_FILTER="$2"; shift 2 ;;
    --append)            APPEND=true; shift ;;
    --help)
      echo "Usage: $0 [--our-api <c>] [--swarm-api <addr>] [--concurrent-writes N] [--concurrent-reads M] [--payload-size <n>] [--output <file>] [--system our_system|swarm]"
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

TEMP_DIR=$(mktemp -d)
trap "rm -rf '$TEMP_DIR'" EXIT

put_our_system() {
  local data_b64="$1"
  local out="$2"
  local id
  id=$(basename "$out")
  local json="$TEMP_DIR/put_$$_$id.json"
  local cpath="/tmp/put_conc_$$_$id.json"
  echo "{\"data\":\"$data_b64\"}" > "$json"
  docker cp "$json" "${OUR_CONTAINER}:$cpath" 2>/dev/null || { echo "999999" > "$out"; return; }
  local start end resp latency
  start=$(date +%s.%N)
  resp=$(docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" -d @"$cpath" "http://$OUR_API_ADDR/put" 2>/dev/null || echo "{}")
  end=$(date +%s.%N)
  docker exec "$OUR_CONTAINER" rm -f "$cpath" 2>/dev/null || true
  latency=$(echo "scale=2; ($end - $start) * 1000" | bc -l 2>/dev/null || echo "999999")
  echo "$latency" > "$out"
}

get_our_system() {
  local key="$1"
  local out="$2"
  local id
  id=$(basename "$out")
  local json="$TEMP_DIR/get_$$_$id.json"
  local cpath="/tmp/get_conc_$$_$id.json"
  echo "{\"key\":\"$key\",\"timeout\":\"30s\"}" > "$json"
  docker cp "$json" "${OUR_CONTAINER}:$cpath" 2>/dev/null || { echo "999999" > "$out"; return; }
  local start end latency
  start=$(date +%s.%N)
  docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" -d @"$cpath" "http://$OUR_API_ADDR/get" >/dev/null 2>&1 || true
  end=$(date +%s.%N)
  docker exec "$OUR_CONTAINER" rm -f "$cpath" 2>/dev/null || true
  latency=$(echo "scale=2; ($end - $start) * 1000" | bc -l 2>/dev/null || echo "999999")
  echo "$latency" > "$out"
}

run_concurrent_our_system() {
  local nw="$1"
  local nr="$2"
  local seed_file="$TEMP_DIR/seed.bin"
  dd if=/dev/urandom of="$seed_file" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
  data_b64=$(base64 -w 0 < "$seed_file" 2>/dev/null || base64 < "$seed_file" | tr -d '\n')

  json="$TEMP_DIR/seed_put.json"
  echo "{\"data\":\"$data_b64\"}" > "$json"
  docker cp "$json" "${OUR_CONTAINER}:/tmp/seed_put.json" 2>/dev/null
  seed_resp=$(docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" -d @/tmp/seed_put.json "http://$OUR_API_ADDR/put" 2>/dev/null || echo "{}")
  SEED_KEY=$(echo "$seed_resp" | jq -r '.multihash_hex // .cid // empty')
  if [[ -z "$SEED_KEY" || "$SEED_KEY" == "null" ]]; then
    cid=$(echo "$seed_resp" | jq -r '.cid // empty')
    SEED_KEY=$(echo "$cid" | grep -oE '[a-fA-F0-9]{64}' | head -1)
  fi
  if [[ -z "$SEED_KEY" || ${#SEED_KEY} -lt 32 ]]; then
    echo -e "${RED}Seed put failed${NC}" >&2
    return 1
  fi

  latencies_dir="$TEMP_DIR/lat_$$"
  mkdir -p "$latencies_dir"
  start_wall=$(date +%s.%N)
  total_bytes=0

  for i in $(seq 1 "$nw"); do
    f="$TEMP_DIR/wr_$i.bin"
    dd if=/dev/urandom of="$f" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
    b64=$(base64 -w 0 < "$f" 2>/dev/null || base64 < "$f" | tr -d '\n')
    put_our_system "$b64" "$latencies_dir/w_$i" &
    total_bytes=$((total_bytes + PAYLOAD_SIZE))
  done

  for i in $(seq 1 "$nr"); do
    get_our_system "$SEED_KEY" "$latencies_dir/r_$i" &
    total_bytes=$((total_bytes + PAYLOAD_SIZE))
  done

  wait
  end_wall=$(date +%s.%N)
  wall_s=$(echo "scale=4; $end_wall - $start_wall" | bc -l)

  all_lat=()
  for f in "$latencies_dir"/w_* "$latencies_dir"/r_*; do
    [[ -f "$f" ]] && all_lat+=("$(cat "$f")")
  done
  p99=""
  if [[ ${#all_lat[@]} -gt 0 ]]; then
    sorted=($(printf '%s\n' "${all_lat[@]}" | sort -n))
    p99_idx=$(( ${#sorted[@]} * 99 / 100 ))
    [[ $p99_idx -ge ${#sorted[@]} ]] && p99_idx=$(( ${#sorted[@]} - 1 ))
    p99="${sorted[$p99_idx]}"
  fi
  throughput=0
  [[ "$wall_s" != "0" ]] && throughput=$(echo "scale=2; ($total_bytes / 1048576) / $wall_s" | bc -l)
  echo "$throughput|${p99:-N/A}"
}

run_concurrent_swarm() {
  local nw="$1"
  local nr="$2"
  seed_file="$TEMP_DIR/seed.bin"
  dd if=/dev/urandom of="$seed_file" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
  SEED_HASH=$(upload_file "$SWARM_API" "$seed_file" 2>/dev/null || echo "")
  if [[ -z "$SEED_HASH" || ${#SEED_HASH} -lt 64 ]]; then
    echo -e "${RED}Swarm seed upload failed${NC}" >&2
    return 1
  fi

  latencies_dir="$TEMP_DIR/lat_$$"
  mkdir -p "$latencies_dir"
  start_wall=$(date +%s.%N)
  total_bytes=0

  for i in $(seq 1 "$nw"); do
    f="$TEMP_DIR/wr_$i.bin"
    dd if=/dev/urandom of="$f" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
    ( start=$(date +%s.%N); upload_file "$SWARM_API" "$f" >/dev/null 2>&1; end=$(date +%s.%N); echo "scale=2; ($end - $start) * 1000" | bc -l > "$latencies_dir/w_$i" ) &
    total_bytes=$((total_bytes + PAYLOAD_SIZE))
  done

  for i in $(seq 1 "$nr"); do
    out="$TEMP_DIR/rd_$i.bin"
    ( start=$(date +%s.%N); download_file "$SWARM_API" "$SEED_HASH" "$out" 2>/dev/null; end=$(date +%s.%N); echo "scale=2; ($end - $start) * 1000" | bc -l > "$latencies_dir/r_$i" ) &
    total_bytes=$((total_bytes + PAYLOAD_SIZE))
  done

  wait
  end_wall=$(date +%s.%N)
  wall_s=$(echo "scale=4; $end_wall - $start_wall" | bc -l)

  all_lat=()
  for f in "$latencies_dir"/w_* "$latencies_dir"/r_*; do
    [[ -f "$f" ]] && all_lat+=("$(cat "$f" 2>/dev/null)")
  done
  p99=""
  if [[ ${#all_lat[@]} -gt 0 ]]; then
    sorted=($(printf '%s\n' "${all_lat[@]}" | sort -n))
    p99_idx=$(( ${#sorted[@]} * 99 / 100 ))
    [[ $p99_idx -ge ${#sorted[@]} ]] && p99_idx=$(( ${#sorted[@]} - 1 ))
    p99="${sorted[$p99_idx]}"
  fi
  throughput=0
  [[ "$wall_s" != "0" ]] && throughput=$(echo "scale=2; ($total_bytes / 1048576) / $wall_s" | bc -l)
  echo "$throughput|${p99:-N/A}"
}

if [[ "$APPEND" != "true" ]] || [[ ! -f "$OUTPUT_FILE" ]] || [[ ! -s "$OUTPUT_FILE" ]]; then
  echo "system,concurrent_writes,concurrent_reads,throughput_mbps,p99_latency_ms" > "$OUTPUT_FILE"
fi
echo -e "${BLUE}Concurrent Read/Write Test${NC}"
echo "  Concurrent writes: $CONCURRENT_WRITES, reads: $CONCURRENT_READS"
echo "  Payload size: $PAYLOAD_SIZE bytes"
echo ""

if [[ -z "$SYSTEM_FILTER" || "$SYSTEM_FILTER" == "our_system" ]]; then
  echo -e "${GREEN}Our system ($CONCURRENT_WRITES w / $CONCURRENT_READS r)...${NC}"
  result=$(run_concurrent_our_system "$CONCURRENT_WRITES" "$CONCURRENT_READS" 2>/dev/null || echo "0|N/A")
  thr=$(echo "$result" | cut -d'|' -f1)
  p99=$(echo "$result" | cut -d'|' -f2)
  echo "  throughput=$thr MB/s, p99=$p99 ms"
  echo "our_system,$CONCURRENT_WRITES,$CONCURRENT_READS,$thr,$p99" >> "$OUTPUT_FILE"
fi

if [[ -z "$SYSTEM_FILTER" || "$SYSTEM_FILTER" == "swarm" ]]; then
  echo -e "${GREEN}Swarm ($CONCURRENT_WRITES w / $CONCURRENT_READS r)...${NC}"
  result=$(run_concurrent_swarm "$CONCURRENT_WRITES" "$CONCURRENT_READS" 2>/dev/null || echo "0|N/A")
  thr=$(echo "$result" | cut -d'|' -f1)
  p99=$(echo "$result" | cut -d'|' -f2)
  echo "  throughput=$thr MB/s, p99=$p99 ms"
  echo "swarm,$CONCURRENT_WRITES,$CONCURRENT_READS,$thr,$p99" >> "$OUTPUT_FILE"
fi

echo ""
echo "Results: $OUTPUT_FILE"
cat "$OUTPUT_FILE"
