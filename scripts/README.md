# Scripts

Only current Tarsus orchestration, experiments, and utilities live here.

## Docker orchestration

| Script | Purpose |
|---|---|
| `docker/start.sh [N]` | Generate `docker-compose.yml`, build, and start an N-peer private cluster |
| `docker/status.sh` | Show cluster status |
| `docker/logs.sh [service]` | Read container logs |
| `docker/stop.sh` | Stop the current cluster |
| `docker/clean.sh` | Remove current cluster containers and volumes |

`start.sh` accepts any integer `N >= 2`. Production campaign cells set fresh
volumes, fixed index-shard and Bloom settings, bounded connection targets, and
public-bootstrap isolation through environment variables.

## Production campaign

[`tests/tarsus_campaign/README.md`](tests/tarsus_campaign/README.md) documents
the complete workflow. Its principal entry points are:

| Script | Purpose |
|---|---|
| `run_campaign.sh` | Run or resume the peer/catalog/shard/Bloom matrix |
| `validate_campaign.sh` | Accept only complete cells with the expected rows and artifacts |
| `analyze_campaign.py` | Merge accepted cells and calculate summary statistics |
| `plot_campaign.py` | Generate the three manuscript figures |
| `run_resilience_cell.sh` | Run the optional bounded provider-failure mechanism case |
| `with_neighbor_limits.sh` | Temporarily tune and reliably restore Linux neighbor thresholds |

The harness does not rerun a cell containing `COMPLETE`. Interrupted-cell batch
files are recreated before population so stale JSON requests cannot be read as
tuple names.

## Focused query instrumentation

`tests/tuple_index/query_cost.sh` is a small query-cost probe for development.
The production paper results come from `tests/tarsus_campaign`, not from this
probe.

## Utilities

`utils/resource_monitor.sh` records host/container resources for campaign
artifacts. Other utility scripts support error handling and local inspection.
