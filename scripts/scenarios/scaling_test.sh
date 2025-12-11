#!/usr/bin/env bash
set -euo pipefail

# Purpose: Run scaling test across multiple node counts to analyze performance patterns.
# Usage: bash scripts/scenarios/scaling_test.sh [NODES_LIST] [MIN_OUTBOUND]

NODES_LIST="${1:-5,10,20}"
MIN_OUTBOUND="${2:-4}"
TOPOLOGY="${TOPOLOGY:-star}"

# Parse node counts
IFS=',' read -ra NODE_COUNTS <<< "$NODES_LIST"

echo "=== Scaling Test ==="
echo "Node counts to test: ${NODE_COUNTS[*]}"
echo "Min outbound: $MIN_OUTBOUND"
echo "Topology: $TOPOLOGY"
echo ""

RESULTS_DIR="artifacts/scaling_tests/$(date +%s)"
mkdir -p "$RESULTS_DIR"

# Build once
echo "Building node..."
make build >/dev/null 2>&1

# Run test for each node count
for N in "${NODE_COUNTS[@]}"; do
  echo "=========================================="
  echo "Testing with N=$N nodes"
  echo "=========================================="
  
  RUN_ID=$(date +%s)
  RUN_DIR="artifacts/runs/$RUN_ID"
  
  # Start nodes
  echo "Starting $N nodes..."
  if ! N=$N TOPOLOGY=$TOPOLOGY MIN_OUTBOUND=$MIN_OUTBOUND RUN_ID=$RUN_ID DURATION_S=180 make local >/dev/null 2>&1; then
    echo "ERROR: Failed to start nodes for N=$N" >&2
    continue
  fi
  
  echo "Waiting 45s for bootstrap and initial connections..."
  sleep 45
  
  # Check neighbors to verify connectivity
  echo "Checking connectivity..."
  bash scripts/scenarios/check_neighbors.sh $RUN_ID > "$RUN_DIR/neighbors_check.txt" 2>&1 || true
  
  # Get bootstrap address
  BOOT_ADDR=$(jq -r '.[0].control_addr' "$RUN_DIR/nodes.json")
  if [[ -z "$BOOT_ADDR" || "$BOOT_ADDR" == "null" ]]; then
    echo "ERROR: Could not get bootstrap address" >&2
    continue
  fi
  
  # Put test data on bootstrap
  echo "Putting test data..."
  CID1=$(curl -s -X POST -H "Content-Type: application/json" -d '{"data":"alpha"}' "http://$BOOT_ADDR/put" | jq -r .cid)
  CID2=$(curl -s -X POST -H "Content-Type: application/json" -d '{"data":"beta"}' "http://$BOOT_ADDR/put" | jq -r .cid)
  sleep 2
  
  # Capture metrics over time (60s with 2s intervals)
  echo "Capturing metrics for 60s..."
  bash scripts/scenarios/capture_dial_metrics.sh $RUN_ID 60 2 >/dev/null 2>&1 || true
  
  # Submit restore to all leaves
  echo "Submitting restore jobs..."
  bash scripts/scenarios/submit_restore.sh $RUN_ID 1 "$CID1" "$CID2" > "$RUN_DIR/restore_results.txt" 2>&1 || true
  
  # Wait a bit for any final activity
  sleep 10
  
  # Capture final metrics snapshot
  echo "Capturing final metrics..."
  FINAL_METRICS_FILE="$RUN_DIR/final_metrics.jsonl"
  > "$FINAL_METRICS_FILE"
  TS=$(date +%s)
  jq -r '.[] | "\(.id)|\(.control_addr)"' "$RUN_DIR/nodes.json" | while IFS='|' read -r node_id control_addr; do
    metrics_json=$(curl -sSf "http://$control_addr/metrics" 2>/dev/null || echo "{}")
    if [[ -n "$metrics_json" && "$metrics_json" != "{}" ]]; then
      echo "$metrics_json" | jq -c ". + {node_id: $node_id, ts: $TS}" >> "$FINAL_METRICS_FILE"
    fi
  done
  
  # Generate plots
  if [[ -f "$RUN_DIR/raw/metrics.jsonl" ]]; then
    echo "Generating plots..."
    python3 scripts/plots/quick_plots.py "$RUN_DIR/raw/metrics.jsonl" --save-table >/dev/null 2>&1 || true
  fi
  
  # Record run info
  echo "$RUN_ID" >> "$RESULTS_DIR/runs.txt"
  echo "$N|$RUN_ID" >> "$RESULTS_DIR/scaling_data.txt"
  
  echo "Completed N=$N (RUN_ID=$RUN_ID)"
  echo ""
  
  # Cleanup: shutdown nodes
  echo "Shutting down nodes..."
  jq -r '.[] | .control_addr' "$RUN_DIR/nodes.json" | while read -r addr; do
    curl -s "http://$addr/shutdown" >/dev/null 2>&1 || true
  done
  sleep 2
done

echo "=========================================="
echo "Scaling test complete!"
echo "Results directory: $RESULTS_DIR"
echo ""
echo "Generating scaling analysis plots..."
python3 scripts/plots/scaling_analysis.py "$RESULTS_DIR" || echo "Scaling analysis script not found, skipping"

