# Results Directory Structure

This document describes the results directory structure and utilities for organizing test results.

## Directory Structure

Test results are organized in the following structure:

```
artifacts/swarm_comparison_tests/
└── <timestamp>/
    ├── README.md           # Test run metadata and documentation
    ├── metadata.json       # Structured test metadata
    ├── our_system/         # Our system test results
    │   ├── upload_results.csv
    │   ├── download_results.csv
    │   └── ...
    ├── swarm/              # Swarm test results
    │   ├── upload_results.csv
    │   ├── download_results.csv
    │   └── ...
    ├── comparison/         # Aggregated comparison data
    │   ├── aggregated_upload.csv
    │   ├── aggregated_download.csv
    │   ├── statistics.json
    │   └── ...
    ├── plots/              # Generated visualizations
    │   ├── upload_latency_comparison.png
    │   ├── download_throughput.png
    │   └── ...
    └── logs/               # Test execution logs
        ├── test.log
        ├── summary.log
        ├── errors.log
        └── ...
```

## Quick Start

To use the results directory structure in your script:

```bash
#!/usr/bin/env bash
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source results directory utilities (should be first)
source "$ROOT_DIR/scripts/utils/results_dir.sh"

# Initialize structure (creates directories)
init_results_dir

# Source other utilities (they will use the results directory)
source "$ROOT_DIR/scripts/utils/test_logger.sh"
source "$ROOT_DIR/scripts/utils/error_handler.sh"
```

## Functions

### `init_results_dir [timestamp] [run_id]`

Creates the results directory structure for a test run.

**Parameters:**
- `timestamp`: Optional custom timestamp (default: `YYYYMMDD_HHMMSS`)
- `run_id`: Optional custom run ID (default: Unix timestamp)

**Returns:** Path to the created results directory

**Example:**
```bash
RESULTS_DIR=$(init_results_dir)
echo "Results will be saved to: $RESULTS_DIR"
```

### `get_results_dir`

Returns the path to the main results directory.

### `get_our_system_dir`

Returns the path to the `our_system/` subdirectory.

### `get_swarm_dir`

Returns the path to the `swarm/` subdirectory.

### `get_comparison_dir`

Returns the path to the `comparison/` subdirectory.

### `get_plots_dir`

Returns the path to the `plots/` subdirectory.

### `get_logs_dir`

Returns the path to the `logs/` subdirectory.

### `get_result_path <system> <filename> [subdir]`

Returns a full path for saving results in the appropriate subdirectory.

**Parameters:**
- `system`: System name (`our_system`, `swarm`, `comparison`, `plots`, `logs`)
- `filename`: Name of the file to save
- `subdir`: Optional subdirectory within the system directory

**Example:**
```bash
# Save our system upload results
UPLOAD_CSV=$(get_result_path "our_system" "upload_results.csv")
echo "system,latency" > "$UPLOAD_CSV"

# Save comparison plot
PLOT_PATH=$(get_result_path "plots" "comparison.png")
# ... generate plot and save to $PLOT_PATH
```

### `save_test_metadata <test_name> [params] [start_time] [end_time] [result]`

Saves test metadata to `metadata.json` in the results directory.

**Parameters:**
- `test_name`: Name of the test
- `params`: JSON object string with test parameters (default: `{}`)
- `start_time`: Unix timestamp of test start (default: current time)
- `end_time`: Unix timestamp of test end (optional)
- `result`: Test result string (optional)

**Example:**
```bash
save_test_metadata \
  "upload_latency_test" \
  '{"nodes": 10, "payload_size": 1024}' \
  "$(date +%s)" \
  "$(date +%s)" \
  "PASS"
```

## Integration with Other Utilities

The results directory structure integrates seamlessly with:

### Test Logger (`test_logger.sh`)

When `results_dir.sh` is sourced before `test_logger.sh`, logs are automatically written to `logs/test.log` and `logs/summary.log`:

```bash
source scripts/utils/results_dir.sh
source scripts/utils/test_logger.sh

init_results_dir
log_test_start "my_test" "params"
# ... test execution ...
log_test_end "my_test" "PASS" "10.5"
# Logs are saved to logs/test.log and logs/summary.log
```

### Error Handler (`error_handler.sh`)

Errors are automatically logged to `logs/errors.log`:

```bash
source scripts/utils/results_dir.sh
source scripts/utils/error_handler.sh

init_results_dir
log_error "Something went wrong" "context"
# Error is saved to logs/errors.log
```

## Environment Variables

- `RESULTS_BASE_DIR`: Base directory for results (default: `artifacts/swarm_comparison_tests`)
- `TIMESTAMP`: Timestamp for this run (default: `YYYYMMDD_HHMMSS`)
- `RUN_ID`: Unique run identifier (default: Unix timestamp)

These can be overridden before sourcing:

```bash
export RESULTS_BASE_DIR="custom/results/path"
export TIMESTAMP="20260216_120000"
export RUN_ID="my-custom-run-id"
source scripts/utils/results_dir.sh
init_results_dir
```

## Example: Complete Test Script

```bash
#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source utilities in order
source "$ROOT_DIR/scripts/utils/results_dir.sh"
source "$ROOT_DIR/scripts/utils/test_logger.sh"
source "$ROOT_DIR/scripts/utils/error_handler.sh"

# Initialize results directory
init_results_dir

# Log test start
log_test_start "upload_test" "nodes=10, size=1024"
test_start=$(date +%s)

# Save results to appropriate directories
OUR_UPLOAD_CSV=$(get_result_path "our_system" "upload_results.csv")
SWARM_UPLOAD_CSV=$(get_result_path "swarm" "upload_results.csv")

# ... perform tests and save results ...
echo "system,latency" > "$OUR_UPLOAD_CSV"
echo "our_system,10.5" >> "$OUR_UPLOAD_CSV"

# Save metadata
save_test_metadata \
  "upload_test" \
  '{"nodes": 10, "payload_size": 1024}' \
  "$test_start" \
  "$(date +%s)" \
  "PASS"

# Log test end
test_end=$(date +%s)
duration=$(echo "$test_end - $test_start" | bc -l)
log_test_end "upload_test" "PASS" "$duration"

echo "Results saved to: $RESULTS_DIR"
```

## Directory Organization Best Practices

1. **Use system-specific directories**: Save results to `our_system/` or `swarm/` based on the system being tested
2. **Save comparisons separately**: Aggregate comparison data goes in `comparison/`
3. **Organize plots**: All visualizations go in `plots/`
4. **Centralize logs**: All logs go in `logs/`
5. **Use descriptive filenames**: Include test name, system, and metric in filenames
6. **Save metadata**: Use `save_test_metadata()` to track test configuration and results

## File Naming Conventions

Suggested naming conventions:

- **CSV files**: `<test_name>_<system>_<metric>.csv`
  - Example: `upload_latency_our_system.csv`, `download_throughput_swarm.csv`
- **JSON files**: `<test_name>_<system>_<type>.json`
  - Example: `replication_stats_our_system.json`, `comparison_summary.json`
- **Plots**: `<metric>_comparison.png` or `<test_name>_<system>.png`
  - Example: `upload_latency_comparison.png`, `replication_propagation_our_system.png`
- **Logs**: Descriptive names like `test.log`, `summary.log`, `errors.log`

## Migration from Legacy Structure

If you have scripts using the legacy structure (`artifacts/swarm_tests/<RUN_ID>/`), they will continue to work. The utilities automatically fall back to the legacy structure if `results_dir.sh` is not sourced or `RESULTS_DIR` is not set.

To migrate existing scripts:

1. Add `source scripts/utils/results_dir.sh` at the beginning
2. Call `init_results_dir` before other utilities
3. Update file paths to use `get_result_path()` or the exported directory variables
