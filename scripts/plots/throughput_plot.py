#!/usr/bin/env python3
"""
Plot throughput vs file size with latency overlay.
Reads throughput.csv and generates a dual-axis plot.
"""

import csv
import sys
import argparse
from pathlib import Path

try:
    import matplotlib
    matplotlib.use('Agg')
    import matplotlib.pyplot as plt
    HAS_MATPLOTLIB = True
except ImportError as e:
    HAS_MATPLOTLIB = False
    print(f"ERROR: matplotlib not available: {e}", file=sys.stderr)


def load_csv(csv_path: Path):
    rows = []
    with open(csv_path, 'r') as f:
        reader = csv.DictReader(f)
        for row in reader:
            try:
                rows.append({
                    "size_label": row["size_label"],
                    "size_bytes": int(row["size_bytes"]),
                    "put_duration_s": float(row["put_duration_s"]),
                    "restore_duration_s": float(row["restore_duration_s"]),
                    "restore_bytes": int(row["restore_bytes"]),
                    "throughput_mb_s": float(row["throughput_mb_s"]),
                })
            except (KeyError, ValueError):
                continue
    return rows


def main():
    parser = argparse.ArgumentParser(description="Plot throughput vs file size with latency overlay")
    parser.add_argument("results_dir", nargs="?", help="Throughput test results directory")
    parser.add_argument("--csv", help="Path to throughput.csv (default: results_dir/throughput.csv)")
    parser.add_argument("--output", help="Output plot path (default: results_dir/plots/throughput_latency.png)")
    args = parser.parse_args()

    if not HAS_MATPLOTLIB:
        sys.exit(1)

    if args.results_dir:
        results_dir = Path(args.results_dir)
    else:
        results_dir = None

    if args.csv:
        csv_path = Path(args.csv)
    elif results_dir:
        csv_path = results_dir / "throughput.csv"
    else:
        print("ERROR: Provide results_dir or --csv", file=sys.stderr)
        sys.exit(1)

    if not csv_path.exists():
        print(f"ERROR: CSV not found: {csv_path}", file=sys.stderr)
        sys.exit(1)

    rows = load_csv(csv_path)
    if not rows:
        print("ERROR: No data found in CSV", file=sys.stderr)
        sys.exit(1)

    rows.sort(key=lambda r: r["size_bytes"])
    sizes = [r["size_bytes"] for r in rows]
    labels = [r["size_label"] for r in rows]
    throughput = [r["throughput_mb_s"] for r in rows]
    latency = [r["restore_duration_s"] for r in rows]

    fig, ax1 = plt.subplots(figsize=(10, 7))

    ax1.plot(sizes, throughput, marker='o', color='steelblue', label='Throughput (MB/s)')
    ax1.set_xlabel('File Size (bytes)', fontsize=11, fontweight='bold')
    ax1.set_ylabel('Throughput (MB/s)', fontsize=11, fontweight='bold', color='steelblue')
    ax1.tick_params(axis='y', labelcolor='steelblue')
    ax1.set_xscale('log')
    ax1.grid(True, alpha=0.3)

    ax2 = ax1.twinx()
    ax2.plot(sizes, latency, marker='s', color='darkorange', label='Latency (s)')
    ax2.set_ylabel('Latency (seconds)', fontsize=11, fontweight='bold', color='darkorange')
    ax2.tick_params(axis='y', labelcolor='darkorange')

    ax1.set_title('Throughput vs File Size with Latency Overlay', fontsize=13, fontweight='bold')

    # Add size labels near points
    for x, y, lbl in zip(sizes, throughput, labels):
        ax1.annotate(lbl, (x, y), textcoords="offset points", xytext=(0, 6), ha='center', fontsize=8)

    lines, labels_1 = ax1.get_legend_handles_labels()
    lines2, labels_2 = ax2.get_legend_handles_labels()
    ax1.legend(lines + lines2, labels_1 + labels_2, loc='best')

    if args.output:
        output_path = Path(args.output)
    elif results_dir:
        output_path = results_dir / "plots" / "throughput_latency.png"
    else:
        output_path = Path("throughput_latency.png")

    output_path.parent.mkdir(parents=True, exist_ok=True)
    plt.tight_layout()
    plt.savefig(output_path, dpi=300, bbox_inches='tight')
    print(f"Plot saved to: {output_path}")


if __name__ == "__main__":
    main()
