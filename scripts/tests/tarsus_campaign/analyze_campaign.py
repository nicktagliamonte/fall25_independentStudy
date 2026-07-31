#!/usr/bin/env python3
"""Merge validated Tarsus cells and compute publication-facing summaries."""

from __future__ import annotations

import csv
import json
import math
import random
import statistics
import sys
from collections import defaultdict
from pathlib import Path


def percentile(values: list[float], fraction: float) -> float:
    ordered = sorted(values)
    if len(ordered) == 1:
        return ordered[0]
    position = (len(ordered) - 1) * fraction
    lower = math.floor(position)
    upper = math.ceil(position)
    if lower == upper:
        return ordered[lower]
    return ordered[lower] + (ordered[upper] - ordered[lower]) * (position - lower)


def bootstrap_median_ci(values: list[float], seed: int = 20260729) -> tuple[float, float]:
    rng = random.Random(seed)
    medians = [
        statistics.median(rng.choices(values, k=len(values)))
        for _ in range(2000)
    ]
    return percentile(medians, 0.025), percentile(medians, 0.975)


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} RUN_DIR", file=sys.stderr)
        return 2
    run_dir = Path(sys.argv[1])
    cell_dirs = sorted(path.parent for path in run_dir.glob("cells/*/COMPLETE"))
    if not cell_dirs:
        print(f"no complete cells under {run_dir}", file=sys.stderr)
        return 1

    analysis_dir = run_dir / "analysis"
    analysis_dir.mkdir(parents=True, exist_ok=True)
    rows: list[dict[str, str]] = []
    population_rows: list[dict[str, object]] = []
    for cell_dir in cell_dirs:
        with (cell_dir / "queries.csv").open(newline="") as handle:
            rows.extend(csv.DictReader(handle))
        with (cell_dir / "cell.json").open() as handle:
            cell = json.load(handle)
        with (cell_dir / "populate-summary.json").open() as handle:
            population = json.load(handle)
        population_rows.append(
            {
                **cell,
                "populate_requested": population["requested"],
                "populate_duration_ns": population["duration_ns"],
                "mutation_local": population["mutation_delta"]["local"],
                "mutation_remote": population["mutation_delta"]["remote"],
                "mutation_failures": population["mutation_delta"]["failures"],
            }
        )

    with (analysis_dir / "queries_all.csv").open("w", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)
    with (analysis_dir / "population_all.csv").open("w", newline="") as handle:
        fields = list(population_rows[0])
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        writer.writerows(population_rows)

    key_fields = (
        "node_count",
        "catalog_size",
        "index_shards",
        "bloom_pruning",
        "label",
        "query_kind",
    )
    groups: dict[tuple[str, ...], list[dict[str, str]]] = defaultdict(list)
    for row in rows:
        groups[tuple(row[field] for field in key_fields)].append(row)

    summary_fields = list(key_fields) + [
        "trials",
        "duration_mean_ms",
        "duration_median_ms",
        "duration_p95_ms",
        "duration_stdev_ms",
        "median_ci95_low_ms",
        "median_ci95_high_ms",
        "shards_contacted_mean",
        "shards_succeeded_mean",
        "shards_failed_mean",
        "nodes_fetched_mean",
        "branches_considered_mean",
        "branches_pruned_mean",
        "index_candidates_mean",
        "index_matches_mean",
        "owner_attempts_mean",
        "verified_matches_mean",
    ]
    summaries: list[dict[str, object]] = []
    for key, group in sorted(groups.items()):
        durations = [float(row["duration_ns"]) / 1_000_000 for row in group]
        ci_low, ci_high = bootstrap_median_ci(durations)
        summaries.append(
            {
                **dict(zip(key_fields, key)),
                "trials": len(group),
                "duration_mean_ms": statistics.fmean(durations),
                "duration_median_ms": statistics.median(durations),
                "duration_p95_ms": percentile(durations, 0.95),
                "duration_stdev_ms": statistics.stdev(durations) if len(durations) > 1 else 0,
                "median_ci95_low_ms": ci_low,
                "median_ci95_high_ms": ci_high,
                "shards_contacted_mean": statistics.fmean(float(row["shards_contacted"]) for row in group),
                "shards_succeeded_mean": statistics.fmean(float(row["shards_succeeded"]) for row in group),
                "shards_failed_mean": statistics.fmean(float(row["shards_failed"]) for row in group),
                "nodes_fetched_mean": statistics.fmean(float(row["nodes_fetched"]) for row in group),
                "branches_considered_mean": statistics.fmean(float(row["branches_considered"]) for row in group),
                "branches_pruned_mean": statistics.fmean(float(row["branches_pruned"]) for row in group),
                "index_candidates_mean": statistics.fmean(float(row["index_candidates"]) for row in group),
                "index_matches_mean": statistics.fmean(float(row["index_matches"]) for row in group),
                "owner_attempts_mean": statistics.fmean(float(row["owner_attempts"]) for row in group),
                "verified_matches_mean": statistics.fmean(float(row["verified_matches"]) for row in group),
            }
        )
    with (analysis_dir / "query_summary.csv").open("w", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=summary_fields)
        writer.writeheader()
        writer.writerows(summaries)

    with (analysis_dir / "query_summary.tex").open("w") as handle:
        handle.write("% Generated by analyze_campaign.py; do not edit by hand.\n")
        handle.write("\\begin{tabular}{rrrrlrrr}\n")
        handle.write("\\toprule\nPeers & Tuples & Shards & Bloom & Query & Median ms & P95 ms & PHT nodes \\\\\n")
        handle.write("\\midrule\n")
        for row in summaries:
            label = str(row["label"]).replace("_", "\\_")
            handle.write(
                f"{row['node_count']} & {row['catalog_size']} & {row['index_shards']} & "
                f"{'on' if row['bloom_pruning'] == 'true' else 'off'} & "
                f"{label} & "
                f"{float(row['duration_median_ms']):.2f} & "
                f"{float(row['duration_p95_ms']):.2f} & "
                f"{float(row['nodes_fetched_mean']):.1f} \\\\\n"
            )
        handle.write("\\bottomrule\n\\end{tabular}\n")

    with (analysis_dir / "analysis.json").open("w") as handle:
        json.dump(
            {
                "complete_cells": len(cell_dirs),
                "query_rows": len(rows),
                "groups": len(summaries),
                "bootstrap_samples": 2000,
                "bootstrap_seed": 20260729,
            },
            handle,
            indent=2,
        )
        handle.write("\n")
    print(f"analyzed {len(cell_dirs)} cells and {len(rows)} query rows")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
