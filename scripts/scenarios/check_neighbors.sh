#!/usr/bin/env bash
set -euo pipefail

# Purpose: Quick experiment to check neighbor counts across nodes after bootstrapping.
# Usage: bash scripts/scenarios/check_neighbors.sh <RUN_ID>
#   RUN_ID: directory name under artifacts/runs/

RUN_ID="${1:-}"

# Show help
if [[ "$RUN_ID" == "-h" || "$RUN_ID" == "--help" || -z "$RUN_ID" ]]; then
  echo "Usage: $0 <RUN_ID>"
  echo ""
  echo "Check neighbor counts across nodes after bootstrapping."
  echo ""
  echo "Parameters:"
  echo "  RUN_ID    Directory name under artifacts/runs/ (e.g., 1765382271)"
  echo ""
  echo "Example:"
  echo "  # Start nodes"
  echo "  RUN_ID=\$(date +%s)"
  echo "  N=5 TOPOLOGY=star MIN_OUTBOUND=4 RUN_ID=\$RUN_ID make local"
  echo ""
  echo "  # Wait for bootstrap, then check neighbors"
  echo "  sleep 30"
  echo "  $0 \$RUN_ID"
  echo ""
  echo "See scripts/scenarios/README.md for full documentation."
  exit 0
fi

NODES_JSON="artifacts/runs/$RUN_ID/nodes.json"
if [[ ! -f "$NODES_JSON" ]]; then
  echo "Error: $NODES_JSON not found" >&2
  exit 1
fi

echo "Checking neighbors for run $RUN_ID..."
echo ""

# Read nodes.json and check neighbors for each node
jq -r '.[] | "\(.id)|\(.control_addr)"' "$NODES_JSON" | while IFS='|' read -r node_id control_addr; do
  echo -n "Node $node_id ($control_addr): "
  
  # Call /neighbors endpoint
  neighbors_json=$(curl -sSf "http://$control_addr/neighbors" 2>/dev/null || echo "[]")
  neighbor_count=$(echo "$neighbors_json" | jq 'length')
  
  echo "$neighbor_count neighbors"
  
  # If there are neighbors, show first few peer IDs
  if [[ "$neighbor_count" -gt 0 ]]; then
    echo "$neighbors_json" | jq -r '.[] | "  - \(.peer)"' | head -n 5
    if [[ "$neighbor_count" -gt 5 ]]; then
      echo "  ... and $((neighbor_count - 5)) more"
    fi
  fi
  echo ""
done

echo "Summary:"
total_neighbors=$(jq -r '.[] | .control_addr' "$NODES_JSON" | while read -r addr; do
  curl -sSf "http://$addr/neighbors" 2>/dev/null | jq 'length' || echo "0"
done | awk '{sum+=$1} END {print sum}')

node_count=$(jq 'length' "$NODES_JSON")
echo "  Total nodes: $node_count"
echo "  Total neighbor connections: $total_neighbors"
echo "  Average neighbors per node: $(awk "BEGIN {printf \"%.1f\", $total_neighbors / $node_count}")"

