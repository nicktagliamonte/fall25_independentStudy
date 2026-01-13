#!/usr/bin/env bash
set -euo pipefail

# Purpose: Run restore efficiency tests across multiple node counts using quick_activity.sh
# Usage: bash scripts/scenarios/restore_efficiency_test.sh [NODES_LIST] [RUNS] [MIN_OUTBOUND]

NODES_LIST="${1:-5,10,20,50}"
RUNS="${2:-5}"
MIN_OUTBOUND="${3:-4}"
TOPOLOGY="${TOPOLOGY:-star}"

# Parse node counts
IFS=',' read -ra NODE_COUNTS <<< "$NODES_LIST"

echo "=== Restore Efficiency Test ==="
echo "Node counts: ${NODE_COUNTS[*]}"
echo "Runs per node count: $RUNS"
echo "Min outbound: $MIN_OUTBOUND"
echo "Topology: $TOPOLOGY"
echo ""

RESULTS_DIR="artifacts/restore_efficiency_tests/$(date +%s)"
mkdir -p "$RESULTS_DIR"

# Cleanup function
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
    
    # Run quick_activity.sh (it handles everything: start nodes, put data, restore, capture metrics)
    if bash scripts/scenarios/quick_activity.sh "$N" "$MIN_OUTBOUND" >/dev/null 2>&1; then
      # Extract RUN_ID from the last created run directory
      RUN_ID=$(ls -td artifacts/runs/*/ 2>/dev/null | head -1 | xargs basename)
      
      if [[ -n "$RUN_ID" && -f "artifacts/runs/$RUN_ID/nodes.json" ]]; then
        # Capture final metrics snapshot (for restore efficiency)
        FINAL_METRICS_FILE="artifacts/runs/$RUN_ID/final_metrics.jsonl"
        > "$FINAL_METRICS_FILE"
        TS=$(date +%s)
        jq -r '.[] | "\(.id)|\(.control_addr)"' "artifacts/runs/$RUN_ID/nodes.json" 2>/dev/null | while IFS='|' read -r node_id control_addr; do
          metrics_json=$(curl -sSf "http://$control_addr/metrics" 2>/dev/null || echo "{}")
          if [[ -n "$metrics_json" && "$metrics_json" != "{}" ]]; then
            echo "$metrics_json" | jq -c ". + {node_id: $node_id, ts: $TS}" >> "$FINAL_METRICS_FILE"
          fi
        done
        
        # Record run info
        echo "$N|$RUN_ID" >> "$RESULTS_DIR/runs.txt"
        echo "    Completed (RUN_ID=$RUN_ID)"
      else
        echo "    WARNING: Could not determine RUN_ID" >&2
      fi
      
      # Cleanup: shutdown nodes
      if [[ -f "artifacts/runs/$RUN_ID/nodes.json" ]]; then
        jq -r '.[] | .control_addr' "artifacts/runs/$RUN_ID/nodes.json" 2>/dev/null | while read -r addr; do
          curl -s "http://$addr/shutdown" >/dev/null 2>&1 || true
        done
        sleep 2
      fi
    else
      echo "    ERROR: quick_activity.sh failed" >&2
    fi
  done
  
  echo "  Completed all $RUNS runs for N=$N"
  echo ""
done

echo "=========================================="
echo "Restore efficiency test complete!"
echo "Results directory: $RESULTS_DIR"
echo ""
echo "To generate plot, run:"
echo "  python3 scripts/plots/restore_efficiency_plot.py $RESULTS_DIR"
echo ""
echo "Or use Makefile:"
echo "  make plot-restore-efficiency RESULTS_DIR=$RESULTS_DIR"
echo ""
# Write results dir to file for easy access
echo "$RESULTS_DIR" > "$RESULTS_DIR/.results_dir"

