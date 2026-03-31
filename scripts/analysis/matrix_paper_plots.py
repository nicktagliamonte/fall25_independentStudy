#!/usr/bin/env python3
# Purpose: Build paper-oriented figures from test_results/matrix cell directories 

"""
Reads matrix layout: <test>_n<N>_i<I>_{vnipfs|swarm}/ and writes PNGs + CAPTIONS.md.

Excluded from comparative plots (non-comparable or out of scope here):
  routing_overhead, repair_time, replication, replication_distribution

Comparative (vn-IPFS vs Swarm when both cells exist):
  download_warm, upload (batch 1 primary), concurrent, storage_efficiency (caveat in captions)

vn-IPFS-only figure (no Swarm analogue in harness):
  lookup_complexity
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path
from typing import Any

# Defaults are repo-relative so the script works no matter the shell cwd.
_REPO_ROOT = Path(__file__).resolve().parent.parent.parent
_DEFAULT_MATRIX_ROOT = _REPO_ROOT / "test_results" / "matrix"
_DEFAULT_OUTPUT_DIR = _DEFAULT_MATRIX_ROOT / "_paper_figures"

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
import numpy as np
import pandas as pd

try:
    import seaborn as sns

    sns.set_theme(style="whitegrid", context="paper")
except Exception:
    plt.style.use("seaborn-v0_8-whitegrid")

LABEL_OUR = "vn-IPFS"
LABEL_SWARM = "Swarm"

COMPARATIVE_TESTS = (
    "download_warm",
    "upload",
    "concurrent",
    "storage_efficiency",
)
VNIPFS_ONLY_TESTS = ("lookup_complexity",)
SKIP_TESTS = (
    "routing_overhead",
    "repair_time",
    "replication",
    "replication_distribution",
)


def parse_cell_name(name: str) -> dict[str, Any] | None:
    m = re.match(r"^(.+)_n(\d+)_i(\d+)_(vnipfs|swarm)$", name)
    if not m:
        return None
    return {
        "test": m.group(1),
        "n": int(m.group(2)),
        "i": int(m.group(3)),
        "side": m.group(4),
    }


def discover_cells(matrix_root: Path, iterations: int) -> dict[tuple[str, int], dict[str, Path]]:
    """Map (test, N) -> {'vnipfs': path, 'swarm': path} for dirs that exist."""
    out: dict[tuple[str, int], dict[str, Path]] = {}
    for d in sorted(matrix_root.iterdir()):
        if not d.is_dir() or d.name.startswith("_"):
            continue
        meta = parse_cell_name(d.name)
        if not meta or meta["i"] != iterations:
            continue
        key = (meta["test"], meta["n"])
        out.setdefault(key, {})
        out[key][meta["side"]] = d
    return out


def relabel_system(s: str) -> str:
    s = str(s).strip()
    if s in ("our_system", "ours", "vnipfs"):
        return LABEL_OUR
    if s in ("swarm", "bee"):
        return LABEL_SWARM
    return s


def read_download_warm(path: Path, n: int) -> pd.DataFrame | None:
    f = next(path.glob("download_n*_warm.csv"), None)
    if f is None or not f.exists():
        return None
    df = pd.read_csv(f)
    if df.empty or "payload_size" not in df.columns:
        return None
    for col in ("ttfb_ms", "total_ms"):
        if col in df.columns:
            df[col] = pd.to_numeric(df[col], errors="coerce")
    df = df.dropna(subset=["payload_size"])
    df = df[df["ttfb_ms"].notna() & df["total_ms"].notna()]
    if df.empty:
        return None
    df["node_count"] = n
    df["system"] = df["system"].map(relabel_system)
    return df


def read_upload_batch1(path: Path, n: int) -> pd.DataFrame | None:
    f = path / f"upload_n{n}_batch1.csv"
    if not f.exists():
        return None
    df = pd.read_csv(f)
    if df.empty or "latency_ms" not in df.columns:
        return None
    df = df[df["latency_ms"].astype(str) != "ERROR"].copy()
    df["latency_ms"] = pd.to_numeric(df["latency_ms"], errors="coerce")
    df = df.dropna(subset=["latency_ms"])
    if df.empty:
        return None
    df["node_count"] = n
    df["system"] = df["system"].map(relabel_system)
    return df


def read_concurrent(path: Path, n: int) -> pd.DataFrame | None:
    f = path / "concurrent_results.csv"
    if not f.exists():
        return None
    df = pd.read_csv(f)
    need = ("system", "concurrent_writes", "concurrent_reads", "throughput_mbps", "p99_latency_ms")
    if not all(c in df.columns for c in need):
        return None
    df["throughput_mbps"] = pd.to_numeric(df["throughput_mbps"], errors="coerce")
    df["p99_latency_ms"] = pd.to_numeric(df["p99_latency_ms"], errors="coerce")
    df = df.dropna(subset=["throughput_mbps"])
    if df.empty:
        return None
    df["node_count"] = n
    df["system"] = df["system"].map(relabel_system)
    df["load_label"] = (
        df["concurrent_writes"].astype(int).astype(str)
        + "w/"
        + df["concurrent_reads"].astype(int).astype(str)
        + "r"
    )
    return df


def read_storage(path: Path, n: int) -> pd.DataFrame | None:
    f = path / "storage_efficiency_results.csv"
    if not f.exists():
        return None
    df = pd.read_csv(f)
    cols = ("system", "payload_size", "nodes", "disk_bytes", "efficiency_ratio")
    if not all(c in df.columns for c in cols):
        return None
    df["efficiency_ratio"] = pd.to_numeric(df["efficiency_ratio"], errors="coerce")
    df["nodes"] = pd.to_numeric(df["nodes"], errors="coerce")
    df = df.dropna(subset=["efficiency_ratio"])
    if df.empty:
        return None
    df["node_count"] = n
    df["system"] = df["system"].map(relabel_system)
    return df


def read_lookup_complexity(path: Path, n: int) -> pd.DataFrame | None:
    """Cold lookup rows only; hops may be NaN (N/A); optional lookup_latency_ms column."""
    f = path / "lookup_complexity_results.csv"
    if not f.exists():
        return None
    df = pd.read_csv(f)
    if "hops" not in df.columns or "operation" not in df.columns:
        return None
    df = df[df["operation"].astype(str).str.lower() == "lookup"].copy()
    if df.empty:
        return None
    df["hops"] = pd.to_numeric(df["hops"], errors="coerce")
    if "lookup_latency_ms" in df.columns:
        df["lookup_latency_ms"] = pd.to_numeric(df["lookup_latency_ms"], errors="coerce")
    else:
        df["lookup_latency_ms"] = np.nan
    df["node_count"] = n
    df["system"] = df["system"].map(relabel_system)
    return df


def plot_download_warm(
    cells: dict[tuple[str, int], dict[str, Path]],
    node_counts: list[int],
    out_dir: Path,
) -> list[str]:
    written: list[str] = []
    for metric, ylabel, fname in (
        ("total_ms", "Mean total time (ms)", "fig01_download_warm_total_ms.png"),
        ("ttfb_ms", "Mean TTFB (ms)", "fig02_download_warm_ttfb_ms.png"),
    ):
        fig, axes = plt.subplots(1, len(node_counts), figsize=(4.2 * len(node_counts), 4), squeeze=False)
        for j, n in enumerate(node_counts):
            ax = axes[0, j]
            key = ("download_warm", n)
            parts = cells.get(key, {})
            rows = []
            for side in ("vnipfs", "swarm"):
                p = parts.get(side)
                if p is None:
                    continue
                d = read_download_warm(p, n)
                if d is None:
                    continue
                g = d.groupby(["system", "payload_size"], as_index=False)[metric].mean()
                rows.append(g)
            if not rows:
                ax.set_visible(False)
                continue
            plot_df = pd.concat(rows, ignore_index=True)
            payload_sizes = sorted(plot_df["payload_size"].unique())
            x = np.arange(len(payload_sizes))
            systems = [LABEL_OUR, LABEL_SWARM]
            width = 0.35
            for si, sys in enumerate(systems):
                means = []
                for ps in payload_sizes:
                    sub = plot_df[(plot_df["system"] == sys) & (plot_df["payload_size"] == ps)]
                    means.append(float(sub[metric].mean()) if len(sub) else np.nan)
                if not any(np.isfinite(means)):
                    continue
                offset = (si - 0.5) * width
                ax.bar(x + offset, means, width, label=sys)
            ax.set_xticks(x)
            ax.set_xticklabels([_fmt_size(ps) for ps in payload_sizes], rotation=15, ha="right")
            ax.set_ylabel(ylabel)
            ax.set_title(f"N = {n}")
            ax.legend()
        fig.suptitle(f"Warm same-node GET — {ylabel.split('(')[0].strip()}", fontsize=12, y=1.02)
        fig.tight_layout()
        path = out_dir / fname
        fig.savefig(path, dpi=150, bbox_inches="tight")
        plt.close(fig)
        written.append(str(path))
    return written


def _fmt_size(ps: float | int) -> str:
    ps = int(ps)
    if ps >= 1048576:
        return "1 MiB"
    if ps >= 102400:
        return "100 KiB"
    if ps >= 10240:
        return "10 KiB"
    return "1 KiB"


def plot_upload_batch1(
    cells: dict[tuple[str, int], dict[str, Path]],
    node_counts: list[int],
    out_dir: Path,
) -> list[str]:
    fig, axes = plt.subplots(1, len(node_counts), figsize=(4.2 * len(node_counts), 4), squeeze=False)
    written: list[str] = []
    for j, n in enumerate(node_counts):
        ax = axes[0, j]
        key = ("upload", n)
        parts = cells.get(key, {})
        rows = []
        for side in ("vnipfs", "swarm"):
            p = parts.get(side)
            if p is None:
                continue
            d = read_upload_batch1(p, n)
            if d is None:
                continue
            rows.append(d)
        if not rows:
            ax.set_visible(False)
            continue
        all_df = pd.concat(rows, ignore_index=True)
        g = all_df.groupby(["system", "payload_size"], as_index=False)["latency_ms"].mean()
        payload_sizes = sorted(g["payload_size"].unique())
        x = np.arange(len(payload_sizes))
        width = 0.35
        for si, sys in enumerate([LABEL_OUR, LABEL_SWARM]):
            means = []
            for ps in payload_sizes:
                sub = g[(g["system"] == sys) & (g["payload_size"] == ps)]
                means.append(float(sub["latency_ms"].mean()) if len(sub) else np.nan)
            if not any(np.isfinite(means)):
                continue
            offset = (si - 0.5) * width
            ax.bar(x + offset, means, width, label=sys)
        ax.set_xticks(x)
        ax.set_xticklabels([_fmt_size(ps) for ps in payload_sizes], rotation=15, ha="right")
        ax.set_ylabel("Mean upload latency (ms), batch size 1")
        ax.set_title(f"N = {n}")
        ax.legend()
    fig.suptitle("Upload (batch size 1, mean latency)", fontsize=12, y=1.02)
    fig.tight_layout()
    path = out_dir / "fig03_upload_latency_batch1_mean.png"
    fig.savefig(path, dpi=150, bbox_inches="tight")
    plt.close(fig)
    written.append(str(path))
    return written


def plot_concurrent(
    cells: dict[tuple[str, int], dict[str, Path]],
    node_counts: list[int],
    out_dir: Path,
) -> list[str]:
    fig, axes = plt.subplots(1, len(node_counts), figsize=(4.5 * len(node_counts), 4.2), squeeze=False)
    for j, n in enumerate(node_counts):
        ax = axes[0, j]
        key = ("concurrent", n)
        parts = cells.get(key, {})
        rows = []
        for side in ("vnipfs", "swarm"):
            p = parts.get(side)
            if p is None:
                continue
            d = read_concurrent(p, n)
            if d is None:
                continue
            rows.append(d)
        if not rows:
            ax.set_visible(False)
            continue
        plot_df = pd.concat(rows, ignore_index=True)

        def _load_key(lb: str) -> tuple[int, int]:
            m = re.match(r"(\d+)w/(\d+)r", str(lb))
            if m:
                return (int(m.group(1)), int(m.group(2)))
            return (0, 0)

        labels = sorted(plot_df["load_label"].unique(), key=_load_key)
        x = np.arange(len(labels))
        width = 0.35
        for si, sys in enumerate([LABEL_OUR, LABEL_SWARM]):
            means = []
            for lb in labels:
                sub = plot_df[(plot_df["system"] == sys) & (plot_df["load_label"] == lb)]
                means.append(float(sub["throughput_mbps"].mean()) if len(sub) else np.nan)
            if not any(np.isfinite(means)):
                continue
            offset = (si - 0.5) * width
            ax.bar(x + offset, means, width, label=sys)
        ax.set_xticks(x)
        ax.set_xticklabels(labels, rotation=20, ha="right")
        ax.set_ylabel("Throughput (MB/s)")
        ax.set_title(f"N = {n}")
        ax.legend()
    fig.suptitle("Concurrent throughput", fontsize=12, y=1.02)
    fig.tight_layout()
    path = out_dir / "fig04_concurrent_throughput.png"
    fig.savefig(path, dpi=150, bbox_inches="tight")
    plt.close(fig)
    return [str(path)]


def plot_storage(
    cells: dict[tuple[str, int], dict[str, Path]],
    node_counts: list[int],
    out_dir: Path,
    exclude_node_counts: frozenset[int],
) -> list[str]:
    rows_out = []
    plot_nodes = [n for n in node_counts if n not in exclude_node_counts]
    if not plot_nodes:
        return []
    for n in plot_nodes:
        key = ("storage_efficiency", n)
        parts = cells.get(key, {})
        for side in ("vnipfs", "swarm"):
            p = parts.get(side)
            if p is None:
                continue
            d = read_storage(p, n)
            if d is None:
                continue
            for _, r in d.iterrows():
                rows_out.append(
                    {
                        "node_count": n,
                        "system": r["system"],
                        "efficiency_ratio": r["efficiency_ratio"],
                        "nodes_reported": r["nodes"],
                        "payload_size": r["payload_size"],
                    }
                )
    if not rows_out:
        return []
    df = pd.DataFrame(rows_out)
    fig, ax = plt.subplots(figsize=(8, 4))
    systems = [LABEL_OUR, LABEL_SWARM]
    x = np.arange(len(plot_nodes))
    width = 0.35
    for si, sys in enumerate(systems):
        vals = []
        for n in plot_nodes:
            sub = df[(df["system"] == sys) & (df["node_count"] == n)]
            vals.append(float(sub["efficiency_ratio"].mean()) if len(sub) else np.nan)
        if not any(np.isfinite(vals)):
            continue
        offset = (si - 0.5) * width
        ax.bar(x + offset, vals, width, label=sys)
    ax.set_xticks(x)
    ax.set_xticklabels([str(n) for n in plot_nodes])
    ax.set_xlabel("Node count")
    ax.set_ylabel("Efficiency Ratio")
    ax.set_title("Storage efficiency")
    ax.legend()
    fig.tight_layout()
    path = out_dir / "fig05_storage_efficiency_snapshot.png"
    fig.savefig(path, dpi=150, bbox_inches="tight")
    plt.close(fig)
    return [str(path)]


def plot_lookup_vnipfs_only(
    cells: dict[tuple[str, int], dict[str, Path]],
    node_counts: list[int],
    out_dir: Path,
) -> list[str]:
    rows = []
    for n in node_counts:
        key = ("lookup_complexity", n)
        parts = cells.get(key, {})
        p = parts.get("vnipfs")
        if p is None:
            continue
        d = read_lookup_complexity(p, n)
        if d is None:
            continue
        rows.append(d)
    if not rows:
        return []
    df = pd.concat(rows, ignore_index=True)
    present_n = sorted(df["node_count"].unique())
    hop_means = df.groupby("node_count")["hops"].mean()
    lat_means = df.groupby("node_count")["lookup_latency_ms"].mean()
    has_hops = hop_means.notna().any()
    has_lat = lat_means.notna().any()

    fig, axes = plt.subplots(1, 2, figsize=(10, 4))
    ax0, ax1 = axes[0], axes[1]
    if has_hops:
        ax0.plot(
            hop_means.index.astype(float),
            hop_means.values,
            marker="o",
            linewidth=2,
            color="C0",
        )
        ax0.set_ylabel("Mean routing query events (cold lookup)")
    else:
        ax0.text(0.5, 0.5, "No data", ha="center", va="center")
    ax0.set_xticks(present_n)
    ax0.set_xlabel("Node count")
    ax0.set_title("Cold lookup — hops")

    if has_lat:
        ax1.plot(
            lat_means.index.astype(float),
            lat_means.values,
            marker="s",
            linewidth=2,
            color="C1",
        )
        ax1.set_ylabel("Mean lookup_latency_ms (cold)")
    else:
        ax1.text(0.5, 0.5, "No data", ha="center", va="center")
    ax1.set_xticks(present_n)
    ax1.set_xlabel("Node count")
    ax1.set_title("Cold lookup — latency")

    fig.tight_layout()
    path = out_dir / "fig06_lookup_complexity_vnipfs_only.png"
    fig.savefig(path, dpi=150, bbox_inches="tight")
    plt.close(fig)
    return [str(path)]


def write_captions(
    out_dir: Path,
    figures: list[str],
    matrix_root: Path,
    iterations: int,
    node_counts: list[int],
    exclude_storage: frozenset[int],
) -> None:
    cap = out_dir / "CAPTIONS.md"
    lines = [
        "# Matrix paper figures",
        "",
        f"Generated from `{matrix_root}` with `_n<N>_i{iterations}_{{vnipfs,swarm}}` layout.",
        f"Node counts included: {', '.join(map(str, node_counts))}.",
        "",
        "**Not plotted (non-comparable or omitted by design):** "
        + ", ".join(SKIP_TESTS)
        + ".",
        "",
        "## Figure notes",
        "",
        "- **fig01 / fig02 (download warm):** Same-node cached GET after upload; LAN microbenchmark. "
        "Compares mean `total_ms` / `ttfb_ms` by payload size. Not predictive of wide-area behavior.",
        "",
        "- **fig03 (upload):** Batch size **1** mean latency only; Swarm client path may differ from vn-IPFS Docker/exec path.",
        "",
        "- **fig04 (concurrent):** Throughput as reported by the harness for each load label.",
        "",
        "- **fig05 (storage):** Uses `efficiency_ratio`; **definitions differ between stacks**. "
        f"Node counts excluded from this plot: {sorted(exclude_storage) or 'none'}.",
        "",
        "- **fig06 (lookup complexity):** **vn-IPFS only**; cold `lookup-key` hop count + latency. "
        "Does not prove asymptotics by itself — pair with analysis.",
        "",
        "## Files",
        "",
    ]
    for f in sorted(figures):
        lines.append(f"- `{Path(f).name}`")
    cap.write_text("\n".join(lines) + "\n", encoding="utf-8")


def write_manifest(out_dir: Path, meta: dict[str, Any]) -> None:
    (out_dir / "manifest.json").write_text(json.dumps(meta, indent=2) + "\n", encoding="utf-8")


def _sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def _print_input_csv_fingerprints(
    matrix_root: Path,
    iterations: int,
    node_counts: list[int],
    *,
    file,
) -> None:
    """Log which CSVs feed each figure so unchanged PNGs can be traced to unchanged inputs."""
    print("Input CSV fingerprints (SHA256):", file=file)
    for n in node_counts:
        for test, rel_names in (
            ("download_warm", (f"download_n{n}_warm.csv",)),
            ("upload", (f"upload_n{n}_batch1.csv",)),
            ("concurrent", ("concurrent_results.csv",)),
            ("storage_efficiency", ("storage_efficiency_results.csv",)),
            ("lookup_complexity", ("lookup_complexity_results.csv",)),
        ):
            for side in ("vnipfs", "swarm"):
                cell = matrix_root / f"{test}_n{n}_i{iterations}_{side}"
                for rel in rel_names:
                    p = cell / rel
                    if p.is_file():
                        print(f"  {p.relative_to(matrix_root)}  {_sha256_file(p)[:16]}…", file=file)


def main() -> int:
    ap = argparse.ArgumentParser(description="Paper-oriented plots from test_results/matrix.")
    ap.add_argument(
        "--matrix-root",
        type=Path,
        default=_DEFAULT_MATRIX_ROOT,
        help=f"Matrix root (default: {_DEFAULT_MATRIX_ROOT})",
    )
    ap.add_argument(
        "--output-dir",
        type=Path,
        default=_DEFAULT_OUTPUT_DIR,
        help=f"Output directory for PNGs and CAPTIONS.md (default: {_DEFAULT_OUTPUT_DIR})",
    )
    ap.add_argument(
        "--verbose",
        action="store_true",
        help="Print resolved paths, discovered cells, and SHA256 of input CSVs used for plots",
    )
    ap.add_argument("--iterations", type=int, default=10, help="Match matrix cell i= value")
    ap.add_argument(
        "--node-counts",
        type=str,
        default="10,50,100",
        help="Comma-separated N list (default: 10,50,100)",
    )
    ap.add_argument(
        "--exclude-storage-node-counts",
        type=str,
        default="10",
        help="Comma-separated N to omit from fig05 storage (default: 10 — often mis-recorded vs larger N)",
    )
    args = ap.parse_args()
    matrix_root = args.matrix_root.resolve()
    out_dir = args.output_dir.resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    iterations = args.iterations
    node_counts = [int(x.strip()) for x in args.node_counts.split(",") if x.strip()]
    exclude_storage = frozenset(
        int(x.strip()) for x in args.exclude_storage_node_counts.split(",") if x.strip()
    )

    if not matrix_root.is_dir():
        print(f"Error: matrix root not found: {matrix_root}", file=sys.stderr)
        return 1

    print(f"Matrix root: {matrix_root}", file=sys.stderr)
    print(f"Output dir:  {out_dir}", file=sys.stderr)

    cells = discover_cells(matrix_root, iterations)
    print(
        f"Discovered {len(cells)} cell(s) matching i={iterations} "
        f"(pattern <test>_n<N>_i{iterations}_{{vnipfs|swarm}})",
        file=sys.stderr,
    )
    if args.verbose:
        for key in sorted(cells.keys()):
            print(f"  cell {key}: {cells[key]}", file=sys.stderr)
        _print_input_csv_fingerprints(matrix_root, iterations, node_counts, file=sys.stderr)
    all_written: list[str] = []

    all_written.extend(plot_download_warm(cells, node_counts, out_dir))
    all_written.extend(plot_upload_batch1(cells, node_counts, out_dir))
    all_written.extend(plot_concurrent(cells, node_counts, out_dir))
    all_written.extend(plot_storage(cells, node_counts, out_dir, exclude_storage))
    all_written.extend(plot_lookup_vnipfs_only(cells, node_counts, out_dir))

    meta = {
        "matrix_root": str(matrix_root),
        "output_dir": str(out_dir),
        "iterations": iterations,
        "node_counts": node_counts,
        "exclude_storage_node_counts": sorted(exclude_storage),
        "comparative_tests": list(COMPARATIVE_TESTS),
        "vnipfs_only_tests": list(VNIPFS_ONLY_TESTS),
        "skipped_tests": list(SKIP_TESTS),
        "figures": [str(Path(p).name) for p in all_written],
    }
    write_manifest(out_dir, meta)
    write_captions(out_dir, all_written, matrix_root, iterations, node_counts, exclude_storage)

    print(f"Wrote {len(all_written)} figure(s) to {out_dir}")
    print(f"Captions: {out_dir / 'CAPTIONS.md'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
