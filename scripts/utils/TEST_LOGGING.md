# Test Logging Infrastructure

This document describes the structured logging utilities available in `scripts/utils/test_logger.sh` and how to use them in test scripts.

## Quick Start

To use test logging in your script, source the logger at the beginning:

```bash
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source test logger
source "$ROOT_DIR/scripts/utils/test_logger.sh"
```

## Core Functions

### `log_test_start <test_name> [params]`

Logs the start of a test with optional parameters.

**Parameters:**
- `test_name`: Name of the test (required)
- `params`: Optional parameters string (e.g., "nodes=10, size=1024")

**Example:**
```bash
log_test_start "upload_latency_test" "nodes=10, payload_size=1024, iterations=5"
```

**Output:**
- Writes to `test.log` and `summary.log`
- Displays formatted banner in console

### `log_test_end <test_name> <result> [duration]`

Logs the completion of a test with result and optional duration.

**Parameters:**
- `test_name`: Name of the test (required)
- `result`: Test result (PASS, FAIL, SKIP, etc.)
- `duration`: Duration in seconds (optional)

**Example:**
```bash
log_test_end "upload_latency_test" "PASS" "45.23"
```

**Output:**
- Writes to `test.log` and `summary.log`
- Displays formatted completion banner with result and duration

### `log_error <message> [context]`

Logs an error message with optional context.

**Parameters:**
- `message`: Error message (required)
- `context`: Optional context information

**Example:**
```bash
log_error "Failed to start container" "container: my-container, reason: port conflict"
```

**Note:** This function is compatible with `error_handler.sh`'s `log_error` interface.

## Additional Logging Functions

### `log_info <message> [context]`
Logs an informational message.

### `log_warn <message> [context]`
Logs a warning message.

### `log_debug <message> [context]`
Logs a debug message (only shown if `LOG_LEVEL=0`).

### `log_success <message> [context]`
Logs a success message.

## Log Levels

Control logging verbosity with the `LOG_LEVEL` environment variable:

- `0` (DEBUG): Show all messages including debug
- `1` (INFO): Show info, warnings, and errors (default)
- `2` (WARN): Show only warnings and errors
- `3` (ERROR): Show only errors

**Example:**
```bash
export LOG_LEVEL=0  # Enable debug logging
log_debug "This will be shown"
log_info "This will be shown"

export LOG_LEVEL=2  # Only warnings and errors
log_debug "This will NOT be shown"
log_info "This will NOT be shown"
log_warn "This will be shown"
log_error "This will be shown"
```

## Utility Functions

### `get_log_file`
Returns the path to the detailed test log file.

### `get_summary_log_file`
Returns the path to the summary log file.

### `get_log_dir`
Returns the path to the log directory.

### `get_timestamp`
Returns a formatted timestamp string.

### `format_duration <seconds>`
Formats a duration in seconds to a human-readable string (e.g., "45.23s", "1m 23.45s", "123ms").

## Log File Structure

Logs are written to:
- **Detailed log**: `artifacts/swarm_tests/<RUN_ID>/test.log`
  - Contains all log entries with timestamps, run IDs, and levels
  - Format: `[timestamp] [RUN_ID] [LEVEL] message | Context: context`

- **Summary log**: `artifacts/swarm_tests/<RUN_ID>/summary.log`
  - Contains test start/end entries for quick overview
  - Format: `[timestamp] START/END: test_name | Params/Result: ... | Duration: ...`

## Environment Variables

- `RUN_ID`: Test run identifier (default: timestamp from `date +%s`)
- `LOG_DIR`: Base directory for logs (default: `artifacts/swarm_tests`)
- `LOG_LEVEL`: Logging verbosity (default: `1` for INFO)

## Examples

### Example 1: Basic Test Lifecycle

```bash
#!/usr/bin/env bash
source scripts/utils/test_logger.sh

# Start test
log_test_start "my_test" "param1=value1, param2=value2"

# Test execution
test_start_time=$(date +%s.%N)
# ... perform test operations ...
test_end_time=$(date +%s.%N)
duration=$(echo "$test_end_time - $test_start_time" | bc -l)

# End test
if [[ $exit_code -eq 0 ]]; then
  log_test_end "my_test" "PASS" "$duration"
else
  log_test_end "my_test" "FAIL" "$duration"
fi
```

### Example 2: Using Log Levels

```bash
#!/usr/bin/env bash
source scripts/utils/test_logger.sh

# Enable debug logging
export LOG_LEVEL=0

log_debug "Debug information"
log_info "General information")
log_warn "Warning message"
log_error "Error occurred"
```

### Example 3: Integration with Error Handler

```bash
#!/usr/bin/env bash
source scripts/utils/error_handler.sh
source scripts/utils/test_logger.sh

log_test_start "api_test" "endpoint=http://localhost:8080"

if ! check_api_endpoint "http://localhost:8080/health" 5 3; then
  log_error "API health check failed" "url: http://localhost:8080/health"
  log_test_end "api_test" "FAIL" "0"
  exit 1
fi

log_test_end "api_test" "PASS" "2.5"
```

### Example 4: Nested Tests

```bash
#!/usr/bin/env bash
source scripts/utils/test_logger.sh

log_test_start "test_suite" "total_tests=5"

# Test 1
log_test_start "test_1" "nodes=10"
# ... test 1 execution ...
log_test_end "test_1" "PASS" "10.5"

# Test 2
log_test_start "test_2" "nodes=20"
# ... test 2 execution ...
log_test_end "test_2" "FAIL" "15.2"

log_test_end "test_suite" "PARTIAL" "25.7"
```

## Integration Checklist

When adding logging to a script:

- [ ] Source `test_logger.sh` at the beginning
- [ ] Call `log_test_start` before test execution
- [ ] Call `log_test_end` after test execution (in success and failure paths)
- [ ] Use `log_info`, `log_warn`, `log_error` for important events
- [ ] Set `RUN_ID` if you want to group multiple scripts into one run
- [ ] Consider setting `LOG_LEVEL` for debugging

## Best Practices

1. **Always log test start and end**: This creates a clear audit trail
2. **Include meaningful parameters**: Help identify test configuration
3. **Use consistent test names**: Makes it easier to parse logs
4. **Log important events**: Use `log_info` for milestones, `log_warn` for recoverable issues
5. **Set RUN_ID for test suites**: Group related tests together
6. **Use duration tracking**: Helps identify performance regressions

## Console Output Format

The logger provides color-coded console output:

- **Test Start**: Cyan banner with test name and parameters
- **Test End**: Cyan banner with result symbol (✓/✗/⊘) and duration
- **Info**: Blue text
- **Warning**: Yellow text
- **Error**: Red text
- **Debug**: Cyan text (only if LOG_LEVEL=0)
- **Success**: Green text

## Compatibility

The test logger is designed to work alongside `error_handler.sh`:

- Both use the same `RUN_ID` and log directory structure
- `log_error` function is compatible with error_handler's interface
- Can be sourced together without conflicts
