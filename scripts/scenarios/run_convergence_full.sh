#!/usr/bin/env bash
set -euo pipefail

# Purpose: Run full-scale convergence test and generate plot in one command.
# Usage: bash scripts/scenarios/run_convergence_full.sh

echo "=========================================="
echo "Full-Scale Convergence Test"
echo "=========================================="
echo "Node counts: 5, 10, 20, 50, 100, 500"
echo "Runs per node count: 5"
echo ""
echo "This will take a while (approximately 2-3 hours)..."
echo ""

# Run the convergence test and capture output
TEST_OUTPUT=$(bash scripts/scenarios/convergence_test.sh "5,10,20,50,100,500" 5 4 2>&1 | tee /tmp/convergence_test.log)

# Extract results directory from output
RESULTS_DIR=$(echo "$TEST_OUTPUT" | grep -E "^Results directory: artifacts/convergence_tests/[0-9]+" | tail -1 | sed 's/Results directory: //')

# Fallback: try to read from .results_dir file
if [[ -z "$RESULTS_DIR" ]]; then
  LATEST_DIR=$(ls -td artifacts/convergence_tests/*/ 2>/dev/null | head -1)
  if [[ -n "$LATEST_DIR" && -f "$LATEST_DIR/.results_dir" ]]; then
    RESULTS_DIR=$(cat "$LATEST_DIR/.results_dir")
  elif [[ -n "$LATEST_DIR" ]]; then
    RESULTS_DIR="${LATEST_DIR%/}"
  fi
fi

if [[ -z "$RESULTS_DIR" || ! -d "$RESULTS_DIR" ]]; then
  echo "ERROR: Could not determine results directory" >&2
  echo "Check /tmp/convergence_test.log for details" >&2
  exit 1
fi

echo ""
echo "=========================================="
echo "Generating Plot"
echo "=========================================="
echo "Results directory: $RESULTS_DIR"
echo ""

# Generate the plot
python3 scripts/plots/convergence_plot.py "$RESULTS_DIR"

echo ""
echo "=========================================="
echo "Complete!"
echo "=========================================="
echo "Plot saved to: $RESULTS_DIR/convergence_plot.png"
echo ""
