#!/usr/bin/env bash
set -euo pipefail

# Purpose: Upload latency test comparing our system vs Swarm v0.5.8
# Usage: ./scripts/scenarios/swarm_upload_test.sh [options]
#   --our-api <addr>     Our system API address (default: read from bootstrap.json)
#   --swarm-api <addr>   Swarm API address (default: http://172.20.0.10:8500)
#   --iterations <n>     Number of iterations per size (default: 10)
#   --output <file>      Output CSV file (default: upload_latency_results.csv)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source error handler
source "$ROOT_DIR/scripts/utils/error_handler.sh"

# Source Swarm API functions
source "$ROOT_DIR/scripts/swarm/api.sh"

# Initialize error logging
RUN_ID="${RUN_ID:-$(date +%s)}"
ERROR_LOG_DIR="artifacts/swarm_tests/$RUN_ID"
export RUN_ID ERROR_LOG_DIR
mkdir -p "$ERROR_LOG_DIR"

# Default values
OUR_API=""
SWARM_API="http://172.20.0.200:8500"
ITERATIONS=10
OUTPUT_FILE="upload_latency_results.csv"
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
    --output)
      OUTPUT_FILE="$2"
      shift 2
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --our-api <addr>     Our system API address"
      echo "  --swarm-api <addr>   Swarm API address (default: http://172.20.0.10:8500)"
      echo "  --iterations <n>     Iterations per size (default: 10)"
      echo "  --output <file>      Output CSV file (default: upload_latency_results.csv)"
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
      # Control server binds to 127.0.0.1 inside container, so we need to access via docker exec
      OUR_API="http://$OUR_API_ADDR"
      echo "Detected control API: $OUR_API (container: $OUR_CONTAINER)"
    fi
  fi
  
  # Try docker-compose if direct docker didn't work
  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
    if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
      if docker-compose ps bootstrap 2>/dev/null | grep -q "Up"; then
        OUR_CONTAINER="bootstrap"
        BOOTSTRAP_CTRL="/app/logs/bootstrap.json"
        OUR_API_ADDR=$(docker-compose exec -T bootstrap jq -r '.addr // .Addr' "$BOOTSTRAP_CTRL" 2>/dev/null || echo "")
        
        if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
          OUR_API="http://$OUR_API_ADDR"
          echo "Detected control API: $OUR_API (container: $OUR_CONTAINER via docker-compose)"
        fi
      fi
    fi
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
  # Format might be "container_name" or "http://127.0.0.1:port"
  if [[ "$OUR_API" =~ ^[a-zA-Z0-9_-]+$ ]]; then
    OUR_CONTAINER="$OUR_API"
    # Read address from container
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
echo "Upload Latency Test"
echo "=========================================="
echo "Our System API: $OUR_API"
echo "Swarm API: $SWARM_API"
echo "Iterations per size: $ITERATIONS"
echo "Output file: $OUTPUT_FILE"
echo ""

# Check Docker and required tools
if ! check_docker; then
  exit 1
fi

if ! check_required_tools bc jq; then
  exit 1
fi

# Verify API endpoints are accessible
echo "Verifying API endpoints..."

# Check our system API (via docker exec if needed)
if [[ -n "$OUR_CONTAINER" ]]; then
  if check_api_endpoint_container "$OUR_CONTAINER" "http://$OUR_API_ADDR/health" 5 3; then
    echo -e "${GREEN}✓ Our system API is accessible${NC}"
  else
    log_error "Our system API not accessible" "container: $OUR_CONTAINER, addr: $OUR_API_ADDR"
    echo -e "${YELLOW}Warning: Our system API may not be accessible${NC}"
  fi
else
  if check_api_endpoint "$OUR_API/health" 5 3; then
    echo -e "${GREEN}✓ Our system API is accessible${NC}"
  else
    log_error "Our system API not accessible" "url: $OUR_API"
    echo -e "${YELLOW}Warning: Our system API ($OUR_API) may not be accessible${NC}"
  fi
fi

# Check Swarm API
if check_api_endpoint "$SWARM_API/" 5 3; then
  echo -e "${GREEN}✓ Swarm API is accessible${NC}"
else
  log_error "Swarm API not accessible" "url: $SWARM_API"
  echo -e "${YELLOW}Warning: Swarm API ($SWARM_API) may not be accessible${NC}"
fi

# Create temp directory for test files
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

# Initialize CSV output
echo "system,payload_size,iteration,latency_ms" > "$OUTPUT_FILE"

# Function to generate test file of specified size
generate_test_file() {
  local size=$1
  local output="$2"
  
  # Generate random data
  dd if=/dev/urandom of="$output" bs=1 count="$size" 2>/dev/null
}

# Function to upload to our system and measure latency
upload_our_system() {
  local file_path="$1"
  
  if [[ -z "$OUR_CONTAINER" || -z "$OUR_API_ADDR" ]]; then
    echo "ERROR: Container or API address not set" >&2
    return 1
  fi
  
  # Read file and base64 encode
  local data_b64=$(base64 -w 0 < "$file_path" 2>/dev/null || base64 < "$file_path" | tr -d '\n')
  
  # Control server binds to 127.0.0.1 inside container, so we need docker exec
  local api_url="http://$OUR_API_ADDR/put"
  
  # Create JSON payload file
  local json_payload="$TEMP_DIR/put_payload_$$.json"
  echo "{\"data\":\"$data_b64\"}" > "$json_payload"
  
  # Measure upload time
  local start_time=$(date +%s.%N)
  
  # Copy JSON to container with retry
  if ! retry_with_backoff 3 1 10 \
    docker cp "$json_payload" "${OUR_CONTAINER}:/tmp/put_payload_$$.json" >/dev/null 2>&1; then
    log_error "Failed to copy payload to container" "container: $OUR_CONTAINER"
    rm -f "$json_payload"
    echo "ERROR"
    return 1
  fi
  
  # Execute upload with timeout
  local response=""
  if ! response=$(with_timeout 30 docker exec "$OUR_CONTAINER" curl -sSf -X POST \
    -H "Content-Type: application/json" \
    -d @/tmp/put_payload_$$.json \
    "$api_url" 2>&1); then
    log_error "Upload request failed" "container: $OUR_CONTAINER, url: $api_url"
    docker exec "$OUR_CONTAINER" rm -f "/tmp/put_payload_$$.json" >/dev/null 2>&1 || true
    rm -f "$json_payload"
    echo "ERROR"
    return 1
  fi
  
  docker exec "$OUR_CONTAINER" rm -f "/tmp/put_payload_$$.json" >/dev/null 2>&1 || true
  rm -f "$json_payload"
  local end_time=$(date +%s.%N)
  
  # Calculate latency in milliseconds
  local latency=$(echo "scale=2; ($end_time - $start_time) * 1000" | bc -l)
  
  # Check if upload succeeded
  if echo "$response" | jq -e '.cid' >/dev/null 2>&1; then
    echo "$latency"
    return 0
  else
    log_error "Upload failed" "response: $response"
    echo "ERROR"
    return 1
  fi
}

# Function to upload to Swarm and measure latency
upload_swarm() {
  local file_path="$1"
  
  # Measure upload time
  local start_time=$(date +%s.%N)
  
  # Retry upload with backoff
  local hash=""
  if ! hash=$(retry_with_backoff 3 2 10 upload_file "$SWARM_API" "$file_path" 2>&1); then
    log_error "Swarm upload failed after retries" "api: $SWARM_API, file: $file_path"
    local end_time=$(date +%s.%N)
    local latency=$(echo "scale=2; ($end_time - $start_time) * 1000" | bc -l)
    echo "ERROR"
    return 1
  fi
  
  local end_time=$(date +%s.%N)
  
  # Calculate latency in milliseconds
  local latency=$(echo "scale=2; ($end_time - $start_time) * 1000" | bc -l)
  
  # Check if upload succeeded
  if [[ -n "$hash" && "$hash" != "ERROR"* && ${#hash} -ge 64 ]]; then
    echo "$latency"
    return 0
  else
    log_error "Swarm upload returned invalid hash" "hash: $hash"
    echo "ERROR"
    return 1
  fi
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
  
  # Test our system
  echo -e "\n  ${GREEN}Testing our system...${NC}"
  our_latencies=()
  our_failures=0
  
  for i in $(seq 1 $ITERATIONS); do
    echo -n "    Iteration $i/$ITERATIONS... "
    latency=$(upload_our_system "$test_file" 2>&1)
    
    if [[ $? -eq 0 && "$latency" =~ ^[0-9]+\.?[0-9]*$ ]]; then
      our_latencies+=("$latency")
      echo "$(printf "%.2f" "$latency") ms"
      echo "our_system,$size,$i,$latency" >> "$OUTPUT_FILE"
    else
      ((our_failures++))
      echo "FAILED"
      echo "our_system,$size,$i,ERROR" >> "$OUTPUT_FILE"
    fi
  done
  
  # Test Swarm
  echo -e "\n  ${GREEN}Testing Swarm...${NC}"
  swarm_latencies=()
  swarm_failures=0
  
  for i in $(seq 1 $ITERATIONS); do
    echo -n "    Iteration $i/$ITERATIONS... "
    latency=$(upload_swarm "$test_file" 2>&1)
    
    if [[ $? -eq 0 && "$latency" =~ ^[0-9]+\.?[0-9]*$ ]]; then
      swarm_latencies+=("$latency")
      echo "$(printf "%.2f" "$latency") ms"
      echo "swarm,$size,$i,$latency" >> "$OUTPUT_FILE"
    else
      ((swarm_failures++))
      echo "FAILED"
      echo "swarm,$size,$i,ERROR" >> "$OUTPUT_FILE"
    fi
  done
  
  # Print statistics
  echo -e "\n  ${BLUE}Statistics for $size_label:${NC}"
  
  if [[ ${#our_latencies[@]} -gt 0 ]]; then
    our_stats=$(calculate_stats "${our_latencies[@]}")
    IFS=',' read -r our_min our_max our_avg our_p50 our_p90 our_p99 <<< "$our_stats"
    echo -e "    ${GREEN}Our System:${NC}"
    echo "      Min:    $(printf "%.2f" "$our_min") ms"
    echo "      Max:    $(printf "%.2f" "$our_max") ms"
    echo "      Avg:    $(printf "%.2f" "$our_avg") ms"
    echo "      P50:    $(printf "%.2f" "$our_p50") ms"
    echo "      P90:    $(printf "%.2f" "$our_p90") ms"
    echo "      P99:    $(printf "%.2f" "$our_p99") ms"
    if [[ $our_failures -gt 0 ]]; then
      echo "      Failures: $our_failures/$ITERATIONS"
    fi
  else
    echo -e "    ${RED}Our System: All iterations failed${NC}"
  fi
  
  if [[ ${#swarm_latencies[@]} -gt 0 ]]; then
    swarm_stats=$(calculate_stats "${swarm_latencies[@]}")
    IFS=',' read -r swarm_min swarm_max swarm_avg swarm_p50 swarm_p90 swarm_p99 <<< "$swarm_stats"
    echo -e "    ${GREEN}Swarm:${NC}"
    echo "      Min:    $(printf "%.2f" "$swarm_min") ms"
    echo "      Max:    $(printf "%.2f" "$swarm_max") ms"
    echo "      Avg:    $(printf "%.2f" "$swarm_avg") ms"
    echo "      P50:    $(printf "%.2f" "$swarm_p50") ms"
    echo "      P90:    $(printf "%.2f" "$swarm_p90") ms"
    echo "      P99:    $(printf "%.2f" "$swarm_p99") ms"
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
echo "  python3 -c \"import pandas as pd; df = pd.read_csv('$OUTPUT_FILE'); print(df.groupby(['system', 'payload_size'])['latency_ms'].describe())\""
