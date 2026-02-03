#!/usr/bin/env bash
set -euo pipefail

# Purpose: Measure message/tuple propagation depth across network sizes to demonstrate O(log_k N) scaling.
# Usage: bash scripts/scenarios/propagation_depth_test.sh [NODES_LIST] [RUNS] [MIN_OUTBOUND]

NODES_LIST="${1:-10,20,40,80,160,320}"
RUNS="${2:-3}"
MIN_OUTBOUND="${3:-4}"

# Parse node counts
IFS=',' read -ra NODE_COUNTS <<< "$NODES_LIST"

echo "=== Propagation Depth Test ==="
echo "Node counts: ${NODE_COUNTS[*]}"
echo "Runs per node count: $RUNS"
echo "Min outbound: $MIN_OUTBOUND"
echo ""

RESULTS_DIR="artifacts/propagation_depth_tests/$(date +%s)"
mkdir -p "$RESULTS_DIR"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

# Cleanup function
cleanup_docker() {
  echo "Cleaning up Docker containers..."
  docker-compose down >/dev/null 2>&1 || true
  sleep 2
}

# Cleanup before starting
cleanup_docker

# Run test for each node count
for N in "${NODE_COUNTS[@]}"; do
  echo "=========================================="
  echo "Testing N=$N nodes ($RUNS runs)"
  echo "=========================================="
  
  # Run multiple times for this node count
  for run_num in $(seq 1 "$RUNS"); do
    echo "  Run $run_num/$RUNS..."
    
    RUN_ID="prop_depth_$(date +%s)_${N}_${run_num}"
    RUN_DIR="$RESULTS_DIR/$RUN_ID"
    mkdir -p "$RUN_DIR"
    
    # Start Docker nodes
    echo "    Starting $N Docker nodes..."
    if ! bash scripts/docker/start.sh "$N" >"$RUN_DIR/docker_start.log" 2>&1; then
      echo "    ERROR: Failed to start Docker nodes" >&2
      cleanup_docker
      continue
    fi
    
    # Wait for network to stabilize
    echo "    Waiting for network to stabilize..."
    sleep 30
    
    # Collect neighbor graph from all nodes
    echo "    Collecting neighbor graph..."
    NODES_JSON="$RUN_DIR/nodes.json"
    > "$NODES_JSON"
    
    # Build nodes.json with control addresses
    echo "[" > "$NODES_JSON"
    FIRST=true
    for i in $(seq 1 "$N"); do
      if [[ "$i" -eq 1 ]]; then
        SERVICE="bootstrap"
      else
        SERVICE="node$i"
      fi
      
      CTRL_FILE="/app/logs/$SERVICE.json"
      if docker-compose exec -T "$SERVICE" test -f "$CTRL_FILE" 2>/dev/null; then
        CTRL_ADDR=$(docker-compose exec -T "$SERVICE" jq -r '.addr' "$CTRL_FILE" 2>/dev/null || echo "")
        if [[ -n "$CTRL_ADDR" && "$CTRL_ADDR" != "null" ]]; then
          PEER_ID=$(docker-compose exec -T "$SERVICE" curl -sf "http://$CTRL_ADDR/id" 2>/dev/null | jq -r '.peer' || echo "")
          if [[ -n "$PEER_ID" && "$PEER_ID" != "null" ]]; then
            if [[ "$FIRST" == "true" ]]; then
              FIRST=false
            else
              echo "," >> "$NODES_JSON"
            fi
            echo "{\"id\":$i,\"peer_id\":\"$PEER_ID\",\"control_addr\":\"$CTRL_ADDR\",\"service\":\"$SERVICE\"}" >> "$NODES_JSON"
          fi
        fi
      fi
    done
    echo "]" >> "$NODES_JSON"
    
    # Collect neighbor lists for each node
    NEIGHBORS_FILE="$RUN_DIR/neighbors.json"
    > "$NEIGHBORS_FILE"
    echo "{" > "$NEIGHBORS_FILE"
    FIRST_NODE=true
    jq -r '.[] | "\(.id)|\(.control_addr)|\(.peer_id)|\(.service)"' "$NODES_JSON" | while IFS='|' read -r node_id control_addr peer_id service; do
      if [[ -z "$node_id" || -z "$control_addr" || -z "$service" ]]; then
        continue
      fi
      
      if [[ "$FIRST_NODE" == "true" ]]; then
        FIRST_NODE=false
      else
        echo "," >> "$NEIGHBORS_FILE"
      fi
      
      # Get neighbors via control endpoint (curl from inside container)
      NEIGHBORS_JSON=$(docker-compose exec -T "$service" curl -sf "http://$control_addr/neighbors" 2>/dev/null || echo "[]")
      NEIGHBOR_PEERS=$(echo "$NEIGHBORS_JSON" | jq -r '.[] | .peer' 2>/dev/null || echo "")
      
      echo -n "\"$peer_id\":[" >> "$NEIGHBORS_FILE"
      FIRST_NEIGHBOR=true
      for neighbor_peer in $NEIGHBOR_PEERS; do
        if [[ -n "$neighbor_peer" && "$neighbor_peer" != "null" ]]; then
          if [[ "$FIRST_NEIGHBOR" == "true" ]]; then
            FIRST_NEIGHBOR=false
          else
            echo -n "," >> "$NEIGHBORS_FILE"
          fi
          echo -n "\"$neighbor_peer\"" >> "$NEIGHBORS_FILE"
        fi
      done
      echo -n "]" >> "$NEIGHBORS_FILE"
    done
    echo "" >> "$NEIGHBORS_FILE"
    echo "}" >> "$NEIGHBORS_FILE"
    
    # Compute propagation depth using Python script
    echo "    Computing propagation depths..."
    python3 <<EOF > "$RUN_DIR/depths.json" 2>&1 || true
import json
import sys
from collections import deque

# Load nodes and neighbors
with open("$NODES_JSON") as f:
    nodes = json.load(f)

with open("$NEIGHBORS_FILE") as f:
    neighbors = json.load(f)

# Build adjacency list
graph = {}
peer_to_id = {}
for node in nodes:
    peer_id = node['peer_id']
    peer_to_id[peer_id] = node['id']
    graph[peer_id] = neighbors.get(peer_id, [])

# BFS from bootstrap (node 1) to compute distances
source_peer = None
for node in nodes:
    if node['id'] == 1:
        source_peer = node['peer_id']
        break

if not source_peer or source_peer not in graph:
    print(json.dumps({"error": "source not found"}), file=sys.stderr)
    sys.exit(1)

distances = {}
queue = deque([(source_peer, 0)])
distances[source_peer] = 0

while queue:
    current, dist = queue.popleft()
    for neighbor in graph.get(current, []):
        if neighbor not in distances:
            distances[neighbor] = dist + 1
            queue.append((neighbor, dist + 1))

# Calculate propagation depth metrics
all_depths = sorted([d for d in distances.values() if d > 0])
if not all_depths:
    result = {
        "n_nodes": $N,
        "k_avg": 0,
        "depth_50": 0,
        "depth_90": 0,
        "depth_100": 0,
        "all_depths": []
    }
else:
    n_reachable = len(all_depths)
    depth_50_idx = int(n_reachable * 0.5)
    depth_90_idx = int(n_reachable * 0.9)
    depth_100_idx = n_reachable - 1
    
    # Calculate average degree k
    total_edges = sum(len(graph.get(p, [])) for p in graph)
    k_avg = total_edges / len(graph) if graph else 0
    
    result = {
        "n_nodes": $N,
        "k_avg": k_avg,
        "depth_50": all_depths[depth_50_idx] if depth_50_idx < len(all_depths) else all_depths[-1],
        "depth_90": all_depths[depth_90_idx] if depth_90_idx < len(all_depths) else all_depths[-1],
        "depth_100": all_depths[depth_100_idx] if depth_100_idx < len(all_depths) else all_depths[-1],
        "all_depths": all_depths,
        "n_reachable": n_reachable
    }

print(json.dumps(result, indent=2))
EOF
    
    # Record run info
    if [[ -f "$RUN_DIR/depths.json" ]]; then
      DEPTH_DATA=$(cat "$RUN_DIR/depths.json")
      if echo "$DEPTH_DATA" | jq -e . >/dev/null 2>&1; then
        echo "$N|$RUN_ID" >> "$RESULTS_DIR/runs.txt"
        echo "$DEPTH_DATA" >> "$RESULTS_DIR/depths.jsonl"
      fi
    fi
    
    # Cleanup Docker for next run
    cleanup_docker
    
    echo "    Completed (RUN_ID=$RUN_ID)"
  done
  
  echo "  Completed all $RUNS runs for N=$N"
  echo ""
done

echo "=========================================="
echo "Propagation depth test complete!"
echo "Results directory: $RESULTS_DIR"
echo ""
echo "To generate plots, run:"
echo "  python3 scripts/plots/propagation_depth_plot.py $RESULTS_DIR"
echo "  # Or: make plot-propagation-depth RESULTS_DIR=$RESULTS_DIR"
echo ""
echo "$RESULTS_DIR" > "$RESULTS_DIR/.results_dir"
