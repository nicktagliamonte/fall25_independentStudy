#!/usr/bin/env bash
set -euo pipefail

# Purpose: Quick validation test - run one restore operation and verify data is captured
# Usage: bash scripts/scenarios/validate_restore_test.sh [N]

N="${1:-5}"
MIN_OUTBOUND="${2:-4}"

echo "=== Restore Data Validation Test ==="
echo "Testing with N=$N nodes (single run)"
echo ""

# Cleanup
pkill -f "bin/node run" 2>/dev/null || true
sleep 1

# Build
make build >/dev/null 2>&1

# Run quick_activity.sh
echo "Running quick_activity.sh..."
if bash scripts/scenarios/quick_activity.sh "$N" "$MIN_OUTBOUND"; then
  # Find the RUN_ID
  RUN_ID=$(ls -td artifacts/runs/*/ 2>/dev/null | head -1 | xargs basename)
  
  if [[ -z "$RUN_ID" ]]; then
    echo "ERROR: Could not find RUN_ID" >&2
    exit 1
  fi
  
  echo ""
  echo "=== Validation Results ==="
  echo "RUN_ID: $RUN_ID"
  echo ""
  
  # Check for metrics files
  if [[ -f "artifacts/runs/$RUN_ID/raw/metrics.jsonl" ]]; then
    echo "✓ metrics.jsonl exists"
    
    # Check restore metrics
    RESTORES=$(jq -s '[.[] | .restores_started // 0] | add' "artifacts/runs/$RUN_ID/raw/metrics.jsonl" 2>/dev/null || echo "0")
    RESTORES_OK=$(jq -s '[.[] | .restores_ok // 0] | add' "artifacts/runs/$RUN_ID/raw/metrics.jsonl" 2>/dev/null || echo "0")
    
    echo "  Total restores_started: $RESTORES"
    echo "  Total restores_ok: $RESTORES_OK"
    
    if [[ "$RESTORES" -gt 0 ]]; then
      echo "✓ Restore data found in metrics!"
      
      # Calculate restores per node
      NUM_NODES=$(jq 'length' "artifacts/runs/$RUN_ID/nodes.json" 2>/dev/null || echo "0")
      if [[ "$NUM_NODES" -gt 0 ]]; then
        RESTORES_PER_NODE=$(awk "BEGIN {printf \"%.3f\", $RESTORES/$NUM_NODES}")
        echo "  Restores per node: $RESTORES_PER_NODE"
        echo ""
        echo "✓ VALIDATION PASSED: Data looks good!"
        echo "  You can proceed with the full test."
      fi
    else
      echo "✗ WARNING: No restore data found!"
      echo "  Restores may not have completed."
    fi
  else
    echo "✗ ERROR: metrics.jsonl not found"
  fi
  
  # Check final metrics
  if [[ -f "artifacts/runs/$RUN_ID/final_metrics.jsonl" ]]; then
    echo "✓ final_metrics.jsonl exists"
    FINAL_RESTORES=$(jq -s '[.[] | .restores_started // 0] | add' "artifacts/runs/$RUN_ID/final_metrics.jsonl" 2>/dev/null || echo "0")
    echo "  Final restores_started: $FINAL_RESTORES"
  else
    echo "⚠ final_metrics.jsonl not found (will be created by restore_efficiency_test.sh)"
  fi
  
  # Cleanup
  echo ""
  echo "Cleaning up..."
  jq -r '.[] | .control_addr' "artifacts/runs/$RUN_ID/nodes.json" 2>/dev/null | while read -r addr; do
    curl -s "http://$addr/shutdown" >/dev/null 2>&1 || true
  done
  
else
  echo "ERROR: quick_activity.sh failed" >&2
  exit 1
fi

echo ""
echo "Validation complete!"

