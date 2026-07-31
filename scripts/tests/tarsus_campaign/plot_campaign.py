#!/usr/bin/env python3
"""Generate publication figures from a validated Tarsus campaign."""

from __future__ import annotations

import csv
import statistics
import sys
from pathlib import Path

import matplotlib.pyplot as plt


LABELS = {
    "exact": "exact",
    "prefix-one-percent": "prefix (1%)",
    "substring-rare": "substring (1%)",
    "substring-medium": "substring (9%)",
    "substring-common": "substring (40%)",
}
MARKERS = ["o", "s", "^", "D", "v"]


def median_ms(rows: list[dict[str, str]]) -> float:
    return statistics.median(float(row["duration_ns"]) / 1_000_000 for row in rows)


def load_csv(path: Path) -> list[dict[str, str]]:
    with path.open(newline="") as handle:
        return list(csv.DictReader(handle))


def select(
    rows: list[dict[str, str]],
    *,
    nodes: int | None = None,
    catalog: int | None = None,
    shards: int | None = None,
    bloom: str | None = None,
    label: str | None = None,
    kind: str | None = None,
) -> list[dict[str, str]]:
    selected = []
    for row in rows:
        if nodes is not None and int(row["node_count"]) != nodes:
            continue
        if catalog is not None and int(row["catalog_size"]) != catalog:
            continue
        if shards is not None and int(row["index_shards"]) != shards:
            continue
        if bloom is not None and row["bloom_pruning"] != bloom:
            continue
        if label is not None and row["label"] != label:
            continue
        if kind is not None and row["query_kind"] != kind:
            continue
        selected.append(row)
    return selected


def configure() -> None:
    plt.rcParams.update(
        {
            "font.size": 8,
            "axes.labelsize": 8,
            "axes.titlesize": 8,
            "legend.fontsize": 7,
            "xtick.labelsize": 7,
            "ytick.labelsize": 7,
            "pdf.fonttype": 42,
            "ps.fonttype": 42,
        }
    )


def query_scaling(rows: list[dict[str, str]], output: Path) -> None:
    fig, ax = plt.subplots(figsize=(3.45, 2.55))
    peers = [10, 50, 90]
    series = ["exact", "prefix-one-percent", "substring-rare", "substring-medium", "substring-common"]
    for index, label in enumerate(series):
        values = []
        for node_count in peers:
            if label == "exact":
                group = select(rows, nodes=node_count, catalog=10000, shards=16, bloom="true", kind="exact")
            else:
                group = select(rows, nodes=node_count, catalog=10000, shards=16, bloom="true", label=label)
            values.append(median_ms(group))
        ax.plot(peers, values, marker=MARKERS[index], linewidth=1.1, markersize=3.8, label=LABELS[label])
    ax.set_yscale("log")
    ax.set_xticks(peers)
    ax.set_xlabel("peers")
    ax.set_ylabel("median query-path latency (ms, log scale)")
    ax.grid(True, which="both", linewidth=0.35, alpha=0.5)
    # Keep the key outside the axes: the high-selectivity curves occupy most
    # of the useful plot area and an in-axes legend hides measured points.
    ax.legend(
        ncol=2,
        frameon=False,
        loc="lower center",
        bbox_to_anchor=(0.5, 1.01),
        borderaxespad=0,
    )
    fig.tight_layout(pad=0.4)
    fig.savefig(output, bbox_inches="tight")
    plt.close(fig)


def shard_tradeoff(rows: list[dict[str, str]], population: list[dict[str, str]], output: Path) -> None:
    fig, axes = plt.subplots(1, 2, figsize=(7.0, 2.55))
    shards = [1, 4, 16, 64]
    mutation_shards = [1, 4, 16]
    throughput = {}
    for row in population:
        if int(row["node_count"]) == 90 and int(row["catalog_size"]) == 10000 and row["bloom_pruning"] == "True":
            throughput[int(row["index_shards"])] = int(row["populate_requested"]) / (int(row["populate_duration_ns"]) / 1e9)
    axes[0].plot(mutation_shards, [throughput[value] for value in mutation_shards], marker="o", linewidth=1.2, color="black")
    axes[0].set_xscale("log", base=2)
    axes[0].set_xticks(mutation_shards, [str(value) for value in mutation_shards])
    axes[0].set_xlabel("index shards")
    axes[0].set_ylabel("population throughput (tuples/s)")
    axes[0].grid(True, linewidth=0.35, alpha=0.5)
    axes[0].set_title("(a) mutation path")

    labels = ["prefix-one-percent", "substring-rare", "substring-medium", "substring-common"]
    for index, label in enumerate(labels):
        values = [
            median_ms(select(rows, nodes=90, catalog=10000, shards=shard, bloom="true", label=label))
            for shard in shards
        ]
        axes[1].plot(shards, values, marker=MARKERS[index], linewidth=1.1, markersize=3.8, label=LABELS[label])
    axes[1].set_xscale("log", base=2)
    axes[1].set_yscale("log")
    axes[1].set_xticks(shards, [str(value) for value in shards])
    axes[1].set_xlabel("index shards")
    axes[1].set_ylabel("median query-path latency (ms)")
    axes[1].grid(True, which="both", linewidth=0.35, alpha=0.5)
    axes[1].set_title("(b) query path")
    handles, legend_labels = axes[1].get_legend_handles_labels()
    # Reserve a figure-level header band for the key so it cannot obscure the
    # query panel, its title, or measured points.
    fig.tight_layout(pad=0.5, w_pad=1.0, rect=(0, 0, 1, 0.84))
    fig.legend(
        handles,
        legend_labels,
        frameon=False,
        fontsize=6.5,
        ncol=2,
        loc="upper center",
        bbox_to_anchor=(0.75, 0.99),
        borderaxespad=0,
    )
    fig.savefig(output, bbox_inches="tight")
    plt.close(fig)


def bloom_ablation(rows: list[dict[str, str]], output: Path) -> None:
    fig, axes = plt.subplots(1, 2, figsize=(7.0, 2.55))
    labels = ["substring-rare", "substring-medium", "substring-common"]
    display = ["1%", "9%", "40%"]
    x = list(range(len(labels)))
    width = 0.36
    for offset, bloom, legend, color in [(-width / 2, "true", "Bloom on", "0.25"), (width / 2, "false", "Bloom off", "0.75")]:
        medians = []
        nodes = []
        for label in labels:
            group = select(rows, nodes=90, catalog=10000, shards=16, bloom=bloom, label=label)
            medians.append(median_ms(group))
            nodes.append(statistics.fmean(float(row["nodes_fetched"]) for row in group))
        axes[0].bar([value + offset for value in x], medians, width, label=legend, color=color, edgecolor="black", linewidth=0.4)
        axes[1].bar([value + offset for value in x], nodes, width, label=legend, color=color, edgecolor="black", linewidth=0.4)
    for ax in axes:
        ax.set_xticks(x, display)
        ax.set_xlabel("matching catalog fraction")
        ax.grid(True, axis="y", linewidth=0.35, alpha=0.5)
    axes[0].set_ylabel("median query-path latency (ms)")
    axes[0].set_title("(a) server-side query")
    axes[1].set_ylabel("mean PHT nodes fetched")
    axes[1].set_title("(b) index work")
    handles, legend_labels = axes[0].get_legend_handles_labels()
    fig.tight_layout(pad=0.5, w_pad=1.0, rect=(0, 0, 1, 0.88))
    fig.legend(
        handles,
        legend_labels,
        frameon=False,
        ncol=2,
        loc="upper center",
        bbox_to_anchor=(0.25, 0.99),
        borderaxespad=0,
    )
    fig.savefig(output, bbox_inches="tight")
    plt.close(fig)


def main() -> int:
    if len(sys.argv) != 3:
        print(f"usage: {sys.argv[0]} RUN_DIR OUTPUT_DIR", file=sys.stderr)
        return 2
    run_dir = Path(sys.argv[1])
    output_dir = Path(sys.argv[2])
    output_dir.mkdir(parents=True, exist_ok=True)
    rows = load_csv(run_dir / "analysis" / "queries_all.csv")
    population = load_csv(run_dir / "analysis" / "population_all.csv")
    configure()
    query_scaling(rows, output_dir / "query_scaling.pdf")
    shard_tradeoff(rows, population, output_dir / "shard_tradeoff.pdf")
    bloom_ablation(rows, output_dir / "bloom_ablation.pdf")
    print(f"wrote 3 figures to {output_dir}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
