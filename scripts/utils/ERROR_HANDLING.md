# Error Handling Utilities

This document describes the error handling utilities available in `scripts/utils/error_handler.sh` and how to use them in test scripts.

## Quick Start

To use error handling in your script, source the error handler at the beginning:

```bash
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source error handler
source "$ROOT_DIR/scripts/utils/error_handler.sh"

# Initialize error logging
RUN_ID="${RUN_ID:-$(date +%s)}"
ERROR_LOG_DIR="artifacts/swarm_tests/$RUN_ID"
export RUN_ID ERROR_LOG_DIR
mkdir -p "$ERROR_LOG_DIR"
```

## Available Functions

### Logging Functions

#### `log_error <message> [context]`
Logs an error message to both the error log file and stderr.

```bash
log_error "Failed to start container" "container: my-container"
```

#### `log_warning <message> [context]`
Logs a warning message to both the error log file and stderr.

```bash
log_warning "API endpoint slow to respond" "url: http://example.com/api"
```

### Container Health Checking

#### `check_container_health <container> [max_attempts]`
Checks if a Docker container is healthy and running. Returns 0 on success, 1 on failure.

```bash
if check_container_health "my-container" 5; then
  echo "Container is healthy"
else
  log_error "Container is not healthy"
fi
```

### API Endpoint Verification

#### `check_api_endpoint <url> [timeout] [max_attempts]`
Verifies that an HTTP API endpoint is accessible. Returns 0 on success, 1 on failure.

```bash
if check_api_endpoint "http://localhost:8080/health" 5 3; then
  echo "API is accessible"
fi
```

#### `check_api_endpoint_container <container> <url> [timeout] [max_attempts]`
Verifies that an HTTP API endpoint is accessible from within a Docker container.

```bash
if check_api_endpoint_container "my-container" "http://localhost:8080/health" 5 3; then
  echo "API is accessible in container"
fi
```

### Retry Logic

#### `retry_with_backoff <max_attempts> <initial_delay> <max_delay> <command...>`
Retries a command with exponential backoff. Returns 0 on success, 1 on failure.

```bash
if retry_with_backoff 3 1 10 docker cp file.txt container:/tmp/; then
  echo "Copy succeeded"
fi
```

### Timeout Handling

#### `with_timeout <timeout_seconds> <command...>`
Executes a command with a timeout. Returns the command's exit code, or 124 on timeout.

```bash
if response=$(with_timeout 30 curl -s http://example.com); then
  echo "Request succeeded"
else
  log_error "Request timed out or failed"
fi
```

### Tool Validation

#### `check_docker`
Checks if Docker is installed and the daemon is running.

```bash
if ! check_docker; then
  exit 1
fi
```

#### `check_docker_compose`
Checks if docker-compose is available.

```bash
if ! check_docker_compose; then
  log_warning "docker-compose not found, some features may be limited"
fi
```

#### `check_required_tools <tool1> [tool2] ...`
Checks if required command-line tools are available.

```bash
if ! check_required_tools curl jq bc; then
  exit 1
fi
```

### Utility Functions

#### `get_error_log_file`
Returns the path to the current error log file.

```bash
ERROR_LOG=$(get_error_log_file)
echo "Errors logged to: $ERROR_LOG"
```

#### `get_error_log_dir`
Returns the path to the current error log directory.

```bash
ERROR_DIR=$(get_error_log_dir)
```

## Error Log Location

Errors are logged to: `artifacts/swarm_tests/<RUN_ID>/errors.log`

The `RUN_ID` defaults to a timestamp (`date +%s`) but can be overridden via environment variable:

```bash
export RUN_ID="my-test-run-001"
```

## Examples

### Example 1: Checking Container Health Before Use

```bash
# Check container health before using it
if ! check_container_health "my-container" 5; then
  log_error "Container not healthy, cannot proceed"
  exit 1
fi
```

### Example 2: Retrying Failed Operations

```bash
# Retry upload with backoff
if ! retry_with_backoff 3 2 10 upload_file "$API_URL" "$file_path"; then
  log_error "Upload failed after retries"
  exit 1
fi
```

### Example 3: Timeout Protection

```bash
# Execute command with timeout
if ! response=$(with_timeout 30 docker exec container curl -s http://api/endpoint); then
  log_error "Request timed out"
  exit 1
fi
```

### Example 4: API Verification Before Test

```bash
# Verify APIs are accessible before starting tests
if ! check_api_endpoint "http://localhost:8080/health" 5 3; then
  log_error "Our system API not accessible"
  exit 1
fi

if ! check_api_endpoint "http://172.20.0.200:8500/" 5 3; then
  log_error "Swarm API not accessible"
  exit 1
fi
```

## Integration Checklist

When adding error handling to a script:

- [ ] Source `error_handler.sh` at the beginning
- [ ] Initialize `RUN_ID` and `ERROR_LOG_DIR`
- [ ] Replace manual Docker checks with `check_container_health`
- [ ] Replace manual API checks with `check_api_endpoint` or `check_api_endpoint_container`
- [ ] Wrap potentially failing operations with `retry_with_backoff`
- [ ] Add timeouts to long-running operations with `with_timeout`
- [ ] Replace `echo "Error: ..."` with `log_error`
- [ ] Replace `echo "Warning: ..."` with `log_warning`
- [ ] Validate required tools with `check_required_tools`

## Notes

- All error handling functions are exported and can be used in subshells
- Error logs are automatically created in the appropriate directory
- Functions handle edge cases like missing commands gracefully
- Timeout function falls back to background process killing if `timeout` command is not available
