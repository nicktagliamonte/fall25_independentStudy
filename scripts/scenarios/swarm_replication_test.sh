#!/usr/bin/env bash
set -euo pipefail

# Purpose: Replication propagation test - measure time for content to propagate to nodes
# Usage: ./scripts/scenarios/swarm_replication_test.sh [options]
#   --our-api <container> Our system container name (default: auto-detect bootstrap)
#   --swarm-api <addr>   Swarm API address (default: http://172.20.0.200:8500)
#   --poll-interval <s>  Polling interval in seconds (default: 1)
#   --max-wait <s>       Maximum wait time in seconds (default: 60)
#   --output <file>      Output CSV file (default: replication_propagation.csv)

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
POLL_INTERVAL=1
MAX_WAIT=60
OUTPUT_FILE="replication_propagation.csv"
PAYLOAD_SIZE=10240  # 10KB test payload
NODES=4  # Default number of nodes to start
AUTO_START=true  # Auto-start nodes if not running
CLEANUP=false  # Clean up nodes after test

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
    --poll-interval)
      POLL_INTERVAL="$2"
      shift 2
      ;;
    --max-wait)
      MAX_WAIT="$2"
      shift 2
      ;;
    --output)
      OUTPUT_FILE="$2"
      shift 2
      ;;
    --nodes)
      NODES="$2"
      shift 2
      ;;
    --no-auto-start)
      AUTO_START=false
      shift
      ;;
    --cleanup)
      CLEANUP=true
      shift
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --our-api <container> Our system container name (default: auto-detect)"
      echo "  --swarm-api <addr>   Swarm API address (default: http://172.20.0.200:8500)"
      echo "  --poll-interval <s>  Polling interval in seconds (default: 1)"
      echo "  --max-wait <s>       Maximum wait time in seconds (default: 60)"
      echo "  --output <file>      Output CSV file (default: replication_propagation.csv)"
      echo "  --nodes <n>          Number of nodes to start (default: 4)"
      echo "  --no-auto-start      Don't auto-start nodes (fail if not running)"
      echo "  --cleanup            Stop nodes after test completes"
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
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Check for required tools
if ! command -v jq >/dev/null 2>&1; then
  echo "Error: 'jq' command not found. Please install it." >&2
  exit 1
fi

# Function to detect our system API address and container
detect_our_api() {
  OUR_CONTAINER=""
  OUR_API_ADDR=""
  
  if [[ -z "$OUR_API" ]]; then
    # Try to find bootstrap container
    if docker ps --format '{{.Names}}' | grep -q "^fall25-bootstrap$"; then
      OUR_CONTAINER="fall25-bootstrap"
      OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
      if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
        OUR_API="http://$OUR_API_ADDR"
        return 0
      fi
    fi
    
    # Try docker-compose if direct docker didn't work
    if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
      if docker-compose ps bootstrap 2>/dev/null | grep -q "Up"; then
        OUR_CONTAINER="bootstrap"
        OUR_API_ADDR=$(docker-compose exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
        if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
          OUR_API="http://$OUR_API_ADDR"
          return 0
        fi
      fi
    fi
    
    return 1
  else
    if [[ "$OUR_API" =~ ^[a-zA-Z0-9_-]+$ ]]; then
      OUR_CONTAINER="$OUR_API"
      OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
      if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
        OUR_API="http://$OUR_API_ADDR"
        return 0
      fi
    fi
    return 1
  fi
}

# Function to check if our system nodes are running
check_our_nodes_running() {
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
    local running=$(docker-compose ps --services 2>/dev/null | grep -E '^(bootstrap|node)' | wc -l)
    [[ $running -gt 0 ]]
  else
    local running=$(docker ps --format '{{.Names}}' | grep -E '^fall25-(bootstrap|node)' | wc -l)
    [[ $running -gt 0 ]]
  fi
}

# Function to check if Swarm nodes are running
check_swarm_nodes_running() {
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.swarm.yml" ]]; then
    local running=$(docker-compose -f docker-compose.swarm.yml ps --services 2>/dev/null | grep -E '^(swarm-bootstrap|swarm-node)' | wc -l)
    [[ $running -gt 0 ]]
  else
    false
  fi
}

# Function to start our system nodes
start_our_nodes() {
  local node_count="$1"
  echo -e "${BLUE}Starting our system ($node_count nodes)...${NC}"
  
  if ! "$ROOT_DIR/scripts/docker/start.sh" "$node_count" >/dev/null 2>&1; then
    echo -e "${RED}Failed to start our system nodes${NC}" >&2
    return 1
  fi
  
  # Wait for bootstrap to be ready
  echo "  Waiting for bootstrap to be ready..."
  local max_wait=60
  for i in $(seq 1 $max_wait); do
    if docker-compose -f "$ROOT_DIR/docker-compose.yml" exec -T bootstrap curl -sf "http://\$(jq -r .addr /app/logs/bootstrap.json)/health" >/dev/null 2>&1; then
      echo "  Bootstrap ready after ${i}s"
      break
    fi
    if [[ $i -eq $max_wait ]]; then
      echo -e "${YELLOW}Warning: Bootstrap may not be fully ready${NC}"
    fi
    sleep 1
  done
  
  # Update OUR_CONTAINER and OUR_API_ADDR
  detect_our_api || true
  
  sleep 3  # Brief stabilization
  return 0
}

# Function to start Swarm nodes
start_swarm_nodes() {
  local node_count="$1"
  echo -e "${BLUE}Starting Swarm ($node_count nodes)...${NC}"
  
  if ! "$ROOT_DIR/scripts/docker/swarm/start.sh" "$node_count" >/dev/null 2>&1; then
    echo -e "${RED}Failed to start Swarm nodes${NC}" >&2
    return 1
  fi
  
  # Wait for bootstrap to be ready
  echo "  Waiting for Swarm bootstrap to be ready..."
  if check_api_endpoint "http://172.20.0.200:8500/" 5 12; then
    echo "  Swarm bootstrap ready"
  else
    log_error "Swarm bootstrap failed to become ready" "api: http://172.20.0.200:8500/"
    return 1
  fi
  
  sleep 3  # Brief stabilization
  return 0
}

# Function to cleanup nodes
cleanup_nodes() {
  if [[ "$CLEANUP" == "true" ]]; then
    echo -e "\n${BLUE}Cleaning up nodes...${NC}"
    
    # Stop Swarm first
    if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.swarm.yml" ]]; then
      if docker-compose -f docker-compose.swarm.yml ps 2>/dev/null | grep -q "Up"; then
        docker-compose -f docker-compose.swarm.yml stop >/dev/null 2>&1 || true
        docker-compose -f docker-compose.swarm.yml rm -f >/dev/null 2>&1 || true
      fi
    fi
    
    # Stop our system
    if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
      if docker-compose ps 2>/dev/null | grep -q "Up"; then
        docker-compose stop >/dev/null 2>&1 || true
        docker-compose rm -f >/dev/null 2>&1 || true
      fi
    fi
    
    echo "  Cleanup complete"
  fi
}

# Trap to cleanup on exit
trap cleanup_nodes EXIT

# Function to count running nodes
count_our_nodes() {
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
    docker-compose ps --services 2>/dev/null | grep -E '^(bootstrap|node)' | grep -c . || echo "0"
  else
    docker ps --format '{{.Names}}' | grep -E '^fall25-(bootstrap|node)' | wc -l
  fi
}

count_swarm_nodes() {
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.swarm.yml" ]]; then
    docker-compose -f docker-compose.swarm.yml ps --services 2>/dev/null | grep -E '^(swarm-bootstrap|swarm-node)' | grep -c . || echo "0"
  else
    echo "0"
  fi
}

# Auto-start nodes if needed
if [[ "$AUTO_START" == "true" ]]; then
  OUR_NODES_RUNNING=false
  SWARM_NODES_RUNNING=false
  
  # Check current node counts
  OUR_CURRENT_COUNT=$(count_our_nodes)
  SWARM_CURRENT_COUNT=$(count_swarm_nodes)
  
  # Start our system nodes if needed or if count doesn't match
  if [[ $OUR_CURRENT_COUNT -eq 0 ]]; then
    echo -e "${YELLOW}Our system nodes are not running, starting $NODES nodes...${NC}"
    if ! start_our_nodes "$NODES"; then
      echo -e "${RED}Failed to start our system nodes${NC}" >&2
      exit 1
    fi
    OUR_NODES_RUNNING=true
  elif [[ $OUR_CURRENT_COUNT -ne $NODES ]]; then
    echo -e "${YELLOW}Our system has $OUR_CURRENT_COUNT nodes, but need $NODES. Restarting...${NC}"
    if ! start_our_nodes "$NODES"; then
      echo -e "${RED}Failed to restart our system nodes${NC}" >&2
      exit 1
    fi
    OUR_NODES_RUNNING=true
  else
    if detect_our_api; then
      OUR_NODES_RUNNING=true
      echo -e "${GREEN}Our system nodes are already running ($OUR_CURRENT_COUNT nodes)${NC}"
    else
      echo -e "${YELLOW}Our system nodes are running but API not ready. Restarting...${NC}"
      if ! start_our_nodes "$NODES"; then
        echo -e "${RED}Failed to restart our system nodes${NC}" >&2
        exit 1
      fi
      OUR_NODES_RUNNING=true
    fi
  fi
  
  # Start Swarm nodes if needed or if count doesn't match
  if [[ $SWARM_CURRENT_COUNT -eq 0 ]]; then
    echo -e "${YELLOW}Swarm nodes are not running, starting $NODES nodes...${NC}"
    if ! start_swarm_nodes "$NODES"; then
      echo -e "${RED}Failed to start Swarm nodes${NC}" >&2
      exit 1
    fi
    SWARM_NODES_RUNNING=true
  elif [[ $SWARM_CURRENT_COUNT -ne $NODES ]]; then
    echo -e "${YELLOW}Swarm has $SWARM_CURRENT_COUNT nodes, but need $NODES. Restarting...${NC}"
    if ! start_swarm_nodes "$NODES"; then
      echo -e "${RED}Failed to restart Swarm nodes${NC}" >&2
      exit 1
    fi
    SWARM_NODES_RUNNING=true
  else
    SWARM_NODES_RUNNING=true
    echo -e "${GREEN}Swarm nodes are already running ($SWARM_CURRENT_COUNT nodes)${NC}"
  fi
  
  # Verify both systems have the same count
  OUR_FINAL_COUNT=$(count_our_nodes)
  SWARM_FINAL_COUNT=$(count_swarm_nodes)
  
  if [[ $OUR_FINAL_COUNT -ne $SWARM_FINAL_COUNT ]]; then
    echo -e "${RED}Error: Node count mismatch! Our system: $OUR_FINAL_COUNT, Swarm: $SWARM_FINAL_COUNT${NC}" >&2
    echo -e "${YELLOW}Restarting both systems with $NODES nodes...${NC}"
    start_our_nodes "$NODES" || exit 1
    start_swarm_nodes "$NODES" || exit 1
    OUR_FINAL_COUNT=$(count_our_nodes)
    SWARM_FINAL_COUNT=$(count_swarm_nodes)
  fi
  
  if [[ $OUR_FINAL_COUNT -ne $SWARM_FINAL_COUNT ]]; then
    echo -e "${RED}Error: Still have node count mismatch after restart!${NC}" >&2
    exit 1
  fi
  
  echo -e "${GREEN}Both systems have $OUR_FINAL_COUNT nodes${NC}"
  echo ""
else
  # Check if nodes are running, fail if not
  OUR_CURRENT_COUNT=$(count_our_nodes)
  SWARM_CURRENT_COUNT=$(count_swarm_nodes)
  
  if [[ $OUR_CURRENT_COUNT -eq 0 ]]; then
    echo -e "${RED}Error: Our system nodes are not running. Use --auto-start or start them manually.${NC}" >&2
    exit 1
  fi
  
  if ! detect_our_api; then
    echo -e "${RED}Error: Could not detect our system API address.${NC}" >&2
    exit 1
  fi
  
  if [[ $SWARM_CURRENT_COUNT -eq 0 ]]; then
    echo -e "${RED}Error: Swarm nodes are not running. Use --auto-start or start them manually.${NC}" >&2
    exit 1
  fi
  
  if [[ $OUR_CURRENT_COUNT -ne $SWARM_CURRENT_COUNT ]]; then
    echo -e "${RED}Error: Node count mismatch! Our system: $OUR_CURRENT_COUNT, Swarm: $SWARM_CURRENT_COUNT${NC}" >&2
    echo -e "${YELLOW}Use --auto-start to ensure both systems have the same node count.${NC}" >&2
    exit 1
  fi
  
  echo -e "${GREEN}Both systems have $OUR_CURRENT_COUNT nodes${NC}"
  echo ""
fi

echo "=========================================="
echo "Replication Propagation Test"
echo "=========================================="
echo "Our System API: $OUR_API (container: $OUR_CONTAINER)"
echo "Swarm API: $SWARM_API"
echo "Poll interval: ${POLL_INTERVAL}s"
echo "Max wait: ${MAX_WAIT}s"
echo "Output file: $OUTPUT_FILE"
echo ""

# Get list of our system nodes
get_our_nodes() {
  local nodes=()
  if [[ -n "$OUR_CONTAINER" ]]; then
    # Try docker-compose first
    if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
      local services=$(docker-compose ps --services 2>/dev/null | grep -E '^(bootstrap|node)' || true)
      for service in $services; do
        if docker-compose ps "$service" 2>/dev/null | grep -q "Up"; then
          nodes+=("$service")
        fi
      done
    else
      # Fallback to docker ps
      local containers=$(docker ps --format '{{.Names}}' | grep -E '^fall25-(bootstrap|node)' || true)
      for container in $containers; do
        nodes+=("$container")
      done
    fi
  fi
  echo "${nodes[@]}"
}

# Get list of Swarm nodes
get_swarm_nodes() {
  local nodes=()
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.swarm.yml" ]]; then
    local services=$(docker-compose -f docker-compose.swarm.yml ps --services 2>/dev/null | grep -E '^(swarm-bootstrap|swarm-node)' || true)
    for service in $services; do
      if docker-compose -f docker-compose.swarm.yml ps "$service" 2>/dev/null | grep -q "Up"; then
        nodes+=("$service")
      fi
    done
  fi
  echo "${nodes[@]}"
}

# Get control address for our system node
get_our_node_addr() {
  local node="$1"
  if [[ "$node" == "bootstrap" ]] || [[ "$node" == "fall25-bootstrap" ]]; then
    echo "$OUR_API_ADDR"
  else
    # Try docker-compose
    if command -v docker-compose >/dev/null 2>&1; then
      local ctrl_file="/app/logs/${node}.json"
      docker-compose exec -T "$node" jq -r '.addr // .Addr' "$ctrl_file" 2>/dev/null || echo ""
    else
      local ctrl_file="/app/logs/${node}.json"
      docker exec "$node" jq -r '.addr // .Addr' "$ctrl_file" 2>/dev/null || echo ""
    fi
  fi
}

# Check if CID exists on our system node
check_our_node_has_cid() {
  local node="$1"
  local cid="$2"
  local addr="$3"
  
  if [[ -z "$addr" || "$addr" == "null" ]]; then
    return 1
  fi
  
  # Call /snapshot endpoint and check if CID is in the list
  local snapshot_json=""
  if command -v docker-compose >/dev/null 2>&1 && [[ "$node" != "fall25-"* ]]; then
    if ! snapshot_json=$(with_timeout 5 docker-compose exec -T "$node" curl -sSf "http://$addr/snapshot?limit=10000" 2>/dev/null); then
      log_warning "Failed to fetch snapshot from node" "node: $node, addr: $addr"
      snapshot_json='{"cids":[]}'
    fi
  else
    if ! snapshot_json=$(with_timeout 5 docker exec "$node" curl -sSf "http://$addr/snapshot?limit=10000" 2>/dev/null); then
      log_warning "Failed to fetch snapshot from node" "node: $node, addr: $addr"
      snapshot_json='{"cids":[]}'
    fi
  fi
  
  echo "$snapshot_json" | jq -e --arg cid "$cid" '.cids[]? == $cid' >/dev/null 2>&1
}

# Check if hash exists on Swarm node
check_swarm_node_has_hash() {
  local node="$1"
  local hash="$2"
  
  # Swarm nodes are accessible via their IP addresses
  # Extract IP from node name (swarm-bootstrap -> 172.20.0.200, swarm-node1 -> 172.20.0.201, etc.)
  local ip=""
  if [[ "$node" == "swarm-bootstrap" ]]; then
    ip="172.20.0.200"
  elif [[ "$node" =~ ^swarm-node([0-9]+)$ ]]; then
    local node_num="${BASH_REMATCH[1]}"
    ip="172.20.0.$((200 + node_num))"
  else
    return 1
  fi
  
  # Try to fetch content from /bzz:/<hash>/ with timeout
  check_api_endpoint "http://${ip}:8500/bzz:/${hash}/" 2 1 >/dev/null 2>&1
}

# Upload content to our system bootstrap
upload_our_system() {
  local data_b64="$1"
  local api_url="http://$OUR_API_ADDR/put"
  
  local json_payload=$(mktemp)
  echo "{\"data\":\"$data_b64\"}" > "$json_payload"
  
  if ! retry_with_backoff 3 1 5 docker cp "$json_payload" "${OUR_CONTAINER}:/tmp/put_payload_$$.json" >/dev/null 2>&1; then
    log_error "Failed to copy payload to container" "container: $OUR_CONTAINER"
    rm -f "$json_payload"
    return 1
  fi
  
  local response=""
  if ! response=$(with_timeout 30 docker exec "$OUR_CONTAINER" curl -sSf -X POST \
    -H "Content-Type: application/json" \
    -d @/tmp/put_payload_$$.json \
    "$api_url" 2>&1); then
    log_error "Upload request failed" "container: $OUR_CONTAINER, url: $api_url"
    docker exec "$OUR_CONTAINER" rm -f "/tmp/put_payload_$$.json" >/dev/null 2>&1 || true
    rm -f "$json_payload"
    return 1
  fi
  docker exec "$OUR_CONTAINER" rm -f "/tmp/put_payload_$$.json" >/dev/null 2>&1 || true
  
  rm -f "$json_payload"
  
  echo "$response" | jq -r '.cid // empty' 2>/dev/null || echo ""
}

# Upload content to Swarm bootstrap
upload_swarm() {
  local file_path="$1"
  upload_file "$SWARM_API" "$file_path" 2>&1
}

# Measure propagation for our system
measure_our_propagation() {
  local cid="$1"
  shift
  local nodes_array=("$@")
  
  local total_nodes=${#nodes_array[@]}
  if [[ $total_nodes -eq 0 ]]; then
    echo "0,0,0"
    return
  fi
  
  local time_50pct=0
  local time_90pct=0
  local time_100pct=0
  local found_50pct=false
  local found_90pct=false
  local found_100pct=false
  
  local start_time=$(date +%s)
  local found_nodes=()
  local found_count=0
  
  # Bootstrap always has it immediately, so we start with 1 node having it
  found_count=1
  
  while [[ $(($(date +%s) - start_time)) -lt $MAX_WAIT ]]; do
    local current_time=$(date +%s)
    local elapsed=$((current_time - start_time))
    
    # Check each node
    for node in "${nodes_array[@]}"; do
      # Skip if already found
      local already_found=false
      for found_node in "${found_nodes[@]}"; do
        if [[ "$found_node" == "$node" ]]; then
          already_found=true
          break
        fi
      done
      
      if [[ "$already_found" == "false" ]]; then
        local addr=$(get_our_node_addr "$node")
        if [[ -n "$addr" && "$addr" != "null" ]]; then
          if check_our_node_has_cid "$node" "$cid" "$addr"; then
            found_count=$((found_count + 1))
            found_nodes+=("$node")
          fi
        fi
      fi
    done
    
    # Check thresholds
    local pct_50=$((total_nodes / 2 + (total_nodes % 2)))
    local pct_90=$((total_nodes * 9 / 10 + (total_nodes % 10 > 0 ? 1 : 0)))
    
    if [[ $found_count -ge $pct_50 && "$found_50pct" == "false" ]]; then
      time_50pct=$elapsed
      found_50pct=true
    fi
    
    if [[ $found_count -ge $pct_90 && "$found_90pct" == "false" ]]; then
      time_90pct=$elapsed
      found_90pct=true
    fi
    
    if [[ $found_count -ge $total_nodes && "$found_100pct" == "false" ]]; then
      time_100pct=$elapsed
      found_100pct=true
      break
    fi
    
    sleep "$POLL_INTERVAL"
  done
  
  # If we didn't reach thresholds, use max wait time
  if [[ "$found_50pct" == "false" ]]; then
    time_50pct=$MAX_WAIT
  fi
  if [[ "$found_90pct" == "false" ]]; then
    time_90pct=$MAX_WAIT
  fi
  if [[ "$found_100pct" == "false" ]]; then
    time_100pct=$MAX_WAIT
  fi
  
  echo "$time_50pct,$time_90pct,$time_100pct"
}

# Measure propagation for Swarm
measure_swarm_propagation() {
  local hash="$1"
  shift
  local nodes_array=("$@")
  
  local total_nodes=${#nodes_array[@]}
  if [[ $total_nodes -eq 0 ]]; then
    echo "0,0,0"
    return
  fi
  
  local time_50pct=0
  local time_90pct=0
  local time_100pct=0
  local found_50pct=false
  local found_90pct=false
  local found_100pct=false
  
  local start_time=$(date +%s)
  local found_nodes=()
  local found_count=1  # Bootstrap has it
  
  while [[ $(($(date +%s) - start_time)) -lt $MAX_WAIT ]]; do
    local current_time=$(date +%s)
    local elapsed=$((current_time - start_time))
    
    # Check each node
    for node in "${nodes_array[@]}"; do
      # Skip if already found
      local already_found=false
      for found_node in "${found_nodes[@]}"; do
        if [[ "$found_node" == "$node" ]]; then
          already_found=true
          break
        fi
      done
      
      if [[ "$already_found" == "false" ]]; then
        if check_swarm_node_has_hash "$node" "$hash"; then
          found_count=$((found_count + 1))
          found_nodes+=("$node")
        fi
      fi
    done
    
    # Check thresholds
    local pct_50=$((total_nodes / 2 + (total_nodes % 2)))
    local pct_90=$((total_nodes * 9 / 10 + (total_nodes % 10 > 0 ? 1 : 0)))
    
    if [[ $found_count -ge $pct_50 && "$found_50pct" == "false" ]]; then
      time_50pct=$elapsed
      found_50pct=true
    fi
    
    if [[ $found_count -ge $pct_90 && "$found_90pct" == "false" ]]; then
      time_90pct=$elapsed
      found_90pct=true
    fi
    
    if [[ $found_count -ge $total_nodes && "$found_100pct" == "false" ]]; then
      time_100pct=$elapsed
      found_100pct=true
      break
    fi
    
    sleep "$POLL_INTERVAL"
  done
  
  # If we didn't reach thresholds, use max wait time
  if [[ "$found_50pct" == "false" ]]; then
    time_50pct=$MAX_WAIT
  fi
  if [[ "$found_90pct" == "false" ]]; then
    time_90pct=$MAX_WAIT
  fi
  if [[ "$found_100pct" == "false" ]]; then
    time_100pct=$MAX_WAIT
  fi
  
  echo "$time_50pct,$time_90pct,$time_100pct"
}

# Create temp directory
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

# Generate test file
TEST_FILE="$TEMP_DIR/test_${PAYLOAD_SIZE}.bin"
dd if=/dev/urandom of="$TEST_FILE" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
DATA_B64=$(base64 -w 0 < "$TEST_FILE")

# Initialize CSV output
echo "system,n_nodes,time_to_50pct_s,time_to_90pct_s,time_to_100pct_s" > "$OUTPUT_FILE"

# Get node lists and verify counts match
OUR_NODES=($(get_our_nodes))
SWARM_NODES=($(get_swarm_nodes))

OUR_NODE_COUNT=${#OUR_NODES[@]}
SWARM_NODE_COUNT=${#SWARM_NODES[@]}

# Verify counts match
if [[ $OUR_NODE_COUNT -ne $SWARM_NODE_COUNT ]]; then
  echo -e "${RED}Error: Node count mismatch detected!${NC}" >&2
  echo "  Our system: $OUR_NODE_COUNT nodes" >&2
  echo "  Swarm: $SWARM_NODE_COUNT nodes" >&2
  echo "  This should not happen if --auto-start was used." >&2
  exit 1
fi

echo "Both systems have $OUR_NODE_COUNT nodes (including bootstrap)"
echo ""

# Test our system
if [[ $OUR_NODE_COUNT -gt 0 ]]; then
  echo -e "${BLUE}Testing our system propagation...${NC}"
  
  # Upload to bootstrap
  echo "  Uploading to bootstrap..."
  CID=$(upload_our_system "$DATA_B64")
  if [[ -z "$CID" ]]; then
    echo -e "  ${RED}✗ Upload failed${NC}"
  else
    echo "  Uploaded CID: $CID"
    
    # Measure propagation (exclude bootstrap from nodes to check)
    NODES_TO_CHECK=()
    for node in "${OUR_NODES[@]}"; do
      if [[ "$node" != "bootstrap" && "$node" != "fall25-bootstrap" ]]; then
        NODES_TO_CHECK+=("$node")
      fi
    done
    
    echo "  Monitoring propagation to ${#NODES_TO_CHECK[@]} nodes..."
    TIMES=$(measure_our_propagation "$CID" "${NODES_TO_CHECK[@]}")
    time_50=0 time_90=0 time_100=0
    IFS=',' read -r time_50 time_90 time_100 <<< "$TIMES"
    
    echo "  Results: 50%=${time_50}s, 90%=${time_90}s, 100%=${time_100}s"
    echo "our_system,$OUR_NODE_COUNT,$time_50,$time_90,$time_100" >> "$OUTPUT_FILE"
  fi
fi

# Test Swarm
if [[ $SWARM_NODE_COUNT -gt 0 ]]; then
  echo -e "\n${BLUE}Testing Swarm propagation...${NC}"
  
  # Upload to bootstrap
  echo "  Uploading to bootstrap..."
  HASH=$(upload_swarm "$TEST_FILE")
  if [[ -z "$HASH" || "$HASH" == "ERROR"* ]]; then
    echo -e "  ${RED}✗ Upload failed${NC}"
  else
    echo "  Uploaded hash: $HASH"
    
    # Measure propagation (exclude bootstrap)
    NODES_TO_CHECK=()
    for node in "${SWARM_NODES[@]}"; do
      if [[ "$node" != "swarm-bootstrap" ]]; then
        NODES_TO_CHECK+=("$node")
      fi
    done
    
    echo "  Monitoring propagation to ${#NODES_TO_CHECK[@]} nodes..."
    TIMES=$(measure_swarm_propagation "$HASH" "${NODES_TO_CHECK[@]}")
    time_50=0 time_90=0 time_100=0
    IFS=',' read -r time_50 time_90 time_100 <<< "$TIMES"
    
    echo "  Results: 50%=${time_50}s, 90%=${time_90}s, 100%=${time_100}s"
    echo "swarm,$SWARM_NODE_COUNT,$time_50,$time_90,$time_100" >> "$OUTPUT_FILE"
  fi
fi

echo ""
echo "=========================================="
echo "Test Complete"
echo "=========================================="
echo "Results saved to: $OUTPUT_FILE"
echo ""
echo "To view results:"
echo "  cat $OUTPUT_FILE"
