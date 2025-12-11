#!/usr/bin/env bash
set -euo pipefail

# Purpose: Capture dial metrics for 60s and aggregate dials_* totals.
# Usage: bash scripts/scenarios/capture_dial_metrics.sh <RUN_ID> [DURATION_S]

RUN_ID="${1:-}"
DURATION_S="${2:-60}"
INTERVAL_S="${3:-2}"

if [[ -z "$RUN_ID" ]]; then
  echo "Usage: $0 <RUN_ID> [DURATION_S] [INTERVAL_S]" >&2
  echo "  RUN_ID: directory name under artifacts/runs/" >&2
  echo "  DURATION_S: duration to capture (default: 60)" >&2
  echo "  INTERVAL_S: polling interval in seconds (default: 2)" >&2
  exit 1
fi

NODES_JSON="artifacts/runs/$RUN_ID/nodes.json"
if [[ ! -f "$NODES_JSON" ]]; then
  echo "Error: $NODES_JSON not found" >&2
  exit 1
fi

RAW_DIR="artifacts/runs/$RUN_ID/raw"
mkdir -p "$RAW_DIR"

METRICS_FILE="$RAW_DIR/metrics.jsonl"
echo "Capturing metrics for ${DURATION_S}s (interval: ${INTERVAL_S}s)..."

# Clear previous metrics file
> "$METRICS_FILE"

# Capture metrics
START_TIME=$(date +%s)
END_TIME=$((START_TIME + DURATION_S))
ITERATION=0

while [[ $(date +%s) -lt $END_TIME ]]; do
  ITERATION=$((ITERATION + 1))
  TS=$(date +%s)
  
  # Poll each node's /metrics endpoint
  jq -r '.[] | "\(.id)|\(.control_addr)"' "$NODES_JSON" | while IFS='|' read -r node_id control_addr; do
    metrics_json=$(curl -sSf "http://$control_addr/metrics" 2>/dev/null || echo "{}")
    if [[ -n "$metrics_json" && "$metrics_json" != "{}" ]]; then
      echo "$metrics_json" | jq -c ". + {node_id: $node_id, ts: $TS, iteration: $ITERATION}" >> "$METRICS_FILE"
    fi
  done
  
  # Sleep until next interval
  sleep "$INTERVAL_S"
done

echo "Capture complete. Aggregating dial metrics..."

# Aggregate dial totals across all nodes
TOTALS=$(jq -s '{
  dials_attempted: ([.[].dials_attempted // 0] | add),
  dials_succeeded: ([.[].dials_succeeded // 0] | add),
  dials_failed: ([.[].dials_failed // 0] | add)
}' "$METRICS_FILE")

echo ""
echo "=== Dial Metrics Summary (${DURATION_S}s capture) ==="
echo "$TOTALS" | jq '.'
echo ""
echo "Metrics saved to: $METRICS_FILE"
echo "Total samples: $(wc -l < "$METRICS_FILE")"

# Check if dials are non-zero
ATTEMPTED=$(echo "$TOTALS" | jq -r '.dials_attempted')
SUCCEEDED=$(echo "$TOTALS" | jq -r '.dials_succeeded')
FAILED=$(echo "$TOTALS" | jq -r '.dials_failed')

if [[ "$ATTEMPTED" -gt 0 ]]; then
  echo ""
  echo "✅ SUCCESS: Dial metrics are non-zero!"
  echo "   dials_attempted: $ATTEMPTED"
  echo "   dials_succeeded: $SUCCEEDED"
  echo "   dials_failed: $FAILED"
else
  echo ""
  echo "⚠️  WARNING: Dial metrics are zero - dial loop may not be running"
fi

