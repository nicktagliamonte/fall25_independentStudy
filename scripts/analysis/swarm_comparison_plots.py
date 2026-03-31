#!/usr/bin/env python3
# Purpose: Generate comparison plots, exports, and report from full_comparison (or similar) CSVs.
# Plan 9.4: vn-IPFS vs Swarm visualization, scaling, replication, overhead, summary report.

from __future__ import annotations

import argparse
import json
import math
import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

import numpy as np
import pandas as pd

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.backends.backend_pdf import PdfPages
import seaborn as sns

sns.set_theme(style="whitegrid", context="talk")
plt.rcParams["figure.figsize"] = (11, 6)
plt.rcParams["font.size"] = 11

SYSTEM_DISPLAY = {"our_system": "vn-IPFS", "swarm": "Swarm"}
SYSTEM_ORDER = ["our_system", "swarm"]


def _parse_n_from_upload_name(path: Path) -> Optional[int]:
    m = re.search(r"_n(\d+)_", path.name)
    return int(m.group(1)) if m else None


def load_upload_tables(results_dir: Path) -> pd.DataFrame:
    frames: List[pd.DataFrame] = []
    for f in sorted(results_dir.glob("upload_n*_batch*.csv")):
        n = _parse_n_from_upload_name(f)
        if n is None:
            continue
        df = pd.read_csv(f)
        df["node_count"] = n
        frames.append(df)
    if not frames:
        return pd.DataFrame()
    return pd.concat(frames, ignore_index=True)


def load_download_tables(results_dir: Path) -> pd.DataFrame:
    frames: List[pd.DataFrame] = []
    for f in sorted(results_dir.glob("download_n*.csv")):
        m = re.search(r"_n(\d+)_(cold|warm)", f.stem)
        if not m:
            continue
        n, mode = int(m.group(1)), m.group(2)
        df = pd.read_csv(f)
        df["node_count"] = n
        df["cache_mode"] = mode
        for col in ("ttfb_ms", "total_ms"):
            if col in df.columns:
                df[col] = pd.to_numeric(df[col], errors="coerce")
        frames.append(df)
    if not frames:
        return pd.DataFrame()
    return pd.concat(frames, ignore_index=True)


def load_lookup_complexity(results_dir: Path) -> pd.DataFrame:
    p = results_dir / "lookup_complexity_results.csv"
    if not p.exists():
        return pd.DataFrame()
    df = pd.read_csv(p)
    if not all(c in df.columns for c in ("system", "node_count", "operation", "hops")):
        return pd.DataFrame()
    df = df[~df["hops"].isin(["N/A", "", "FAILED", np.nan])].copy()
    df["hops"] = pd.to_numeric(df["hops"], errors="coerce")
    df = df.dropna(subset=["hops"])
    return df


def load_replication(results_dir: Path) -> pd.DataFrame:
    p = results_dir / "replication_results.csv"
    if not p.exists():
        return pd.DataFrame()
    df = pd.read_csv(p)
    need = ("system", "payload_size", "nodes", "replicas_target", "time_to_R_s")
    if not all(c in df.columns for c in need):
        return pd.DataFrame()
    df = df[df["time_to_R_s"].astype(str).str.upper().isin(["SKIP", "TIMEOUT"]) == False].copy()
    df["time_to_R_s"] = pd.to_numeric(df["time_to_R_s"], errors="coerce")
    df = df.dropna(subset=["time_to_R_s"])
    return df


def load_replication_distribution(results_dir: Path) -> pd.DataFrame:
    p = results_dir / "replication_distribution.csv"
    if not p.exists():
        return pd.DataFrame()
    df = pd.read_csv(p)
    for c in ("near", "midrange", "farflung"):
        if c in df.columns:
            df[c] = pd.to_numeric(df[c], errors="coerce")
    return df


def load_repair_time(results_dir: Path) -> pd.DataFrame:
    p = results_dir / "repair_time_results.csv"
    if not p.exists():
        return pd.DataFrame()
    df = pd.read_csv(p)
    if not all(c in df.columns for c in ("system", "node_count", "repair_time_s")):
        return pd.DataFrame()
    df = df[df["repair_time_s"].astype(str).str.upper() != "SKIP"].copy()
    df["repair_time_s"] = pd.to_numeric(df["repair_time_s"], errors="coerce")
    df = df.dropna(subset=["repair_time_s"])
    return df


def load_network_bytes(results_dir: Path, node_sizes: List[int]) -> pd.DataFrame:
    p = results_dir / "upload_network_bytes.csv"
    if not p.exists():
        return pd.DataFrame()
    df = pd.read_csv(p)
    for c in ("node_count", "nodes"):
        if c in df.columns:
            df = df.rename(columns={c: "node_count"})
            return df
    if not node_sizes:
        return df
    mask_first = (df["system"] == "our_system") & (df["payload_size"] == 1024) & (df["batch_size"] == 1)
    starts = df.index[mask_first].tolist()
    blocks: List[pd.DataFrame] = []
    for i, start in enumerate(starts):
        end = starts[i + 1] if i + 1 < len(starts) else len(df)
        part = df.iloc[start:end].copy()
        nc = node_sizes[i] if i < len(node_sizes) else np.nan
        part["node_count"] = nc
        blocks.append(part)
    if blocks:
        return pd.concat(blocks, ignore_index=True)
    df["node_count"] = node_sizes[0] if node_sizes else np.nan
    return df


def summarize_group(df: pd.DataFrame, value_col: str, group_cols: List[str]) -> pd.DataFrame:
    if df.empty or value_col not in df.columns:
        return pd.DataFrame()
    g = df.groupby(group_cols, dropna=False)[value_col]
    out = g.agg(count="count", mean="mean", median="median", std="std").reset_index()
    return out


def _display_system(s: str) -> str:
    return SYSTEM_DISPLAY.get(str(s), str(s))


def plot_latency_bars(
    upload_df: pd.DataFrame,
    download_df: pd.DataFrame,
    out_dir: Path,
    pdf: Optional[PdfPages],
) -> None:
    fig, axes = plt.subplots(1, 3, figsize=(14, 5), constrained_layout=True)
    default_payload = 1024
    batch = 1

    up = upload_df[
        (upload_df.get("payload_size") == default_payload) & (upload_df.get("batch_size") == batch)
    ]
    if not up.empty:
        s = summarize_group(up, "latency_ms", ["node_count", "system"])
        if not s.empty:
            pivot = s.pivot(index="node_count", columns="system", values="median")
            pivot = pivot.rename(columns=_display_system)
            pivot.plot(kind="bar", ax=axes[0], rot=0, color=["#2ecc71", "#3498db"])
            axes[0].set_title(f"Upload latency (median)\npayload={default_payload} B, batch={batch}")
            axes[0].set_ylabel("ms")
            axes[0].set_xlabel("Network size N")
            axes[0].legend(title="")

    down = download_df[
        (download_df.get("payload_size") == default_payload)
        & (download_df.get("cache_mode") == "cold")
    ]
    if not down.empty:
        s = summarize_group(down, "total_ms", ["node_count", "system"])
        if not s.empty:
            pivot = s.pivot(index="node_count", columns="system", values="median")
            pivot = pivot.rename(columns=_display_system)
            pivot.plot(kind="bar", ax=axes[1], rot=0, color=["#2ecc71", "#3498db"])
            axes[1].set_title("Download latency (median, cold)")
            axes[1].set_ylabel("ms")
            axes[1].set_xlabel("Network size N")
            axes[1].legend(title="")

    if not down.empty:
        s = summarize_group(down, "ttfb_ms", ["node_count", "system"])
        if not s.empty:
            pivot = s.pivot(index="node_count", columns="system", values="median")
            pivot = pivot.rename(columns=_display_system)
            pivot.plot(kind="bar", ax=axes[2], rot=0, color=["#2ecc71", "#3498db"])
            axes[2].set_title("Lookup proxy: TTFB (median, cold)")
            axes[2].set_ylabel("ms")
            axes[2].set_xlabel("Network size N")
            axes[2].legend(title="")

    path = out_dir / "latency_comparison_by_N.png"
    fig.savefig(path, dpi=200, bbox_inches="tight")
    if pdf:
        pdf.savefig(fig, bbox_inches="tight")
    plt.close(fig)


def plot_throughput(
    upload_df: pd.DataFrame,
    download_df: pd.DataFrame,
    out_dir: Path,
    pdf: Optional[PdfPages],
) -> None:
    fig, axes = plt.subplots(1, 2, figsize=(12, 5), constrained_layout=True)
    default_payload = 1024
    batch = 1

    up = upload_df[
        (upload_df.get("payload_size") == default_payload) & (upload_df.get("batch_size") == batch)
    ].copy()
    if not up.empty and "latency_ms" in up.columns:
        up["thr_bps"] = default_payload / (up["latency_ms"].clip(lower=1e-6) / 1000.0)
        s = summarize_group(up, "thr_bps", ["node_count", "system"])
        if not s.empty:
            pv = s.pivot(index="node_count", columns="system", values="median")
            pv = pv.rename(columns=_display_system)
            pv.plot(kind="bar", ax=axes[0], rot=0, color=["#2ecc71", "#3498db"])
            axes[0].set_title(f"Upload throughput (median)\n{default_payload} B / latency")
            axes[0].set_ylabel("bytes/s")
            axes[0].set_xlabel("Network size N")
            axes[0].legend(title="")

    down = download_df[
        (download_df.get("payload_size") == default_payload)
        & (download_df.get("cache_mode") == "cold")
    ].copy()
    if not down.empty and "total_ms" in down.columns:
        down["thr_bps"] = default_payload / (down["total_ms"].clip(lower=1e-6) / 1000.0)
        s = summarize_group(down, "thr_bps", ["node_count", "system"])
        if not s.empty:
            pv = s.pivot(index="node_count", columns="system", values="median")
            pv = pv.rename(columns=_display_system)
            pv.plot(kind="bar", ax=axes[1], rot=0, color=["#2ecc71", "#3498db"])
            axes[1].set_title("Download throughput (median, cold)")
            axes[1].set_ylabel("bytes/s")
            axes[1].set_xlabel("Network size N")
            axes[1].legend(title="")

    path = out_dir / "throughput_comparison_by_N.png"
    fig.savefig(path, dpi=200, bbox_inches="tight")
    if pdf:
        pdf.savefig(fig, bbox_inches="tight")
    plt.close(fig)


def plot_scaling_loglog(
    upload_df: pd.DataFrame,
    download_df: pd.DataFrame,
    out_dir: Path,
    pdf: Optional[PdfPages],
) -> None:
    fig, axes = plt.subplots(1, 2, figsize=(12, 5), constrained_layout=True)
    default_payload = 1024
    batch = 1

    for ax, (df, col, title) in zip(
        axes,
        [
            (
                upload_df[
                    (upload_df.get("payload_size") == default_payload)
                    & (upload_df.get("batch_size") == batch)
                ],
                "latency_ms",
                "Upload median latency vs N",
            ),
            (
                download_df[
                    (download_df.get("payload_size") == default_payload)
                    & (download_df.get("cache_mode") == "cold")
                ],
                "total_ms",
                "Download (cold) median total vs N",
            ),
        ],
    ):
        if df.empty or col not in df.columns:
            ax.set_visible(False)
            continue
        s = summarize_group(df, col, ["node_count", "system"])
        if s.empty:
            continue
        for sys in SYSTEM_ORDER:
            sub = s[s["system"] == sys]
            if sub.empty:
                continue
            xs = sub["node_count"].astype(float).values
            ys = sub["median"].astype(float).values
            m = (xs > 0) & (ys > 0)
            xs, ys = xs[m], ys[m]
            if len(xs) < 2:
                ax.scatter(xs, ys, label=_display_system(sys), s=80)
                continue
            ax.loglog(xs, ys, "o-", label=_display_system(sys), linewidth=2, markersize=8)
            n0, y0 = xs[0], ys[0]
            nn = np.geomspace(xs.min(), xs.max(), 50)
            olog = y0 * np.log(nn) / max(np.log(n0), 1e-9)
            ax.loglog(nn, olog, "--", color="gray", alpha=0.7, label="O(log N) ref (scaled)" if sys == SYSTEM_ORDER[0] else "")
        ax.set_xlabel("Network size N (log)")
        ax.set_ylabel("Time (log)")
        ax.set_title(title + "\n(dashed: scaled log N through first point)")
        ax.legend()

    path = out_dir / "scaling_loglog.png"
    fig.savefig(path, dpi=200, bbox_inches="tight")
    if pdf:
        pdf.savefig(fig, bbox_inches="tight")
    plt.close(fig)


def power_law_exponent(n: np.ndarray, t: np.ndarray) -> Tuple[float, float]:
    """Return slope and intercept for log(t) ~ intercept + slope * log(n)."""
    m = (n > 0) & (t > 0) & np.isfinite(n) & np.isfinite(t)
    if m.sum() < 2:
        return float("nan"), float("nan")
    lx = np.log(n[m])
    ly = np.log(t[m])
    slope, intercept = np.polyfit(lx, ly, 1)
    return float(slope), float(intercept)


def plot_replication(repl_df: pd.DataFrame, out_dir: Path, pdf: Optional[PdfPages]) -> None:
    if repl_df.empty:
        return
    fig, ax = plt.subplots(figsize=(8, 5), constrained_layout=True)
    s = summarize_group(repl_df, "time_to_R_s", ["nodes", "system"])
    if s.empty:
        plt.close(fig)
        return
    pivot = s.pivot(index="nodes", columns="system", values="median")
    pivot = pivot.rename(columns=_display_system)
    pivot.plot(kind="bar", ax=ax, rot=0, color=["#2ecc71", "#3498db"])
    ax.set_ylabel("Time to R replicas (s, median)")
    ax.set_xlabel("Nodes")
    ax.set_title("Replication speed: time to R replicas")
    ax.legend(title="")
    path = out_dir / "replication_time_to_R.png"
    fig.savefig(path, dpi=200, bbox_inches="tight")
    if pdf:
        pdf.savefig(fig, bbox_inches="tight")
    plt.close(fig)


def plot_replication_distribution(dist_df: pd.DataFrame, out_dir: Path, pdf: Optional[PdfPages]) -> None:
    sub = dist_df[dist_df["system"] == "our_system"].copy()
    if sub.empty:
        return
    fig, ax = plt.subplots(figsize=(8, 5), constrained_layout=True)
    melt = sub.melt(
        id_vars=["node_count"],
        value_vars=[c for c in ("near", "midrange", "farflung") if c in sub.columns],
        var_name="bucket",
        value_name="count",
    )
    if melt.empty:
        plt.close(fig)
        return
    for bucket, g in melt.groupby("bucket"):
        ax.plot(g["node_count"], g["count"], "o-", label=bucket)
    ax.set_xlabel("Network size N")
    ax.set_ylabel("Replica count (distribution buckets)")
    ax.set_title("Replication distribution efficiency (vn-IPFS)\n(Swarm: N/A in dataset)")
    ax.legend()
    path = out_dir / "replication_distribution.png"
    fig.savefig(path, dpi=200, bbox_inches="tight")
    if pdf:
        pdf.savefig(fig, bbox_inches="tight")
    plt.close(fig)


def plot_overhead_bytes(net_df: pd.DataFrame, out_dir: Path, pdf: Optional[PdfPages]) -> None:
    if net_df.empty or "bytes_transferred" not in net_df.columns:
        return
    df = net_df.dropna(subset=["node_count", "payload_size", "batch_size"]).copy()
    df["payload_bytes"] = pd.to_numeric(df["payload_size"], errors="coerce")
    df["bytes_transferred"] = pd.to_numeric(df["bytes_transferred"], errors="coerce")
    df = df.dropna(subset=["payload_bytes", "bytes_transferred"])
    df["overhead_ratio"] = df["bytes_transferred"] / df["payload_bytes"].clip(lower=1)
    df = df[df["batch_size"] == 1]
    if df.empty:
        return
    fig, ax = plt.subplots(figsize=(9, 5), constrained_layout=True)
    for sys in SYSTEM_ORDER:
        sub = df[df["system"] == sys]
        if sub.empty:
            continue
        agg = sub.groupby("node_count", as_index=False)["overhead_ratio"].median()
        ax.plot(agg["node_count"], agg["overhead_ratio"], "o-", label=_display_system(sys), linewidth=2, markersize=8)
    ax.set_xlabel("Network size N")
    ax.set_ylabel("Bytes transferred / payload (median)")
    ax.set_title("Network efficiency proxy (batch_size=1)\n(Token routing vs CID path — byte-level, not message counts)")
    ax.legend()
    ax.set_yscale("log")
    path = out_dir / "network_overhead_ratio.png"
    fig.savefig(path, dpi=200, bbox_inches="tight")
    if pdf:
        pdf.savefig(fig, bbox_inches="tight")
    plt.close(fig)


def plot_lookup_hops(lookup_df: pd.DataFrame, out_dir: Path, pdf: Optional[PdfPages]) -> None:
    if lookup_df.empty:
        return
    fig, ax = plt.subplots(figsize=(8, 5), constrained_layout=True)
    s = summarize_group(lookup_df, "hops", ["node_count", "system", "operation"])
    if s.empty:
        plt.close(fig)
        return
    put_lk = s[s["operation"] == "put"]
    if not put_lk.empty:
        pv = put_lk.pivot(index="node_count", columns="system", values="median")
        pv = pv.rename(columns=_display_system)
        pv.plot(kind="bar", ax=ax, rot=0, color=["#2ecc71", "#3498db"])
    ax.set_ylabel("Median hops")
    ax.set_xlabel("Network size N")
    ax.set_title("Routing depth proxy (measured hops, put)\nLookup rows often N/A in this run")
    ax.legend(title="")
    path = out_dir / "message_hops_put.png"
    fig.savefig(path, dpi=200, bbox_inches="tight")
    if pdf:
        pdf.savefig(fig, bbox_inches="tight")
    plt.close(fig)


def plot_ratio_and_speedup(
    upload_df: pd.DataFrame,
    download_df: pd.DataFrame,
    out_dir: Path,
    pdf: Optional[PdfPages],
) -> None:
    fig, axes = plt.subplots(2, 2, figsize=(12, 10), constrained_layout=True)
    default_payload = 1024
    batch = 1

    up = upload_df[
        (upload_df.get("payload_size") == default_payload) & (upload_df.get("batch_size") == batch)
    ]
    su = summarize_group(up, "latency_ms", ["node_count", "system"]) if not up.empty else pd.DataFrame()
    if not su.empty:
        pv = su.pivot(index="node_count", columns="system", values="median")
        if "our_system" in pv.columns and "swarm" in pv.columns:
            ratio = pv["our_system"] / pv["swarm"].replace(0, np.nan)
            ratio.plot(kind="bar", ax=axes[0, 0], color="#e74c3c", legend=False)
            axes[0, 0].axhline(1.0, color="black", linestyle="--", linewidth=1)
            axes[0, 0].set_title("Latency ratio vn-IPFS / Swarm (upload)\n>1 means vn-IPFS slower")
            axes[0, 0].set_ylabel("Ratio")
            speedup = pv["swarm"] / pv["our_system"].replace(0, np.nan)
            speedup.plot(kind="bar", ax=axes[0, 1], color="#9b59b6", legend=False)
            axes[0, 1].axhline(1.0, color="black", linestyle="--", linewidth=1)
            axes[0, 1].set_title("Speedup: Swarm / vn-IPFS (upload)\n>1 means vn-IPFS faster")
            axes[0, 1].set_ylabel("Ratio")

    down = download_df[
        (download_df.get("payload_size") == default_payload)
        & (download_df.get("cache_mode") == "cold")
    ]
    sd = summarize_group(down, "total_ms", ["node_count", "system"]) if not down.empty else pd.DataFrame()
    if not sd.empty:
        pv = sd.pivot(index="node_count", columns="system", values="median")
        if "our_system" in pv.columns and "swarm" in pv.columns:
            ratio = pv["our_system"] / pv["swarm"].replace(0, np.nan)
            ratio.plot(kind="bar", ax=axes[1, 0], color="#e74c3c", legend=False)
            axes[1, 0].axhline(1.0, color="black", linestyle="--", linewidth=1)
            axes[1, 0].set_title("Latency ratio vn-IPFS / Swarm (download cold)")
            axes[1, 0].set_ylabel("Ratio")
            speedup = pv["swarm"] / pv["our_system"].replace(0, np.nan)
            speedup.plot(kind="bar", ax=axes[1, 1], color="#9b59b6", legend=False)
            axes[1, 1].axhline(1.0, color="black", linestyle="--", linewidth=1)
            axes[1, 1].set_title("Speedup: Swarm / vn-IPFS (download cold)\n>1 means vn-IPFS faster")
            axes[1, 1].set_ylabel("Ratio")

    path = out_dir / "ratio_speedup.png"
    fig.savefig(path, dpi=200, bbox_inches="tight")
    if pdf:
        pdf.savefig(fig, bbox_inches="tight")
    plt.close(fig)


def plot_heatmap_download_payload_n(download_df: pd.DataFrame, out_dir: Path, pdf: Optional[PdfPages]) -> None:
    cold = download_df[download_df.get("cache_mode") == "cold"].copy()
    if cold.empty or "total_ms" not in cold.columns:
        return
    s = summarize_group(cold, "total_ms", ["payload_size", "node_count", "system"])
    if s.empty:
        return
    fig, axes = plt.subplots(1, 2, figsize=(12, 5), constrained_layout=True)
    for ax, sys in zip(axes, SYSTEM_ORDER):
        sub = s[s["system"] == sys]
        if sub.empty:
            ax.set_visible(False)
            continue
        pv = sub.pivot(index="payload_size", columns="node_count", values="median")
        sns.heatmap(pv, annot=True, fmt=".2f", cmap="mako", ax=ax, cbar_kws={"label": "ms"})
        ax.set_title(f"Cold download median total_ms — {_display_system(sys)}")
        ax.set_xlabel("N")
        ax.set_ylabel("payload_size (B)")
    path = out_dir / "heatmap_download_cold_latency.png"
    fig.savefig(path, dpi=200, bbox_inches="tight")
    if pdf:
        pdf.savefig(fig, bbox_inches="tight")
    plt.close(fig)


def plot_heatmap_payload_batch(upload_df: pd.DataFrame, out_dir: Path, pdf: Optional[PdfPages]) -> None:
    if upload_df.empty:
        return
    s = summarize_group(upload_df, "latency_ms", ["payload_size", "batch_size", "system"])
    if s.empty:
        return
    fig, axes = plt.subplots(1, 2, figsize=(12, 5), constrained_layout=True)
    for ax, sys in zip(axes, SYSTEM_ORDER):
        sub = s[s["system"] == sys]
        if sub.empty:
            ax.set_visible(False)
            continue
        pv = sub.pivot(index="payload_size", columns="batch_size", values="median")
        sns.heatmap(pv, annot=True, fmt=".1f", cmap="viridis", ax=ax, cbar_kws={"label": "ms"})
        ax.set_title(f"Upload median latency — {_display_system(sys)}")
        ax.set_xlabel("batch_size")
        ax.set_ylabel("payload_size (B)")
    path = out_dir / "heatmap_upload_latency.png"
    fig.savefig(path, dpi=200, bbox_inches="tight")
    if pdf:
        pdf.savefig(fig, bbox_inches="tight")
    plt.close(fig)


def build_metrics_bundle(
    upload_df: pd.DataFrame,
    download_df: pd.DataFrame,
    lookup_df: pd.DataFrame,
    repl_df: pd.DataFrame,
    net_df: pd.DataFrame,
    repair_df: pd.DataFrame,
) -> Dict[str, pd.DataFrame]:
    return {
        "upload_raw": upload_df,
        "download_raw": download_df,
        "lookup_complexity": lookup_df,
        "replication": repl_df,
        "upload_network_bytes": net_df,
        "repair_time": repair_df,
    }


def export_combined_csv(bundle: Dict[str, pd.DataFrame], out_dir: Path) -> None:
    summary_rows: List[Dict[str, Any]] = []
    long_parts: List[pd.DataFrame] = []
    for name, df in bundle.items():
        if df is None or df.empty:
            continue
        tmp = df.copy()
        tmp.insert(0, "source_table", name)
        long_parts.append(tmp)
        p = out_dir / f"export_{name}.csv"
        tmp.to_csv(p, index=False)
        summary_rows.append({"table": name, "rows": len(tmp), "file": str(p.name)})
    pd.DataFrame(summary_rows).to_csv(out_dir / "export_manifest.csv", index=False)
    if long_parts:
        pd.concat(long_parts, ignore_index=True, sort=False).to_csv(
            out_dir / "all_metrics_long.csv", index=False
        )


def write_report(
    out_dir: Path,
    upload_df: pd.DataFrame,
    download_df: pd.DataFrame,
    lookup_df: pd.DataFrame,
    repl_df: pd.DataFrame,
    net_df: pd.DataFrame,
    repair_df: pd.DataFrame,
) -> None:
    lines: List[str] = []
    lines.append("# Swarm comparison report")
    lines.append("")
    lines.append(f"Generated: {datetime.now(timezone.utc).isoformat()}Z")
    lines.append("")

    def stat_block(df: pd.DataFrame, col: str, label: str) -> None:
        if df.empty or col not in df.columns:
            lines.append(f"### {label}\n\n_No data._\n")
            return
        lines.append(f"### {label}\n")
        for sys in SYSTEM_ORDER:
            sub = df[df["system"] == sys][col].dropna()
            if sub.empty:
                continue
            lines.append(f"- **{_display_system(sys)}**: n={len(sub)}, mean={sub.mean():.4g}, median={sub.median():.4g}, std={sub.std():.4g}")
        lines.append("")

    stat_block(upload_df, "latency_ms", "Upload latency (all conditions)")
    stat_block(
        download_df[download_df.get("cache_mode") == "cold"],
        "total_ms",
        "Download total_ms (cold)",
    )
    stat_block(
        download_df[download_df.get("cache_mode") == "cold"],
        "ttfb_ms",
        "Download ttfb_ms / lookup proxy (cold)",
    )

    lines.append("## Key findings\n")
    findings: List[str] = []
    default_p, batch = 1024, 1
    up = upload_df[
        (upload_df.get("payload_size") == default_p) & (upload_df.get("batch_size") == batch)
    ]
    if not up.empty:
        s = summarize_group(up, "latency_ms", ["node_count", "system"])
        for n in sorted(s["node_count"].dropna().unique()):
            sub = s[s["node_count"] == n]
            o = sub[sub["system"] == "our_system"]["median"]
            w = sub[sub["system"] == "swarm"]["median"]
            if len(o) and len(w) and w.iloc[0] > 0:
                r = float(o.iloc[0] / w.iloc[0])
                if r > 1.0:
                    findings.append(
                        f"At N={int(n)}, **vn-IPFS upload median latency is ~{r:.2f}× Swarm** (payload {default_p} B, batch {batch})."
                    )
                elif r < 1.0:
                    findings.append(
                        f"At N={int(n)}, **Swarm upload median latency is ~{1/r:.2f}× vn-IPFS** (payload {default_p} B, batch {batch})."
                    )

    down = download_df[
        (download_df.get("payload_size") == default_p)
        & (download_df.get("cache_mode") == "cold")
    ]
    if not down.empty:
        s = summarize_group(down, "total_ms", ["node_count", "system"])
        for n in sorted(s["node_count"].dropna().unique()):
            sub = s[s["node_count"] == n]
            o = sub[sub["system"] == "our_system"]["median"]
            w = sub[sub["system"] == "swarm"]["median"]
            if len(o) and len(w) and w.iloc[0] > 0:
                r = float(o.iloc[0] / w.iloc[0])
                if r < 1.0:
                    findings.append(
                        f"At N={int(n)}, **vn-IPFS cold download is faster** (median total_ms ~{1/r:.2f}× lower than Swarm)."
                    )
                elif r > 1.0:
                    findings.append(
                        f"At N={int(n)}, **Swarm cold download is faster** (median total_ms ~{r:.2f}× lower than vn-IPFS)."
                    )

    if not lookup_df.empty:
        findings.append("Lookup hop measurements exist for put operations; **lookup hop rows are often N/A** in the bundled `lookup_complexity_results.csv`.")
    else:
        findings.append("**Lookup hop data** is sparse or missing after filtering N/A.")

    if repl_df.empty or repl_df["system"].nunique() < 2:
        findings.append("**Replication timing comparison** is incomplete where Swarm rows are SKIP/TIMEOUT.")

    if not net_df.empty:
        findings.append(
            "**Byte-level overhead** (upload_network_bytes) shows orders-of-magnitude differences vs payload; interpret as stack/traffic accounting, not pure DHT messages."
        )

    for f in findings:
        lines.append(f"- {f}")
    lines.append("")

    lines.append("## Complexity (log–log regression)\n")
    lines.append(
        "_Note: O(log N) is not a power law; a log–log slope near 0 suggests weak scaling with N, while slope ~1 suggests linear in N._\n"
    )
    for label, df, col, filt in [
        (
            "Upload latency",
            upload_df[
                (upload_df.get("payload_size") == default_p) & (upload_df.get("batch_size") == batch)
            ],
            "latency_ms",
            None,
        ),
        (
            "Download cold total_ms",
            download_df[
                (download_df.get("payload_size") == default_p)
                & (download_df.get("cache_mode") == "cold")
            ],
            "total_ms",
            None,
        ),
    ]:
        if df.empty or col not in df.columns:
            continue
        s = summarize_group(df, col, ["node_count", "system"])
        for sys in SYSTEM_ORDER:
            sub = s[s["system"] == sys]
            xs = sub["node_count"].astype(float).values
            ys = sub["median"].astype(float).values
            slope, _icept = power_law_exponent(xs, ys)
            if math.isnan(slope):
                lines.append(
                    f"- **{label} / {_display_system(sys)}**: insufficient distinct N for log–log regression."
                )
            else:
                lines.append(
                    f"- **{label} / {_display_system(sys)}**: log–log slope β≈{slope:.3f} (approx. T ∝ N^β across observed N)."
                )
    lines.append("")

    lines.append("## Recommendations\n")
    lines.append(
        "- Collect **non-N/A lookup hop counts** for cold key/CID resolution across N to strengthen O(log N) claims.\n"
    )
    lines.append(
        "- Add **explicit message counters** (token routing vs provider announcements) if the paper requires protocol-level overhead.\n"
    )
    lines.append(
        "- **Complete Swarm replication** runs or document SKIP/TIMEOUT causes for fair comparison.\n"
    )
    lines.append(
        "- Tag `upload_network_bytes.csv` with **node_count** column to avoid heuristic splitting for multi-run files.\n"
    )

    (out_dir / "swarm_comparison_report.md").write_text("\n".join(lines), encoding="utf-8")


def try_plotly_dashboard(
    upload_df: pd.DataFrame,
    download_df: pd.DataFrame,
    out_dir: Path,
) -> None:
    try:
        import plotly.graph_objects as go
        from plotly.subplots import make_subplots
    except ImportError:
        return
    if upload_df.empty and download_df.empty:
        return
    fig = make_subplots(rows=1, cols=2, subplot_titles=("Upload latency", "Download cold total_ms"))
    default_p, batch = 1024, 1
    up = upload_df[
        (upload_df.get("payload_size") == default_p) & (upload_df.get("batch_size") == batch)
    ]
    if not up.empty:
        s = summarize_group(up, "latency_ms", ["node_count", "system"])
        for sys in SYSTEM_ORDER:
            sub = s[s["system"] == sys]
            if sub.empty:
                continue
            fig.add_trace(
                go.Bar(
                    x=sub["node_count"],
                    y=sub["median"],
                    name=_display_system(sys),
                    legendgroup="up",
                ),
                row=1,
                col=1,
            )
    down = download_df[
        (download_df.get("payload_size") == default_p)
        & (download_df.get("cache_mode") == "cold")
    ]
    if not down.empty:
        s = summarize_group(down, "total_ms", ["node_count", "system"])
        for sys in SYSTEM_ORDER:
            sub = s[s["system"] == sys]
            if sub.empty:
                continue
            fig.add_trace(
                go.Bar(
                    x=sub["node_count"],
                    y=sub["median"],
                    name=_display_system(sys),
                    legendgroup="down",
                    showlegend=False,
                ),
                row=1,
                col=2,
            )
    fig.update_layout(barmode="group", height=480, title_text="vn-IPFS vs Swarm (interactive)")
    fig.write_html(out_dir / "dashboard.html", include_plotlyjs="cdn")


def main() -> int:
    ap = argparse.ArgumentParser(description="Swarm vs vn-IPFS comparison plots (plan 9.4)")
    ap.add_argument(
        "--input",
        type=Path,
        default=Path("full_comparison"),
        help="Directory containing full_comparison CSVs",
    )
    ap.add_argument(
        "--out",
        type=Path,
        default=None,
        help="Output directory (default: <input>/analysis_plots)",
    )
    ap.add_argument("--no-pdf", action="store_true", help="Skip multi-page PDF")
    ap.add_argument("--no-html", action="store_true", help="Skip Plotly HTML dashboard")
    args = ap.parse_args()
    results_dir = args.input.resolve()
    if not results_dir.is_dir():
        print(f"Input directory not found: {results_dir}", file=sys.stderr)
        return 1
    out_dir = (args.out or (results_dir / "analysis_plots")).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)

    upload_df = load_upload_tables(results_dir)
    download_df = load_download_tables(results_dir)
    lookup_df = load_lookup_complexity(results_dir)
    repl_df = load_replication(results_dir)
    dist_df = load_replication_distribution(results_dir)
    repair_df = load_repair_time(results_dir)
    node_sizes = sorted(upload_df["node_count"].dropna().unique().astype(int).tolist()) if not upload_df.empty else []
    net_df = load_network_bytes(results_dir, node_sizes)

    bundle = build_metrics_bundle(
        upload_df, download_df, lookup_df, repl_df, net_df, repair_df
    )
    export_combined_csv(bundle, out_dir)

    meta = {
        "input_dir": str(results_dir),
        "out_dir": str(out_dir),
        "node_sizes": node_sizes,
        "rows": {k: len(v) for k, v in bundle.items()},
    }
    (out_dir / "run_meta.json").write_text(json.dumps(meta, indent=2), encoding="utf-8")

    pdf_path = out_dir / "figures_all.pdf"
    pdf_ctx = (
        PdfPages(pdf_path)
        if not args.no_pdf
        else None
    )
    try:
        plot_latency_bars(upload_df, download_df, out_dir, pdf_ctx)
        plot_throughput(upload_df, download_df, out_dir, pdf_ctx)
        plot_scaling_loglog(upload_df, download_df, out_dir, pdf_ctx)
        plot_replication(repl_df, out_dir, pdf_ctx)
        plot_replication_distribution(dist_df, out_dir, pdf_ctx)
        plot_overhead_bytes(net_df, out_dir, pdf_ctx)
        plot_lookup_hops(lookup_df, out_dir, pdf_ctx)
        plot_ratio_and_speedup(upload_df, download_df, out_dir, pdf_ctx)
        plot_heatmap_payload_batch(upload_df, out_dir, pdf_ctx)
        plot_heatmap_download_payload_n(download_df, out_dir, pdf_ctx)
    finally:
        if pdf_ctx is not None:
            pdf_ctx.close()

    write_report(out_dir, upload_df, download_df, lookup_df, repl_df, net_df, repair_df)
    if not args.no_html:
        try_plotly_dashboard(upload_df, download_df, out_dir)

    print(f"Wrote outputs under {out_dir}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
