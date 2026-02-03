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

convergence-test:
	@bash scripts/scenarios/convergence_test.sh $(NODES) $(RUNS) $(MIN_OUTBOUND)

plot-convergence:
	@if [ -z "$(RESULTS_DIR)" ]; then \
		echo "Usage: make plot-convergence RESULTS_DIR=<results_dir>"; \
		echo "  RESULTS_DIR: directory from convergence-test (e.g., artifacts/convergence_tests/TIMESTAMP)"; \
		exit 1; \
	fi
	@python3 scripts/plots/convergence_plot.py $(RESULTS_DIR)

plot-restore-efficiency:
	@if [ -z "$(RESULTS_DIR)" ]; then \
		echo "Usage: make plot-restore-efficiency RESULTS_DIR=<results_dir>"; \
		echo "  RESULTS_DIR: directory from convergence-test (e.g., artifacts/convergence_tests/TIMESTAMP)"; \
		exit 1; \
	fi
	@python3 scripts/plots/restore_efficiency_plot.py $(RESULTS_DIR)

discovery-test:
	@bash scripts/scenarios/discovery_test.sh $(NODES) $(RUNS) $(MIN_OUTBOUND) $(K_VALUES)

plot-discovery-dynamics:
	@if [ -z "$(RESULTS_DIR)" ]; then \
		echo "Usage: make plot-discovery-dynamics RESULTS_DIR=<results_dir> [K=<k>]"; \
		echo "  RESULTS_DIR: directory from discovery-test (e.g., artifacts/discovery_tests/TIMESTAMP)"; \
		echo "  K: K value for Panel A CDF (default: 3)"; \
		exit 1; \
	fi
	@if [ -z "$(K)" ]; then \
		python3 scripts/plots/discovery_dynamics_plot.py $(RESULTS_DIR) --k 3; \
	else \
		python3 scripts/plots/discovery_dynamics_plot.py $(RESULTS_DIR) --k $(K); \
	fi

fault-tolerance-test:
	@bash scripts/scenarios/fault_tolerance_test.sh $(NODES) $(RUNS) $(MIN_OUTBOUND)

plot-fault-tolerance:
	@if [ -z "$(RESULTS_DIR)" ]; then \
		echo "Usage: make plot-fault-tolerance RESULTS_DIR=<results_dir>"; \
		echo "  RESULTS_DIR: directory from fault-tolerance-test (e.g., artifacts/fault_tolerance_tests/TIMESTAMP)"; \
		exit 1; \
	fi
	@python3 scripts/plots/fault_tolerance_plot.py $(RESULTS_DIR)

propagation-depth-test:
	@bash scripts/scenarios/propagation_depth_test.sh $(NODES) $(RUNS) $(MIN_OUTBOUND)

plot-propagation-depth:
	@if [ -z "$(RESULTS_DIR)" ]; then \
		echo "Usage: make plot-propagation-depth RESULTS_DIR=<results_dir>"; \
		echo "  RESULTS_DIR: directory from propagation-depth-test (e.g., artifacts/propagation_depth_tests/TIMESTAMP)"; \
		exit 1; \
	fi
	@python3 scripts/plots/propagation_depth_plot.py $(RESULTS_DIR)


