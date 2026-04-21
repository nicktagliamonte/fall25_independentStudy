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

The Swarm comparison test suite is designed to systematically compare our distributed storage system with Ethereum Swarm across key performance dimensions:

- **Upload Latency**: Time to upload content of various sizes (1KB–1MB)
- **Download Throughput**: Time to first byte (TTFB) and total download time

### Test Architecture

The test suite consists of:

- **Orchestration**: `run_comparison.sh` starts both systems, runs upload/download tests across node counts, aggregates results
- **Test Scripts**: `upload_test.sh`, `download_test.sh` for individual metrics
- **Analysis Tools**: `swarm_comparison_analyze.py` and `generate_swarm_report.sh` for results and reports
- **Validation**: `test_api.sh` for quick Swarm API checks

### Directory Structure

```
scripts/
├── docker/             # Docker orchestration
│   ├── start.sh, stop.sh, logs.sh, status.sh, clean.sh
│   └── swarm/          # Swarm/Bee v0.5.8 Docker setup
├── tests/swarm_comparison/
│   ├── run_comparison.sh         # Main orchestrator
│   ├── run_single_comparison.sh  # One test × one N × I iterations → test_results/matrix/…
│   ├── run_overnight_comparison_sequence.sh  # Matrix over N × system (calls run_single_comparison)
│   ├── validate_comparison_artifacts.sh       # Post-run CSV checks (--validate on run_comparison)
│   ├── upload_test.sh            # Upload latency test
│   ├── download_test.sh          # Download latency test
│   ├── test_api.sh               # Swarm API validation
│   └── api.sh                    # Swarm HTTP API helpers
├── analysis/
│   ├── swarm_comparison_analyze.py   # Statistics, plots, HTML report
│   └── generate_swarm_report.sh      # Markdown report generator
└── utils/
    └── error_handler.sh   # Error handling utilities
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

### 1. Validate Swarm API (optional)

If Swarm nodes are already running, validate the API:

```bash
./scripts/tests/swarm_comparison/test_api.sh http://172.20.0.200:8500
```

### 2. Run Full Comparison Test

The main orchestrator starts both systems, runs tests, and aggregates results:

```bash
./scripts/tests/swarm_comparison/run_comparison.sh \
  --nodes 10,50,100 \
  --payload-sizes 1024,10240,102400,1048576 \
  --iterations 5
```

This:
- Starts our system and Swarm Docker containers
- Runs upload and download tests across node counts and payload sizes
- Writes results to `test_results_<timestamp>/` (or `--output-dir`)
- Optionally stops containers (use `--skip-cleanup` to leave them running)

## Test Scripts

### Main Orchestrator (`run_comparison.sh`)

**Purpose**: Start both systems, run upload and download tests across configurations, aggregate results

**What it tests**:
- Upload latency across payload sizes (1KB–1MB)
- Download latency (TTFB, total time)
- Scaling across node counts (allowed values: 10, 50, 100, 500)

**Usage**:
```bash
./scripts/tests/swarm_comparison/run_comparison.sh \
  --nodes 10,50,100 \
  --payload-sizes 1024,10240,102400,1048576 \
  --iterations 5 \
  --output-dir ./test_results
```

**Output**: `test_results_<timestamp>/` or `--output-dir` with CSV files, logs, summary

### Upload Test (`upload_test.sh`)

**Purpose**: Measure upload latency for various payload sizes

**Time to first durable local accept (methodology)**  
Both systems are compared on wall-clock time from the start of the HTTP request until the response completes, after the uploader’s node has accepted and stored the payload locally. vn-IPFS does not wait for DHT token publication or peer replication before responding. Swarm’s `POST /bzz:/` is treated the same way (local accept). This is the apples-to-apples definition for upload latency in the comparison suite.

**Equivalence**

- **Semantics**: First successful local store only; token DHT sync and replication run asynchronously on vn-IPFS (see `docs/API.md` PUT).
- **Client shape (fair comparison)**  
  - **Swarm**: `curl` from the host with raw body, `Content-Type: application/octet-stream`.  
  - **vn-IPFS (required for comparable numbers)**: Bootstrap must expose the control API on the host. In `scripts/docker/vnipfs-compose.template.yml`, `SNG40_CONTROL_LISTEN=0.0.0.0:9400` and `ports: "9400:9400"` are set so `upload_test.sh` can probe `http://127.0.0.1:9400/health` and use **direct host `curl`** (no `docker cp` / `docker exec` inside the timed window).  
  - When the direct path is used, vn-IPFS upload uses **`POST /put` with `Content-Type: application/octet-stream`** and `--data-binary @file`, matching Swarm’s encoding and payload size on the wire.  
  - If port 9400 is not reachable from the host, the script falls back to JSON+base64 via `docker exec`; those numbers are **not** comparable to Swarm and should be flagged in logs.
  - While a bootstrap container is detected, the script **polls** `http://127.0.0.1:9400/health` for up to **`SNG40_UPLOAD_DIRECT_WAIT_SEC`** seconds (default **120**) so the **first** `upload_test` invocation (e.g. `batch_size=1`) uses the same direct path as later batches. Before each payload size, one **discarded** warmup upload per system runs unless **`SNG40_UPLOAD_WARMUP=0`**, to reduce cold-start skew on the first recorded iteration (especially Swarm). After **timed** vn-IPFS iterations for that size, when **`batch_size ≥ 5`**, another **discarded** Swarm-only batch runs so the first **recorded** Swarm batch is less likely to spike after cluster load (large N).

**Usage** (typically invoked by `run_comparison.sh`; can run standalone if nodes are up):
```bash
./scripts/tests/swarm_comparison/upload_test.sh --iterations 10 --output upload.csv
```

**Output**: CSV with columns: `system,payload_size,iteration,latency_ms`

### Download Test (`download_test.sh`)

**Purpose**: Measure download performance (TTFB, total time)

**Usage**:
```bash
./scripts/tests/swarm_comparison/download_test.sh --iterations 10 --output download.csv
```

**Output**: CSV with columns: `system,payload_size,iteration,ttfb_ms,total_ms`

### Catalog growth (`catalog_growth_test.sh`)

**Purpose**: vn-IPFS only — measure **upload** latency for each successive object and **download** latency for a **fixed** first object as the **number of distinct objects on the network** grows (1 … **N**). Each row is one point on the x-axis “files on network.”

**Method (vn-IPFS)**: PUT from bootstrap; GET (`format=raw&remote_only=1`) from a **worker** when available (last `fall25-node*` with a control addr), targeting the **first** object’s key. `remote_only` forces DHT token resolution plus peer fetch on that node (not a local replica read). **Download** latency defaults to **host wall time** (`date +%s%N` around the full `docker exec` + curl), aligned with Swarm catalog (`CATALOG_GROWTH_HOST_WALL_GET`, default **1**). Set **`CATALOG_GROWTH_HOST_WALL_GET=0`** for in-container `curl` `time_total` only (typically smaller ms).

**Method (Swarm)**: `catalog_growth_swarm_test.sh` — **PUT** always on **swarm-bootstrap** (vn-IPFS parity; worker-only uploads see `no suitable peer` on other nodes in this mesh). Timed **GET**: `curl` **inside** the last healthy `swarm-node*` (or bootstrap if no workers) to **`http://127.0.0.1:8500`**. Swarm has no `remote_only`; the node may satisfy the hash from **local chunk store** after an earlier fetch. **`CATALOG_GROWTH_SWARM_FETCH`** (default **`latest`**) controls which hash is timed: **`latest`** = root just uploaded in that row (getter has not seen that hash yet, so no warm local chunk for that root — avoids sub-ms rows driven only by cache + Docker RTT). **`first`** = same fixed root as vn-IPFS for all rows; the script runs best-effort **`DELETE bzz-pin:/…`** and **`DELETE bzz:/…/`** before each GET to evict local state — enable pinning on the stack with **`SWARM_ENABLE_PINNING=true`** when running `scripts/docker/swarm/start.sh` (passed into compose) so `bzz-pin` DELETE is active; otherwise **`first`** may still warm-cache after the first successful fetch. Same CSV columns with `system=swarm`.

**Sub-ms download column**: in-container **`curl` `time_total`** can sit below 1 ms on a warm path; **`CATALOG_GROWTH_SWARM_HOST_WALL_GET=1`** times **`docker exec` + GET on the host** (Linux **`date +%s%N`**). **`SWARM_STORE_CACHE_CAPACITY=0`** (compose + `entrypoint.sh` → `--store.cache.size`) disables Swarm’s default 10k in-memory chunk cache. **`run_swarm_catalog_benchmark.sh`** applies pinning, zero cache, **`first`**, host-wall GET, CSV + plot in one step (`SWARM_CATALOG_N`, `SWARM_CATALOG_FILES`, `SWARM_CATALOG_OUT_DIR`, `SWARM_CATALOG_PAYLOAD_BYTES` optional).

**Combined run**: `run_comparison.sh --tests catalog_growth --system both` writes `catalog_growth_n<N>.csv` with vn-IPFS rows first, then Swarm rows (`--append`). Use `python3 scripts/analysis/catalog_growth_plot.py` for overlaid lines; `--system swarm` or `--system our_system` for one trace.

**Node count**: Use a **meaningful** cluster size (e.g. **50**); the CSV `node_count` column labels the run (`--node-count` / `CATALOG_GROWTH_NODE_COUNT`).

**Not in the default suite** — run with `--tests catalog_growth` (or `run_single_comparison.sh --test catalog_growth`). Tuning: **`CATALOG_GROWTH_MAX_OBJECTS`** (default **256**), **`CATALOG_GROWTH_PAYLOAD_BYTES`** (default **8192** bytes).

**Usage**:
```bash
./scripts/tests/swarm_comparison/run_comparison.sh --tests catalog_growth --nodes 50 --system vnipfs --output-dir ./test_results/catalog_growth_run
# one-shot: down -v, start N nodes, catalog CSV + plot (env: VN_CATALOG_N, VN_CATALOG_FILES, VN_CATALOG_OUT_DIR, …)
./scripts/tests/swarm_comparison/run_vnipfs_catalog_benchmark.sh
# or standalone:
CATALOG_GROWTH_MAX_OBJECTS=200 ./scripts/tests/swarm_comparison/catalog_growth_test.sh --node-count 50 --max-files 200 --payload-size 8192 --output catalog_growth_results.csv
```

**Output**: `system,node_count,files_on_network,payload_size,upload_ms,download_total_ms`

**Plot (errors and host-jitter spikes suppressed; linear interpolation; clean line chart)**:
```bash
python3 scripts/analysis/catalog_growth_plot.py test_results/catalog_growth_512/catalog_growth_n50.csv
# writes catalog_growth_n50_latency.png beside the CSV; tune --window, --n-sigma, --min-abs-ms if needed
```

### Lookup Complexity (`lookup_complexity_test.sh`)

**Purpose**: Measure `network_hops` / latency from a **cold** `lookup-key` run against the **running vn-IPFS** stack (after `run_comparison.sh` starts Docker).

**Usage**:
```bash
./scripts/tests/swarm_comparison/lookup_complexity_test.sh --node-count 50 --iterations 10 --output lookup_complexity_results.csv
```

Cold `lookup-key` passes **comma-separated** bootstrap multiaddrs (Docker DNS for the bootstrap container name, then `/ip4` on the compose **node-network**). The binary tries each dial until one succeeds (default connect budget **120s** per dial, overridable with `LOOKUP_KEY_CONNECT_TIMEOUT`); the `--timeout` flag applies to the **GetToken** phase only.

**Output**: `system,node_count,operation,hops,hops_raw,lookup_latency_ms`

### Storage Efficiency Test (`storage_efficiency_test.sh`)

**Purpose**: Upload known payload, measure disk delta across nodes, compute efficiency ratio

**Usage**:
```bash
./scripts/tests/swarm_comparison/storage_efficiency_test.sh \
  --payload-size 65536 \
  --replication-count 1 \
  --output storage_efficiency_results.csv
```

**Output**: CSV with columns: `system,payload_size,nodes,disk_bytes,efficiency_ratio`

### Swarm API Check (`test_api.sh`)

**Purpose**: Validate Swarm API (upload, download, pin, etc.)

**Usage**:
```bash
./scripts/tests/swarm_comparison/test_api.sh [api_address]
```

## Running Tests

### Basic Test Execution

1. **Run full comparison** (recommended; starts Docker, runs tests, stops):
   ```bash
   ./scripts/tests/swarm_comparison/run_comparison.sh --nodes 10 --iterations 3
   ```

2. **Start nodes manually** (for standalone runs):
   ```bash
   ./scripts/docker/start.sh 10
   ./scripts/docker/swarm/start.sh 10
   ```

3. **Run individual tests** (with nodes already running):
   ```bash
   ./scripts/tests/swarm_comparison/upload_test.sh --iterations 5
   ./scripts/tests/swarm_comparison/download_test.sh --iterations 5
   ```

### Test Configuration

Use `--help` on each script for options. Main orchestrator options:
- `--nodes 10,50,100` (comma-separated; each must be 10, 50, 100, or 500)
- `--payload-sizes 1024,10240,102400,1048576`
- `--iterations 5`
- `--output-dir <dir>`
- `--tests <name>` or `--tests name1,name2,...` (default: full suite). Run `./scripts/tests/swarm_comparison/run_comparison.sh --tests list` for names.
- `--skip-start` — assume vn-IPFS and Swarm are already running at the single `--nodes` value; skips compose startup and implies `--skip-cleanup`. Use for rerunning one test against a warm cluster.
- `--skip-cleanup` (leave containers running after tests)

### Per-test × node-count matrix (structured reruns)

**Goal**: One directory per cell — one logical test, one `N`, fixed iteration count — so failures are isolated and logs do not overwrite a full-suite run.

**Recommended order** (example: 10 iterations per cell; repeat the sequence for each `N`):

1. `upload`
2. `download_warm`
3. `lookup_complexity`
4. `replication`
5. `replication_distribution`
6. `repair_time`
7. `routing_overhead`
8. `storage_efficiency`
9. `concurrent`

(`lookup_latency` is intentionally **not** in the default orchestrator or overnight sequence: on a typical Docker LAN it is often flat and not worth paper space. Run explicitly with `--tests lookup_latency` or `INCLUDE_LOOKUP_LATENCY=1` if needed.)

Use **`N ∈ {10, 50, 100}`** routinely; add **`500`** only when the host has enough CPU, RAM, and disk.

**Wrapper (default layout)** — writes to `test_results/matrix/<test>_n<N>_i<I>/`:

```bash
./scripts/tests/swarm_comparison/run_single_comparison.sh --test upload --nodes 10 --iterations 10
./scripts/tests/swarm_comparison/run_single_comparison.sh --test download_warm --nodes 10 --iterations 10
# … same pattern for other test names; then repeat for --nodes 50 and 100
```

**Direct orchestrator** (same semantics, custom output dir):

```bash
./scripts/tests/swarm_comparison/run_comparison.sh \
  --nodes 10 --iterations 10 --tests upload --output-dir ./test_results/matrix/upload_n10_i10
```

**Warm cluster, single test** (no `docker compose up` in the orchestrator; you must already have both stacks at the target `N`):

```bash
./scripts/tests/swarm_comparison/run_single_comparison.sh \
  --test upload --nodes 10 --iterations 5 --skip-start
```

**Full matrix loop** (illustrative; long-running):

```bash
MATRIX_TESTS=(
  upload download_warm lookup_complexity
  replication replication_distribution repair_time
  routing_overhead storage_efficiency concurrent
)
for N in 10 50 100; do
  for T in "${MATRIX_TESTS[@]}"; do
    ./scripts/tests/swarm_comparison/run_single_comparison.sh --test "$T" --nodes "$N" --iterations 10
  done
done
```

**Note**: `routing_overhead`, `storage_efficiency`, and `concurrent` are scheduled on the **last** node count in a multi-`N` `run_comparison.sh` invocation. For `run_single_comparison.sh` there is only one `N`, so that `N` is always “last” and those tests run as expected.

### Large N (50 / 100): cost, partial CSVs, timeouts

- **Using partial uploads anyway**: The suite default is **`batch_size` 1 and 5** only (10 and 20 were dropped: slow and noisy at larger N). For any run, only plot strata that **completed** with full iterations and state `N` and `batch_size` in the text. Legacy dirs with `batch10`/`batch20` CSVs should not be mixed into paper figures without an explicit rationale.
- **Defaults**: With `--test-timeout` unset, the orchestrator uses **5400s** per sub-step when max `N` is **50**, and **7200s** when max `N` is **≥100** (same 7200 for 500). Raise further with `--test-timeout` if a single `upload_test.sh` still hits 124.
- **Harness**: After timed vn-IPFS uploads for each payload size, the upload script runs an extra **discarded Swarm-only batch** when `batch_size ≥ 5` to reduce a huge first Swarm batch latency after cluster load.

## Interpreting Results

### DHT hop count vs upload latency (do not conflate)

- **Upload / download CSVs** report **milliseconds** (end-to-end HTTP and local work). They are **not** DHT hop counts.
- **`lookup_complexity_results.csv`** (`operation=lookup`) and optional `network_hops` fields on some GET responses report **abstract query-event counts**, not ms.
- **Ideal Kademlia** suggests **O(log N)** hop count for lookups in a stable, large table. **Observed** comparison runs may not show that slope: small `N`, churn, token DHT mode, and one-off cold lookup variance dominate. Treat O(log N) as **theoretical reference**, not an automatic pass/fail on the plots.

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
  --results-dir ./test_results_20260216_120002 \
  --output REPORT.md
```

The report includes:
- Executive summary
- Detailed results tables
- Performance comparisons
- Conclusions and recommendations
- Links to plots and raw data

### Storage Efficiency Metric (Definition)

Storage efficiency measures how much disk space a system uses relative to the logical payload stored. Two equivalent formulations:

1. **Efficiency ratio (primary)**  
   `efficiency_ratio = (payload_size * replication_count) / actual_disk_usage`  
   - Higher is better. Values &gt; 1 mean the system uses less disk than the nominal `payload_size * replication_count`.
   - Ideal upper bound: 1.0 (no overhead; disk = payload × replicas). Overhead (indexes, metadata, chunk alignment) typically yields values &lt; 1.

2. **Overhead ratio (alternative)**  
   `overhead_ratio = actual_disk_usage / payload_size`  
   - Lower is better. Represents bytes of disk per byte of logical payload.
   - Equivalent relationship: `efficiency_ratio = replication_count / overhead_ratio`.

For comparison tests, report `efficiency_ratio` per system so both systems are evaluated on the same scale.

### Storage Usage Per Node

**vn-IPFS**: Use `GET /storage/stats` (returns `disk_bytes`) or `docker exec <container> du -sb /app/data/<node>`.

**Swarm (Bee)**:
- **docker exec (recommended)**: `docker exec <container> du -sb /app/data` — returns actual disk bytes. Containers: `swarm-bootstrap`, `swarm-node1`, `swarm-node2`, …; data dir `/app/data` (from `SWARM_DATA_DIR`).
- **Bee API (if available)**: `curl -s http://<node-host>:8500/status` returns `reserveSize`, `reserveSizeWithinRadius`, `storageRadius`. These are chunk-based metrics, not raw disk bytes; use for reserve state, not efficiency ratio. Prefer `du -sb` for `actual_disk_usage` in the efficiency formula.

### Replication Status: Counting Nodes Holding a Key

For replication speed tests (time to R replicas), you need to count how many nodes hold a given content reference.

**vn-IPFS**:
- `GET /replication/status?key=<hex>` — DHT token view: returns `replica_count`, `providers`, `near_count`, `midrange_count`, `farflung_count` (N/M/F distribution).
- `GET /has_key?key=<hex>` — per-node: returns `has_key` true/false. Poll all nodes and sum to get replica count.

**Swarm (Bee)** — Pinning API and chunk checks:

1. **Pinning API** (per-node; content must be pinned):
   - `GET /pins/{reference}` — 200 = root hash is pinned on this node. Poll each node to count pins.
   - `GET /pins` — list of all pinned root references on this node.
   - Note: Pinning is explicit. Uploaded content is not pinned by default; use `Swarm-Pin: true` on upload or `POST /pins/{reference}` after upload.

2. **Chunk local check** (per-node):
   - `HEAD /chunks/{address}` — 200 = chunk exists locally; 404 = not found. Use the chunk address (for BZZ uploads, the root reference may be a manifest; single-chunk content uses that ref as chunk address).
   - Poll each node: `curl -sI http://172.20.0.<N>:8500/chunks/<address>` — count 200 responses.

3. **Stewardship**:
   - `GET /stewardship/{reference}` — 200 = content is available (can be served). Does not distinguish local vs proxied; less suitable for replica counting.

4. **Tag sync status** (upload progress, not replica count):
   - `GET /tags/{uid}` — returns `seen`, `stored`, `sent`, `synced`; useful for tracking upload propagation, not cross-node replica count.

**Recommended for replication_test.sh (Swarm)**:
- If using pinned uploads: poll `GET /pins/{reference}` on each Swarm node; count 200 responses.
- Otherwise: poll `HEAD /chunks/{address}` on each node (use BZZ reference as chunk address when it is single-chunk content).

### Key-Based Lookup vs CID-Based Retrieval: Equivalent Operations

For fair comparison, the same logical operation is defined for both systems:

| Logical operation | vn-IPFS (key-based) | Swarm (CID-based) |
|-------------------|---------------------|-------------------|
| **Store**         | Put payload P → Key = SHA256(P); sync token to DHT | Upload P → BZZ hash (content address) |
| **Fetch**         | Lookup by Key → GetToken(key) → DirectFetch from provider | Fetch by hash → `GET /bzz:/<hash>/` |
| **Identifier**    | Key (64 hex chars, SHA256 of data) | BZZ reference (content hash, 64+ hex) |
| **Semantics**     | Content-addressed; Key = content-derived identifier | Content-addressed; hash = content-derived identifier |

Both perform the same logical flow: **store payload P, then fetch P by its content-derived identifier**. Key (vn-IPFS) and BZZ hash (Swarm) are semantically equivalent: each identifies content by its digest (key K = content hash of payload; for vn-IPFS, K = SHA256(data)). For single-chunk content, Swarm's upload return value is the chunk address used for retrieval. vn-IPFS uses Key as primary; CID (multihash) is available for compatibility.

### Routing Overhead

For routing-overhead comparison (token routing vs provider announcements), `routing_overhead_test.sh` measures message counts per operation. vn-IPFS uses token lookup; Swarm uses provider announcements and retrieval.

### Key Metrics to Watch

1. **Upload Latency**:
   - Lower is better
   - Compare mean and median across systems
   - Check for outliers (high stddev)

2. **Download Throughput**:
   - Higher throughput (MB/s) is better
   - TTFB should be low for good user experience
   - Total time should scale linearly with payload size

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

1. **Check Logs**:
   ```bash
   docker logs bootstrap
   docker logs node1
   docker logs swarm-bootstrap
   docker logs swarm-node1
   ```

2. **Validate Swarm API**:
   ```bash
   ./scripts/tests/swarm_comparison/test_api.sh
   ```

3. **Check Resource Usage**:
   ```bash
   docker stats
   ```

4. **Inspect Network**:
   ```bash
   docker network inspect fall25_independentstudy_node-network
   ```

5. **Leave containers running** for inspection: use `--skip-cleanup` with `run_comparison.sh`

### Getting Help

If you encounter issues not covered here:

1. Check error logs in `artifacts/swarm_tests/<RUN_ID>/`
2. Run Swarm API check: `./scripts/tests/swarm_comparison/test_api.sh`
3. Check Docker and Swarm documentation
4. Review test script source in `scripts/tests/swarm_comparison/`

## Advanced Usage

### Integration with CI/CD

Example CI/CD integration:

```yaml
name: Swarm Comparison Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Setup Docker
        run: sudo systemctl start docker
      - name: Run Comparison Tests
        run: |
          ./scripts/tests/swarm_comparison/run_comparison.sh \
            --nodes 10 --iterations 3
      - name: Generate Report
        run: |
          ./scripts/analysis/generate_swarm_report.sh \
            --results-dir $(ls -td test_results_* | head -1)
      - name: Upload Results
        uses: actions/upload-artifact@v4
        with:
          name: test-results
          path: test_results_*/
```

### Extending the Test Suite

To add new tests:

1. Create scripts in `scripts/tests/swarm_comparison/`
2. Source `api.sh` for Swarm helpers, `scripts/utils/error_handler.sh` for error handling
3. Output results in CSV format
4. Add orchestration in `run_comparison.sh` if needed

### Best Practices

1. **Use consistent node counts**: Compare results across similar configurations
2. **Save test results**: Keep `test_results_*` directories for comparison over time
3. **Clean up after tests**: Use `./scripts/docker/stop.sh` and `./scripts/docker/swarm/start.sh` stop logic, or `--skip-cleanup` for debugging
4. **Point analysis at results**: Use `--results-dir` with `generate_swarm_report.sh` to target `test_results_*` output

## Additional Resources

- **Scripts README**: See `scripts/README.md` for layout and usage
- **Swarm API Helpers**: See `scripts/tests/swarm_comparison/api.sh`
- **Swarm Setup**: See `docs/SWARM_SETUP.md`

---

**Last Updated**: 2026-03-10  
**Script Directory**: `scripts/tests/swarm_comparison/`
