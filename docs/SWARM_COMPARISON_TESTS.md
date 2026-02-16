# Swarm Comparison Test Suite

This document provides comprehensive documentation for the Swarm comparison test suite, which evaluates the performance of our distributed storage system against Ethereum Swarm (Bee v0.5.8).

## Table of Contents

1. [Overview](#overview)
2. [Prerequisites](#prerequisites)
3. [Quick Start](#quick-start)
4. [Test Scenarios](#test-scenarios)
5. [Running Tests](#running-tests)
6. [Interpreting Results](#interpreting-results)
7. [Troubleshooting](#troubleshooting)
8. [Advanced Usage](#advanced-usage)

## Overview

The Swarm comparison test suite is designed to systematically compare our distributed storage system with Ethereum Swarm across multiple performance dimensions:

- **Upload Latency**: Time to upload content of various sizes
- **Download Throughput**: Time to first byte (TTFB) and total download time
- **Content Replication**: Time for content to propagate across nodes
- **Network Convergence**: Time for new nodes to integrate into the network
- **Resource Usage**: CPU, memory, and network I/O during operations

### Test Architecture

The test suite consists of:

- **Test Scenarios**: Individual test scripts for specific metrics
- **Orchestration**: Main comparison test that runs multiple scenarios
- **Analysis Tools**: Scripts to analyze results and generate reports
- **Validation**: Smoke tests and validation scripts to ensure setup is correct
- **Monitoring**: Resource and network metrics collection

### Directory Structure

```
scripts/
├── scenarios/          # Test scenario scripts
│   ├── swarm_smoke_test.sh          # Quick validation test
│   ├── swarm_upload_test.sh          # Upload latency test
│   ├── swarm_download_test.sh        # Download throughput test
│   ├── swarm_replication_test.sh     # Content replication test
│   ├── swarm_convergence_test.sh     # Network convergence test
│   └── swarm_comparison_test.sh      # Full test suite orchestrator
├── analysis/           # Analysis and reporting tools
│   ├── swarm_comparison_analyze.py   # Python analysis script
│   └── generate_swarm_report.sh      # Markdown report generator
├── monitoring/         # Resource monitoring
│   ├── resource_monitor.sh            # Docker stats collection
│   └── network_metrics.sh             # Network metrics collection
├── validation/        # Validation scripts
│   └── validate_swarm_setup.sh       # Swarm setup validation
└── utils/              # Utility scripts
    ├── error_handler.sh               # Error handling utilities
    ├── test_logger.sh                 # Structured logging
    └── results_dir.sh                 # Results directory management
```

## Prerequisites

### Required Software

- **Docker** and **Docker Compose**: For containerized test environments
- **Bash** 4.0+: For running test scripts
- **Python 3.7+**: For analysis scripts (with pandas, matplotlib, seaborn)
- **jq**: For JSON parsing
- **bc**: For mathematical calculations
- **curl**: For HTTP requests
- **awk**, **sed**: For text processing

### System Requirements

- **CPU**: Multi-core recommended for running multiple nodes
- **Memory**: At least 4GB free for running test nodes
- **Disk Space**: ~10GB for Docker images and test data
- **Network**: Docker network access (default: `172.20.0.0/16`)

### Docker Setup

Ensure Docker is running and you have permission to use it:

```bash
docker info
```

If you see permission errors, add your user to the docker group or use `sudo`.

## Quick Start

### 1. Validate Setup

Before running tests, validate that your Swarm setup is correct:

```bash
./scripts/validation/validate_swarm_setup.sh
```

This checks:
- Docker is available
- Swarm nodes are running
- API endpoints are accessible
- Basic upload/download operations work

### 2. Run Smoke Test

Run a quick smoke test to verify both systems work:

```bash
./scripts/scenarios/swarm_smoke_test.sh --cleanup
```

This will:
- Start 2 nodes for each system
- Upload a 1KB file
- Download it back
- Verify both systems complete successfully
- Clean up nodes (if `--cleanup` is specified)

### 3. Run Full Test Suite

Run the complete comparison test:

```bash
./scripts/scenarios/swarm_comparison_test.sh \
  --nodes 10,20,40 \
  --payload-sizes 1024,10240,102400,1048576 \
  --iterations 5
```

This runs comprehensive tests across multiple node counts and payload sizes.

## Test Scenarios

### Smoke Test (`swarm_smoke_test.sh`)

**Purpose**: Quick validation before running full test suite

**What it tests**:
- Basic upload/download operations
- Both systems can handle simple operations

**Usage**:
```bash
./scripts/scenarios/swarm_smoke_test.sh [--nodes N] [--skip-start] [--cleanup]
```

**Output**: Pass/fail status for each system

### Upload Latency Test (`swarm_upload_test.sh`)

**Purpose**: Measure upload latency for various payload sizes

**What it tests**:
- Time to upload content of different sizes (1KB to 1MB)
- Latency statistics (mean, median, stddev, percentiles)

**Usage**:
```bash
./scripts/scenarios/swarm_upload_test.sh \
  --iterations 10 \
  --output upload_results.csv
```

**Output**: CSV file with columns: `system,payload_size,iteration,latency_ms`

### Download Throughput Test (`swarm_download_test.sh`)

**Purpose**: Measure download performance

**What it tests**:
- Time to first byte (TTFB)
- Total download time
- Throughput calculation

**Usage**:
```bash
./scripts/scenarios/swarm_download_test.sh \
  --iterations 10 \
  --output download_results.csv
```

**Output**: CSV file with columns: `system,payload_size,iteration,ttfb_ms,total_ms`

### Replication Propagation Test (`swarm_replication_test.sh`)

**Purpose**: Measure content propagation time across nodes

**What it tests**:
- Time for content to reach 50%, 90%, and 100% of nodes
- Content availability across network

**Usage**:
```bash
./scripts/scenarios/swarm_replication_test.sh \
  --nodes 10 \
  --poll-interval 1 \
  --max-wait 60
```

**Output**: CSV file with columns: `system,n_nodes,time_to_50pct_s,time_to_90pct_s,time_to_100pct_s`

### Network Convergence Test (`swarm_convergence_test.sh`)

**Purpose**: Measure network convergence when adding new nodes

**What it tests**:
- Time for new node to acquire K neighbors
- Time for existing nodes to discover new node
- Time for network metrics to stabilize

**Usage**:
```bash
./scripts/scenarios/swarm_convergence_test.sh \
  --nodes 10 \
  --k-neighbors 4 \
  --max-wait 120
```

**Output**: CSV file with columns: `system,n_nodes,time_to_k_neighbors_s,time_to_discovery_s,time_to_stable_s`

### Full Comparison Test (`swarm_comparison_test.sh`)

**Purpose**: Orchestrate multiple test scenarios

**What it tests**:
- Runs upload and download tests across multiple configurations
- Aggregates results
- Generates summary reports

**Usage**:
```bash
./scripts/scenarios/swarm_comparison_test.sh \
  --nodes 10,20,40 \
  --payload-sizes 1024,10240,102400,1048576 \
  --iterations 5 \
  --output-dir ./test_results
```

**Output**: Directory with CSV files, logs, and summary reports

## Running Tests

### Basic Test Execution

1. **Start nodes** (if not already running):
   ```bash
   # Start our system (10 nodes)
   ./scripts/docker/start.sh 10
   
   # Start Swarm (10 nodes)
   ./scripts/docker/swarm/start.sh 10
   ```

2. **Run individual test**:
   ```bash
   ./scripts/scenarios/swarm_upload_test.sh --iterations 5
   ```

3. **Run full suite**:
   ```bash
   ./scripts/scenarios/swarm_comparison_test.sh
   ```

### Test Configuration

Tests can be configured via:
- **Command-line arguments**: See `--help` for each script
- **Environment variables**: Some scripts respect environment variables
- **Configuration file**: `scripts/scenarios/swarm_test_config.sh` defines defaults

Example configuration:
```bash
export SWARM_TEST_NODE_COUNTS="10,20,40"
export SWARM_TEST_PAYLOAD_SIZES="1024,10240,102400"
export SWARM_TEST_ITERATIONS=5
```

### Running Tests in Parallel

For faster execution, you can run independent tests in parallel:

```bash
# Terminal 1: Upload test
./scripts/scenarios/swarm_upload_test.sh --output upload.csv &

# Terminal 2: Download test
./scripts/scenarios/swarm_download_test.sh --output download.csv &

# Wait for both to complete
wait
```

### Monitoring During Tests

Monitor resource usage during tests:

```bash
# Terminal 1: Run test
./scripts/scenarios/swarm_comparison_test.sh

# Terminal 2: Monitor resources
./scripts/monitoring/resource_monitor.sh \
  --containers bootstrap,node1,swarm-bootstrap \
  --interval 1 \
  --duration 300 \
  --output resource_usage.csv
```

## Interpreting Results

### CSV File Format

Test results are stored in CSV files with the following formats:

**Upload Results**:
```csv
system,payload_size,iteration,latency_ms
our_system,1024,1,154.96
swarm,1024,1,253.79
```

**Download Results**:
```csv
system,payload_size,iteration,ttfb_ms,total_ms
our_system,1024,1,1.29,1.30
swarm,1024,1,2.45,3.12
```

**Replication Results**:
```csv
system,n_nodes,time_to_50pct_s,time_to_90pct_s,time_to_100pct_s
our_system,10,5.2,8.5,12.3
swarm,10,3.1,6.8,10.2
```

### Statistical Analysis

Use the Python analysis script to generate statistics and visualizations:

```bash
python3 scripts/analysis/swarm_comparison_analyze.py \
  --results-dir test_results_20260216_120002 \
  --output-dir analysis_output
```

This generates:
- Statistical summaries (mean, median, stddev, percentiles)
- Comparison plots (box plots, line charts, bar charts)
- HTML report with tables and visualizations

### Generating Reports

Generate a markdown report from test results:

```bash
./scripts/analysis/generate_swarm_report.sh \
  --results-dir artifacts/swarm_comparison_tests/20260216_131223 \
  --output REPORT.md
```

The report includes:
- Executive summary
- Detailed results tables
- Performance comparisons
- Conclusions and recommendations
- Links to plots and raw data

### Key Metrics to Watch

1. **Upload Latency**:
   - Lower is better
   - Compare mean and median across systems
   - Check for outliers (high stddev)

2. **Download Throughput**:
   - Higher throughput (MB/s) is better
   - TTFB should be low for good user experience
   - Total time should scale linearly with payload size

3. **Replication Time**:
   - Time to 50%: Initial propagation speed
   - Time to 100%: Complete network coverage
   - Lower times indicate better replication

4. **Network Convergence**:
   - Time to K neighbors: How quickly nodes connect
   - Time to discovery: Network awareness speed
   - Lower times indicate better network dynamics

## Troubleshooting

### Common Issues

#### 1. Docker Containers Won't Start

**Symptoms**: Containers fail to start or exit immediately

**Solutions**:
- Check Docker daemon is running: `docker info`
- Check available disk space: `df -h`
- Check Docker logs: `docker logs <container-name>`
- Verify network doesn't conflict: `docker network ls`
- Try removing old containers: `docker-compose down`

#### 2. API Endpoints Not Accessible

**Symptoms**: Tests fail with "API endpoint not accessible" errors

**Solutions**:
- Verify containers are running: `docker ps`
- Check API addresses are correct
- Test connectivity manually: `curl http://172.20.0.200:8500/`
- Check firewall rules
- Verify network configuration: `docker network inspect fall25_independentstudy_node-network`

#### 3. Upload/Download Failures

**Symptoms**: Upload or download operations fail

**Solutions**:
- Check container logs: `docker logs swarm-bootstrap`
- Verify file size limits
- Check available disk space in containers
- Test with smaller payloads first
- Verify API endpoints are correct

#### 4. Nodes Not Connecting

**Symptoms**: Network convergence tests fail, nodes don't discover each other

**Solutions**:
- Verify all nodes are on the same Docker network
- Check bootstrap node is accessible
- Verify network configuration in docker-compose files
- Check for IP address conflicts
- Review network logs: `docker logs <node-name>`

#### 5. Test Timeouts

**Symptoms**: Tests timeout waiting for operations to complete

**Solutions**:
- Increase timeout values in test scripts
- Check system load: `top` or `htop`
- Reduce number of concurrent operations
- Verify network connectivity
- Check for resource constraints (CPU, memory, disk I/O)

#### 6. Swarm-Specific Issues

**Symptoms**: Swarm operations fail or behave unexpectedly

**Solutions**:
- Verify Swarm version: Check Docker image tag
- Check Swarm logs: `docker logs swarm-bootstrap`
- Verify Swarm configuration: Check `docker-compose.swarm.yml`
- Test Swarm API directly: `curl http://172.20.0.200:8500/`
- Review Swarm documentation for known issues

### Debugging Tips

1. **Enable Verbose Output**:
   ```bash
   ./scripts/scenarios/swarm_upload_test.sh --verbose
   ```

2. **Check Logs**:
   ```bash
   # Our system logs
   docker logs bootstrap
   docker logs node1
   
   # Swarm logs
   docker logs swarm-bootstrap
   docker logs swarm-node1
   ```

3. **Validate Setup**:
   ```bash
   ./scripts/validation/validate_swarm_setup.sh --verbose
   ```

4. **Run Smoke Test**:
   ```bash
   ./scripts/scenarios/swarm_smoke_test.sh --verbose
   ```

5. **Check Resource Usage**:
   ```bash
   docker stats
   ```

6. **Inspect Network**:
   ```bash
   docker network inspect fall25_independentstudy_node-network
   ```

### Getting Help

If you encounter issues not covered here:

1. Check error logs in `artifacts/swarm_tests/<RUN_ID>/errors.log`
2. Review test execution logs in `artifacts/swarm_tests/<RUN_ID>/logs/`
3. Run validation script: `./scripts/validation/validate_swarm_setup.sh`
4. Check Docker and Swarm documentation
5. Review test script source code for detailed error handling

## Advanced Usage

### Custom Test Configurations

Create custom test configurations by modifying `scripts/scenarios/swarm_test_config.sh`:

```bash
# Edit configuration
vim scripts/scenarios/swarm_test_config.sh

# Source it before running tests
source scripts/scenarios/swarm_test_config.sh
./scripts/scenarios/swarm_comparison_test.sh
```

### Integration with CI/CD

Example CI/CD integration:

```yaml
# Example GitHub Actions workflow
name: Swarm Comparison Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v2
      - name: Setup Docker
        run: |
          sudo systemctl start docker
      - name: Run Smoke Test
        run: |
          ./scripts/scenarios/swarm_smoke_test.sh --cleanup
      - name: Run Full Tests
        run: |
          ./scripts/scenarios/swarm_comparison_test.sh \
            --nodes 10 \
            --iterations 3
      - name: Generate Report
        run: |
          ./scripts/analysis/generate_swarm_report.sh \
            --results-dir $(ls -td test_results_* | head -1)
      - name: Upload Results
        uses: actions/upload-artifact@v2
        with:
          name: test-results
          path: test_results_*/
```

### Extending the Test Suite

To add new test scenarios:

1. Create new script in `scripts/scenarios/`
2. Follow existing script patterns
3. Use utilities from `scripts/utils/`
4. Output results in CSV format
5. Add documentation to this file

Example template:
```bash
#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

source "$ROOT_DIR/scripts/utils/error_handler.sh"
source "$ROOT_DIR/scripts/utils/test_logger.sh"

# Your test logic here
```

### Performance Tuning

For better performance:

1. **Increase node counts gradually**: Start with 2-4 nodes, then scale up
2. **Use SSD storage**: Faster disk I/O improves test performance
3. **Allocate more resources**: Increase Docker memory/CPU limits
4. **Reduce test iterations**: Use fewer iterations for faster feedback
5. **Run tests in parallel**: Use separate terminals for independent tests

### Best Practices

1. **Always run smoke test first**: Validate setup before full tests
2. **Use consistent node counts**: Compare results across similar configurations
3. **Save test results**: Keep results for comparison over time
4. **Monitor resources**: Watch CPU/memory during tests
5. **Clean up after tests**: Remove containers to free resources
6. **Document custom configurations**: Note any changes from defaults

## Additional Resources

- **Test Plan**: See `swarm_comparison_test_plan.txt` for detailed test plan
- **API Documentation**: See `scripts/swarm/api.sh` for Swarm API functions
- **Error Handling**: See `scripts/utils/ERROR_HANDLING.md` for error handling guide
- **Logging**: See `scripts/utils/TEST_LOGGING.md` for logging documentation
- **Results Directory**: See `scripts/utils/RESULTS_DIRECTORY.md` for results structure

## Version History

- **v1.0** (2026-02-16): Initial test suite implementation
  - Upload/download latency tests
  - Replication and convergence tests
  - Analysis and reporting tools
  - Validation and smoke tests

---

**Last Updated**: 2026-02-16  
**Maintainer**: Test Suite Development Team
