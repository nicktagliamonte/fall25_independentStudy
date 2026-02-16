#!/usr/bin/env bash
set -euo pipefail

# Purpose: Monitor Docker container resource usage during test execution
# Usage: ./scripts/monitoring/resource_monitor.sh [options]
#   --containers <list>  Comma-separated container names/patterns (default: auto-detect)
#   --interval <s>       Polling interval in seconds (default: 1)
#   --output <file>      Output CSV file (default: resource_usage.csv)
#   --duration <s>       Duration to monitor in seconds (default: run until interrupted)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source error handler
source "$ROOT_DIR/scripts/utils/error_handler.sh"

# Initialize error logging
RUN_ID="${RUN_ID:-$(date +%s)}"
ERROR_LOG_DIR="artifacts/swarm_tests/$RUN_ID"
export RUN_ID ERROR_LOG_DIR
mkdir -p "$ERROR_LOG_DIR"

# Default values
CONTAINERS=""
INTERVAL=1
OUTPUT_FILE="resource_usage.csv"
DURATION=0  # 0 means run until interrupted

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --containers)
      CONTAINERS="$2"
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
    --duration)
      DURATION="$2"
      shift 2
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --containers <list>  Comma-separated container names/patterns (default: auto-detect)"
      echo "  --interval <s>       Polling interval in seconds (default: 1)"
      echo "  --output <file>      Output CSV file (default: resource_usage.csv)"
      echo "  --duration <s>       Duration to monitor in seconds (default: run until interrupted)"
      echo ""
      echo "Examples:"
      echo "  # Monitor all containers, run until Ctrl+C"
      echo "  $0"
      echo ""
      echo "  # Monitor specific containers for 60 seconds"
      echo "  $0 --containers bootstrap,node2,swarm-bootstrap --duration 60"
      echo ""
      echo "  # Monitor with 0.5s interval"
      echo "  $0 --interval 0.5 --duration 120"
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

# Function to detect containers to monitor
detect_containers() {
  local containers=()
  
  if [[ -n "$CONTAINERS" ]]; then
    # Use provided container list
    IFS=',' read -ra CONTAINER_ARRAY <<< "$CONTAINERS"
    for pattern in "${CONTAINER_ARRAY[@]}"; do
      # Check if it's a pattern or exact name
      if docker ps --format '{{.Names}}' | grep -q "^${pattern}$"; then
        containers+=("$pattern")
      elif docker ps --format '{{.Names}}' | grep -q "${pattern}"; then
        # Pattern match
        docker ps --format '{{.Names}}' | grep "${pattern}" | while read -r name; do
          containers+=("$name")
        done
      fi
    done
  else
    # Auto-detect: find all our system and Swarm containers
    while IFS= read -r name; do
      if [[ "$name" =~ ^(fall25-|swarm-) ]]; then
        containers+=("$name")
      fi
    done < <(docker ps --format '{{.Names}}')
  fi
  
  # Remove duplicates and sort
  printf '%s\n' "${containers[@]}" | sort -u
}

# Function to parse memory string (e.g., "123.45MiB / 512MiB" -> 123.45)
parse_memory_mb() {
  local mem_str="$1"
  # Extract the first part before " / " (e.g., "123.45MiB")
  local mem_value_str=$(echo "$mem_str" | awk -F' / ' '{print $1}')
  
  # Extract number and unit
  local mem_value=$(echo "$mem_value_str" | sed 's/[^0-9.]//g')
  local mem_unit=$(echo "$mem_value_str" | sed 's/[0-9.]//g' | tr -d ' ')
  
  if [[ -z "$mem_value" || -z "$mem_unit" ]]; then
    echo "0"
    return
  fi
  
  # Convert to MB
  case "$mem_unit" in
    *KiB|*KB)
      echo "scale=2; $mem_value / 1024" | bc -l 2>/dev/null || echo "0"
      ;;
    *MiB|*MB)
      echo "$mem_value"
      ;;
    *GiB|*GB)
      echo "scale=2; $mem_value * 1024" | bc -l 2>/dev/null || echo "0"
      ;;
    *)
      echo "0"
      ;;
  esac
}

# Function to parse network I/O (e.g., "1.2GB / 3.4GB" -> bytes)
parse_network_bytes() {
  local net_str="$1"
  local direction="$2"  # "sent" or "recv"
  
  if [[ -z "$net_str" || "$net_str" == "-" ]]; then
    echo "0"
    return
  fi
  
  if [[ "$direction" == "sent" ]]; then
    local value=$(echo "$net_str" | awk -F' / ' '{print $1}' | tr -d ' ')
  else
    local value=$(echo "$net_str" | awk -F' / ' '{print $2}' | tr -d ' ')
  fi
  
  if [[ -z "$value" || "$value" == "-" ]]; then
    echo "0"
    return
  fi
  
  # Extract number and unit
  local num=$(echo "$value" | sed 's/[^0-9.]//g')
  local unit=$(echo "$value" | sed 's/[0-9.]//g')
  
  if [[ -z "$num" ]]; then
    echo "0"
    return
  fi
  
  # Convert to bytes
  case "$unit" in
    B)
      echo "$num" | awk '{printf "%.0f", $1}'
      ;;
    KB|KiB)
      echo "$num" | awk '{printf "%.0f", $1 * 1024}'
      ;;
    MB|MiB)
      echo "$num" | awk '{printf "%.0f", $1 * 1024 * 1024}'
      ;;
    GB|GiB)
      echo "$num" | awk '{printf "%.0f", $1 * 1024 * 1024 * 1024}'
      ;;
    *)
      echo "0"
      ;;
  esac
}

# Function to get container stats
get_container_stats() {
  local container="$1"
  local timestamp=$(date +%s.%N)
  
  # Get container name if we have an ID
  local container_name="$container"
  if ! docker ps --format '{{.Names}}' | grep -q "^${container}$"; then
    # Try to get name from ID
    container_name=$(docker ps --format '{{.Names}}' --filter "id=$container" 2>/dev/null | head -1)
    if [[ -z "$container_name" ]]; then
      container_name="$container"
    fi
  fi
  
  # Get stats using docker stats (one-shot, no-stream)
  # Format: CPUPerc MemUsage NetIO (tab-separated)
  local stats=$(docker stats --no-stream --format "{{.CPUPerc}}\t{{.MemUsage}}\t{{.NetIO}}" "$container" 2>/dev/null || echo "")
  
  if [[ -z "$stats" ]]; then
    return 1
  fi
  
  # Parse stats - docker stats returns tab-separated values
  local cpu_pct=$(echo "$stats" | awk -F'\t' '{print $1}' | sed 's/%//' | tr -d ' ')
  local mem_usage=$(echo "$stats" | awk -F'\t' '{print $2}')
  local net_io=$(echo "$stats" | awk -F'\t' '{print $3}')
  
  # Parse memory to MB (format: "123.45MiB / 512MiB")
  local mem_mb=$(parse_memory_mb "$mem_usage")
  
  # Parse network I/O to bytes (format: "1.2GB / 3.4GB")
  local net_sent=$(parse_network_bytes "$net_io" "sent")
  local net_recv=$(parse_network_bytes "$net_io" "recv")
  
  # Output CSV line
  echo "$timestamp,$container_name,$cpu_pct,$mem_mb,$net_sent,$net_recv"
}

# Check for required tools
if ! command -v bc >/dev/null 2>&1; then
  echo "Error: 'bc' command not found. Please install it." >&2
  exit 1
fi

# Detect containers to monitor
echo -e "${BLUE}Detecting containers to monitor...${NC}"
CONTAINER_LIST=($(detect_containers))

if [[ ${#CONTAINER_LIST[@]} -eq 0 ]]; then
  echo -e "${YELLOW}No containers found to monitor${NC}" >&2
  exit 1
fi

echo -e "${GREEN}Monitoring ${#CONTAINER_LIST[@]} container(s):${NC}"
for container in "${CONTAINER_LIST[@]}"; do
  echo "  - $container"
done
echo ""

# Initialize CSV output
echo "timestamp,container_name,cpu_pct,mem_mb,net_sent_bytes,net_recv_bytes" > "$OUTPUT_FILE"

# Signal handler for graceful shutdown
cleanup() {
  echo ""
  echo -e "${GREEN}Monitoring stopped${NC}"
  echo "Results saved to: $OUTPUT_FILE"
  exit 0
}

trap cleanup INT TERM

# Start monitoring
echo -e "${BLUE}Starting resource monitoring...${NC}"
echo "  Interval: ${INTERVAL}s"
if [[ $DURATION -gt 0 ]]; then
  echo "  Duration: ${DURATION}s"
else
  echo "  Duration: until interrupted (Ctrl+C)"
fi
echo "  Output: $OUTPUT_FILE"
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
  
  # Collect stats for each container
  for container in "${CONTAINER_LIST[@]}"; do
    # Check if container still exists
    if docker ps --format '{{.Names}}' | grep -q "^${container}$"; then
      get_container_stats "$container" >> "$OUTPUT_FILE" 2>/dev/null || true
    fi
  done
  
  ITERATION=$((ITERATION + 1))
  
  # Progress indicator every 10 iterations
  if [[ $((ITERATION % 10)) -eq 0 ]]; then
    if [[ $DURATION -gt 0 ]]; then
      ELAPSED=$(($(date +%s) - START_TIME))
      echo -e "\r${YELLOW}Monitoring... ${ELAPSED}s/${DURATION}s${NC}" >&2
    else
      echo -e "\r${YELLOW}Monitoring... ${ITERATION} samples${NC}" >&2
    fi
  fi
  
  sleep "$INTERVAL"
done

cleanup
