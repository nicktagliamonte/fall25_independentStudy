.PHONY: build local quick-activity plots scaling-test

build:
	@echo "Building node..."
	@go build -o bin/node ./cmd/node

local:
	@bash scripts/harness/local_mesh.sh

quick-activity:
	@bash scripts/scenarios/quick_activity.sh $(N) $(MIN_OUTBOUND)

plots:
	@if [ -z "$(RUN_ID)" ]; then \
		echo "Usage: make plots RUN_ID=<run_id>"; \
		echo "  RUN_ID: directory name under artifacts/runs/"; \
		exit 1; \
	fi
	@python3 scripts/plots/quick_plots.py artifacts/runs/$(RUN_ID)/raw/metrics.jsonl --save-table

scaling-test:
	@bash scripts/scenarios/scaling_test.sh $(NODES) $(MIN_OUTBOUND)

scenario-discovery:
	@if [ -z "$(RUN_ID)" ]; then \
		echo "Usage: make scenario-discovery RUN_ID=<run_id> [K=<k>]"; \
		echo "  RUN_ID: directory name under artifacts/runs/"; \
		echo "  K: target number of neighbors (default: 3)"; \
		exit 1; \
	fi
	@bash scripts/scenarios/discovery.sh $(RUN_ID) $(K)

scenario-failure-repair:
	@if [ -z "$(RUN_ID)" ]; then \
		echo "Usage: make scenario-failure-repair RUN_ID=<run_id> [VICTIM_ID=<id>] [DONOR_ID=<id>]"; \
		echo "  RUN_ID: directory name under artifacts/runs/"; \
		echo "  VICTIM_ID: node ID to fail (default: random leaf)"; \
		echo "  DONOR_ID: node ID to snapshot from (default: 1)"; \
		exit 1; \
	fi
	@bash scripts/scenarios/failure_repair.sh $(RUN_ID) $(VICTIM_ID) $(DONOR_ID)

scenario-partition-merge:
	@if [ -z "$(RUN_ID)" ]; then \
		echo "Usage: make scenario-partition-merge RUN_ID=<run_id> [T1=<seconds>] [T2=<seconds>] [GROUPS=<spec>]"; \
		echo "  RUN_ID: directory name under artifacts/runs/"; \
		echo "  T1: partition duration in seconds (default: 30)"; \
		echo "  T2: post-merge duration in seconds (default: 30)"; \
		echo "  GROUPS: group specification (default: auto)"; \
		exit 1; \
	fi
	@bash scripts/scenarios/partition_merge.sh $(RUN_ID) $(T1) $(T2) $(GROUPS)

plot-discovery:
	@if [ -z "$(RUN_ID)" ]; then \
		echo "Usage: make plot-discovery RUN_ID=<run_id>"; \
		echo "  RUN_ID: directory name under artifacts/runs/"; \
		exit 1; \
	fi
	@python3 scripts/plots/discovery_plot.py $(RUN_ID)

plot-partition:
	@if [ -z "$(RUN_ID)" ]; then \
		echo "Usage: make plot-partition RUN_ID=<run_id>"; \
		echo "  RUN_ID: directory name under artifacts/runs/"; \
		exit 1; \
	fi
	@python3 scripts/plots/partition_scaling.py $(RUN_ID)

plot-repair:
	@if [ -z "$(RUN_ID)" ]; then \
		echo "Usage: make plot-repair RUN_ID=<run_id>"; \
		echo "  RUN_ID: directory name under artifacts/runs/"; \
		exit 1; \
	fi
	@python3 scripts/plots/repair_scaling.py $(RUN_ID)


