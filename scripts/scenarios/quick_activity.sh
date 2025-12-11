#!/usr/bin/env bash
set -euo pipefail

# Purpose: One-command activity test: spawn nodes, put data, restore, capture metrics.
# Usage: bash scripts/scenarios/quick_activity.sh [N] [MIN_OUTBOUND]

N="${1:-5}"
MIN_OUTBOUND="${2:-4}"
TOPOLOGY="${TOPOLOGY:-star}"
DURATION_S="${DURATION_S:-120}"

echo "=== Quick Activity Test ==="
echo "Nodes: $N, Min Outbound: $MIN_OUTBOUND, Topology: $TOPOLOGY"
echo ""

# 1. Fresh start: build and spawn nodes
echo "Step 1: Building and starting nodes..."
RUN_ID=$(date +%s)
if ! N=$N TOPOLOGY=$TOPOLOGY MIN_OUTBOUND=$MIN_OUTBOUND RUN_ID=$RUN_ID DURATION_S=$DURATION_S make local >/dev/null 2>&1; then
  echo "ERROR: Failed to start nodes" >&2
  exit 1
fi

echo "RUN_ID=$RUN_ID"
echo "Waiting 30s for nodes to bootstrap..."
sleep 30

# 2. Verify connections
echo ""
echo "Step 2: Verifying connections..."
bash scripts/scenarios/check_neighbors.sh $RUN_ID >/dev/null 2>&1 || true

# 3. Get bootstrap address
BOOT_ADDR=$(jq -r '.[0].control_addr' artifacts/runs/$RUN_ID/nodes.json)
if [[ -z "$BOOT_ADDR" || "$BOOT_ADDR" == "null" ]]; then
  echo "ERROR: Could not get bootstrap address" >&2
  exit 1
fi

# 4. Put two CIDs on node 1 (bootstrap)
echo ""
echo "Step 3: Putting test data on bootstrap..."
CID1=$(curl -s -X POST -H "Content-Type: application/json" -d '{"data":"alpha"}' "http://$BOOT_ADDR/put" | jq -r .cid)
CID2=$(curl -s -X POST -H "Content-Type: application/json" -d '{"data":"beta"}' "http://$BOOT_ADDR/put" | jq -r .cid)

if [[ -z "$CID1" || "$CID1" == "null" || -z "$CID2" || "$CID2" == "null" ]]; then
  echo "ERROR: Failed to put data on bootstrap" >&2
  exit 1
fi

echo "CID1: $CID1"
echo "CID2: $CID2"
sleep 5

# 5. Submit restore to all leaves
echo ""
echo "Step 4: Submitting restore to all leaves..."
if ! bash scripts/scenarios/submit_restore.sh $RUN_ID 1 "$CID1" "$CID2" >/dev/null 2>&1; then
  echo "WARNING: Some restores may have failed (check logs above)" >&2
fi

# 6. Capture metrics (time-series) and aggregate
echo ""
echo "Step 5: Capturing metrics..."
RAW_DIR="artifacts/runs/$RUN_ID/raw"
mkdir -p "$RAW_DIR"

METRICS_FILE="$RAW_DIR/metrics.jsonl"
> "$METRICS_FILE"

# Capture initial metrics snapshot
INITIAL_TS=$(date +%s)
ITERATION=0
jq -r '.[] | "\(.id)|\(.control_addr)"' artifacts/runs/$RUN_ID/nodes.json | while IFS='|' read -r node_id control_addr; do
  metrics_json=$(curl -sSf "http://$control_addr/metrics" 2>/dev/null || echo "{}")
  if [[ -n "$metrics_json" && "$metrics_json" != "{}" ]]; then
    echo "$metrics_json" | jq -c ". + {node_id: $node_id, ts: $INITIAL_TS, iteration: $ITERATION}" >> "$METRICS_FILE"
  fi
done

# Capture a few more snapshots over time to show convergence (optional)
echo "  Capturing time-series snapshots..."
for i in {1..3}; do
  sleep 5
  ITERATION=$i
  TS=$(date +%s)
  jq -r '.[] | "\(.id)|\(.control_addr)"' artifacts/runs/$RUN_ID/nodes.json | while IFS='|' read -r node_id control_addr; do
    metrics_json=$(curl -sSf "http://$control_addr/metrics" 2>/dev/null || echo "{}")
    if [[ -n "$metrics_json" && "$metrics_json" != "{}" ]]; then
      echo "$metrics_json" | jq -c ". + {node_id: $node_id, ts: $TS, iteration: $ITERATION}" >> "$METRICS_FILE"
    fi
  done
done

# 7. Print aggregated totals
echo ""
echo "=== Aggregated Metrics Totals ==="
TOTALS=$(jq -s '{
  dials_attempted: ([.[].dials_attempted // 0] | add),
  dials_succeeded: ([.[].dials_succeeded // 0] | add),
  dials_failed: ([.[].dials_failed // 0] | add),
  restores_started: ([.[].restores_started // 0] | add),
  restores_ok: ([.[].restores_ok // 0] | add),
  restores_failed: ([.[].restores_failed // 0] | add),
  restore_bytes: ([.[].restore_bytes // 0] | add),
  gossip_learned: ([.[].gossip_learned // 0] | add)
}' "$METRICS_FILE" 2>/dev/null || echo '{}')

echo "$TOTALS" | jq '.'

# Print per-node breakdown
echo ""
echo "=== Per-Node Metrics ==="
jq -r '.[] | "\(.node_id)|\(.control_addr)"' artifacts/runs/$RUN_ID/nodes.json | while IFS='|' read -r node_id control_addr; do
  node_metrics=$(curl -sSf "http://$control_addr/metrics" 2>/dev/null || echo "{}")
  if [[ -n "$node_metrics" && "$node_metrics" != "{}" ]]; then
    echo "Node $node_id:"
    echo "$node_metrics" | jq '{
      dials_attempted,
      dials_succeeded,
      dials_failed,
      restores_started,
      restores_ok,
      restores_failed,
      restore_bytes
    }' | sed 's/^/  /'
  fi
done

echo ""
echo "=== Summary ==="
echo "RUN_ID: $RUN_ID"
echo "Nodes: $N"
echo "CIDs restored: $CID1, $CID2"
echo "Metrics saved to: $METRICS_FILE"

# Check if restore results exist
if [[ -d "artifacts/runs/$RUN_ID/restore_results" ]]; then
  echo "Restore results: artifacts/runs/$RUN_ID/restore_results/"
fi

# Generate plots if metrics file exists
if [[ -f "$METRICS_FILE" ]]; then
  echo ""
  echo "Step 6: Generating plots..."
  if python3 scripts/plots/quick_plots.py "$METRICS_FILE" --save-table 2>/dev/null; then
    echo "Plots and table saved to artifacts/runs/$RUN_ID/plots/"
  else
    echo "Plot generation skipped (install matplotlib for plots: pip install matplotlib)"
  fi
fi

echo ""
echo "Test complete!"

