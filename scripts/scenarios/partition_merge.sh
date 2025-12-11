#!/usr/bin/env bash
set -euo pipefail

# Purpose: Test network partition and merge scenario
# Usage: bash scripts/scenarios/partition_merge.sh <RUN_ID> [T1] [T2] [groups]
#   RUN_ID: directory name under artifacts/runs/
#   T1: Duration of partition in seconds (default: 30)
#   T2: Duration after merge in seconds (default: 30)
#   groups: Comma-separated group specification (default: "1,2-3,4-5")

RUN_ID="${1:-}"
T1="${2:-30}"
T2="${3:-30}"
GROUPS="${4:-}"

# Show help
if [[ "$RUN_ID" == "-h" || "$RUN_ID" == "--help" || -z "$RUN_ID" ]]; then
  echo "Usage: $0 <RUN_ID> [T1] [T2] [groups]"
  echo ""
  echo "Test network partition and merge scenario:"
  echo "  1. Apply partition profile for T1 seconds"
  echo "  2. Clear partition profile"
  echo "  3. Run for T2 seconds (merged)"
  echo "  4. Record timestamps and metrics"
  echo ""
  echo "Parameters:"
  echo "  RUN_ID   Directory name under artifacts/runs/"
  echo "  T1       Partition duration in seconds (default: 30)"
  echo "  T2       Post-merge duration in seconds (default: 30)"
  echo "  groups   Group specification (default: auto-detect from nodes.json)"
  echo ""
  echo "Output:"
  echo "  artifacts/runs/<RUN_ID>/partition.csv"
  exit 0
fi

NODES_JSON="artifacts/runs/$RUN_ID/nodes.json"
if [[ ! -f "$NODES_JSON" ]]; then
  echo "Error: $NODES_JSON not found" >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
NET_DIR="$ROOT_DIR/scripts/net"
if [[ -f "$NET_DIR/profiles.sh" ]]; then
  . "$NET_DIR/profiles.sh"
else
  echo "Error: scripts/net/profiles.sh not found" >&2
  exit 1
fi

OUTPUT_CSV="artifacts/runs/$RUN_ID/partition.csv"
mkdir -p "$(dirname "$OUTPUT_CSV")"

# Auto-detect groups if not provided (split nodes into 2 groups)
if [[ -z "$GROUPS" ]]; then
  NODE_COUNT=$(jq 'length' "$NODES_JSON")
  if [[ $NODE_COUNT -lt 2 ]]; then
    echo "Error: Need at least 2 nodes for partition test" >&2
    exit 1
  fi
  MID=$((NODE_COUNT / 2))
  GROUP1="1-$MID"
  GROUP2="$((MID + 1))-$NODE_COUNT"
  GROUPS="$GROUP1,$GROUP2"
fi

echo "Partition/Merge Test"
echo "==================="
echo "Run ID: $RUN_ID"
echo "Partition duration (T1): ${T1}s"
echo "Post-merge duration (T2): ${T2}s"
echo "Groups: $GROUPS"
echo ""

# CSV header
echo "run_id,phase,ts_start,ts_end,duration_s,groups" > "$OUTPUT_CSV"

# Phase 1: Apply partition
echo "Phase 1: Applying partition profile..."
PARTITION_START=$(date +%s)

if apply_profile "$RUN_ID" "partition" "$GROUPS"; then
  echo "  Partition profile applied"
else
  echo "  Failed to apply partition profile" >&2
  exit 1
fi

# Record metrics snapshot before partition
echo "  Capturing pre-partition metrics..."
PRE_PARTITION_METRICS="artifacts/runs/$RUN_ID/pre_partition_metrics.json"
jq -r '.[] | .control_addr' "$NODES_JSON" | while read -r addr; do
  curl -sSf "http://$addr/metrics" >/dev/null 2>&1 || true
done

# Wait for T1 seconds
echo "  Waiting ${T1}s with partition active..."
sleep "$T1"

PARTITION_END=$(date +%s)
PARTITION_DURATION=$((PARTITION_END - PARTITION_START))
echo "$RUN_ID,partition,$PARTITION_START,$PARTITION_END,$PARTITION_DURATION,$GROUPS" >> "$OUTPUT_CSV"
echo "  Partition phase complete (${PARTITION_DURATION}s)"
echo ""

# Phase 2: Clear partition (merge)
echo "Phase 2: Clearing partition (merging network)..."
MERGE_START=$(date +%s)

if clear_profile "$RUN_ID"; then
  echo "  Partition profile cleared"
else
  echo "  Failed to clear partition profile" >&2
fi

# Record metrics snapshot after merge
echo "  Capturing post-merge metrics..."
POST_MERGE_METRICS="artifacts/runs/$RUN_ID/post_merge_metrics.json"
jq -r '.[] | .control_addr' "$NODES_JSON" | while read -r addr; do
  curl -sSf "http://$addr/metrics" >/dev/null 2>&1 || true
done

# Wait for T2 seconds
echo "  Waiting ${T2}s after merge..."
sleep "$T2"

MERGE_END=$(date +%s)
MERGE_DURATION=$((MERGE_END - MERGE_START))
echo "$RUN_ID,merge,$MERGE_START,$MERGE_END,$MERGE_DURATION,$GROUPS" >> "$OUTPUT_CSV"
echo "  Merge phase complete (${MERGE_DURATION}s)"
echo ""

# Final metrics snapshot
echo "Phase 3: Capturing final metrics..."
FINAL_METRICS="artifacts/runs/$RUN_ID/final_partition_metrics.json"
jq -r '.[] | "\(.id)|\(.control_addr)"' "$NODES_JSON" | while IFS='|' read -r node_id addr; do
  METRICS=$(curl -sSf "http://$addr/metrics" 2>/dev/null || echo "{}")
  echo "$METRICS" | jq ". + {node_id: $node_id, ts: $(date +%s)}" >> "$FINAL_METRICS" 2>/dev/null || true
done

TOTAL_DURATION=$((MERGE_END - PARTITION_START))

echo "Partition/Merge test complete!"
echo "Results written to: $OUTPUT_CSV"
echo ""
echo "Summary:"
echo "  Partition duration: ${PARTITION_DURATION}s"
echo "  Merge duration: ${MERGE_DURATION}s"
echo "  Total duration: ${TOTAL_DURATION}s"
echo "  Groups: $GROUPS"

# Generate plots if Python script is available
if command -v python3 >/dev/null 2>&1; then
  echo ""
  echo "Generating partition scaling plots..."
  if python3 "$ROOT_DIR/scripts/plots/partition_scaling.py" "$RUN_ID" 2>/dev/null; then
    echo "Plots generated successfully"
  else
    echo "Warning: Failed to generate plots (matplotlib may not be installed)" >&2
  fi
fi

