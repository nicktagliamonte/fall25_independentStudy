#!/usr/bin/env bash
# Purpose: Test configuration defaults and environment variable overrides
# Usage: source scripts/scenarios/swarm_test_config.sh
#   Then use variables like $SWARM_TEST_NODE_COUNTS, etc.

# Node counts for scalability tests
SWARM_TEST_NODE_COUNTS="${SWARM_TEST_NODE_COUNTS:-10,20,40,80}"

# Payload sizes in bytes (1KB to 1MB)
SWARM_TEST_PAYLOAD_SIZES="${SWARM_TEST_PAYLOAD_SIZES:-1024,10240,102400,1048576}"

# Number of iterations per test
SWARM_TEST_ITERATIONS="${SWARM_TEST_ITERATIONS:-5}"

# Stabilization wait time in seconds (time to wait for nodes to be ready)
SWARM_TEST_STABILIZATION_WAIT="${SWARM_TEST_STABILIZATION_WAIT:-30}"

# Propagation check interval in seconds (how often to poll for content propagation)
SWARM_TEST_PROPAGATION_INTERVAL="${SWARM_TEST_PROPAGATION_INTERVAL:-2}"

# Propagation timeout in seconds (max time to wait for content to propagate)
SWARM_TEST_PROPAGATION_TIMEOUT="${SWARM_TEST_PROPAGATION_TIMEOUT:-300}"

# Convergence test: target number of neighbors for new node
SWARM_TEST_K_NEIGHBORS="${SWARM_TEST_K_NEIGHBORS:-4}"

# Convergence test: max wait time in seconds
SWARM_TEST_CONVERGENCE_TIMEOUT="${SWARM_TEST_CONVERGENCE_TIMEOUT:-120}"

# Resource monitoring: polling interval in seconds
SWARM_TEST_RESOURCE_INTERVAL="${SWARM_TEST_RESOURCE_INTERVAL:-1}"

# Network metrics: polling interval in seconds
SWARM_TEST_METRICS_INTERVAL="${SWARM_TEST_METRICS_INTERVAL:-1}"

# Test data directory
SWARM_TEST_DATA_DIR="${SWARM_TEST_DATA_DIR:-artifacts/test_data}"

# Output directory (will be timestamped if not set)
SWARM_TEST_OUTPUT_DIR="${SWARM_TEST_OUTPUT_DIR:-}"

# Swarm API address
SWARM_TEST_SWARM_API="${SWARM_TEST_SWARM_API:-http://172.20.0.200:8500}"

# Our system API (auto-detected if not set)
SWARM_TEST_OUR_API="${SWARM_TEST_OUR_API:-}"

# Cleanup after tests (true/false)
SWARM_TEST_CLEANUP="${SWARM_TEST_CLEANUP:-false}"

# Auto-start nodes if not running (true/false)
SWARM_TEST_AUTO_START="${SWARM_TEST_AUTO_START:-true}"

# Export all variables for use in other scripts
export SWARM_TEST_NODE_COUNTS
export SWARM_TEST_PAYLOAD_SIZES
export SWARM_TEST_ITERATIONS
export SWARM_TEST_STABILIZATION_WAIT
export SWARM_TEST_PROPAGATION_INTERVAL
export SWARM_TEST_PROPAGATION_TIMEOUT
export SWARM_TEST_K_NEIGHBORS
export SWARM_TEST_CONVERGENCE_TIMEOUT
export SWARM_TEST_RESOURCE_INTERVAL
export SWARM_TEST_METRICS_INTERVAL
export SWARM_TEST_DATA_DIR
export SWARM_TEST_OUTPUT_DIR
export SWARM_TEST_SWARM_API
export SWARM_TEST_OUR_API
export SWARM_TEST_CLEANUP
export SWARM_TEST_AUTO_START

# Helper function to print current configuration
print_config() {
  echo "=========================================="
  echo "Swarm Test Configuration"
  echo "=========================================="
  echo "Node counts: $SWARM_TEST_NODE_COUNTS"
  echo "Payload sizes: $SWARM_TEST_PAYLOAD_SIZES bytes"
  echo "Iterations: $SWARM_TEST_ITERATIONS"
  echo "Stabilization wait: ${SWARM_TEST_STABILIZATION_WAIT}s"
  echo "Propagation interval: ${SWARM_TEST_PROPAGATION_INTERVAL}s"
  echo "Propagation timeout: ${SWARM_TEST_PROPAGATION_TIMEOUT}s"
  echo "K neighbors (convergence): $SWARM_TEST_K_NEIGHBORS"
  echo "Convergence timeout: ${SWARM_TEST_CONVERGENCE_TIMEOUT}s"
  echo "Resource monitoring interval: ${SWARM_TEST_RESOURCE_INTERVAL}s"
  echo "Metrics collection interval: ${SWARM_TEST_METRICS_INTERVAL}s"
  echo "Test data directory: $SWARM_TEST_DATA_DIR"
  if [[ -n "$SWARM_TEST_OUTPUT_DIR" ]]; then
    echo "Output directory: $SWARM_TEST_OUTPUT_DIR"
  else
    echo "Output directory: auto-generated (timestamped)"
  fi
  echo "Swarm API: $SWARM_TEST_SWARM_API"
  if [[ -n "$SWARM_TEST_OUR_API" ]]; then
    echo "Our system API: $SWARM_TEST_OUR_API"
  else
    echo "Our system API: auto-detect"
  fi
  echo "Cleanup after tests: $SWARM_TEST_CLEANUP"
  echo "Auto-start nodes: $SWARM_TEST_AUTO_START"
  echo "=========================================="
}

# If script is run directly (not sourced), print config
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  print_config
fi
