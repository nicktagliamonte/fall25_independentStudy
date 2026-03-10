#!/usr/bin/env bash
set -euo pipefail

# Purpose: Run multiple scaling tests to collect convergence time data with statistical rigor.
# Usage: bash scripts/scenarios/convergence_test.sh [NODES_LIST] [RUNS] [MIN_OUTBOUND]

NODES_LIST="${1:-5,10,20,50}"
RUNS="${2:-5}"
MIN_OUTBOUND="${3:-4}"
TOPOLOGY="${TOPOLOGY:-star}"

# Parse node counts
IFS=',' read -ra NODE_COUNTS <<< "$NODES_LIST"

echo "=== Convergence Time Test ==="
echo "Node counts: ${NODE_COUNTS[*]}"
echo "Runs per node count: $RUNS"
echo "Min outbound: $MIN_OUTBOUND"
echo "Topology: $TOPOLOGY"
echo ""

# Compatibility for macOS which doesn't have timeout by default
if ! command -v timeout &> /dev/null; then
  if command -v gtimeout &> /dev/null; then
    timeout() { gtimeout "$@"; }
  else
    timeout() { perl -e 'alarm shift; exec @ARGV' "$@"; }
  fi
fi

RESULTS_DIR="artifacts/convergence_tests/$(date +%s)"
mkdir -p "$RESULTS_DIR"

# Cleanup function to kill any existing nodes
cleanup_existing_nodes() {
  echo "Checking for existing nodes..."
  # Find and kill any running node processes
  pkill -f "bin/node run" 2>/dev/null || true
  sleep 1
  # Clean up any stale control files
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
  
  RUN_IDS=()
  
  # Run multiple times for this node count
  for run_num in $(seq 1 "$RUNS"); do
    echo "  Run $run_num/$RUNS..."
    
    # Generate unique RUN_ID (timestamp + run number)
    RUN_ID=$(date +%s)$(printf "%03d" $run_num)
    RUN_DIR="artifacts/runs/$RUN_ID"
    RUN_IDS+=("$RUN_ID")
    
    # Start nodes (show output for first run to debug)
    echo "    Starting nodes (RUN_ID=$RUN_ID)..."
    START_TIME=$(date +%s)
    
    # Use timeout to prevent hanging (5 minutes max)
    # Show output for first run of first node count only
    if [[ $run_num -eq 1 && $N -eq ${NODE_COUNTS[0]} ]]; then
      echo "    [DEBUG] Running: N=$N TOPOLOGY=$TOPOLOGY MIN_OUTBOUND=$MIN_OUTBOUND RUN_ID=$RUN_ID"
      if ! timeout 300 bash -c "N=$N TOPOLOGY=$TOPOLOGY MIN_OUTBOUND=$MIN_OUTBOUND RUN_ID=$RUN_ID DURATION_S=180 make local"; then
        ELAPSED=$(($(date +%s) - START_TIME))
        echo "    ERROR: Failed to start nodes after ${ELAPSED}s (timeout or error)" >&2
        # Try to cleanup any partial nodes
        cleanup_existing_nodes
        continue
      fi
    else
      if ! timeout 300 bash -c "N=$N TOPOLOGY=$TOPOLOGY MIN_OUTBOUND=$MIN_OUTBOUND RUN_ID=$RUN_ID DURATION_S=180 make local >/dev/null 2>&1"; then
        ELAPSED=$(($(date +%s) - START_TIME))
        echo "    ERROR: Failed to start nodes after ${ELAPSED}s (timeout or error)" >&2
        # Try to cleanup any partial nodes
        cleanup_existing_nodes
        continue
      fi
    fi
    
    ELAPSED=$(($(date +%s) - START_TIME))
    echo "    Nodes started in ${ELAPSED}s"
    
    # Verify nodes.json was created
    if [[ ! -f "$RUN_DIR/nodes.json" ]]; then
      echo "    ERROR: nodes.json not created" >&2
      continue
    fi
    
    echo "    Nodes started, waiting for bootstrap..."
    
    # Wait for bootstrap
    sleep 30
    
    # Get bootstrap address
    BOOT_ADDR=$(jq -r '.[0].control_addr' "$RUN_DIR/nodes.json" 2>/dev/null || echo "")
    if [[ -z "$BOOT_ADDR" || "$BOOT_ADDR" == "null" ]]; then
      echo "    ERROR: Could not get bootstrap address" >&2
      # Cleanup
      jq -r '.[] | .control_addr' "$RUN_DIR/nodes.json" 2>/dev/null | while read -r addr; do
        curl -s "http://$addr/shutdown" >/dev/null 2>&1 || true
      done
      continue
    fi
    
    # Put test data on bootstrap
    CID1=$(curl -s -X POST -H "Content-Type: application/json" -d '{"data":"alpha"}' "http://$BOOT_ADDR/put" 2>/dev/null | jq -r .cid || echo "")
    CID2=$(curl -s -X POST -H "Content-Type: application/json" -d '{"data":"beta"}' "http://$BOOT_ADDR/put" 2>/dev/null | jq -r .cid || echo "")
    sleep 2
    
    # Capture metrics over time (90s with 2s intervals for convergence detection)
    bash scripts/scenarios/capture_dial_metrics.sh "$RUN_ID" 90 2 >/dev/null 2>&1 || true
    
    # Submit restore to all leaves (to generate activity)
    bash scripts/scenarios/submit_restore.sh "$RUN_ID" 1 "$CID1" "$CID2" >/dev/null 2>&1 || true
    
    # Wait for activity to settle
    sleep 10
    
    # Capture final metrics snapshot (for restore efficiency analysis)
    FINAL_METRICS_FILE="$RUN_DIR/final_metrics.jsonl"
    > "$FINAL_METRICS_FILE"
    TS=$(date +%s)
    jq -r '.[] | "\(.id)|\(.control_addr)"' "$RUN_DIR/nodes.json" 2>/dev/null | while IFS='|' read -r node_id control_addr; do
      metrics_json=$(curl -sSf "http://$control_addr/metrics" 2>/dev/null || echo "{}")
      if [[ -n "$metrics_json" && "$metrics_json" != "{}" ]]; then
        echo "$metrics_json" | jq -c ". + {node_id: $node_id, ts: $TS}" >> "$FINAL_METRICS_FILE"
      fi
    done
    
    # Record run info
    echo "$N|$RUN_ID" >> "$RESULTS_DIR/runs.txt"
    
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
echo "Convergence test complete!"
echo "Results directory: $RESULTS_DIR"
echo ""
echo "To generate plots, run:"
echo "  # Panel A: Convergence Time"
echo "  python3 scripts/plots/convergence_plot.py $RESULTS_DIR"
echo "  # Or: make plot-convergence RESULTS_DIR=$RESULTS_DIR"
echo ""
echo "  # Panel C: Restore Efficiency"
echo "  python3 scripts/plots/restore_efficiency_plot.py $RESULTS_DIR"
echo "  # Or: make plot-restore-efficiency RESULTS_DIR=$RESULTS_DIR"
echo ""
# Write results dir to file for easy access
echo "$RESULTS_DIR" > "$RESULTS_DIR/.results_dir"

