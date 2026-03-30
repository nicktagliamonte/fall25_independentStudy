# Scripts Directory

Scripts for Docker orchestration, Swarm comparison tests, and results analysis.

## Structure

```
scripts/
├── docker/              # Docker orchestration
├── tests/
│   └── swarm_comparison/   # Swarm comparison test suite
├── analysis/            # Results analysis and reporting
├── utils/               # Shared utilities
└── README.md            # This file
```

## Docker Orchestration (`docker/`)

Start, stop, and manage Docker containers for our system and Swarm.

| Script | Purpose |
|--------|---------|
| `start.sh [N]` | Start N nodes (default: 4). Generates docker-compose.yml from template. |
| `start_vnipfs.sh [N]` | Start vn-IPFS nodes. N must be 10, 50, 100, or 500 (default: 10). Generates docker-compose.vnipfs.yml. |
| `stop.sh` | Stop all nodes. |
| `status.sh` | Show container status. |
| `logs.sh [service] [--follow]` | View logs. |
| `clean.sh` | Clean up containers and volumes. |

**Swarm subdirectory:** `docker/swarm/` contains Swarm/Bee v0.5.8 Docker setup. `start.sh` generates `docker-compose.swarm.yml` for N in {10, 50, 100, 500} (same as vn-IPFS). Health checks and `depends_on: condition: service_healthy` coordinate startup.

**Usage:**
```bash
./scripts/docker/start.sh 10      # Start 10 nodes (generic)
./scripts/docker/start_vnipfs.sh 50   # Start 50 vn-IPFS nodes (10, 50, 100, or 500)
./scripts/docker/status.sh
./scripts/docker/stop.sh          # Stops docker-compose.yml
# For vnipfs: docker-compose -f docker-compose.vnipfs.yml down
```

## Swarm Comparison Test Suite (`tests/swarm_comparison/`)

Compares our distributed storage system vs Ethereum Swarm (Bee v0.5.8) on upload latency and download throughput.

| Script | Purpose |
|--------|---------|
| `run_comparison.sh` | Main orchestrator. Starts both systems, runs upload/download tests across node counts, aggregates results. Default suite skips `lookup_latency` (optional; often uninformative on LAN). |
| `upload_test.sh` | Upload latency test (our system vs Swarm). |
| `download_test.sh` | Download latency test (our system vs Swarm). |
| `test_api.sh [addr]` | Quick Swarm API validation. |
| `api.sh` | Swarm HTTP API helper functions (sourced by tests). |

**Usage:**
```bash
# Full comparison (starts Docker, runs tests, aggregates)
./scripts/tests/swarm_comparison/run_comparison.sh --nodes 10,20 --iterations 5

# Options: --nodes, --payload-sizes, --iterations, --output-dir, --skip-cleanup
```

**Output:** Results in `test_results_<timestamp>/` (or `--output-dir`): CSV files, summary report, logs.

## Analysis (`analysis/`)

Process test results and generate reports.

| Script | Purpose |
|--------|---------|
| `swarm_comparison_analyze.py` | Reads CSV from test runs. Generates statistics, box plots, line charts, HTML report. |
| `generate_swarm_report.sh` | Generates markdown report from results. Use `--results-dir` to point to `test_results_*` output. |

**Usage:**
```bash
# Analyze results (from run_comparison output)
python3 scripts/analysis/swarm_comparison_analyze.py ./test_results_20250101_120000

# Generate markdown report
./scripts/analysis/generate_swarm_report.sh --results-dir ./test_results_20250101_120000 --output REPORT.md
```

## Utils (`utils/`)

| Script | Purpose |
|--------|---------|
| `error_handler.sh` | Error logging, retry, container health checks. Sourced by test scripts. |

## Removed (Cleanup)

The following were removed as obsolete or redundant:

- **scenarios/**: Old CID-based tests, convergence, discovery, propagation, fault tolerance, restore efficiency, partition merge, scaling. Replaced by the single Swarm comparison suite.
- **harness/**: ec2_peer, ec2_bootstrap, local_mesh – unused.
- **monitoring/**: resource_monitor, network_metrics – not in essential set.
- **validation/**: validate_swarm_setup – deprecated.
- **test_data/**: verify_download, generate_test_files – not needed.
- **util/**: generate_put_json – used by removed throughput test.
- **net/**: profiles.sh – used by removed partition_merge.
- **plots/**: Standalone plot scripts (throughput, propagation, scaling, etc.) – swarm_comparison_analyze.py generates plots.
- **swarm/**: Moved api.sh and test_api.sh into tests/swarm_comparison/.
- **inspect_comparison_logs.sh**, **partition_recovery_test.sh**: Removed (ad-hoc log grep; manual partition test not in `run_comparison.sh`). Optional `partition_recovery_results.csv` in analysis is legacy if present.
