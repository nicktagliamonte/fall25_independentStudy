#!/usr/bin/env bash
set -euo pipefail

# Purpose: Comprehensive comparison test orchestrator for our system vs Swarm
# Usage: ./scripts/scenarios/swarm_comparison_test.sh [options]
#   --nodes <list>        Comma-separated node counts (default: 10,20,40)
#   --payload-sizes <list> Comma-separated payload sizes in bytes (default: 1024,10240,102400,1048576)
#   --iterations <n>      Iterations per test (default: 5)
#   --output-dir <dir>    Output directory for results (default: ./test_results_<timestamp>)
#   --skip-cleanup        Don't stop containers after tests (useful for debugging)

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
NODES="10,20,40"
PAYLOAD_SIZES="1024,10240,102400,1048576"  # 1KB, 10KB, 100KB, 1MB
ITERATIONS=5
OUTPUT_DIR=""
SKIP_CLEANUP=false

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --nodes)
      NODES="$2"
      shift 2
      ;;
    --payload-sizes)
      PAYLOAD_SIZES="$2"
      shift 2
      ;;
    --iterations)
      ITERATIONS="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --skip-cleanup)
      SKIP_CLEANUP=true
      shift
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --nodes <list>        Comma-separated node counts (default: 10,20,40)"
      echo "  --payload-sizes <list> Comma-separated payload sizes in bytes (default: 1024,10240,102400,1048576)"
      echo "  --iterations <n>      Iterations per test (default: 5)"
      echo "  --output-dir <dir>    Output directory (default: ./test_results_<timestamp>)"
      echo "  --skip-cleanup        Don't stop containers after tests"
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

# Create output directory
if [[ -z "$OUTPUT_DIR" ]]; then
  TIMESTAMP=$(date +%Y%m%d_%H%M%S)
  OUTPUT_DIR="$ROOT_DIR/test_results_$TIMESTAMP"
fi

mkdir -p "$OUTPUT_DIR"
echo "Output directory: $OUTPUT_DIR"

# Convert comma-separated lists to arrays
IFS=',' read -ra NODE_COUNTS <<< "$NODES"
IFS=',' read -ra PAYLOAD_ARRAY <<< "$PAYLOAD_SIZES"

echo "=========================================="
echo "Swarm Comparison Test Suite"
echo "=========================================="
echo "Node counts: ${NODE_COUNTS[*]}"
echo "Payload sizes: ${PAYLOAD_ARRAY[*]} bytes"
echo "Iterations per test: $ITERATIONS"
echo "Output directory: $OUTPUT_DIR"
echo ""

# Function to cleanup containers
cleanup_containers() {
  if [[ "$SKIP_CLEANUP" == "true" ]]; then
    echo -e "${YELLOW}Skipping cleanup (--skip-cleanup)${NC}"
    return
  fi
  
  echo -e "\n${BLUE}Cleaning up containers...${NC}"
  
  # Stop Swarm first (it uses the network as external)
  if docker-compose -f "$ROOT_DIR/docker-compose.swarm.yml" ps 2>/dev/null | grep -q "Up"; then
    echo "  Stopping Swarm containers..."
    docker-compose -f "$ROOT_DIR/docker-compose.swarm.yml" stop >/dev/null 2>&1 || true
    docker-compose -f "$ROOT_DIR/docker-compose.swarm.yml" rm -f >/dev/null 2>&1 || true
  fi
  
  # Stop our system (this will try to remove the network, but that's okay)
  if docker-compose -f "$ROOT_DIR/docker-compose.yml" ps 2>/dev/null | grep -q "Up"; then
    echo "  Stopping our system containers..."
    # Use stop + rm instead of down to avoid network removal issues
    docker-compose -f "$ROOT_DIR/docker-compose.yml" stop >/dev/null 2>&1 || true
    docker-compose -f "$ROOT_DIR/docker-compose.yml" rm -f >/dev/null 2>&1 || true
  fi
  
  echo "  Cleanup complete"
}

# Function to ensure clean state before starting
ensure_clean_state() {
  echo -e "  ${CYAN}Ensuring clean state...${NC}"
  
  # Stop Swarm first (uses network as external)
  if docker-compose -f "$ROOT_DIR/docker-compose.swarm.yml" ps 2>/dev/null | grep -q "Up"; then
    docker-compose -f "$ROOT_DIR/docker-compose.swarm.yml" stop >/dev/null 2>&1 || true
    docker-compose -f "$ROOT_DIR/docker-compose.swarm.yml" rm -f >/dev/null 2>&1 || true
  fi
  
  # Stop our system
  if docker-compose -f "$ROOT_DIR/docker-compose.yml" ps 2>/dev/null | grep -q "Up"; then
    docker-compose -f "$ROOT_DIR/docker-compose.yml" stop >/dev/null 2>&1 || true
    docker-compose -f "$ROOT_DIR/docker-compose.yml" rm -f >/dev/null 2>&1 || true
  fi
  
  # Ensure network exists (created by our system's docker-compose)
  if ! docker network inspect fall25_independentstudy_node-network >/dev/null 2>&1; then
    echo "  Creating shared network..."
    docker network create --driver bridge --subnet 172.20.0.0/16 fall25_independentstudy_node-network 2>/dev/null || true
  fi
  
  sleep 2  # Brief pause for cleanup to complete
}

# Function to wait for system to stabilize
wait_for_stabilization() {
  local system="$1"
  local nodes="$2"
  local max_wait=60  # 1 minute max wait
  local check_interval=2
  
  echo -e "  ${CYAN}Waiting for $system to stabilize (max ${max_wait}s)...${NC}"
  
  if [[ "$system" == "our_system" ]]; then
    # Wait for bootstrap to be ready
    for i in $(seq 1 $max_wait); do
      if docker-compose -f "$ROOT_DIR/docker-compose.yml" exec -T bootstrap curl -sf "http://\$(jq -r .addr /app/logs/bootstrap.json)/health" >/dev/null 2>&1; then
        # Check that we have the expected number of nodes
        local running_nodes=$(docker-compose -f "$ROOT_DIR/docker-compose.yml" ps --services 2>/dev/null | grep -E '^(bootstrap|node)' | wc -l)
        if [[ $running_nodes -ge $nodes ]]; then
          echo "    $system ready after ${i}s"
          sleep 2  # Brief extra stabilization time
          return 0
        fi
      fi
      if [[ $((i % 10)) -eq 0 ]]; then
        echo "    Still waiting... (${i}s/${max_wait}s)"
      fi
      sleep $check_interval
    done
  elif [[ "$system" == "swarm" ]]; then
    # Wait for Swarm bootstrap
    for i in $(seq 1 $max_wait); do
      if curl -sf "http://172.20.0.200:8500/" >/dev/null 2>&1; then
        local running_nodes=$(docker-compose -f "$ROOT_DIR/docker-compose.swarm.yml" ps --services 2>/dev/null | grep -E '^(swarm-bootstrap|swarm-node)' | wc -l)
        if [[ $running_nodes -ge $nodes ]]; then
          echo "    $system ready after ${i}s"
          sleep 2  # Brief extra stabilization time
          return 0
        fi
      fi
      if [[ $((i % 10)) -eq 0 ]]; then
        echo "    Still waiting... (${i}s/${max_wait}s)"
      fi
      sleep $check_interval
    done
  fi
  
  echo -e "    ${YELLOW}Warning: $system may not be fully ready after ${max_wait}s${NC}"
  return 0
}

# Function to run upload test
run_upload_test() {
  local node_count="$1"
  local output_file="$OUTPUT_DIR/upload_n${node_count}.csv"
  
  echo -e "\n${GREEN}Running upload latency test (N=$node_count)...${NC}"
  
  "$ROOT_DIR/scripts/scenarios/swarm_upload_test.sh" \
    --iterations "$ITERATIONS" \
    --output "$output_file" \
    2>&1 | tee "$OUTPUT_DIR/upload_n${node_count}.log"
  
  if [[ -f "$output_file" ]]; then
    echo -e "  ${GREEN}✓ Upload test complete: $output_file${NC}"
    return 0
  else
    echo -e "  ${RED}✗ Upload test failed${NC}"
    return 1
  fi
}

# Function to run download test
run_download_test() {
  local node_count="$1"
  local output_file="$OUTPUT_DIR/download_n${node_count}.csv"
  
  echo -e "\n${GREEN}Running download latency test (N=$node_count)...${NC}"
  
  "$ROOT_DIR/scripts/scenarios/swarm_download_test.sh" \
    --iterations "$ITERATIONS" \
    --output "$output_file" \
    2>&1 | tee "$OUTPUT_DIR/download_n${node_count}.log"
  
  if [[ -f "$output_file" ]]; then
    echo -e "  ${GREEN}✓ Download test complete: $output_file${NC}"
    return 0
  else
    echo -e "  ${RED}✗ Download test failed${NC}"
    return 1
  fi
}

# Function to aggregate results
aggregate_results() {
  echo -e "\n${BLUE}Aggregating results...${NC}"
  
  # Aggregate upload results
  local upload_agg="$OUTPUT_DIR/upload_aggregated.csv"
  echo "system,node_count,payload_size,iteration,latency_ms" > "$upload_agg"
  
  for node_count in "${NODE_COUNTS[@]}"; do
    local upload_file="$OUTPUT_DIR/upload_n${node_count}.csv"
    if [[ -f "$upload_file" ]]; then
      # Add node_count column and append (skip header)
      tail -n +2 "$upload_file" | while IFS=',' read -r system payload_size iteration latency_ms; do
        # Skip error rows
        if [[ "$latency_ms" != "ERROR" ]]; then
          echo "$system,$node_count,$payload_size,$iteration,$latency_ms"
        fi
      done >> "$upload_agg"
    fi
  done
  
  # Aggregate download results
  local download_agg="$OUTPUT_DIR/download_aggregated.csv"
  echo "system,node_count,payload_size,iteration,ttfb_ms,total_ms" > "$download_agg"
  
  for node_count in "${NODE_COUNTS[@]}"; do
    local download_file="$OUTPUT_DIR/download_n${node_count}.csv"
    if [[ -f "$download_file" ]]; then
      # Add node_count column and append (skip header)
      tail -n +2 "$download_file" | while IFS=',' read -r system payload_size iteration ttfb_ms total_ms; do
        # Skip error rows
        if [[ "$ttfb_ms" != "ERROR" && "$total_ms" != "ERROR" ]]; then
          echo "$system,$node_count,$payload_size,$iteration,$ttfb_ms,$total_ms"
        fi
      done >> "$download_agg"
    fi
  done
  
  echo "  Aggregated upload results: $upload_agg"
  echo "  Aggregated download results: $download_agg"
}

# Function to generate summary report
generate_summary_report() {
  local report_file="$OUTPUT_DIR/summary_report.txt"
  
  echo -e "\n${BLUE}Generating summary report...${NC}"
  
  {
    echo "=========================================="
    echo "Swarm Comparison Test Summary Report"
    echo "=========================================="
    echo "Generated: $(date)"
    echo ""
    echo "Test Configuration:"
    echo "  Node counts: ${NODE_COUNTS[*]}"
    echo "  Payload sizes: ${PAYLOAD_ARRAY[*]} bytes"
    echo "  Iterations per test: $ITERATIONS"
    echo ""
    
    # Upload test summary
    echo "Upload Latency Test Results:"
    echo "----------------------------"
    for node_count in "${NODE_COUNTS[@]}"; do
      local upload_file="$OUTPUT_DIR/upload_n${node_count}.csv"
      if [[ -f "$upload_file" ]]; then
        echo ""
        echo "  Node count: $node_count"
        echo "    Results file: upload_n${node_count}.csv"
        
        # Count successful tests
        local our_count=$(tail -n +2 "$upload_file" | grep "^our_system," | grep -v "ERROR" | wc -l)
        local swarm_count=$(tail -n +2 "$upload_file" | grep "^swarm," | grep -v "ERROR" | wc -l)
        echo "    Our system: $our_count successful uploads"
        echo "    Swarm: $swarm_count successful uploads"
      else
        echo "  Node count: $node_count - No results file found"
      fi
    done
    
    # Download test summary
    echo ""
    echo "Download Latency Test Results:"
    echo "------------------------------"
    for node_count in "${NODE_COUNTS[@]}"; do
      local download_file="$OUTPUT_DIR/download_n${node_count}.csv"
      if [[ -f "$download_file" ]]; then
        echo ""
        echo "  Node count: $node_count"
        echo "    Results file: download_n${node_count}.csv"
        
        # Count successful tests
        local our_count=$(tail -n +2 "$download_file" | grep "^our_system," | grep -v "ERROR" | wc -l)
        local swarm_count=$(tail -n +2 "$download_file" | grep "^swarm," | grep -v "ERROR" | wc -l)
        echo "    Our system: $our_count successful downloads"
        echo "    Swarm: $swarm_count successful downloads"
      else
        echo "  Node count: $node_count - No results file found"
      fi
    done
    
    echo ""
    echo "=========================================="
    echo "All results saved to: $OUTPUT_DIR"
    echo "=========================================="
  } > "$report_file"
  
  cat "$report_file"
  echo ""
  echo "  Summary report: $report_file"
}

# Trap to cleanup on exit
trap cleanup_containers EXIT

# Main test loop
for node_count in "${NODE_COUNTS[@]}"; do
  echo ""
  echo "=========================================="
  echo "Testing with $node_count nodes"
  echo "=========================================="
  
  # Ensure clean state before starting
  ensure_clean_state
  
  # Start our system
  echo -e "\n${BLUE}Step 1: Starting our system ($node_count nodes)...${NC}"
  if ! "$ROOT_DIR/scripts/docker/start.sh" "$node_count" >"$OUTPUT_DIR/our_startup_n${node_count}.log" 2>&1; then
    echo -e "${RED}Failed to start our system${NC}" >&2
    echo "  Check log: $OUTPUT_DIR/our_startup_n${node_count}.log" >&2
    cleanup_containers
    continue
  fi
  
  # Start Swarm
  echo -e "\n${BLUE}Step 2: Starting Swarm ($node_count nodes)...${NC}"
  if ! "$ROOT_DIR/scripts/docker/swarm/start.sh" "$node_count" >"$OUTPUT_DIR/swarm_startup_n${node_count}.log" 2>&1; then
    echo -e "${RED}Failed to start Swarm${NC}" >&2
    cleanup_containers
    continue
  fi
  
  # Wait for both systems to stabilize
  echo -e "\n${BLUE}Step 3: Waiting for systems to stabilize...${NC}"
  wait_for_stabilization "our_system" "$node_count"
  wait_for_stabilization "swarm" "$node_count"
  
  # Run upload test
  echo -e "\n${BLUE}Step 4: Running upload latency test...${NC}"
  run_upload_test "$node_count" || echo -e "${YELLOW}Upload test had errors, continuing...${NC}"
  
  # Run download test
  echo -e "\n${BLUE}Step 5: Running download latency test...${NC}"
  run_download_test "$node_count" || echo -e "${YELLOW}Download test had errors, continuing...${NC}"
  
  # Cleanup for next iteration (unless it's the last one)
  if [[ "$node_count" != "${NODE_COUNTS[-1]}" ]]; then
    cleanup_containers
    sleep 5  # Brief pause between test runs
  fi
done

# Aggregate results
aggregate_results

# Generate summary report
generate_summary_report

echo ""
echo "=========================================="
echo "Test Suite Complete!"
echo "=========================================="
echo "Results directory: $OUTPUT_DIR"
echo ""
echo "Files generated:"
echo "  - upload_n<N>.csv: Upload latency results for N nodes"
echo "  - download_n<N>.csv: Download latency results for N nodes"
echo "  - upload_aggregated.csv: All upload results combined"
echo "  - download_aggregated.csv: All download results combined"
echo "  - summary_report.txt: Test summary report"
echo ""

# Final cleanup
cleanup_containers

echo -e "${GREEN}All tests complete!${NC}"
