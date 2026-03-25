#!/usr/bin/env bash
set -euo pipefail

# Purpose: Download latency test comparing our system vs Swarm v0.5.8
# Usage: ./scripts/tests/swarm_comparison/download_test.sh [options]
#   --our-api <container> Our system container name (default: auto-detect bootstrap)
#   --swarm-api <addr>   Swarm API address (default: http://127.0.0.1:8500)
#   --iterations <n>     Number of iterations per size (default: 10)
#   --cache-mode <mode>  cold|warm (default: cold)
#   --output <file>      Output CSV file (default: download_latency_results.csv)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# Source Swarm API functions
source "$SCRIPT_DIR/api.sh"

# Default values
OUR_API=""
SWARM_API="${SWARM_API:-http://127.0.0.1:8500}"
ITERATIONS=10
CACHE_MODE="cold"
OUTPUT_FILE="download_latency_results.csv"
PAYLOAD_SIZES=(1024 10240 102400 1048576)  # 1KB, 10KB, 100KB, 1MB

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)
      OUR_API="$2"
      shift 2
      ;;
    --swarm-api)
      SWARM_API="$2"
      shift 2
      ;;
    --iterations)
      ITERATIONS="$2"
      shift 2
      ;;
    --cache-mode)
      CACHE_MODE="$2"
      shift 2
      ;;
    --output)
      OUTPUT_FILE="$2"
      shift 2
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --our-api <container> Our system container name (default: auto-detect)"
      echo "  --swarm-api <addr>   Swarm API address (default: http://127.0.0.1:8500)"
      echo "  --iterations <n>     Iterations per size (default: 10)"
      echo "  --cache-mode <mode>  cold|warm (default: cold)"
      echo "  --output <file>      Output CSV file (default: download_latency_results.csv)"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Check for required tools
if ! command -v bc >/dev/null 2>&1; then
  echo "Error: 'bc' command not found. Please install it." >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "Error: 'jq' command not found. Please install it." >&2
  exit 1
fi

# Determine our system API address and container
OUR_CONTAINER=""
OUR_API_ADDR=""

if [[ -z "$OUR_API" ]]; then
  # Try to find bootstrap container and read control address
  if docker ps --format '{{.Names}}' | grep -q "^fall25-bootstrap$"; then
    OUR_CONTAINER="fall25-bootstrap"
    BOOTSTRAP_CTRL="/app/logs/bootstrap.json"
    
    # Read control address from container
    OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' "$BOOTSTRAP_CTRL" 2>/dev/null || echo "")
    
    if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
      OUR_API="http://$OUR_API_ADDR"
      echo "Detected control API: $OUR_API (container: $OUR_CONTAINER)"
    fi
  fi
  
  # Try docker-compose if direct docker didn't work
  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
    for compose in "$ROOT_DIR/docker-compose.vnipfs.yml" "$ROOT_DIR/docker-compose.yml"; do
      if [[ ! -f "$compose" ]] || ! command -v docker-compose >/dev/null 2>&1; then continue; fi
      if docker-compose -f "$compose" ps bootstrap 2>/dev/null | grep -q "Up"; then
        OUR_CONTAINER="bootstrap"
        BOOTSTRAP_CTRL="/app/logs/bootstrap.json"
        OUR_API_ADDR=$(docker-compose -f "$compose" exec -T bootstrap jq -r '.addr // .Addr' "$BOOTSTRAP_CTRL" 2>/dev/null || echo "")
        if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
          OUR_API="http://$OUR_API_ADDR"
          echo "Detected control API: $OUR_API (via $(basename "$compose"))"
          break
        fi
      fi
    done
  fi
  
  # Final fallback
  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
    echo -e "${RED}Error: Could not detect our system API address.${NC}" >&2
    echo "  Make sure the bootstrap container is running:" >&2
    echo "    docker ps | grep bootstrap" >&2
    echo "  Or specify manually with --our-api <container_name>" >&2
    exit 1
  fi
else
  # If API was provided, try to extract container name
  if [[ "$OUR_API" =~ ^[a-zA-Z0-9_-]+$ ]]; then
    OUR_CONTAINER="$OUR_API"
    OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
    if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
      OUR_API="http://$OUR_API_ADDR"
    else
      echo -e "${RED}Error: Could not read control address from container: $OUR_CONTAINER${NC}" >&2
      exit 1
    fi
  fi
fi

echo "=========================================="
echo "Download Latency Test"
echo "=========================================="
echo "Our System API: $OUR_API (container: $OUR_CONTAINER)"
echo "Swarm API: $SWARM_API"
echo "Iterations per size: $ITERATIONS"
echo "Cache mode: $CACHE_MODE"
echo "Output file: $OUTPUT_FILE"
echo ""

# Verify API endpoints are accessible
echo "Verifying API endpoints..."

# Check our system API (via docker exec if needed)
if [[ -n "$OUR_CONTAINER" ]]; then
  if docker exec "$OUR_CONTAINER" curl -sSf -m 5 "http://$OUR_API_ADDR/health" >/dev/null 2>&1; then
    echo -e "${GREEN}✓ Our system API is accessible${NC}"
  else
    echo -e "${YELLOW}Warning: Our system API may not be accessible${NC}"
  fi
else
  if curl -sSf -m 5 "$OUR_API/health" >/dev/null 2>&1; then
    echo -e "${GREEN}✓ Our system API is accessible${NC}"
  else
    echo -e "${YELLOW}Warning: Our system API ($OUR_API) may not be accessible${NC}"
  fi
fi

# Check Swarm API
if curl -sSf -m 5 "$SWARM_API/" >/dev/null 2>&1; then
  echo -e "${GREEN}✓ Swarm API is accessible${NC}"
else
  echo -e "${YELLOW}Warning: Swarm API ($SWARM_API) may not be accessible${NC}"
fi

# Create temp directory for test files
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

# Initialize CSV output
echo "system,payload_size,iteration,cache_mode,ttfb_ms,total_ms,lookup_type" > "$OUTPUT_FILE"

# Function to generate test file of specified size
generate_test_file() {
  local size=$1
  local output="$2"
  
  # Generate random data
  dd if=/dev/urandom of="$output" bs=1 count="$size" 2>/dev/null
}

# Function to upload to our system and return key (multihash_hex) for token-based GET.
# Key is primary; CID fallback only when key unavailable.
upload_our_system_get_key() {
  local file_path="$1"
  
  if [[ -z "$OUR_CONTAINER" || -z "$OUR_API_ADDR" ]]; then
    echo "ERROR: Container or API address not set" >&2
    return 1
  fi
  
  local data_b64=$(base64 -w 0 < "$file_path" 2>/dev/null || base64 < "$file_path" | tr -d '\n')
  local api_url="http://$OUR_API_ADDR/put"
  local json_payload="$TEMP_DIR/put_payload_$$.json"
  echo "{\"data\":\"$data_b64\"}" > "$json_payload"
  
  docker cp "$json_payload" "${OUR_CONTAINER}:/tmp/put_payload_$$.json" >/dev/null 2>&1
  local response=$(docker exec "$OUR_CONTAINER" curl -sSf -m 60 -X POST \
    -H "Content-Type: application/json" \
    -d @/tmp/put_payload_$$.json \
    "$api_url" 2>&1)
  docker exec "$OUR_CONTAINER" rm -f "/tmp/put_payload_$$.json" >/dev/null 2>&1 || true
  rm -f "$json_payload"
  
  local key=$(echo "$response" | jq -r '.multihash_hex // empty' 2>/dev/null || echo "")
  if [[ -z "$key" || "$key" == "null" || ${#key} -ne 64 ]]; then
    echo "ERROR: Failed to upload or get key (multihash_hex). Response: $response" >&2
    return 1
  fi
  echo "$key"
}

# Function to get provider info (peer ID and address) for our system
get_provider_info() {
  # Get node's own peer ID and address
  local id_json=$(docker exec "$OUR_CONTAINER" curl -sSf "http://$OUR_API_ADDR/id" 2>/dev/null || echo "{}")
  local peer_id=$(echo "$id_json" | jq -r '.peer' 2>/dev/null || echo "")
  local addrs=$(echo "$id_json" | jq -r '.addrs[0]' 2>/dev/null || echo "")
  
  if [[ -z "$peer_id" || "$peer_id" == "null" || -z "$addrs" || "$addrs" == "null" ]]; then
    echo "ERROR: Could not get provider info" >&2
    return 1
  fi
  
  echo "$peer_id|$addrs"
}

# Function to download from our system and measure latency
# Uses GET_CONTAINER/GET_API_ADDR (cold: different node; warm: same as upload)
download_our_system() {
  local key="$1"
  local peer_id="$2"
  local addr="$3"
  local output_path="$4"
  
  if [[ -z "$GET_CONTAINER" || -z "$GET_API_ADDR" ]]; then
    echo "ERROR: Getter container or API address not set" >&2
    return 1
  fi
  
  local get_req="$TEMP_DIR/get_req_$$.json"
  echo "{\"key\":\"$key\",\"timeout\":\"30s\"}" > "$get_req"
  
  # Measure download time
  local start_time=$(date +%s.%N)
  
  # Copy request to getter container and execute curl inside it
  docker cp "$get_req" "${GET_CONTAINER}:/tmp/get_req_$$.json" >/dev/null 2>&1
  
  local curl_output=$(docker exec "$GET_CONTAINER" curl -sSf -m 35 -w "\n%{time_starttransfer}\n%{time_total}" -X POST \
    -H "Content-Type: application/json" \
    -d @/tmp/get_req_$$.json \
    "http://$GET_API_ADDR/get" 2>&1)
  
  docker exec "$GET_CONTAINER" rm -f "/tmp/get_req_$$.json" >/dev/null 2>&1 || true
  rm -f "$get_req"
  
  local end_time=$(date +%s.%N)
  
  # Parse curl output (response body, then time_starttransfer, then time_total)
  local response_body=$(echo "$curl_output" | head -n -2)
  local ttfb_curl=$(echo "$curl_output" | tail -n 2 | head -n 1)
  local total_curl=$(echo "$curl_output" | tail -n 1)
  
  # Calculate times in milliseconds
  local ttfb_ms=$(echo "scale=2; $ttfb_curl * 1000" | bc -l)
  local total_ms=$(echo "scale=2; $total_curl * 1000" | bc -l)
  
  # Check if download succeeded
  local data_b64=$(echo "$response_body" | jq -r '.data_b64' 2>/dev/null || echo "")
  
  if [[ -n "$data_b64" && "$data_b64" != "null" ]]; then
    local hops=$(echo "$response_body" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
    echo "$data_b64" | base64 -d > "$output_path" 2>/dev/null || echo "$data_b64" | base64 -D > "$output_path" 2>/dev/null
    if [[ -n "$hops" && "$hops" != "null" ]]; then
      echo "$ttfb_ms|$total_ms|$hops"
    else
      echo "$ttfb_ms|$total_ms|"
    fi
    return 0
  else
    echo "ERROR: Failed to download. Response: $response_body" >&2
    return 1
  fi
}

# Function to download from Swarm and measure latency
download_swarm() {
  local hash="$1"
  local output_path="$2"
  
  # Measure download time with curl write-out format
  # time_starttransfer = TTFB, time_total = total time
  local curl_output=$(curl -sSfL -w "\n%{time_starttransfer}\n%{time_total}" \
    -o "$output_path" \
    "$SWARM_API/bzz:/$hash/" 2>&1)
  
  # Parse curl output
  local ttfb_curl=$(echo "$curl_output" | tail -n 2 | head -n 1)
  local total_curl=$(echo "$curl_output" | tail -n 1)
  
  # Calculate times in milliseconds
  local ttfb_ms=$(echo "scale=2; $ttfb_curl * 1000" | bc -l)
  local total_ms=$(echo "scale=2; $total_curl * 1000" | bc -l)
  
  # Check if download succeeded
  if [[ -f "$output_path" && -s "$output_path" ]]; then
    # Check if it's not an HTML redirect page
    if ! grep -q "<a href=" "$output_path" 2>/dev/null; then
      echo "$ttfb_ms|$total_ms"
      return 0
    fi
  fi
  
  echo "ERROR: Failed to download hash $hash" >&2
  return 1
}

# Function to calculate statistics
calculate_stats() {
  local values=("$@")
  local count=${#values[@]}
  
  if [[ $count -eq 0 ]]; then
    echo "0,0,0,0,0,0"
    return
  fi
  
  # Sort values
  IFS=$'\n' sorted=($(sort -n <<< "${values[*]}"))
  unset IFS
  
  # Calculate percentiles
  local min=${sorted[0]}
  local max=${sorted[$((count - 1))]}
  
  # Average
  local sum=0
  for val in "${sorted[@]}"; do
    sum=$(echo "$sum + $val" | bc -l)
  done
  local avg=$(echo "scale=2; $sum / $count" | bc -l)
  
  # Percentiles
  local p50_idx=$((count * 50 / 100))
  local p90_idx=$((count * 90 / 100))
  local p99_idx=$((count * 99 / 100))
  
  local p50=${sorted[$p50_idx]}
  local p90=${sorted[$p90_idx]}
  local p99=${sorted[$p99_idx]}
  
  printf "%.2f,%.2f,%.2f,%.2f,%.2f,%.2f" "$min" "$max" "$avg" "$p50" "$p90" "$p99"
}

# Resolve getter container for cold vs warm
GET_CONTAINER="$OUR_CONTAINER"
GET_API_ADDR="$OUR_API_ADDR"
if [[ "$CACHE_MODE" == "cold" ]]; then
  for c in $(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-node' | head -5); do
    if [[ "$c" != "$OUR_CONTAINER" ]]; then
      ctrl_path="/app/logs/$(echo "$c" | sed 's/^fall25-//').json"
      addr=$(docker exec "$c" jq -r '.addr // .Addr' "$ctrl_path" 2>/dev/null || echo "")
      if [[ -n "$addr" && "$addr" != "null" ]]; then
        GET_CONTAINER="$c"
        GET_API_ADDR="$addr"
        echo "Cold mode: using $GET_CONTAINER for get (content not in local cache)"
        break
      fi
    fi
  done
  if [[ "$GET_CONTAINER" == "$OUR_CONTAINER" ]]; then
    echo -e "${YELLOW}Cold mode: no other node found, falling back to upload node (may be warm)${NC}"
  fi
fi

# Get provider info for our system (needed for /get requests)
echo "Getting provider info for our system..."
PROVIDER_INFO=$(get_provider_info)
if [[ $? -ne 0 ]]; then
  echo -e "${RED}Error: Could not get provider info${NC}" >&2
  exit 1
fi

PROVIDER_PEER_ID=$(echo "$PROVIDER_INFO" | cut -d'|' -f1)
PROVIDER_ADDR=$(echo "$PROVIDER_INFO" | cut -d'|' -f2)
echo "  Peer ID: $PROVIDER_PEER_ID"
echo "  Address: $PROVIDER_ADDR"
echo ""

# Test each payload size
for size in "${PAYLOAD_SIZES[@]}"; do
  size_kb=$((size / 1024))
  if [[ $size_kb -ge 1024 ]]; then
    size_label="${size_kb}KB"
  else
    size_label="${size_kb}KB"
  fi
  
  echo -e "\n${BLUE}Testing payload size: $size_label ($size bytes)${NC}"
  
  # Generate test file
  test_file="$TEMP_DIR/test_${size}.bin"
  echo "  Generating test file..."
  generate_test_file "$size" "$test_file"
  
  # Upload to both systems first
  echo -e "\n  ${GREEN}Uploading to both systems...${NC}"
  
  # Upload to our system
  echo "    Uploading to our system..."
  OUR_KEY=$(upload_our_system_get_key "$test_file" 2>&1)
  if [[ $? -ne 0 || -z "$OUR_KEY" ]]; then
    echo -e "    ${RED}Failed to upload to our system${NC}"
    OUR_KEY=""
  else
    echo "    Uploaded key: ${OUR_KEY:0:16}..."
  fi
  
  # Upload to Swarm
  echo "    Uploading to Swarm..."
  SWARM_HASH=$(upload_file "$SWARM_API" "$test_file" 2>&1)
  if [[ $? -ne 0 || -z "$SWARM_HASH" || "$SWARM_HASH" == "ERROR"* ]]; then
    echo -e "    ${RED}Failed to upload to Swarm${NC}"
    SWARM_HASH=""
  else
    echo "    Uploaded hash: $SWARM_HASH"
  fi
  
  if [[ -z "$OUR_KEY" && -z "$SWARM_HASH" ]]; then
    echo -e "    ${RED}Both uploads failed, skipping this size${NC}"
    continue
  fi
  
  # Wait a moment for content to propagate
  sleep 2
  
  # Test our system downloads
  echo -e "\n  ${GREEN}Testing our system downloads...${NC}"
  our_ttfb=()
  our_total=()
  our_failures=0
  
  if [[ -n "$OUR_KEY" ]]; then
    for i in $(seq 1 $ITERATIONS); do
      echo -n "    Iteration $i/$ITERATIONS... "
      output_file="$TEMP_DIR/our_download_${size}_${i}.bin"
      if [[ "$CACHE_MODE" == "warm" ]]; then
        download_our_system "$OUR_KEY" "$PROVIDER_PEER_ID" "$PROVIDER_ADDR" "$TEMP_DIR/our_prime_$$.bin" >/dev/null 2>&1 || true
      fi
      result=$(download_our_system "$OUR_KEY" "$PROVIDER_PEER_ID" "$PROVIDER_ADDR" "$output_file" 2>&1)
      if [[ $? -eq 0 ]]; then
        IFS='|' read -r ttfb total hops <<< "$result"
        hops=${hops:-}
        our_ttfb+=("$ttfb")
        our_total+=("$total")
        echo "TTFB: $(printf "%.2f" "$ttfb") ms, Total: $(printf "%.2f" "$total") ms${hops:+ hops=$hops}"
        echo "our_system,$size,$i,$CACHE_MODE,$ttfb,$total,key" >> "$OUTPUT_FILE"
      else
        ((our_failures++))
        echo "FAILED"
        echo "our_system,$size,$i,$CACHE_MODE,ERROR,ERROR,key" >> "$OUTPUT_FILE"
      fi
    done
  else
    echo "    Skipping our system (upload failed)"
  fi
  
  # Test Swarm downloads
  echo -e "\n  ${GREEN}Testing Swarm downloads...${NC}"
  swarm_ttfb=()
  swarm_total=()
  swarm_failures=0
  
  if [[ -n "$SWARM_HASH" ]]; then
    for i in $(seq 1 $ITERATIONS); do
      echo -n "    Iteration $i/$ITERATIONS... "
      output_file="$TEMP_DIR/swarm_download_${size}_${i}.bin"
      if [[ "$CACHE_MODE" == "warm" ]]; then
        download_swarm "$SWARM_HASH" "$TEMP_DIR/swarm_prime_$$.bin" >/dev/null 2>&1 || true
      fi
      result=$(download_swarm "$SWARM_HASH" "$output_file" 2>&1)
      if [[ $? -eq 0 ]]; then
        IFS='|' read -r ttfb total <<< "$result"
        swarm_ttfb+=("$ttfb")
        swarm_total+=("$total")
        echo "TTFB: $(printf "%.2f" "$ttfb") ms, Total: $(printf "%.2f" "$total") ms"
        echo "swarm,$size,$i,$CACHE_MODE,$ttfb,$total,cid" >> "$OUTPUT_FILE"
      else
        ((swarm_failures++))
        echo "FAILED"
        echo "swarm,$size,$i,$CACHE_MODE,ERROR,ERROR,cid" >> "$OUTPUT_FILE"
      fi
    done
  else
    echo "    Skipping (upload failed)"
  fi
  
  # Print statistics
  echo -e "\n  ${BLUE}Statistics for $size_label:${NC}"
  
  if [[ ${#our_ttfb[@]} -gt 0 ]]; then
    our_ttfb_stats=$(calculate_stats "${our_ttfb[@]}")
    our_total_stats=$(calculate_stats "${our_total[@]}")
    IFS=',' read -r ttfb_min ttfb_max ttfb_avg ttfb_p50 ttfb_p90 ttfb_p99 <<< "$our_ttfb_stats"
    IFS=',' read -r total_min total_max total_avg total_p50 total_p90 total_p99 <<< "$our_total_stats"
    echo -e "    ${GREEN}Our System (TTFB):${NC}"
    echo "      Min:    $(printf "%.2f" "$ttfb_min") ms"
    echo "      Max:    $(printf "%.2f" "$ttfb_max") ms"
    echo "      Avg:    $(printf "%.2f" "$ttfb_avg") ms"
    echo "      P50:    $(printf "%.2f" "$ttfb_p50") ms"
    echo "      P90:    $(printf "%.2f" "$ttfb_p90") ms"
    echo "      P99:    $(printf "%.2f" "$ttfb_p99") ms"
    echo -e "    ${GREEN}Our System (Total):${NC}"
    echo "      Min:    $(printf "%.2f" "$total_min") ms"
    echo "      Max:    $(printf "%.2f" "$total_max") ms"
    echo "      Avg:    $(printf "%.2f" "$total_avg") ms"
    echo "      P50:    $(printf "%.2f" "$total_p50") ms"
    echo "      P90:    $(printf "%.2f" "$total_p90") ms"
    echo "      P99:    $(printf "%.2f" "$total_p99") ms"
    if [[ $our_failures -gt 0 ]]; then
      echo "      Failures: $our_failures/$ITERATIONS"
    fi
  else
    echo -e "    ${RED}Our System: All iterations failed${NC}"
  fi
  
  if [[ ${#swarm_ttfb[@]} -gt 0 ]]; then
    swarm_ttfb_stats=$(calculate_stats "${swarm_ttfb[@]}")
    swarm_total_stats=$(calculate_stats "${swarm_total[@]}")
    IFS=',' read -r ttfb_min ttfb_max ttfb_avg ttfb_p50 ttfb_p90 ttfb_p99 <<< "$swarm_ttfb_stats"
    IFS=',' read -r total_min total_max total_avg total_p50 total_p90 total_p99 <<< "$swarm_total_stats"
    echo -e "    ${GREEN}Swarm (TTFB):${NC}"
    echo "      Min:    $(printf "%.2f" "$ttfb_min") ms"
    echo "      Max:    $(printf "%.2f" "$ttfb_max") ms"
    echo "      Avg:    $(printf "%.2f" "$ttfb_avg") ms"
    echo "      P50:    $(printf "%.2f" "$ttfb_p50") ms"
    echo "      P90:    $(printf "%.2f" "$ttfb_p90") ms"
    echo "      P99:    $(printf "%.2f" "$ttfb_p99") ms"
    echo -e "    ${GREEN}Swarm (Total):${NC}"
    echo "      Min:    $(printf "%.2f" "$total_min") ms"
    echo "      Max:    $(printf "%.2f" "$total_max") ms"
    echo "      Avg:    $(printf "%.2f" "$total_avg") ms"
    echo "      P50:    $(printf "%.2f" "$total_p50") ms"
    echo "      P90:    $(printf "%.2f" "$total_p90") ms"
    echo "      P99:    $(printf "%.2f" "$total_p99") ms"
    if [[ $swarm_failures -gt 0 ]]; then
      echo "      Failures: $swarm_failures/$ITERATIONS"
    fi
  else
    echo -e "    ${RED}Swarm: All iterations failed${NC}"
  fi
done

echo ""
echo "=========================================="
echo "Test Complete"
echo "=========================================="
echo "Results saved to: $OUTPUT_FILE"
echo ""
echo "To view results:"
echo "  cat $OUTPUT_FILE"
echo ""
echo "To analyze with Python:"
echo "  python3 -c \"import pandas as pd; df = pd.read_csv('$OUTPUT_FILE'); print(df.groupby(['system', 'payload_size'])[['ttfb_ms', 'total_ms']].describe())\""
