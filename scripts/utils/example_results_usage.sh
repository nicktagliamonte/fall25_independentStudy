#!/usr/bin/env bash
# Example: Using the results directory structure in a test script

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source utilities in order (results_dir must be first)
source "$ROOT_DIR/scripts/utils/results_dir.sh"
source "$ROOT_DIR/scripts/utils/test_logger.sh"
source "$ROOT_DIR/scripts/utils/error_handler.sh"

# Initialize results directory structure
echo "Initializing results directory..."
RESULTS_DIR=$(init_results_dir)
echo "Results will be saved to: $RESULTS_DIR"
echo ""

# Log test start
log_test_start "example_test" "nodes=10, payload_size=1024"
test_start_time=$(date +%s)

# Get paths for saving results
OUR_UPLOAD_CSV=$(get_result_path "our_system" "upload_results.csv")
SWARM_UPLOAD_CSV=$(get_result_path "swarm" "upload_results.csv")
COMPARISON_CSV=$(get_result_path "comparison" "aggregated_results.csv")
PLOT_PATH=$(get_result_path "plots" "comparison_chart.png")

echo "Saving results to:"
echo "  Our system: $OUR_UPLOAD_CSV"
echo "  Swarm: $SWARM_UPLOAD_CSV"
echo "  Comparison: $COMPARISON_CSV"
echo "  Plot: $PLOT_PATH"
echo ""

# Simulate saving test results
echo "system,payload_size,latency_ms" > "$OUR_UPLOAD_CSV"
echo "our_system,1024,10.5" >> "$OUR_UPLOAD_CSV"
echo "our_system,1024,11.2" >> "$OUR_UPLOAD_CSV"

echo "system,payload_size,latency_ms" > "$SWARM_UPLOAD_CSV"
echo "swarm,1024,15.3" >> "$SWARM_UPLOAD_CSV"
echo "swarm,1024,16.1" >> "$SWARM_UPLOAD_CSV"

# Save comparison data
echo "system,mean_latency_ms" > "$COMPARISON_CSV"
echo "our_system,10.85" >> "$COMPARISON_CSV"
echo "swarm,15.7" >> "$COMPARISON_CSV"

# Save test metadata
save_test_metadata \
  "example_test" \
  '{"nodes": 10, "payload_size": 1024, "iterations": 2}' \
  "$test_start_time" \
  "$(date +%s)" \
  "PASS"

# Log test end
test_end_time=$(date +%s)
duration=$(echo "$test_end_time - $test_start_time" | bc -l)
log_test_end "example_test" "PASS" "$duration"

echo ""
echo "Test complete! Results saved to: $RESULTS_DIR"
echo ""
echo "Directory structure:"
tree "$RESULTS_DIR" 2>/dev/null || find "$RESULTS_DIR" -type f | head -10
