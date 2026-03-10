#!/usr/bin/env bash
set -euo pipefail

# scripts/scenarios/run_all_and_plot.sh
# Runs all major scenario tests and generates their plots.

# Default configuration (can be overridden by env vars)
# Use smaller values for a quick check if QUICK=1
if [[ "${QUICK:-0}" == "1" ]]; then
    export NODES_LIST="5,10"
    export RUNS="1"
    export DURATION_S="30"
else
    # Default full run
    export NODES_LIST="${NODES_LIST:-5,10,20}"
    export RUNS="${RUNS:-3}"
fi

echo "=========================================="
echo "Starting Full Test Suite & Plot Generation"
echo "TS: $(date)"
echo "Mode: $( [[ "${QUICK:-0}" == "1" ]] && echo "QUICK" || echo "FULL" )"
echo "=========================================="

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

# Ensure python dependencies
if ! python3 -c "import matplotlib, pandas" 2>/dev/null; then
    echo "WARNING: matplotlib or pandas not found. Plots might fail."
    echo "Install with: pip install matplotlib pandas"
    # We continue anyway as tests are valuable even without plots
fi

# 1. Convergence Test
echo ""
echo ">>> Run 1/6: Convergence Test"
bash scripts/scenarios/convergence_test.sh
# Find latest result
CONV_DIR=$(ls -td artifacts/convergence_tests/* 2>/dev/null | head -1)
if [[ -d "$CONV_DIR" ]]; then
    echo "Plotting Convergence Results from $CONV_DIR..."
    python3 scripts/plots/convergence_plot.py "$CONV_DIR"
else
    echo "Error: Could not find convergence test results."
fi

# 2. Discovery Test
echo ""
echo ">>> Run 2/6: Discovery Test"
bash scripts/scenarios/discovery_test.sh
# Find latest result
DISC_DIR=$(ls -td artifacts/discovery_tests/* 2>/dev/null | head -1)
if [[ -d "$DISC_DIR" ]]; then
    echo "Plotting Discovery Results from $DISC_DIR..."
    # Note: Script echo said discovery_dynamics_plot.py but file is discovery_plot.py
    if [[ -f "scripts/plots/discovery_plot.py" ]]; then
        python3 scripts/plots/discovery_plot.py "$DISC_DIR"
    elif [[ -f "scripts/plots/discovery_dynamics_plot.py" ]]; then
        python3 scripts/plots/discovery_dynamics_plot.py "$DISC_DIR"
    else
        echo "Error: Discovery plot script not found."
    fi
else
    echo "Error: Could not find discovery test results."
fi

# 3. Propagation Depth Test
echo ""
echo ">>> Run 3/6: Propagation Depth Test"
bash scripts/scenarios/propagation_depth_test.sh
# Find latest result
PROP_DIR=$(ls -td artifacts/propagation_depth_tests/* 2>/dev/null | head -1)
if [[ -d "$PROP_DIR" ]]; then
    echo "Plotting Propagation Depth Results from $PROP_DIR..."
    python3 scripts/plots/propagation_depth_plot.py "$PROP_DIR"
    
    # Also create repair scaling plot if enough data
    if [[ -f "scripts/plots/repair_scaling.py" ]]; then
         python3 scripts/plots/repair_scaling.py "$PROP_DIR" || true
    fi
else
    echo "Error: Could not find propagation depth test results."
fi

# 4. Restore Efficiency Test
echo ""
echo ">>> Run 4/6: Restore Efficiency Test"
bash scripts/scenarios/restore_efficiency_test.sh
# Find latest result
REST_DIR=$(ls -td artifacts/restore_efficiency_tests/* 2>/dev/null | head -1)
if [[ -d "$REST_DIR" ]]; then
    echo "Plotting Restore Efficiency Results from $REST_DIR..."
    python3 scripts/plots/restore_efficiency_plot.py "$REST_DIR"
else
    echo "Error: Could not find restore efficiency test results."
fi

# 5. Scaling Test (Self-plotting)
echo ""
echo ">>> Run 5/6: Scaling Test"
bash scripts/scenarios/scaling_test.sh
# No manual plot step needed as scaling_test.sh calls scaling_analysis.py

# 6. Throughput Test (Self-plotting)
echo ""
echo ">>> Run 6/6: Throughput Test"
bash scripts/scenarios/throughput_test.sh
# No manual plot step needed as throughput_test.sh calls throughput_plot.py

echo ""
echo "=========================================="
echo "All tests completed."
echo "Check artifacts/ directory for results and plots."
echo "=========================================="
