#!/usr/bin/env bash
set -uo pipefail

# Purpose: Measure peer discovery time (ts_first, ts_k) across nodes with nanosecond precision
# Usage: bash scripts/scenarios/discovery.sh <RUN_ID> [K]
#   RUN_ID: directory name under artifacts/runs/
#   K: target number of neighbors to discover (default: 3)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="${1:-}"
K="${2:-3}"

# Show help
if [[ "$RUN_ID" == "-h" || "$RUN_ID" == "--help" || -z "$RUN_ID" ]]; then
  echo "Usage: $0 <RUN_ID> [K]"
  echo ""
  echo "Measure peer discovery time across nodes."
  echo ""
  echo "Parameters:"
  echo "  RUN_ID    Directory name under artifacts/runs/"
  echo "  K         Target number of neighbors to discover (default: 3)"
  echo ""
  echo "Output:"
  echo "  artifacts/runs/<RUN_ID>/discovery.csv"
  echo ""
  echo "Example:"
  echo "  RUN_ID=\$(date +%s)"
  echo "  N=10 TOPOLOGY=star RUN_ID=\$RUN_ID make local"
  echo "  sleep 30"
  echo "  $0 \$RUN_ID 5"
  exit 0
fi

NODES_JSON="artifacts/runs/$RUN_ID/nodes.json"
if [[ ! -f "$NODES_JSON" ]]; then
  echo "Error: $NODES_JSON not found" >&2
  exit 1
fi

OUTPUT_CSV="artifacts/runs/$RUN_ID/discovery.csv"
mkdir -p "$(dirname "$OUTPUT_CSV")"

echo "Measuring peer discovery for run $RUN_ID (target K=$K neighbors)..."
echo ""

# Verify nodes are accessible before starting
echo "Verifying nodes are accessible..."
jq -r '.[] | .control_addr' "$NODES_JSON" | while read -r addr; do
  if ! curl -sSf -m 2 "http://$addr/health" >/dev/null 2>&1; then
    echo "Warning: Node at $addr is not responding" >&2
  fi
done
echo ""

# CSV header - includes detailed neighbor discovery events
echo "node_id,control_addr,ts_start_ns,ts_first_ns,ts_k_ns,neighbors_at_end" > "$OUTPUT_CSV"
EVENTS_CSV="artifacts/runs/$RUN_ID/discovery_events.csv"
echo "node_id,neighbor_peer,discovery_order,ts_ns,ts_relative_ns" > "$EVENTS_CSV"

# Use nanosecond precision for timestamps
TS_START=$(date +%s%N)
TS_START_SEC=$(date +%s)

# Initial wait to let connections establish (especially important for bootstrap node)
# In star topology, bootstrap should have connections from all leaves
echo "Waiting 10 seconds for initial connections to establish..."
sleep 10

# Poll each node's /neighbors endpoint
jq -r '.[] | "\(.id)|\(.control_addr)"' "$NODES_JSON" | while IFS='|' read -r node_id control_addr; do
  echo "Node $node_id ($control_addr)..."
  
  TS_FIRST=""
  TS_K=""
  NEIGHBORS_AT_END=0
  SEEN_PEERS=()
  DISCOVERY_ORDER=0
  
  # Poll until we reach K neighbors or timeout (60 seconds for mesh topologies)
  POLL_START=$(date +%s)
  POLL_TIMEOUT=60
  LAST_COUNT=0
  STABLE_COUNT=0
  INITIAL_CHECK=true
  
  # Use high-frequency polling for precise timing (every 50ms)
  POLL_INTERVAL=0.05
  
  while :; do
    # Check timeout
    ELAPSED=$(($(date +%s) - POLL_START))
    if [[ $ELAPSED -ge $POLL_TIMEOUT ]]; then
      break
    fi
    
    # Query neighbors (with retry on first check)
    neighbors_json="[]"
    neighbor_count=0
    for retry in {1..2}; do
      neighbors_json=$(curl -sSf "http://$control_addr/neighbors" 2>/dev/null || echo "[]")
      neighbor_count=$(echo "$neighbors_json" | jq 'length' 2>/dev/null || echo "0")
      if [[ "$neighbor_count" -gt 0 ]] || [[ $retry -eq 2 ]]; then
        break
      fi
      sleep 0.01  # 10ms retry delay
    done
    
    # On first check, log initial state
    if [[ "$INITIAL_CHECK" == "true" ]]; then
      INITIAL_CHECK=false
      if [[ "$node_id" -eq 1 ]]; then
        echo "  Bootstrap node initial neighbors: $neighbor_count"
      fi
    fi
    
    # Track individual neighbor discoveries with nanosecond precision
    CURRENT_TS_NS=$(date +%s%N)
    CURRENT_PEERS=$(echo "$neighbors_json" | jq -r '.[] | .peer' 2>/dev/null || echo "")
    
    # Check for new peers
    while IFS= read -r peer_id; do
      if [[ -z "$peer_id" || "$peer_id" == "null" ]]; then
        continue
      fi
      # Check if this peer is new
      IS_NEW=true
      for seen_peer in "${SEEN_PEERS[@]}"; do
        if [[ "$seen_peer" == "$peer_id" ]]; then
          IS_NEW=false
          break
        fi
      done
      
      if [[ "$IS_NEW" == "true" ]]; then
        SEEN_PEERS+=("$peer_id")
        ((DISCOVERY_ORDER++))
        TS_RELATIVE_NS=$((CURRENT_TS_NS - TS_START))
        echo "$node_id,$peer_id,$DISCOVERY_ORDER,$CURRENT_TS_NS,$TS_RELATIVE_NS" >> "$EVENTS_CSV"
        
        # Record first neighbor discovery
        if [[ -z "$TS_FIRST" ]]; then
          TS_FIRST=$CURRENT_TS_NS
          TS_FIRST_REL=$((TS_FIRST - TS_START))
          echo "  First neighbor discovered at +${TS_FIRST_REL}ns ($(awk "BEGIN {printf \"%.6f\", $TS_FIRST_REL/1000000000}")s)"
        fi
        
        # Record K-th neighbor discovery
        if [[ -z "$TS_K" && "$DISCOVERY_ORDER" -ge "$K" ]]; then
          TS_K=$CURRENT_TS_NS
          TS_K_REL=$((TS_K - TS_START))
          echo "  K=$K neighbors reached at +${TS_K_REL}ns ($(awk "BEGIN {printf \"%.6f\", $TS_K_REL/1000000000}")s)"
          break
        fi
      fi
    done <<< "$CURRENT_PEERS"
    
    # If we've reached K, stop polling
    if [[ -n "$TS_K" ]]; then
      break
    fi
    
    # If count is stable for 10 consecutive polls (500ms), stop early
    if [[ "$neighbor_count" -eq "$LAST_COUNT" && "$neighbor_count" -gt 0 ]]; then
      STABLE_COUNT=$((STABLE_COUNT + 1))
      if [[ $STABLE_COUNT -ge 10 ]]; then
        break
      fi
    else
      STABLE_COUNT=0
    fi
    LAST_COUNT=$neighbor_count
    
    sleep "$POLL_INTERVAL"
  done
  
  # Final neighbor count (with retry for reliability)
  neighbors_json="[]"
  neighbor_count=0
  for retry in {1..3}; do
    neighbors_json=$(curl -sSf "http://$control_addr/neighbors" 2>/dev/null || echo "[]")
    neighbor_count=$(echo "$neighbors_json" | jq 'length' 2>/dev/null || echo "0")
    if [[ "$neighbor_count" -gt 0 ]] || [[ $retry -eq 3 ]]; then
      break
    fi
    sleep 0.05
  done
  NEIGHBORS_AT_END=$neighbor_count
  
  # Write CSV row (using nanosecond timestamps)
  echo "$node_id,$control_addr,$TS_START,${TS_FIRST:-},${TS_K:-},$NEIGHBORS_AT_END" >> "$OUTPUT_CSV"
  
  if [[ -n "$TS_FIRST" ]]; then
    TS_FIRST_REL=$((TS_FIRST - TS_START))
    echo "  First: +${TS_FIRST_REL}ns ($(awk "BEGIN {printf \"%.6f\", $TS_FIRST_REL/1000000000}")s)"
  else
    echo "  No neighbors discovered"
  fi
  
  if [[ -n "$TS_K" ]]; then
    TS_K_REL=$((TS_K - TS_START))
    echo "  K=$K: +${TS_K_REL}ns ($(awk "BEGIN {printf \"%.6f\", $TS_K_REL/1000000000}")s)"
  else
    echo "  K=$K not reached (final: $NEIGHBORS_AT_END neighbors)"
  fi
  
  # Debug: Show actual neighbors for Node 1 (bootstrap) if it has any
  if [[ "$node_id" -eq 1 && "$NEIGHBORS_AT_END" -gt 0 ]]; then
    echo "  Neighbors: $(echo "$neighbors_json" | jq -r '.[] | .peer' | tr '\n' ' ')"
  fi
  echo ""
done

echo "Discovery measurement complete!"
echo "Results written to: $OUTPUT_CSV"
echo "Detailed events written to: $EVENTS_CSV"

# Generate plots if Python script is available
if command -v python3 >/dev/null 2>&1; then
  echo ""
  echo "Generating discovery plots..."
  if python3 "$ROOT_DIR/scripts/plots/discovery_plot.py" "$RUN_ID" 2>/dev/null; then
    echo "Plots generated successfully"
  else
    echo "Warning: Failed to generate plots (matplotlib may not be installed)" >&2
  fi
fi

