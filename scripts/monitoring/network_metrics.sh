#!/usr/bin/env bash
set -euo pipefail

# Purpose: Collect network metrics from our system and Swarm during test execution
# Usage: ./scripts/monitoring/network_metrics.sh [options]
#   --our-api <container> Our system container name (default: auto-detect)
#   --swarm-api <addr>   Swarm API address (default: http://172.20.0.200:8500)
#   --interval <s>       Polling interval in seconds (default: 1)
#   --output <file>      Output CSV file (default: network_metrics.csv)
#   --event-marker <str> Event marker to correlate with test events
#   --duration <s>       Duration to monitor in seconds (default: run until interrupted)

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
INTERVAL=1
OUTPUT_FILE="network_metrics.csv"
EVENT_MARKER=""
DURATION=0  # 0 means run until interrupted

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
    --interval)
      INTERVAL="$2"
      shift 2
      ;;
    --output)
      OUTPUT_FILE="$2"
      shift 2
      ;;
    --event-marker)
      EVENT_MARKER="$2"
      shift 2
      ;;
    --duration)
      DURATION="$2"
      shift 2
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --our-api <container> Our system container name (default: auto-detect)"
      echo "  --swarm-api <addr>   Swarm API address (default: http://172.20.0.200:8500)"
      echo "  --interval <s>       Polling interval in seconds (default: 1)"
      echo "  --output <file>      Output CSV file (default: network_metrics.csv)"
      echo "  --event-marker <str> Event marker to correlate with test events"
      echo "  --duration <s>       Duration to monitor in seconds (default: run until interrupted)"
      echo ""
      echo "Examples:"
      echo "  # Monitor during test execution"
      echo "  $0 --event-marker 'upload_test_start' &"
      echo ""
      echo "  # Monitor for 60 seconds"
      echo "  $0 --duration 60 --output test_metrics.csv"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Check Docker and required tools
if ! check_docker; then
  exit 1
fi

if ! check_required_tools jq; then
  exit 1
fi

# Function to detect our system nodes
detect_our_nodes() {
  local nodes=()
  
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
  
  echo "${nodes[@]}"
}

# Function to detect Swarm nodes
detect_swarm_nodes() {
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

# Function to get control address for our system node
get_our_node_addr() {
  local node="$1"
  
  if [[ "$node" == "bootstrap" ]] || [[ "$node" == "fall25-bootstrap" ]]; then
    # Try to detect bootstrap API
    if [[ -n "$OUR_API" ]]; then
      echo "$OUR_API" | sed 's|http://||'
      return
    fi
    
    if command -v docker-compose >/dev/null 2>&1; then
      docker-compose exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo ""
    else
      docker exec fall25-bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo ""
    fi
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

# Function to get our system metrics
get_our_metrics() {
  local node="$1"
  local addr="$2"
  
  if [[ -z "$addr" || "$addr" == "null" ]]; then
    return 1
  fi
  
  local metrics_json=""
  if command -v docker-compose >/dev/null 2>&1 && [[ "$node" != "fall25-"* ]]; then
    if ! metrics_json=$(with_timeout 5 docker-compose exec -T "$node" curl -sSf "http://$addr/metrics" 2>/dev/null); then
      log_warning "Failed to fetch metrics from node" "node: $node, addr: $addr"
      metrics_json="{}"
    fi
  else
    if ! metrics_json=$(with_timeout 5 docker exec "$node" curl -sSf "http://$addr/metrics" 2>/dev/null); then
      log_warning "Failed to fetch metrics from node" "node: $node, addr: $addr"
      metrics_json="{}"
    fi
  fi
  
  if [[ -z "$metrics_json" || "$metrics_json" == "{}" ]]; then
    return 1
  fi
  
  # Also get neighbor count
  local neighbors_json=""
  if command -v docker-compose >/dev/null 2>&1 && [[ "$node" != "fall25-"* ]]; then
    if ! neighbors_json=$(with_timeout 5 docker-compose exec -T "$node" curl -sSf "http://$addr/neighbors" 2>/dev/null); then
      log_warning "Failed to fetch neighbors from node" "node: $node, addr: $addr"
      neighbors_json="[]"
    fi
  else
    if ! neighbors_json=$(with_timeout 5 docker exec "$node" curl -sSf "http://$addr/neighbors" 2>/dev/null); then
      log_warning "Failed to fetch neighbors from node" "node: $node, addr: $addr"
      neighbors_json="[]"
    fi
  fi
  
  local neighbor_count=$(echo "$neighbors_json" | jq 'length' 2>/dev/null || echo "0")
  
  # Combine metrics with neighbor count
  echo "$metrics_json" | jq -c ". + {neighbor_count: $neighbor_count}" 2>/dev/null || echo "{}"
}

# Function to get Swarm metrics (limited API)
get_swarm_metrics() {
  local node="$1"
  
  # Swarm v0.5.8 has limited API
  # We can try to get basic info and check if node is responding
  local ip=""
  if [[ "$node" == "swarm-bootstrap" ]]; then
    ip="172.20.0.200"
  elif [[ "$node" =~ ^swarm-node([0-9]+)$ ]]; then
    local node_num="${BASH_REMATCH[1]}"
    ip="172.20.0.$((200 + node_num))"
  else
    return 1
  fi
  
  # Check if node is responding
  local is_responding=0
  if check_api_endpoint "http://${ip}:8500/" 2 1 >/dev/null 2>&1; then
    is_responding=1
  fi
  
  # Swarm v0.5.8 doesn't expose detailed metrics via API
  # Return basic status
  echo "{\"status\": \"ok\", \"responding\": $is_responding, \"peer_count\": 0, \"chunk_count\": 0}"
}

# Function to collect metrics for all nodes
collect_metrics() {
  local timestamp=$(date +%s.%N)
  local event="$1"
  
  # Collect our system metrics
  local our_nodes=($(detect_our_nodes))
  for node in "${our_nodes[@]}"; do
    local addr=$(get_our_node_addr "$node")
    if [[ -n "$addr" && "$addr" != "null" ]]; then
      local metrics=$(get_our_metrics "$node" "$addr")
      if [[ -n "$metrics" && "$metrics" != "{}" ]]; then
        # Extract key metrics
        local dials_attempted=$(echo "$metrics" | jq -r '.dials_attempted // 0')
        local dials_succeeded=$(echo "$metrics" | jq -r '.dials_succeeded // 0')
        local dials_failed=$(echo "$metrics" | jq -r '.dials_failed // 0')
        local peers_pruned=$(echo "$metrics" | jq -r '.peers_pruned // 0')
        local gossip_learned=$(echo "$metrics" | jq -r '.gossip_learned // 0')
        local neighbor_count=$(echo "$metrics" | jq -r '.neighbor_count // 0')
        local restores_started=$(echo "$metrics" | jq -r '.restores_started // 0')
        local restores_ok=$(echo "$metrics" | jq -r '.restores_ok // 0')
        local restores_failed=$(echo "$metrics" | jq -r '.restores_failed // 0')
        local restore_bytes=$(echo "$metrics" | jq -r '.restore_bytes // 0')
        
        echo "$timestamp,our_system,$node,$dials_attempted,$dials_succeeded,$dials_failed,$peers_pruned,$gossip_learned,$neighbor_count,$restores_started,$restores_ok,$restores_failed,$restore_bytes,$event"
      fi
    fi
  done
  
  # Collect Swarm metrics
  local swarm_nodes=($(detect_swarm_nodes))
  for node in "${swarm_nodes[@]}"; do
    local metrics=$(get_swarm_metrics "$node")
    if [[ -n "$metrics" && "$metrics" != "{}" ]]; then
      local status=$(echo "$metrics" | jq -r '.status // "unknown"')
      local responding=$(echo "$metrics" | jq -r '.responding // 0')
      local peer_count=$(echo "$metrics" | jq -r '.peer_count // 0')
      local chunk_count=$(echo "$metrics" | jq -r '.chunk_count // 0')
      
      # Swarm metrics are limited, so we use 0 for most fields
      echo "$timestamp,swarm,$node,0,0,0,0,0,$peer_count,0,0,0,0,$event"
    fi
  done
}

# Detect our system API if not provided
if [[ -z "$OUR_API" ]]; then
  if docker ps --format '{{.Names}}' | grep -q "^fall25-bootstrap$"; then
    OUR_API=$(docker exec fall25-bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
    if [[ -n "$OUR_API" && "$OUR_API" != "null" ]]; then
      OUR_API="http://$OUR_API"
    fi
  elif command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
    if docker-compose ps bootstrap 2>/dev/null | grep -q "Up"; then
      OUR_API=$(docker-compose exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
      if [[ -n "$OUR_API" && "$OUR_API" != "null" ]]; then
        OUR_API="http://$OUR_API"
      fi
    fi
  fi
fi

echo "=========================================="
echo "Network Metrics Collection"
echo "=========================================="
if [[ -n "$OUR_API" ]]; then
  echo "Our System API: $OUR_API"
else
  echo "Our System: Auto-detecting nodes"
fi
echo "Swarm API: $SWARM_API"
echo "Interval: ${INTERVAL}s"
if [[ $DURATION -gt 0 ]]; then
  echo "Duration: ${DURATION}s"
else
  echo "Duration: until interrupted (Ctrl+C)"
fi
if [[ -n "$EVENT_MARKER" ]]; then
  echo "Event marker: $EVENT_MARKER"
fi
echo "Output: $OUTPUT_FILE"
echo ""

# Initialize CSV output
echo "timestamp,system,node_name,dials_attempted,dials_succeeded,dials_failed,peers_pruned,gossip_learned,neighbor_count,restores_started,restores_ok,restores_failed,restore_bytes,event_marker" > "$OUTPUT_FILE"

# Signal handler for graceful shutdown
cleanup() {
  echo ""
  echo -e "${GREEN}Metrics collection stopped${NC}"
  echo "Results saved to: $OUTPUT_FILE"
  exit 0
}

trap cleanup INT TERM

# Start monitoring
echo -e "${BLUE}Starting metrics collection...${NC}"
echo ""

START_TIME=$(date +%s)
ITERATION=0

while true; do
  # Check duration limit
  if [[ $DURATION -gt 0 ]]; then
    ELAPSED=$(($(date +%s) - START_TIME))
    if [[ $ELAPSED -ge $DURATION ]]; then
      break
    fi
  fi
  
  # Collect metrics
  collect_metrics "$EVENT_MARKER" >> "$OUTPUT_FILE" 2>/dev/null || true
  
  ITERATION=$((ITERATION + 1))
  
  # Progress indicator every 10 iterations
  if [[ $((ITERATION % 10)) -eq 0 ]]; then
    if [[ $DURATION -gt 0 ]]; then
      ELAPSED=$(($(date +%s) - START_TIME))
      echo -e "\r${YELLOW}Collecting metrics... ${ELAPSED}s/${DURATION}s (${ITERATION} samples)${NC}" >&2
    else
      echo -e "\r${YELLOW}Collecting metrics... ${ITERATION} samples${NC}" >&2
    fi
  fi
  
  sleep "$INTERVAL"
done

cleanup
