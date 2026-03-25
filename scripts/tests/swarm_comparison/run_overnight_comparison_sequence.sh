#!/usr/bin/env bash
set -euo pipefail

# Purpose: Run a fixed sequence of matrix cells overnight — each line is a full explicit invocation
# of run_single_comparison.sh (or docker prune), in order. No shared test runner beyond this file.
#
# Run from anywhere: ./scripts/tests/swarm_comparison/run_overnight_comparison_sequence.sh
# Log to a file:    ./scripts/tests/swarm_comparison/run_overnight_comparison_sequence.sh 2>&1 | tee overnight_matrix.log
# Stops on first non-zero exit (set -e). To continue after failures, remove set -e or append "|| true" to a line.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
cd "$ROOT_DIR"

stamp() { echo "=== $(date -Iseconds) === $*"; }

stamp "run_single_comparison.sh --test upload --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test upload --nodes 100 --iterations 10

stamp "run_single_comparison.sh --test download_cold --nodes 50 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test download_cold --nodes 50 --iterations 10

stamp "run_single_comparison.sh --test download_cold --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test download_cold --nodes 100 --iterations 10

stamp "run_single_comparison.sh --test download_warm --nodes 50 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test download_warm --nodes 50 --iterations 10

stamp "run_single_comparison.sh --test download_warm --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test download_warm --nodes 100 --iterations 10

stamp "run_single_comparison.sh --test lookup_latency --nodes 50 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test lookup_latency --nodes 50 --iterations 10

stamp "run_single_comparison.sh --test lookup_latency --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test lookup_latency --nodes 100 --iterations 10

stamp "run_single_comparison.sh --test lookup_complexity --nodes 50 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test lookup_complexity --nodes 50 --iterations 10

stamp "run_single_comparison.sh --test lookup_complexity --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test lookup_complexity --nodes 100 --iterations 10

stamp "run_single_comparison.sh --test replication --nodes 50 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test replication --nodes 50 --iterations 10

stamp "run_single_comparison.sh --test replication --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test replication --nodes 100 --iterations 10

stamp "run_single_comparison.sh --test replication_distribution --nodes 50 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test replication_distribution --nodes 50 --iterations 10

stamp "docker builder prune -a -f"
docker builder prune -a -f

stamp "docker buildx prune -a -f"
docker buildx prune -a -f

stamp "run_single_comparison.sh --test replication_distribution --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test replication_distribution --nodes 100 --iterations 10

stamp "run_single_comparison.sh --test repair_time --nodes 50 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test repair_time --nodes 50 --iterations 10

stamp "run_single_comparison.sh --test repair_time --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test repair_time --nodes 100 --iterations 10

stamp "run_single_comparison.sh --test network_hops --nodes 50 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test network_hops --nodes 50 --iterations 10

stamp "run_single_comparison.sh --test network_hops --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test network_hops --nodes 100 --iterations 10

stamp "run_single_comparison.sh --test routing_overhead --nodes 50 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test routing_overhead --nodes 50 --iterations 10

stamp "run_single_comparison.sh --test routing_overhead --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test routing_overhead --nodes 100 --iterations 10

stamp "run_single_comparison.sh --test storage_efficiency --nodes 50 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test storage_efficiency --nodes 50 --iterations 10

stamp "run_single_comparison.sh --test storage_efficiency --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test storage_efficiency --nodes 100 --iterations 10

stamp "run_single_comparison.sh --test concurrent --nodes 50 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test concurrent --nodes 50 --iterations 10

stamp "run_single_comparison.sh --test concurrent --nodes 100 --iterations 10"
"$SCRIPT_DIR/run_single_comparison.sh" --test concurrent --nodes 100 --iterations 10

stamp "sequence complete"
