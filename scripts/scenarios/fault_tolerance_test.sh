#!/usr/bin/env bash
set -euo pipefail

# Purpose: Run multiple failure/repair tests to collect repair time data with statistical rigor.
# Usage: bash scripts/scenarios/fault_tolerance_test.sh [NODES_LIST] [RUNS] [MIN_OUTBOUND]

NODES_LIST="${1:-5,10,20,50}"
RUNS="${2:-5}"
MIN_OUTBOUND="${3:-4}"
TOPOLOGY="${TOPOLOGY:-star}"

# Parse node counts
IFS=',' read -ra NODE_COUNTS <<< "$NODES_LIST"

echo "=== Fault Tolerance Test ==="
echo "Node counts: ${NODE_COUNTS[*]}"
echo "Runs per node count: $RUNS"
echo "Min outbound: $MIN_OUTBOUND"
echo "Topology: $TOPOLOGY"
echo ""

RESULTS_DIR="artifacts/fault_tolerance_tests/$(date +%s)"
mkdir -p "$RESULTS_DIR"

# Cleanup function to kill any existing nodes
cleanup_existing_nodes() {
  echo "Checking for existing nodes..."
  pkill -f "bin/node run" 2>/dev/null || true
  sleep 1
  find artifacts/runs -name "daemon_*.json" -mmin +10 -delete 2>/dev/null || true
}

# Cleanup before starting
cleanup_existing_nodes

# Build once
echo "Building node..."
make build >/dev/null 2>&1

# Run test for each node count
for N in "${NODE_COUNTS[@]}"; do
  echo "=========================================="
  echo "Testing N=$N nodes ($RUNS runs)"
  echo "=========================================="
  
  # Run multiple times for this node count
  for run_num in $(seq 1 "$RUNS"); do
    echo "  Run $run_num/$RUNS..."
    
    # Generate unique RUN_ID
    RUN_ID=$(date +%s)$(printf "%03d" $run_num)
    RUN_DIR="artifacts/runs/$RUN_ID"
    
    # Start nodes
    echo "    Starting nodes (RUN_ID=$RUN_ID)..."
    START_TIME=$(date +%s)
    
    if [[ $run_num -eq 1 && $N -eq ${NODE_COUNTS[0]} ]]; then
      if ! timeout 300 bash -c "N=$N TOPOLOGY=$TOPOLOGY MIN_OUTBOUND=$MIN_OUTBOUND RUN_ID=$RUN_ID DURATION_S=180 make local"; then
        ELAPSED=$(($(date +%s) - START_TIME))
        echo "    ERROR: Failed to start nodes after ${ELAPSED}s" >&2
        cleanup_existing_nodes
        continue
      fi
    else
      if ! timeout 300 bash -c "N=$N TOPOLOGY=$TOPOLOGY MIN_OUTBOUND=$MIN_OUTBOUND RUN_ID=$RUN_ID DURATION_S=180 make local >/dev/null 2>&1"; then
        ELAPSED=$(($(date +%s) - START_TIME))
        echo "    ERROR: Failed to start nodes after ${ELAPSED}s" >&2
        cleanup_existing_nodes
        continue
      fi
    fi
    
    ELAPSED=$(($(date +%s) - START_TIME))
    echo "    Nodes started in ${ELAPSED}s, waiting for connections..."
    
    # Wait for bootstrap and initial connections
    sleep 30
    
    # Verify nodes.json was created
    if [[ ! -f "$RUN_DIR/nodes.json" ]]; then
      echo "    ERROR: nodes.json not created" >&2
      continue
    fi
    
    # Put some test data on bootstrap node for restore
    BOOT_ADDR=$(jq -r '.[0].control_addr' "$RUN_DIR/nodes.json" 2>/dev/null || echo "")
    if [[ -n "$BOOT_ADDR" && "$BOOT_ADDR" != "null" ]]; then
      echo "    Putting test data on bootstrap..."
      CID1=$(curl -s -X POST -H "Content-Type: application/json" -d '{"data":"test1"}' "http://$BOOT_ADDR/put" 2>/dev/null | jq -r .cid || echo "")
      CID2=$(curl -s -X POST -H "Content-Type: application/json" -d '{"data":"test2"}' "http://$BOOT_ADDR/put" 2>/dev/null | jq -r .cid || echo "")
      sleep 2
    fi
    
    # Run failure/repair test
    echo "    Running failure/repair test..."
    if bash scripts/scenarios/failure_repair.sh "$RUN_ID" "" 1 >/dev/null 2>&1; then
      # Record run info (format: N|RUN_ID)
      echo "$N|$RUN_ID" >> "$RESULTS_DIR/runs.txt"
      echo "    Repair test completed"
    else
      echo "    WARNING: Failure/repair test failed" >&2
    fi
    
    # Cleanup: shutdown nodes
    jq -r '.[] | .control_addr' "$RUN_DIR/nodes.json" 2>/dev/null | while read -r addr; do
      curl -s "http://$addr/shutdown" >/dev/null 2>&1 || true
    done
    sleep 2
    
    echo "    Completed (RUN_ID=$RUN_ID)"
  done
  
  echo "  Completed all $RUNS runs for N=$N"
  echo ""
done

echo "=========================================="
echo "Fault tolerance test complete!"
echo "Results directory: $RESULTS_DIR"
echo ""
echo "To generate plots, run:"
echo "  python3 scripts/plots/fault_tolerance_plot.py $RESULTS_DIR"
echo ""
echo "Or use Makefile:"
echo "  make plot-fault-tolerance RESULTS_DIR=$RESULTS_DIR"
echo ""
# Write results dir to file for easy access
echo "$RESULTS_DIR" > "$RESULTS_DIR/.results_dir"

