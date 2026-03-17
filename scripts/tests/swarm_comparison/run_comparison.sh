#!/usr/bin/env bash
set -euo pipefail

# Purpose: Test orchestration for vn-IPFS vs Swarm comparison.
# Starts both networks sequentially (vn-IPFS then Swarm), waits for health, runs identical tests, collects metrics, shuts down cleanly.
# Usage: ./scripts/tests/swarm_comparison/run_comparison.sh [options]
#   --nodes <list>        Node counts: 10, 50, 100, 500 (default: 10,50)
#   --payload-sizes <list> Comma-separated payload sizes in bytes (default: 1024,10240,102400,1048576)
#   --iterations <n>      Iterations per test (default: 5)
#   --batch-sizes <list>  Comma-separated batch sizes for upload (default: 1,5,10,20)
#   --output-dir <dir>    Output directory for results (default: ./test_results_<timestamp>)
#   --test-timeout <sec>  Per-test timeout in seconds (default: 600)
#   --tests <list>       Comma-separated test names to run (default: all). Use --tests list to print available tests.
#   --skip-cleanup        Don't stop containers after tests (useful for debugging)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# Source error handler
source "$ROOT_DIR/scripts/utils/error_handler.sh"

# Initialize error logging
RUN_ID="${RUN_ID:-$(date +%s)}"
ERROR_LOG_DIR="artifacts/swarm_tests/$RUN_ID"
export RUN_ID ERROR_LOG_DIR
mkdir -p "$ERROR_LOG_DIR"

# Default values (node counts must be 10, 50, 100, or 500 for both vn-IPFS and Swarm)
VALID_NODE_COUNTS="10 50 100 500"
NODES="10,50"
PAYLOAD_SIZES="1024,10240,102400,1048576"  # 1KB, 10KB, 100KB, 1MB
ITERATIONS=5
BATCH_SIZES="1,5,10,20"
OUTPUT_DIR=""
TEST_TIMEOUT_SEC=600
SKIP_CLEANUP=false
TESTS=""  # empty = run all; else comma-separated: upload,download_cold,download_warm,lookup_complexity,replication,replication_distribution,repair_time,network_hops,routing_overhead,storage_efficiency,concurrent

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
    --batch-sizes)
      BATCH_SIZES="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --test-timeout)
      TEST_TIMEOUT_SEC="$2"
      shift 2
      ;;
    --tests)
      TESTS="$2"
      shift 2
      ;;
    --skip-cleanup)
      SKIP_CLEANUP=true
      shift
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --nodes <list>        Node counts: 10, 50, 100, or 500 (default: 10,50)"
      echo "  --payload-sizes <list> Comma-separated payload sizes in bytes (default: 1024,10240,102400,1048576)"
      echo "  --iterations <n>      Iterations per test (default: 5)"
      echo "  --batch-sizes <list>  Batch sizes for upload test (default: 1,5,10,20)"
      echo "  --output-dir <dir>    Output directory (default: ./test_results_<timestamp>)"
      echo "  --test-timeout <sec>  Per-test timeout in seconds (default: 600)"
      echo "  --tests <list>       Comma-separated test names (default: all). Use --tests list to print available tests."
      echo "  --skip-cleanup        Don't stop containers after tests"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Normalize TESTS (strip spaces for matching)
TESTS="${TESTS// /}"

# Handle --tests list
if [[ "$TESTS" == "list" ]]; then
  echo "Available tests (use --tests <name> or --tests <name1,name2,...>):"
  echo "  upload, download_cold, download_warm, lookup_complexity,"
  echo "  replication, replication_distribution, repair_time, network_hops, routing_overhead,"
  echo "  storage_efficiency, concurrent"
  echo ""
  echo "Example: --tests upload,download_cold --nodes 10 --iterations 2"
  exit 0
fi

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Helper: return 0 if test should run (empty TESTS = all; else check membership)
should_run_test() {
  local name="$1"
  [[ -z "$TESTS" ]] && return 0
  [[ ",${TESTS}," == *",${name},"* ]] && return 0
  return 1
}

# Create output directory (resolve relative paths to ROOT_DIR)
if [[ -z "$OUTPUT_DIR" ]]; then
  TIMESTAMP=$(date +%Y%m%d_%H%M%S)
  OUTPUT_DIR="$ROOT_DIR/test_results_$TIMESTAMP"
elif [[ "$OUTPUT_DIR" != /* ]]; then
  OUTPUT_DIR="${ROOT_DIR}/${OUTPUT_DIR#./}"
fi

mkdir -p "$OUTPUT_DIR"
echo "Output directory: $OUTPUT_DIR"

# Convert comma-separated lists to arrays
IFS=',' read -ra NODE_COUNTS <<< "$NODES"
IFS=',' read -ra PAYLOAD_ARRAY <<< "$PAYLOAD_SIZES"
IFS=',' read -ra BATCH_SIZES_ARRAY <<< "$BATCH_SIZES"

# Validate node counts
for nc in "${NODE_COUNTS[@]}"; do
  if [[ ! " $VALID_NODE_COUNTS " =~ " $nc " ]]; then
    echo "Error: Node count '$nc' not allowed. Must be one of: $VALID_NODE_COUNTS" >&2
    exit 1
  fi
done

echo "=========================================="
echo "Swarm Comparison Test Suite"
echo "=========================================="
echo "Node counts: ${NODE_COUNTS[*]}"
echo "Payload sizes: ${PAYLOAD_ARRAY[*]} bytes"
echo "Batch sizes (upload): ${BATCH_SIZES_ARRAY[*]}"
echo "Iterations per test: $ITERATIONS"
[[ -n "$TESTS" ]] && echo "Tests (filtered): $TESTS"
echo "Output directory: $OUTPUT_DIR"
echo ""

# Run command with per-test timeout; falls back to plain run if timeout unavailable
run_with_timeout() {
  if command -v timeout >/dev/null 2>&1 && [[ -n "${TEST_TIMEOUT_SEC:-}" && "$TEST_TIMEOUT_SEC" -gt 0 ]]; then
    if ! timeout "$TEST_TIMEOUT_SEC" "$@"; then
      local ret=$?
      [[ $ret -eq 124 ]] && echo -e "${YELLOW}  (Test timed out after ${TEST_TIMEOUT_SEC}s)${NC}" >&2
      return $ret
    fi
  else
    "$@"
  fi
}

# Function to cleanup containers
cleanup_containers() {
  if [[ "$SKIP_CLEANUP" == "true" ]]; then
    echo -e "${YELLOW}Skipping cleanup (--skip-cleanup)${NC}"
    return
  fi

  echo -e "\n${BLUE}Cleaning up containers...${NC}"

  # Stop Swarm first (uses network as external) - use down to release resources fully
  if [[ -f "$ROOT_DIR/docker-compose.swarm.yml" ]] && docker-compose -f "$ROOT_DIR/docker-compose.swarm.yml" ps 2>/dev/null | grep -q "Up"; then
    echo "  Stopping Swarm..."
    docker-compose -f "$ROOT_DIR/docker-compose.swarm.yml" down >/dev/null 2>&1 || true
  fi

  # Stop vn-IPFS (our system) - check both compose files - use down to release resources fully
  for compose in "$ROOT_DIR/docker-compose.vnipfs.yml" "$ROOT_DIR/docker-compose.yml"; do
    if [[ -f "$compose" ]] && docker-compose -f "$compose" ps 2>/dev/null | grep -q "Up"; then
      echo "  Stopping vn-IPFS/our system..."
      docker-compose -f "$compose" down >/dev/null 2>&1 || true
      break
    fi
  done

  echo "  Cleanup complete"
}

# Function to ensure clean state before starting
ensure_clean_state() {
  echo -e "  ${CYAN}Ensuring clean state...${NC}"

  for compose in "$ROOT_DIR/docker-compose.swarm.yml" "$ROOT_DIR/docker-compose.vnipfs.yml" "$ROOT_DIR/docker-compose.yml"; do
    if [[ -f "$compose" ]] && docker-compose -f "$compose" ps 2>/dev/null | grep -q "Up"; then
      docker-compose -f "$compose" down >/dev/null 2>&1 || true
    fi
  done

  if ! docker network inspect fall25_independentstudy_node-network >/dev/null 2>&1; then
    echo "  Creating shared network..."
    docker network create --driver bridge --subnet 172.20.0.0/16 fall25_independentstudy_node-network 2>/dev/null || true
  fi

  sleep 2
}

# Resolve vn-IPFS compose file (vnipfs or generic)
get_vnipfs_compose() {
  [[ -f "$ROOT_DIR/docker-compose.vnipfs.yml" ]] && echo "$ROOT_DIR/docker-compose.vnipfs.yml" || echo "$ROOT_DIR/docker-compose.yml"
}

# Function to wait for system to stabilize
wait_for_stabilization() {
  local system="$1"
  local nodes="$2"
  local max_wait=90
  [[ "$system" == "swarm" ]] && max_wait=120
  local check_interval=2

  echo -e "  ${CYAN}Waiting for $system to stabilize (max ${max_wait}s)...${NC}"

  if [[ "$system" == "our_system" ]]; then
    local compose=$(get_vnipfs_compose)
    for i in $(seq 1 $max_wait); do
      if docker-compose -f "$compose" exec -T bootstrap curl -sf "http://$(docker-compose -f "$compose" exec -T bootstrap jq -r .addr /app/logs/bootstrap.json)/health" >/dev/null 2>&1; then
        local running_nodes=$(docker-compose -f "$compose" ps --services 2>/dev/null | grep -E '^(bootstrap|node)' | wc -l)
        if [[ $running_nodes -ge $nodes ]]; then
          echo "    $system ready after ${i}s"
          sleep 2
          return 0
        fi
      fi
      [[ $((i % 10)) -eq 0 ]] && echo "    Still waiting... (${i}s/${max_wait}s)"
      sleep $check_interval
    done
  elif [[ "$system" == "swarm" ]]; then
    for i in $(seq 1 $max_wait); do
      if curl -sf "http://172.20.0.200:8500/" >/dev/null 2>&1; then
        local running_nodes=$(docker-compose -f "$ROOT_DIR/docker-compose.swarm.yml" ps --services 2>/dev/null | grep -E '^(swarm-bootstrap|swarm-node)' | wc -l)
        if [[ $running_nodes -ge $nodes ]]; then
          echo "    $system ready after ${i}s"
          sleep 2
          return 0
        fi
      fi
      [[ $((i % 10)) -eq 0 ]] && echo "    Still waiting... (${i}s/${max_wait}s)"
      sleep $check_interval
    done
  fi

  echo -e "    ${YELLOW}Warning: $system may not be fully ready after ${max_wait}s${NC}"
  return 0
}

# Function to run upload test (runs once per batch size)
run_upload_test() {
  local node_count="$1"
  local any_ok=false
  
  echo -e "\n${GREEN}Running upload latency test (N=$node_count, batch_sizes: ${BATCH_SIZES_ARRAY[*]})...${NC}"
  
  for batch_size in "${BATCH_SIZES_ARRAY[@]}"; do
    local output_file="$OUTPUT_DIR/upload_n${node_count}_batch${batch_size}.csv"
    echo -e "  Batch size $batch_size..."
    run_with_timeout "$ROOT_DIR/scripts/tests/swarm_comparison/upload_test.sh" \
      --iterations "$ITERATIONS" \
      --batch-size "$batch_size" \
      --output "$output_file" \
      2>&1 | tee -a "$OUTPUT_DIR/upload_n${node_count}.log" | sed 's/^/    /'
    if [[ -f "$output_file" ]]; then
      echo -e "  ${GREEN}✓ batch_size=$batch_size: $output_file${NC}"
      any_ok=true
    else
      echo -e "  ${RED}✗ batch_size=$batch_size failed${NC}"
    fi
  done
  
  if [[ "$any_ok" == "true" ]]; then
    return 0
  else
    return 1
  fi
}

# Function to run network hops test
run_network_hops_test() {
  local output_file="$OUTPUT_DIR/network_hops_results.csv"
  echo -e "\n${GREEN}Running network hops test...${NC}"
  run_with_timeout "$ROOT_DIR/scripts/tests/swarm_comparison/network_hops_test.sh" \
    --iterations "$ITERATIONS" \
    --output "$output_file" \
    2>&1 | tee "$OUTPUT_DIR/network_hops.log" || true
  if [[ -f "$output_file" ]]; then
    echo -e "  ${GREEN}✓ Network hops test complete: $output_file${NC}"
  fi
}

# Function to run storage efficiency test
run_storage_efficiency_test() {
  local output_file="$OUTPUT_DIR/storage_efficiency_results.csv"
  echo -e "\n${GREEN}Running storage efficiency test...${NC}"
  "$ROOT_DIR/scripts/tests/swarm_comparison/storage_efficiency_test.sh" \
    --payload-size 65536 \
    --output "$output_file" \
    2>&1 | tee "$OUTPUT_DIR/storage_efficiency.log" || true
  if [[ -f "$output_file" ]]; then
    echo -e "  ${GREEN}✓ Storage efficiency test complete: $output_file${NC}"
  fi
}

# Function to run isolated lookup latency test (token routing vs provider discovery)
run_lookup_latency_test() {
  local node_count="$1"
  local output_file="$OUTPUT_DIR/lookup_latency_n${node_count}.csv"
  echo -e "\n${GREEN}Running lookup latency test (isolated token vs provider discovery)...${NC}"
  run_with_timeout "$ROOT_DIR/scripts/tests/swarm_comparison/lookup_latency_test.sh" \
    --iterations "$ITERATIONS" \
    --output "$output_file" \
    2>&1 | tee "$OUTPUT_DIR/lookup_latency_n${node_count}.log" | sed 's/^/  /' || true
  if [[ -f "$output_file" ]]; then
    echo -e "  ${GREEN}✓ Lookup latency test complete: $output_file${NC}"
  fi
}

# Function to run routing overhead test (token lookup vs provider announcement)
run_routing_overhead_test() {
  local output_file="$OUTPUT_DIR/routing_overhead_results.csv"
  echo -e "\n${GREEN}Running routing overhead test (token vs provider announce)...${NC}"
  run_with_timeout "$ROOT_DIR/scripts/tests/swarm_comparison/routing_overhead_test.sh" \
    --payload-size 10240 \
    --output "$output_file" \
    2>&1 | tee "$OUTPUT_DIR/routing_overhead.log" | sed 's/^/  /' || true
  if [[ -f "$output_file" ]]; then
    echo -e "  ${GREEN}✓ Routing overhead: $output_file${NC}"
  fi
}

# Function to run lookup complexity test (O(log N) verification; hops vs node count)
run_lookup_complexity_test() {
  local node_count="$1"
  local output_file="$OUTPUT_DIR/lookup_complexity_results.csv"
  echo -e "\n${GREEN}Running lookup complexity test (N=$node_count, hops vs log N)...${NC}"
  run_with_timeout "$ROOT_DIR/scripts/tests/swarm_comparison/lookup_complexity_test.sh" \
    --node-count "$node_count" \
    --iterations "$ITERATIONS" \
    --output "$output_file" \
    $([[ -f "$output_file" && -s "$output_file" ]] && echo "--append" || true) \
    2>&1 | tee -a "$OUTPUT_DIR/lookup_complexity.log" | sed 's/^/  /' || true
  if [[ -f "$output_file" ]]; then
    echo -e "  ${GREEN}✓ Lookup complexity: $output_file${NC}"
  fi
}

# Function to run concurrent read/write test (matrix: 1w/0r, 5w/5r, 10w/10r)
run_concurrent_test() {
  local output_file="$OUTPUT_DIR/concurrent_results.csv"
  echo -e "\n${GREEN}Running concurrent read/write test (matrix: 1w/0r, 5w/5r, 10w/10r)...${NC}"
  for nw in 1 5 10; do
    local nr
    [[ $nw -eq 1 ]] && nr=0 || nr=$nw
    echo -e "  ($nw w / $nr r)..."
    run_with_timeout "$ROOT_DIR/scripts/tests/swarm_comparison/concurrent_test.sh" \
      --concurrent-writes "$nw" \
      --concurrent-reads "$nr" \
      --output "$output_file" \
      --append \
      2>&1 | tee -a "$OUTPUT_DIR/concurrent.log" | sed 's/^/    /' || true
  done
  if [[ -f "$output_file" ]]; then
    echo -e "  ${GREEN}✓ Concurrent test complete: $output_file${NC}"
  fi
}

# Function to run replication distribution test (N/M/F vs Swarm)
run_replication_distribution_test() {
  local node_count="$1"
  local output_file="$OUTPUT_DIR/replication_distribution.csv"
  local append_flag=""
  [[ -f "$output_file" && -s "$output_file" ]] && append_flag="--append"
  echo -e "  Running replication distribution test (N=$node_count)..."
  run_with_timeout "$ROOT_DIR/scripts/tests/swarm_comparison/replication_distribution_test.sh" \
    --payload-size 65536 \
    --replicas-target 2 \
    --node-count "$node_count" \
    --output "$output_file" \
    $append_flag \
    2>&1 | tee -a "$OUTPUT_DIR/replication_distribution.log" | sed 's/^/    /' || true
  [[ -f "$output_file" ]] && echo -e "  ${GREEN}✓ Replication distribution: $output_file${NC}"
}

# Function to run repair time test
run_repair_time_test() {
  local node_count="$1"
  local output_file="$OUTPUT_DIR/repair_time_results.csv"
  local append_flag=""
  [[ -f "$output_file" && -s "$output_file" ]] && append_flag="--append"
  echo -e "  Running repair time test (N=$node_count)..."
  run_with_timeout "$ROOT_DIR/scripts/tests/swarm_comparison/repair_time_test.sh" \
    --payload-size 65536 \
    --replicas-target 2 \
    --timeout 180 \
    --node-count "$node_count" \
    --output "$output_file" \
    $append_flag \
    2>&1 | tee -a "$OUTPUT_DIR/repair_time.log" | sed 's/^/    /' || true
  [[ -f "$output_file" ]] && echo -e "  ${GREEN}✓ Repair time: $output_file${NC}"
}

# Function to run replication speed test
run_replication_test() {
  local node_count="$1"
  local output_file="$OUTPUT_DIR/replication_results.csv"
  local append_flag=""
  if [[ -f "$output_file" && -s "$output_file" ]]; then
    append_flag="--append"
  fi
  echo -e "\n${GREEN}Running replication speed test (N=$node_count)...${NC}"
  run_with_timeout "$ROOT_DIR/scripts/tests/swarm_comparison/replication_test.sh" \
    --payload-size 65536 \
    --replicas-target 2 \
    --timeout 120 \
    --node-count "$node_count" \
    --output "$output_file" \
    --record-overhead \
    $append_flag \
    2>&1 | tee -a "$OUTPUT_DIR/replication.log" | sed 's/^/  /' || true
  if [[ -f "$output_file" ]]; then
    echo -e "  ${GREEN}✓ Replication test complete: $output_file${NC}"
  fi
}

# Function to run download test (cold or warm mode)
run_download_test() {
  local node_count="$1"
  local cache_mode="$2"
  local output_file="$OUTPUT_DIR/download_n${node_count}_${cache_mode}.csv"
  
  echo -e "\n${GREEN}Running download latency test (N=$node_count, cache_mode=$cache_mode)...${NC}"
  
  run_with_timeout "$ROOT_DIR/scripts/tests/swarm_comparison/download_test.sh" \
    --iterations "$ITERATIONS" \
    --cache-mode "$cache_mode" \
    --output "$output_file" \
    2>&1 | tee "$OUTPUT_DIR/download_n${node_count}_${cache_mode}.log"
  
  if [[ -f "$output_file" ]]; then
    echo -e "  ${GREEN}✓ Download test ($cache_mode) complete: $output_file${NC}"
    return 0
  else
    echo -e "  ${RED}✗ Download test ($cache_mode) failed${NC}"
    return 1
  fi
}

# Function to aggregate results
aggregate_results() {
  echo -e "\n${BLUE}Aggregating results...${NC}"
  
  # Aggregate upload results
  local upload_agg="$OUTPUT_DIR/upload_aggregated.csv"
  echo "system,node_count,payload_size,batch_size,iteration,latency_ms,total_batch_ms" > "$upload_agg"

  for node_count in "${NODE_COUNTS[@]}"; do
    for upload_file in "$OUTPUT_DIR/upload_n${node_count}_batch"*.csv "$OUTPUT_DIR/upload_n${node_count}.csv"; do
      [[ -f "$upload_file" ]] || continue
      tail -n +2 "$upload_file" | while IFS=',' read -r system payload_size batch_size iteration latency_ms total_batch_ms; do
        if [[ "$latency_ms" != "ERROR" ]]; then
          echo "$system,$node_count,$payload_size,${batch_size:-1},$iteration,$latency_ms,${total_batch_ms:-}"
        fi
      done >> "$upload_agg"
    done
  done
  
  # Aggregate download results (cold and warm)
  local download_agg="$OUTPUT_DIR/download_aggregated.csv"
  echo "system,node_count,payload_size,iteration,cache_mode,ttfb_ms,total_ms,lookup_type" > "$download_agg"
  
  for node_count in "${NODE_COUNTS[@]}"; do
    for cache_mode in cold warm; do
      local download_file="$OUTPUT_DIR/download_n${node_count}_${cache_mode}.csv"
      if [[ -f "$download_file" ]]; then
        tail -n +2 "$download_file" | while IFS=',' read -r system payload_size iteration cache_mode_val ttfb_ms total_ms lookup_type; do
          if [[ "$ttfb_ms" != "ERROR" && "$total_ms" != "ERROR" ]]; then
            echo "$system,$node_count,$payload_size,$iteration,$cache_mode_val,$ttfb_ms,$total_ms,${lookup_type:-}"
          fi
        done >> "$download_agg"
      fi
    done
  done
  
  echo "  Aggregated upload results: $upload_agg"
  echo "  Aggregated download results: $download_agg"
  if [[ -f "$OUTPUT_DIR/replication_results.csv" ]]; then
    echo "  Replication results: $OUTPUT_DIR/replication_results.csv"
  fi
  if [[ -f "$OUTPUT_DIR/replication_distribution.csv" ]]; then
    echo "  Replication distribution (N/M/F): $OUTPUT_DIR/replication_distribution.csv"
  fi
  if [[ -f "$OUTPUT_DIR/repair_time_results.csv" ]]; then
    echo "  Repair time results: $OUTPUT_DIR/repair_time_results.csv"
  fi
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
      local found=false
      for upload_file in "$OUTPUT_DIR/upload_n${node_count}_batch"*.csv; do
        [[ -f "$upload_file" ]] || continue
        found=true
        local batch_size=$(basename "$upload_file" .csv | sed "s/upload_n${node_count}_batch//")
        echo ""
        echo "  Node count: $node_count, batch_size: $batch_size"
        echo "    Results file: $(basename "$upload_file")"
        local our_count=$(tail -n +2 "$upload_file" | grep "^our_system," | grep -v "ERROR" | wc -l)
        local swarm_count=$(tail -n +2 "$upload_file" | grep "^swarm," | grep -v "ERROR" | wc -l)
        echo "    Our system: $our_count successful uploads"
        echo "    Swarm: $swarm_count successful uploads"
      done
      [[ "$found" == "true" ]] || echo "  Node count: $node_count - No results file found"
    done
    
    # Download test summary
    echo ""
    echo "Download Latency Test Results (cold and warm):"
    echo "------------------------------"
    for node_count in "${NODE_COUNTS[@]}"; do
      for cache_mode in cold warm; do
        local download_file="$OUTPUT_DIR/download_n${node_count}_${cache_mode}.csv"
        if [[ -f "$download_file" ]]; then
          echo ""
          echo "  Node count: $node_count, cache_mode: $cache_mode"
          echo "    Results file: download_n${node_count}_${cache_mode}.csv"
          local our_count=$(tail -n +2 "$download_file" | grep "^our_system," | grep -v "ERROR" | wc -l)
          local swarm_count=$(tail -n +2 "$download_file" | grep "^swarm," | grep -v "ERROR" | wc -l)
          echo "    Our system: $our_count successful downloads"
          echo "    Swarm: $swarm_count successful downloads"
        fi
      done
      if [[ ! -f "$OUTPUT_DIR/download_n${node_count}_cold.csv" && ! -f "$OUTPUT_DIR/download_n${node_count}_warm.csv" ]]; then
        echo "  Node count: $node_count - No results file found"
      fi
    done
    
    # Replication test summary
    if [[ -f "$OUTPUT_DIR/replication_results.csv" ]]; then
      echo ""
      echo "Replication Speed Test Results:"
      echo "-------------------------------"
      echo "  Results file: replication_results.csv"
      tail -n +2 "$OUTPUT_DIR/replication_results.csv" | while IFS=',' read -r system payload nodes target time_r; do
        echo "    $system: time_to_R=$time_r (target R=$target, payload=$payload, nodes=$nodes)"
      done
    fi

    if [[ -f "$OUTPUT_DIR/routing_overhead_results.csv" ]]; then
      echo ""
      echo "Routing Overhead (Token vs Provider Announce):"
      echo "---------------------------------------------"
      tail -n +2 "$OUTPUT_DIR/routing_overhead_results.csv" | while IFS=',' read -r sys op count otype; do
        echo "    $sys $op: $count msgs ($otype)"
      done
    fi

    echo ""
    echo "=========================================="
    echo "All results saved to: $OUTPUT_DIR"
    echo "=========================================="
  } > "$report_file"
  
  cat "$report_file"
  echo ""
  echo "  Summary report: $report_file"
}

# Trap: ensure cleanup on exit
trap cleanup_containers EXIT

# Start vn-IPFS (use start_vnipfs.sh for 10,50,100,500; else start.sh)
start_vnipfs() {
  local n="$1"
  if [[ " $VALID_NODE_COUNTS " =~ " $n " ]]; then
    "$ROOT_DIR/scripts/docker/start_vnipfs.sh" "$n"
  else
    "$ROOT_DIR/scripts/docker/start.sh" "$n"
  fi
}

# Resource monitor (CPU/memory) - started before first tests, stopped after all
RESOURCE_MONITOR_PID=""

# Main test loop
for node_count in "${NODE_COUNTS[@]}"; do
  echo ""
  echo "=========================================="
  echo "Testing with $node_count nodes"
  echo "=========================================="

  ensure_clean_state

  # Step 1: Start networks sequentially (reduces memory/CPU contention during startup)
  # When only running lookup_complexity, skip Swarm to reduce resource contention for cold lookup
  SKIP_SWARM=false
  if [[ "$TESTS" == "lookup_complexity" ]]; then
    SKIP_SWARM=true
  fi

  echo -e "\n${BLUE}Step 1: Starting vn-IPFS...${NC}"
  if start_vnipfs "$node_count" >>"$OUTPUT_DIR/our_startup_n${node_count}.log" 2>&1; then
    echo "0" >"$OUTPUT_DIR/.vnipfs_${node_count}.ok"
  else
    echo "1" >"$OUTPUT_DIR/.vnipfs_${node_count}.ok"
  fi

  if [[ "$SKIP_SWARM" != "true" ]]; then
    echo -e "\n${BLUE}Step 1b: Starting Swarm...${NC}"
    if "$ROOT_DIR/scripts/docker/swarm/start.sh" "$node_count" >>"$OUTPUT_DIR/swarm_startup_n${node_count}.log" 2>&1; then
      echo "0" >"$OUTPUT_DIR/.swarm_${node_count}.ok"
    else
      echo "1" >"$OUTPUT_DIR/.swarm_${node_count}.ok"
    fi
  else
    echo "0" >"$OUTPUT_DIR/.swarm_${node_count}.ok"
  fi

  VNIPFS_OK=$(cat "$OUTPUT_DIR/.vnipfs_${node_count}.ok" 2>/dev/null || echo "1")
  SWARM_OK=$(cat "$OUTPUT_DIR/.swarm_${node_count}.ok" 2>/dev/null || echo "1")
  rm -f "$OUTPUT_DIR/.vnipfs_${node_count}.ok" "$OUTPUT_DIR/.swarm_${node_count}.ok"

  if [[ "$VNIPFS_OK" != "0" ]]; then
    echo -e "${RED}vn-IPFS failed to start. Log: $OUTPUT_DIR/our_startup_n${node_count}.log${NC}" >&2
    cleanup_containers
    continue
  fi
  if [[ "$SWARM_OK" != "0" ]]; then
    echo -e "${RED}Swarm failed to start. Log: $OUTPUT_DIR/swarm_startup_n${node_count}.log${NC}" >&2
    cleanup_containers
    continue
  fi

  [[ "$SKIP_SWARM" == "true" ]] && echo -e "  ${GREEN}vn-IPFS started (Swarm skipped for lookup_complexity-only run)${NC}" || echo -e "  ${GREEN}Both networks started (sequential)${NC}"

  # Step 2: Wait for both to be healthy (skip swarm wait when Swarm was not started)
  echo -e "\n${BLUE}Step 2: Waiting for both systems to be healthy...${NC}"
  wait_for_stabilization "our_system" "$node_count"
  [[ "$SKIP_SWARM" != "true" ]] && wait_for_stabilization "swarm" "$node_count"

  # C.2 verification: PUT on node A, /replication/status returns replica_count>=1 within 5s
  # Use 45s cap. Pass compose file for consistency.
  echo -e "\n${BLUE}Step 2b: C.2 verification (replication integration)...${NC}"
  vnipfs_compose=$(get_vnipfs_compose)
  if command -v timeout >/dev/null 2>&1; then
    timeout 45 "$ROOT_DIR/scripts/tests/swarm_comparison/verify_replication_integration.sh" --compose "$vnipfs_compose" \
      2>&1 | tee -a "$OUTPUT_DIR/verify_replication_integration.log" | sed 's/^/  /' || { echo -e "  ${YELLOW}C.2 verification had errors or timed out, continuing...${NC}"; true; }
  else
    "$ROOT_DIR/scripts/tests/swarm_comparison/verify_replication_integration.sh" --compose "$vnipfs_compose" \
      2>&1 | tee -a "$OUTPUT_DIR/verify_replication_integration.log" | sed 's/^/  /' || { echo -e "  ${YELLOW}C.2 verification had errors, continuing...${NC}"; true; }
  fi

  # Spawn resource monitor before upload/download tests (samples fall25-* and swarm-* each interval)
  if [[ -z "${RESOURCE_MONITOR_PID:-}" ]]; then
    "$ROOT_DIR/scripts/utils/resource_monitor.sh" --output "$OUTPUT_DIR/resource_usage.csv" --interval 5 &
    RESOURCE_MONITOR_PID=$!
    echo -e "  ${GREEN}Resource monitor started (PID $RESOURCE_MONITOR_PID)${NC}"
  fi

  # Step 3: Run identical test scenarios (only tests in --tests list, or all if empty)
  if should_run_test "upload"; then
    echo -e "\n${BLUE}Step 3: Running upload latency test...${NC}"
    UPLOAD_MONITOR_PID=""
    "$ROOT_DIR/scripts/utils/resource_monitor.sh" --output "$OUTPUT_DIR/resource_usage_upload_n${node_count}.csv" --interval 5 &
    UPLOAD_MONITOR_PID=$!
    run_upload_test "$node_count" || echo -e "${YELLOW}Upload test had errors, continuing...${NC}"
    if [[ -n "$UPLOAD_MONITOR_PID" ]] && kill -0 "$UPLOAD_MONITOR_PID" 2>/dev/null; then
      kill "$UPLOAD_MONITOR_PID" 2>/dev/null || true
      wait "$UPLOAD_MONITOR_PID" 2>/dev/null || true
      echo -e "  ${GREEN}Upload-phase resource monitor stopped${NC}"
    fi
  fi

  if should_run_test "download_cold"; then
    echo -e "\n${BLUE}Step 4a: Running download latency test (cold)...${NC}"
    run_download_test "$node_count" "cold" || echo -e "${YELLOW}Download test (cold) had errors, continuing...${NC}"
  fi
  if should_run_test "download_warm"; then
    echo -e "\n${BLUE}Step 4b: Running download latency test (warm)...${NC}"
    run_download_test "$node_count" "warm" || echo -e "${YELLOW}Download test (warm) had errors, continuing...${NC}"
  fi

  if should_run_test "lookup_complexity"; then
    echo -e "\n${BLUE}Step 5d: Running lookup complexity test (O(log N))...${NC}"
    echo -e "  ${CYAN}Waiting 20s for DHT to stabilize before cold lookup...${NC}"
    sleep 20
    run_lookup_complexity_test "$node_count" || echo -e "${YELLOW}Lookup complexity test had errors, continuing...${NC}"
  fi

  if should_run_test "replication"; then
    echo -e "\n${BLUE}Step 5e: Running replication speed test (time to R replicas)...${NC}"
    run_replication_test "$node_count" || echo -e "${YELLOW}Replication test had errors, continuing...${NC}"
  fi

  if should_run_test "replication_distribution"; then
    echo -e "\n${BLUE}Step 5f: Running replication distribution test (N/M/F)...${NC}"
    run_replication_distribution_test "$node_count" || echo -e "${YELLOW}Replication distribution test had errors, continuing...${NC}"
  fi

  if should_run_test "repair_time"; then
    echo -e "\n${BLUE}Step 5g: Running repair time test (after node failure)...${NC}"
    run_repair_time_test "$node_count" || echo -e "${YELLOW}Repair time test had errors, continuing...${NC}"
  fi

  if [[ "$node_count" == "${NODE_COUNTS[-1]}" ]]; then
    if should_run_test "network_hops"; then
      echo -e "\n${BLUE}Step 6: Running network hops test...${NC}"
      run_network_hops_test || echo -e "${YELLOW}Network hops test had errors, continuing...${NC}"
    fi
    if should_run_test "routing_overhead"; then
      echo -e "\n${BLUE}Step 6b: Running routing overhead test (token vs provider announce)...${NC}"
      run_routing_overhead_test || echo -e "${YELLOW}Routing overhead test had errors, continuing...${NC}"
    fi
    if should_run_test "storage_efficiency"; then
      echo -e "\n${BLUE}Step 7: Running storage efficiency test...${NC}"
      run_storage_efficiency_test || echo -e "${YELLOW}Storage efficiency test had errors, continuing...${NC}"
    fi
    if should_run_test "concurrent"; then
      echo -e "\n${BLUE}Step 8: Running concurrent read/write test...${NC}"
      run_concurrent_test || echo -e "${YELLOW}Concurrent test had errors, continuing...${NC}"
    fi
  fi

  if [[ "$node_count" != "${NODE_COUNTS[-1]}" ]]; then
    cleanup_containers
    sleep 5
  fi
done

# Stop resource monitor
if [[ -n "${RESOURCE_MONITOR_PID:-}" ]] && kill -0 "$RESOURCE_MONITOR_PID" 2>/dev/null; then
  kill "$RESOURCE_MONITOR_PID" 2>/dev/null || true
  wait "$RESOURCE_MONITOR_PID" 2>/dev/null || true
  echo -e "\n${GREEN}Resource monitor stopped${NC}"
fi

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
echo "  - upload_n<N>_batch<B>.csv: Upload latency results (N nodes, batch size B)"
echo "  - upload_network_bytes.csv: Network bytes transferred during upload (system,payload_size,batch_size,bytes_transferred)"
echo "  - download_n<N>_cold.csv, download_n<N>_warm.csv: Download latency results (cold/warm cache)"
echo "  - upload_aggregated.csv: All upload results combined"
echo "  - download_aggregated.csv: system,node_count,payload_size,iteration,cache_mode,ttfb_ms,total_ms,lookup_type"
echo "  - network_hops_results.csv: DHT lookup hops per operation (when available)"
echo "  - storage_efficiency_results.csv: disk_bytes, efficiency_ratio per system (when available)"
echo "  - replication_results.csv: system,payload_size,nodes,replicas_target,time_to_R_s[,replication_bytes] (when available)"
echo "  - replication_distribution.csv: system,node_count,near,midrange,farflung (N/M/F vs Swarm N/A)"
echo "  - repair_time_results.csv: system,node_count,repair_time_s (when available)"
echo "  - concurrent_results.csv: system,concurrent_writes,concurrent_reads,throughput_mbps,p99_latency_ms (when available)"
echo "  - lookup_latency_n<N>.csv: isolated lookup latency (token vs TTFB proxy)"
echo "  - lookup_complexity_results.csv: system,node_count,operation,hops (O(log N) regression)"
echo "  - routing_overhead_results.csv: system,operation,message_count,overhead_type (token vs provider announce)"
echo "  - resource_usage.csv: CPU/memory samples during tests (when available)"
echo "  - resource_usage_upload_n<N>.csv: CPU/memory during upload phase only, per node count"
echo "  - summary_report.txt: Test summary report"
echo ""

# Final cleanup
cleanup_containers

echo -e "${GREEN}All tests complete!${NC}"
