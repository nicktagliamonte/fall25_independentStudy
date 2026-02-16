#!/usr/bin/env bash
set -euo pipefail

# Purpose: Network convergence test - measure time for new node to integrate into network
# Usage: ./scripts/scenarios/swarm_convergence_test.sh [options]
#   --our-api <container> Our system container name (default: auto-detect bootstrap)
#   --swarm-api <addr>   Swarm API address (default: http://172.20.0.200:8500)
#   --nodes <n>          Initial number of nodes (default: 10)
#   --k-neighbors <k>   Target number of neighbors for new node (default: 4)
#   --poll-interval <s> Polling interval in seconds (default: 0.5)
#   --max-wait <s>      Maximum wait time in seconds (default: 120)
#   --output <file>     Output CSV file (default: network_convergence.csv)

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
INITIAL_NODES=10
K_NEIGHBORS=4
POLL_INTERVAL=0.5
MAX_WAIT=120
OUTPUT_FILE="network_convergence.csv"
AUTO_START=true
CLEANUP=false

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
    --nodes)
      INITIAL_NODES="$2"
      shift 2
      ;;
    --k-neighbors)
      K_NEIGHBORS="$2"
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
      echo "  --nodes <n>          Initial number of nodes (default: 10)"
      echo "  --k-neighbors <k>    Target neighbors for new node (default: 4)"
      echo "  --poll-interval <s>  Polling interval in seconds (default: 0.5)"
      echo "  --max-wait <s>       Maximum wait time in seconds (default: 120)"
      echo "  --output <file>      Output CSV file (default: network_convergence.csv)"
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

# Initialize variables
OUR_CONTAINER=""
OUR_API_ADDR=""

# Function to detect our system API address and container
detect_our_api() {
  OUR_CONTAINER=""
  OUR_API_ADDR=""
  
  if [[ -z "$OUR_API" ]]; then
    if docker ps --format '{{.Names}}' | grep -q "^fall25-bootstrap$"; then
      OUR_CONTAINER="fall25-bootstrap"
      OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
      if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
        OUR_API="http://$OUR_API_ADDR"
        return 0
      fi
    fi
    
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
  
  echo "  Waiting for bootstrap to be ready..."
  local max_wait=60
  for i in $(seq 1 $max_wait); do
    if docker-compose -f "$ROOT_DIR/docker-compose.yml" exec -T bootstrap curl -sf "http://\$(jq -r .addr /app/logs/bootstrap.json)/health" >/dev/null 2>&1; then
      echo "  Bootstrap ready after ${i}s"
      break
    fi
    sleep 1
  done
  
  detect_our_api || true
  sleep 3
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
  
  echo "  Waiting for Swarm bootstrap to be ready..."
  local max_wait=60
  for i in $(seq 1 $max_wait); do
    if curl -sf "http://172.20.0.200:8500/" >/dev/null 2>&1; then
      echo "  Swarm bootstrap ready after ${i}s"
      break
    fi
    sleep 1
  done
  
  sleep 3
  return 0
}

# Function to cleanup nodes
cleanup_nodes() {
  if [[ "$CLEANUP" == "true" ]]; then
    echo -e "\n${BLUE}Cleaning up nodes...${NC}"
    
    if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.swarm.yml" ]]; then
      if docker-compose -f docker-compose.swarm.yml ps 2>/dev/null | grep -q "Up"; then
        docker-compose -f docker-compose.swarm.yml stop >/dev/null 2>&1 || true
        docker-compose -f docker-compose.swarm.yml rm -f >/dev/null 2>&1 || true
      fi
    fi
    
    if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
      if docker-compose ps 2>/dev/null | grep -q "Up"; then
        docker-compose stop >/dev/null 2>&1 || true
        docker-compose rm -f >/dev/null 2>&1 || true
      fi
    fi
    
    echo "  Cleanup complete"
  fi
}

# Function to count running nodes
count_our_nodes() {
  local count=0
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
    count=$(docker-compose ps --services 2>/dev/null | grep -E '^(bootstrap|node)' | wc -l)
  else
    count=$(docker ps --format '{{.Names}}' | grep -E '^fall25-(bootstrap|node)' | wc -l)
  fi
  echo "${count:-0}"
}

count_swarm_nodes() {
  local count=0
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.swarm.yml" ]]; then
    # Only count services that are actually Up (not just defined)
    count=$(docker-compose -f docker-compose.swarm.yml ps --services 2>/dev/null | grep -E '^swarm-(bootstrap|node)' | while read -r svc; do
      docker-compose -f docker-compose.swarm.yml ps "$svc" 2>/dev/null | grep -q "Up" && echo "1"
    done | wc -l)
  fi
  echo "${count:-0}"
}

# Trap to cleanup on exit
trap cleanup_nodes EXIT

# Auto-start nodes if needed
if [[ "$AUTO_START" == "true" ]]; then
  OUR_CURRENT_COUNT=$(count_our_nodes)
  SWARM_CURRENT_COUNT=$(count_swarm_nodes)
  
  if [[ $OUR_CURRENT_COUNT -lt $INITIAL_NODES ]]; then
    echo -e "${YELLOW}Our system has $OUR_CURRENT_COUNT nodes, starting $INITIAL_NODES...${NC}"
    start_our_nodes "$INITIAL_NODES" || exit 1
  elif detect_our_api; then
    echo -e "${GREEN}Our system nodes are already running ($OUR_CURRENT_COUNT nodes)${NC}"
  else
    echo -e "${YELLOW}Our system nodes running but API not ready. Restarting...${NC}"
    start_our_nodes "$INITIAL_NODES" || exit 1
  fi
  
  if [[ $SWARM_CURRENT_COUNT -lt $INITIAL_NODES ]]; then
    echo -e "${YELLOW}Swarm has $SWARM_CURRENT_COUNT nodes, starting $INITIAL_NODES...${NC}"
    start_swarm_nodes "$INITIAL_NODES" || exit 1
  else
    echo -e "${GREEN}Swarm nodes are already running ($SWARM_CURRENT_COUNT nodes)${NC}"
  fi
  
  OUR_FINAL_COUNT=$(count_our_nodes)
  SWARM_FINAL_COUNT=$(count_swarm_nodes)
  
  if [[ $OUR_FINAL_COUNT -lt $INITIAL_NODES || $SWARM_FINAL_COUNT -lt $INITIAL_NODES ]]; then
    echo -e "${RED}Error: Could not start enough nodes${NC}" >&2
    exit 1
  fi
  
  echo ""
else
  OUR_CURRENT_COUNT=$(count_our_nodes)
  SWARM_CURRENT_COUNT=$(count_swarm_nodes)
  
  if [[ $OUR_CURRENT_COUNT -lt $INITIAL_NODES ]]; then
    echo -e "${RED}Error: Our system has only $OUR_CURRENT_COUNT nodes, need $INITIAL_NODES${NC}" >&2
    exit 1
  fi
  
  if ! detect_our_api; then
    echo -e "${RED}Error: Could not detect our system API address.${NC}" >&2
    exit 1
  fi
  
  if [[ $SWARM_CURRENT_COUNT -lt $INITIAL_NODES ]]; then
    echo -e "${RED}Error: Swarm has only $SWARM_CURRENT_COUNT nodes, need $INITIAL_NODES${NC}" >&2
    exit 1
  fi
fi

echo "=========================================="
echo "Network Convergence Test"
echo "=========================================="
echo "Initial nodes: $INITIAL_NODES"
echo "Target neighbors (K): $K_NEIGHBORS"
echo "Poll interval: ${POLL_INTERVAL}s"
echo "Max wait: ${MAX_WAIT}s"
echo "Output file: $OUTPUT_FILE"
echo ""

# Get list of our system nodes
get_our_nodes() {
  local nodes=()
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
    local services=$(docker-compose ps --services 2>/dev/null | grep -E '^(bootstrap|node)' || true)
    for service in $services; do
      if docker-compose ps "$service" 2>/dev/null | grep -q "Up"; then
        nodes+=("$service")
      fi
    done
  else
    local containers=$(docker ps --format '{{.Names}}' | grep -E '^fall25-(bootstrap|node)' || true)
    for container in $containers; do
      nodes+=("$container")
    done
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
    if command -v docker-compose >/dev/null 2>&1; then
      local ctrl_file="/app/logs/${node}.json"
      docker-compose exec -T "$node" jq -r '.addr // .Addr' "$ctrl_file" 2>/dev/null || echo ""
    else
      local ctrl_file="/app/logs/${node}.json"
      docker exec "$node" jq -r '.addr // .Addr' "$ctrl_file" 2>/dev/null || echo ""
    fi
  fi
}

# Get peer ID for our system node
get_our_node_peer_id() {
  local node="$1"
  local addr="$2"
  
  if [[ -z "$addr" || "$addr" == "null" ]]; then
    return 1
  fi
  
  if command -v docker-compose >/dev/null 2>&1 && [[ "$node" != "fall25-"* ]]; then
    docker-compose exec -T "$node" curl -sSf "http://$addr/id" 2>/dev/null | jq -r '.peer // empty' || echo ""
  else
    docker exec "$node" curl -sSf "http://$addr/id" 2>/dev/null | jq -r '.peer // empty' || echo ""
  fi
}

# Get neighbor count for our system node
get_our_neighbor_count() {
  local node="$1"
  local addr="$2"
  
  if [[ -z "$addr" || "$addr" == "null" ]]; then
    echo "0"
    return
  fi
  
  local neighbors_json=""
  if command -v docker-compose >/dev/null 2>&1 && [[ "$node" != "fall25-"* ]]; then
    neighbors_json=$(docker-compose exec -T "$node" curl -sSf "http://$addr/neighbors" 2>/dev/null || echo "[]")
  else
    neighbors_json=$(docker exec "$node" curl -sSf "http://$addr/neighbors" 2>/dev/null || echo "[]")
  fi
  
  echo "$neighbors_json" | jq 'length' 2>/dev/null || echo "0"
}

# Check if peer ID is in neighbors list
check_peer_in_neighbors() {
  local node="$1"
  local addr="$2"
  local target_peer="$3"
  
  if [[ -z "$addr" || "$addr" == "null" ]]; then
    return 1
  fi
  
  local neighbors_json=""
  if command -v docker-compose >/dev/null 2>&1 && [[ "$node" != "fall25-"* ]]; then
    neighbors_json=$(docker-compose exec -T "$node" curl -sSf "http://$addr/neighbors" 2>/dev/null || echo "[]")
  else
    neighbors_json=$(docker exec "$node" curl -sSf "http://$addr/neighbors" 2>/dev/null || echo "[]")
  fi
  
  echo "$neighbors_json" | jq -e --arg peer "$target_peer" '.[]? | .peer == $peer' >/dev/null 2>&1
}

# Measure convergence for our system
measure_our_convergence() {
  local new_node="$1"
  local new_peer_id="$2"
  local existing_nodes=("${@:3}")
  
  local time_to_k=0
  local time_to_discovery=0
  local time_to_stable=0
  local found_k=false
  local found_discovery=false
  local found_stable=false
  
  local start_time=$(date +%s)
  local new_node_addr=$(get_our_node_addr "$new_node")
  
  # Track neighbor counts for stability check
  local prev_counts=()
  local stable_iterations=0
  local STABLE_THRESHOLD=3  # Need 3 consecutive stable readings
  
  while [[ $(($(date +%s) - start_time)) -lt $MAX_WAIT ]]; do
    local elapsed=$(($(date +%s) - start_time))
    
    # Check if new node has K neighbors
    if [[ "$found_k" == "false" ]]; then
      local neighbor_count=$(get_our_neighbor_count "$new_node" "$new_node_addr")
      if [[ $neighbor_count -ge $K_NEIGHBORS ]]; then
        time_to_k=$elapsed
        found_k=true
      fi
    fi
    
    # Check if existing nodes have discovered the new node
    if [[ "$found_discovery" == "false" ]]; then
      local all_discovered=true
      for existing_node in "${existing_nodes[@]}"; do
        local existing_addr=$(get_our_node_addr "$existing_node")
        if ! check_peer_in_neighbors "$existing_node" "$existing_addr" "$new_peer_id"; then
          all_discovered=false
          break
        fi
      done
      
      if [[ "$all_discovered" == "true" ]]; then
        time_to_discovery=$elapsed
        found_discovery=true
      fi
    fi
    
    # Check for stability (neighbor counts don't change for STABLE_THRESHOLD iterations)
    if [[ "$found_stable" == "false" ]]; then
      local current_counts=()
      for existing_node in "${existing_nodes[@]}"; do
        local existing_addr=$(get_our_node_addr "$existing_node")
        current_counts+=($(get_our_neighbor_count "$existing_node" "$existing_addr"))
      done
      current_counts+=($(get_our_neighbor_count "$new_node" "$new_node_addr"))
      
      # Compare with previous counts
      if [[ ${#prev_counts[@]} -eq ${#current_counts[@]} ]]; then
        local counts_match=true
        for i in $(seq 0 $((${#current_counts[@]} - 1))); do
          if [[ "${prev_counts[$i]:-0}" != "${current_counts[$i]}" ]]; then
            counts_match=false
            break
          fi
        done
        
        if [[ "$counts_match" == "true" ]]; then
          stable_iterations=$((stable_iterations + 1))
          if [[ $stable_iterations -ge $STABLE_THRESHOLD ]]; then
            time_to_stable=$elapsed
            found_stable=true
          fi
        else
          stable_iterations=0
        fi
      fi
      
      prev_counts=("${current_counts[@]}")
    fi
    
    # If all metrics found, break early
    if [[ "$found_k" == "true" && "$found_discovery" == "true" && "$found_stable" == "true" ]]; then
      break
    fi
    
    sleep "$POLL_INTERVAL"
  done
  
  # Set to max wait if not found
  if [[ "$found_k" == "false" ]]; then
    time_to_k=$MAX_WAIT
  fi
  if [[ "$found_discovery" == "false" ]]; then
    time_to_discovery=$MAX_WAIT
  fi
  if [[ "$found_stable" == "false" ]]; then
    time_to_stable=$MAX_WAIT
  fi
  
  echo "$time_to_k,$time_to_discovery,$time_to_stable"
}

# Measure convergence for Swarm (simplified - Swarm v0.5.8 has limited API)
measure_swarm_convergence() {
  local new_node="$1"
  local existing_nodes=("${@:2}")
  
  # Swarm v0.5.8 doesn't expose peer connections easily via API
  # We'll use a simplified approach: check if new node can serve content
  # This is a proxy for network convergence
  
  local time_to_k=0
  local time_to_discovery=0
  local time_to_stable=0
  local found_k=false
  local found_discovery=false
  local found_stable=false
  
  local start_time=$(date +%s)
  
  # For Swarm, we'll use a heuristic: new node is "converged" when it can serve content
  # This is a simplified metric since Swarm v0.5.8 doesn't expose neighbor lists
  
  while [[ $(($(date +%s) - start_time)) -lt $MAX_WAIT ]]; do
    local elapsed=$(($(date +%s) - start_time))
    
    # Check if new node is responding (proxy for convergence)
    local new_ip=""
    if [[ "$new_node" =~ ^swarm-node([0-9]+)$ ]]; then
      local node_num="${BASH_REMATCH[1]}"
      new_ip="172.20.0.$((200 + node_num))"
    fi
    
    if [[ -n "$new_ip" ]]; then
      if curl -sSfL -m 2 "http://${new_ip}:8500/" >/dev/null 2>&1; then
        if [[ "$found_k" == "false" ]]; then
          time_to_k=$elapsed
          found_k=true
        fi
        if [[ "$found_discovery" == "false" ]]; then
          time_to_discovery=$elapsed
          found_discovery=true
        fi
        if [[ "$found_stable" == "false" && $elapsed -ge 5 ]]; then
          time_to_stable=$elapsed
          found_stable=true
        fi
      fi
    fi
    
    if [[ "$found_k" == "true" && "$found_discovery" == "true" && "$found_stable" == "true" ]]; then
      break
    fi
    
    sleep "$POLL_INTERVAL"
  done
  
  if [[ "$found_k" == "false" ]]; then
    time_to_k=$MAX_WAIT
  fi
  if [[ "$found_discovery" == "false" ]]; then
    time_to_discovery=$MAX_WAIT
  fi
  if [[ "$found_stable" == "false" ]]; then
    time_to_stable=$MAX_WAIT
  fi
  
  echo "$time_to_k,$time_to_discovery,$time_to_stable"
}

# Initialize CSV output
echo "system,n_nodes,time_to_k_neighbors_s,time_to_discovery_s,time_to_stable_s" > "$OUTPUT_FILE"

# Test our system
OUR_NODES=($(get_our_nodes))
if [[ ${#OUR_NODES[@]} -ge $INITIAL_NODES ]]; then
  echo -e "${BLUE}Testing our system convergence...${NC}"
  
  # Get baseline neighbor counts
  echo "  Measuring baseline neighbor counts..."
  for node in "${OUR_NODES[@]}"; do
    local addr=$(get_our_node_addr "$node")
    local count=$(get_our_neighbor_count "$node" "$addr")
    echo "    $node: $count neighbors"
  done
  
  # Add new node (node number INITIAL_NODES + 1)
  local new_node_num=$((INITIAL_NODES + 1))
  local new_node="node${new_node_num}"
  echo "  Adding new node: $new_node"
  
  # Start the new node using docker-compose
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
    # We need to add the node to docker-compose.yml first
    # For simplicity, we'll use the start script with INITIAL_NODES+1 nodes
    echo "  Starting additional node..."
    "$ROOT_DIR/scripts/docker/start.sh" "$new_node_num" >/dev/null 2>&1 || {
      echo -e "  ${YELLOW}Note: Could not add new node dynamically. Using existing nodes.${NC}"
      echo "our_system,$INITIAL_NODES,$MAX_WAIT,$MAX_WAIT,$MAX_WAIT" >> "$OUTPUT_FILE"
      exit 0
    }
    
    sleep 5  # Wait for new node to start
    
    # Get updated node list
    OUR_NODES=($(get_our_nodes))
    local new_node_addr=$(get_our_node_addr "$new_node")
    local new_peer_id=$(get_our_node_peer_id "$new_node" "$new_node_addr")
    
    if [[ -z "$new_peer_id" ]]; then
      echo -e "  ${YELLOW}Could not get peer ID for new node${NC}"
      echo "our_system,$INITIAL_NODES,$MAX_WAIT,$MAX_WAIT,$MAX_WAIT" >> "$OUTPUT_FILE"
    else
      echo "  New node peer ID: $new_peer_id"
      
      # Get existing nodes (exclude the new one)
      local existing_nodes=()
      for node in "${OUR_NODES[@]}"; do
        if [[ "$node" != "$new_node" ]]; then
          existing_nodes+=("$node")
        fi
      done
      
      echo "  Monitoring convergence..."
      local TIMES=$(measure_our_convergence "$new_node" "$new_peer_id" "${existing_nodes[@]}")
      time_to_k=0 time_to_discovery=0 time_to_stable=0
      IFS=',' read -r time_to_k time_to_discovery time_to_stable <<< "$TIMES"
      
      echo "  Results: K-neighbors=${time_to_k}s, Discovery=${time_to_discovery}s, Stable=${time_to_stable}s"
      local final_count=$((INITIAL_NODES + 1))
      echo "our_system,$final_count,$time_to_k,$time_to_discovery,$time_to_stable" >> "$OUTPUT_FILE"
    fi
  else
    echo -e "  ${YELLOW}Cannot add new node dynamically${NC}"
    echo "our_system,$INITIAL_NODES,$MAX_WAIT,$MAX_WAIT,$MAX_WAIT" >> "$OUTPUT_FILE"
  fi
fi

# Test Swarm (simplified due to API limitations)
SWARM_NODES=($(get_swarm_nodes))
if [[ ${#SWARM_NODES[@]} -ge $INITIAL_NODES ]]; then
  echo -e "\n${BLUE}Testing Swarm convergence...${NC}"
  echo "  Note: Swarm v0.5.8 has limited API, using simplified metrics"
  
  # For Swarm, we'll use a simplified test
  local new_node_num=$((INITIAL_NODES + 1))
  local new_node="swarm-node${new_node_num}"
  
  echo "  Adding new Swarm node..."
  "$ROOT_DIR/scripts/docker/swarm/start.sh" "$new_node_num" >/dev/null 2>&1 || {
    echo -e "  ${YELLOW}Could not add new Swarm node${NC}"
    echo "swarm,$INITIAL_NODES,$MAX_WAIT,$MAX_WAIT,$MAX_WAIT" >> "$OUTPUT_FILE"
    exit 0
  }
  
  sleep 5
  
  SWARM_NODES=($(get_swarm_nodes))
  local existing_nodes=()
  for node in "${SWARM_NODES[@]}"; do
    if [[ "$node" != "$new_node" ]]; then
      existing_nodes+=("$node")
    fi
  done
  
  echo "  Monitoring convergence..."
  local TIMES=$(measure_swarm_convergence "$new_node" "${existing_nodes[@]}")
  time_to_k=0 time_to_discovery=0 time_to_stable=0
  IFS=',' read -r time_to_k time_to_discovery time_to_stable <<< "$TIMES"
  
  echo "  Results: K-neighbors=${time_to_k}s, Discovery=${time_to_discovery}s, Stable=${time_to_stable}s"
  local final_count=$((INITIAL_NODES + 1))
  echo "swarm,$final_count,$time_to_k,$time_to_discovery,$time_to_stable" >> "$OUTPUT_FILE"
fi

echo ""
echo "=========================================="
echo "Test Complete"
echo "=========================================="
echo "Results saved to: $OUTPUT_FILE"
echo ""
echo "To view results:"
echo "  cat $OUTPUT_FILE"
