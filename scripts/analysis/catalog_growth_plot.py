#!/usr/bin/env python3
# Purpose: Plot catalog_growth CSV (upload/download vs files_on_network): coerce errors to missing,
#          drop transient spikes (Hampel + neighbor midpoint), fill gaps with linear interpolation.
#          Optional --fit: least-squares line on cleaned points per series (same color, dashed).
#          Two CSVs + --output-pair: same y-axis bounds on upload and download for side-by-side comparison.
#          Tolerates spreadsheet export encoding (+AF8- for _) in headers/cells and trailing summary rows.

from __future__ import annotations

import argparse
import sys
from pathlib import Path

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


def _normalize_export_token(s: str) -> str:
    """Decode common spreadsheet/CSV exports where underscores appear as the literal substring +AF8-."""
    return str(s).replace("+AF8-", "_")


def _normalize_catalog_columns(df: pd.DataFrame) -> pd.DataFrame:
    out = df.copy()
    out.columns = [_normalize_export_token(c) for c in out.columns]
    return out


def _data_rows_only(df: pd.DataFrame) -> pd.DataFrame:
    """Keep rows with integer catalog size; drops footer lines (mean/median/...) and blanks."""
    if "files_on_network" not in df.columns:
        return df
    fon = pd.to_numeric(df["files_on_network"], errors="coerce")
    mask = fon.notna() & (fon >= 1) & (fon == fon.round())
    out = df.loc[mask].copy()
    out["files_on_network"] = fon.loc[mask].astype(int)
    return out


def _to_ms_series(raw: pd.Series) -> pd.Series:
    s = raw.astype(str).str.strip()
    s = s.replace({"ERROR": np.nan, "error": np.nan, "": np.nan, "N/A": np.nan})
    return pd.to_numeric(s, errors="coerce")


def _hampel_mask(
    x: np.ndarray,
    window: int,
    n_sigma: float,
    min_abs_delta_ms: float,
) -> np.ndarray:
    """Centered Hampel: flag i when |x[i] - median(window)| > max(n_sigma * 1.4826 * MAD, min_abs_delta_ms)."""
    n = len(x)
    half = window // 2
    out = np.zeros(n, dtype=bool)
    min_w = max(5, window // 2)
    for i in range(n):
        if not np.isfinite(x[i]):
            continue
        lo = max(0, i - half)
        hi = min(n, i + half + 1)
        w = x[lo:hi]
        w = w[np.isfinite(w)]
        if w.size < min_w:
            continue
        med = float(np.median(w))
        mad = float(np.median(np.abs(w - med)))
        sigma = 1.4826 * mad if mad > 1e-9 else 1e-9
        thresh = max(float(n_sigma) * sigma, float(min_abs_delta_ms))
        if abs(float(x[i]) - med) > thresh:
            out[i] = True
    return out


def _isolated_spike_mask(
    x: np.ndarray,
    min_gap_ms: float,
    span_ratio: float,
) -> np.ndarray:
    """Single-index spikes: far from average of immediate neighbors (host jitter, etc.)."""
    n = len(x)
    out = np.zeros(n, dtype=bool)
    for i in range(1, n - 1):
        a, b, c = x[i - 1], x[i], x[i + 1]
        if not (np.isfinite(a) and np.isfinite(b) and np.isfinite(c)):
            continue
        mid = 0.5 * (float(a) + float(c))
        gap = abs(float(b) - mid)
        span = abs(float(c) - float(a)) + 1e-6
        if gap >= min_gap_ms and gap > span_ratio * span:
            out[i] = True
    return out


def _outlier_mask_series(s: pd.Series, window: int, n_sigma: float, min_abs_ms: float) -> pd.Series:
    x = s.astype(float).to_numpy()
    m1 = _hampel_mask(x, window=window, n_sigma=n_sigma, min_abs_delta_ms=min_abs_ms)
    m2 = _isolated_spike_mask(x, min_gap_ms=max(min_abs_ms * 1.2, 6.0), span_ratio=2.5)
    return pd.Series(m1 | m2, index=s.index)


def _clean_column(
    s: pd.Series,
    window: int,
    n_sigma: float,
    min_abs_ms: float,
) -> pd.Series:
    x = s.astype(float)
    invalid = (x.isna() | ~np.isfinite(x)).to_numpy()

    def pass_fill(ser: pd.Series, inv: np.ndarray) -> pd.Series:
        outliers = _outlier_mask_series(ser.where(~inv), window, n_sigma, min_abs_ms).to_numpy()
        bad = inv | outliers
        y = ser.to_numpy(dtype=float).copy()
        y[bad] = np.nan
        out = pd.Series(y, index=ser.index)
        out = out.interpolate(method="linear", limit_direction="both")
        return out.bfill().ffill()

    once = pass_fill(x, invalid)
    return pass_fill(once, np.zeros(len(once), dtype=bool))


SYSTEM_LABEL = {"our_system": "vn-IPFS", "swarm": "Swarm"}


def _clean_frame(
    df: pd.DataFrame,
    window: int,
    n_sigma: float,
    min_abs_ms: float,
) -> pd.DataFrame:
    df = df.sort_values("files_on_network").reset_index(drop=True)
    raw_u = _to_ms_series(df["upload_ms"])
    raw_d = _to_ms_series(df["download_total_ms"])
    u_rep = _clean_column(raw_u, window, n_sigma, min_abs_ms)
    d_rep = _clean_column(raw_d, window, n_sigma, min_abs_ms)
    return pd.DataFrame(
        {
            "files_on_network": df["files_on_network"].astype(int),
            "upload_ms": u_rep,
            "download_total_ms": d_rep,
        }
    )


def load_series_by_system(
    path: Path,
    window: int,
    n_sigma: float,
    min_abs_ms: float,
    system_filter: str | None,
) -> tuple[dict[str, pd.DataFrame], int | None]:
    df = _normalize_catalog_columns(pd.read_csv(path))
    df = _data_rows_only(df)
    if "system" in df.columns:
        df["system"] = df["system"].map(
            lambda v: _normalize_export_token(v) if pd.notna(v) and str(v).strip() != "" else np.nan
        )

    need = ("files_on_network", "upload_ms", "download_total_ms")
    for c in need:
        if c not in df.columns:
            print(f"Error: missing column {c!r} in {path}", file=sys.stderr)
            sys.exit(1)

    node_count: int | None = None
    if "node_count" in df.columns and len(df.index):
        try:
            node_count = int(df["node_count"].iloc[0])
        except (TypeError, ValueError):
            node_count = None

    if system_filter:
        sf = _normalize_export_token(system_filter)
        df = df[df["system"].astype(str) == sf]
        if df.empty:
            print(f"Error: no rows for system={system_filter!r}", file=sys.stderr)
            sys.exit(1)
        return {_label_for_system(sf): _clean_frame(df, window, n_sigma, min_abs_ms)}, node_count

    if "system" not in df.columns:
        return {"series": _clean_frame(df, window, n_sigma, min_abs_ms)}, node_count

    systems = sorted(df["system"].dropna().astype(str).unique())
    out: dict[str, pd.DataFrame] = {}
    for sys_name in systems:
        sub = df[df["system"].astype(str) == sys_name]
        if sub.empty:
            continue
        out[_label_for_system(sys_name)] = _clean_frame(sub, window, n_sigma, min_abs_ms)
    if not out:
        print("Error: no data rows after grouping by system", file=sys.stderr)
        sys.exit(1)
    return out, node_count


def _label_for_system(sys: str) -> str:
    return SYSTEM_LABEL.get(sys, sys)


def _plot_one_panel_data(
    ax: plt.Axes,
    col: str,
    series_by_label: dict[str, pd.DataFrame],
    colors: list,
    show_fit: bool,
    multi: bool,
) -> None:
    for i, (lab, plot_df) in enumerate(series_by_label.items()):
        x = plot_df["files_on_network"].to_numpy(dtype=float)
        y = plot_df[col].to_numpy(dtype=float)
        c = colors[i % len(colors)]
        ax.plot(x, y, color=c, linewidth=1.35, label=lab if multi else None)
        if show_fit:
            mask = np.isfinite(x) & np.isfinite(y)
            if int(np.sum(mask)) >= 2:
                coef = np.polyfit(x[mask], y[mask], 1)
                x_line = np.linspace(float(np.min(x[mask])), float(np.max(x[mask])), 50)
                y_line = coef[0] * x_line + coef[1]
                ax.plot(
                    x_line,
                    y_line,
                    color=c,
                    linestyle="--",
                    linewidth=1.1,
                    alpha=0.85,
                )


def _combined_y_limits(
    series_maps: list[dict[str, pd.DataFrame]],
    show_fit: bool,
) -> tuple[tuple[float, float], tuple[float, float]]:
    """Union of y-ranges (data + fit lines) for upload and download across multiple loaded series maps."""
    cols = ("upload_ms", "download_total_ms")
    y_min = [np.inf, np.inf]
    y_max = [-np.inf, -np.inf]
    for smap in series_maps:
        for col_idx, col in enumerate(cols):
            for plot_df in smap.values():
                x = plot_df["files_on_network"].to_numpy(dtype=float)
                y = plot_df[col].to_numpy(dtype=float)
                m = np.isfinite(x) & np.isfinite(y)
                if np.any(m):
                    y_min[col_idx] = min(y_min[col_idx], float(np.min(y[m])))
                    y_max[col_idx] = max(y_max[col_idx], float(np.max(y[m])))
                if show_fit and int(np.sum(m)) >= 2:
                    coef = np.polyfit(x[m], y[m], 1)
                    x_line = np.linspace(float(np.min(x[m])), float(np.max(x[m])), 50)
                    y_line = coef[0] * x_line + coef[1]
                    y_min[col_idx] = min(y_min[col_idx], float(np.min(y_line)))
                    y_max[col_idx] = max(y_max[col_idx], float(np.max(y_line)))
    out: list[tuple[float, float]] = []
    for j in range(2):
        lo, hi = y_min[j], y_max[j]
        if not (np.isfinite(lo) and np.isfinite(hi)) or lo >= hi:
            out.append((0.0, 1.0))
        else:
            pad = 0.05 * (hi - lo) if (hi - lo) > 1e-9 else 1.0
            out.append((lo - pad, hi + pad))
    return (out[0], out[1])


def plot_catalog_growth(
    series_by_label: dict[str, pd.DataFrame],
    out_path: Path,
    node_count: int | None,
    *,
    show_fit: bool = False,
    ylim_upload: tuple[float, float] | None = None,
    ylim_download: tuple[float, float] | None = None,
) -> None:
    fig, axes = plt.subplots(2, 1, figsize=(9, 6), sharex=True, constrained_layout=True)
    colors = plt.rcParams["axes.prop_cycle"].by_key()["color"]
    labels = list(series_by_label.keys())
    multi = len(labels) > 1

    ylims = (ylim_upload, ylim_download)
    for ax, col, ylabel, ylim_fix in zip(
        axes,
        ("upload_ms", "download_total_ms"),
        ("Upload (ms)", "Download (ms)"),
        ylims,
    ):
        _plot_one_panel_data(ax, col, series_by_label, colors, show_fit, multi)
        if ylim_fix is not None:
            ax.set_ylim(ylim_fix)
        ax.set_ylabel(ylabel)
        ax.grid(True, axis="y", alpha=0.4)
        if multi:
            ax.legend(loc="upper left", fontsize=8)

    axes[-1].set_xlabel("Objects on network")
    sub = f" (N = {node_count})" if node_count is not None else ""
    fig.suptitle(f"Latency vs catalog size{sub}", fontsize=12)
    fig.savefig(out_path, dpi=150)
    plt.close(fig)


def main() -> int:
    ap = argparse.ArgumentParser(
        description="Plot catalog_growth CSV: spike removal and linear interpolation; single clean line chart.",
    )
    ap.add_argument(
        "csv",
        type=Path,
        nargs="*",
        default=[Path("test_results/catalog_growth_512/catalog_growth_n50.csv")],
        help="One or two input CSV paths; two require --output-pair for aligned y-axes",
    )
    ap.add_argument(
        "-o",
        "--output",
        type=Path,
        default=None,
        help="Output PNG when a single input CSV (default: <csv_stem>_latency.png beside the CSV)",
    )
    ap.add_argument(
        "--output-pair",
        nargs=2,
        type=Path,
        metavar=("OUT1", "OUT2"),
        default=None,
        help="Two PNG paths when two input CSVs: both figures share upload/download y-limits",
    )
    ap.add_argument(
        "--window",
        type=int,
        default=21,
        help="Hampel window (odd integer; centered median/MAD)",
    )
    ap.add_argument(
        "--n-sigma",
        type=float,
        default=3.0,
        help="Hampel threshold in local robust std units (lower = more aggressive)",
    )
    ap.add_argument(
        "--min-abs-ms",
        type=float,
        default=5.0,
        help="Minimum deviation from local median (ms) to count as spike",
    )
    ap.add_argument(
        "--system",
        type=str,
        default=None,
        help="CSV system column value to plot only (e.g. our_system, swarm); default: all series",
    )
    ap.add_argument(
        "--fit",
        action="store_true",
        help="Draw least-squares line (degree 1) on cleaned points per series (dashed, same color)",
    )
    args = ap.parse_args()
    paths = [p.resolve() for p in args.csv]
    for csv_path in paths:
        if not csv_path.is_file():
            print(f"Error: not a file: {csv_path}", file=sys.stderr)
            return 1

    if len(paths) not in (1, 2):
        print("Error: provide one or two CSV paths", file=sys.stderr)
        return 1
    if len(paths) == 2 and args.output_pair is None:
        print("Error: two CSVs require --output-pair OUT1 OUT2 (shared y-axis limits)", file=sys.stderr)
        return 1
    if len(paths) == 1 and args.output_pair is not None:
        print("Error: --output-pair is only for two input CSVs", file=sys.stderr)
        return 1

    win = max(5, int(args.window) | 1)
    win_args = dict(
        window=win,
        n_sigma=max(1.0, float(args.n_sigma)),
        min_abs_ms=max(0.5, float(args.min_abs_ms)),
        system_filter=args.system,
    )
    show_fit = bool(args.fit)

    if len(paths) == 1:
        csv_path = paths[0]
        out = args.output
        if out is None:
            out = csv_path.parent / f"{csv_path.stem}_latency.png"
        else:
            out = out.resolve()
        series_map, node_count = load_series_by_system(csv_path, **win_args)
        plot_catalog_growth(series_map, out, node_count, show_fit=show_fit)
        print(f"Wrote {out}")
        return 0

    s1, nc1 = load_series_by_system(paths[0], **win_args)
    s2, nc2 = load_series_by_system(paths[1], **win_args)
    yl_u, yl_d = _combined_y_limits([s1, s2], show_fit=show_fit)
    out1, out2 = (p.resolve() for p in args.output_pair)
    plot_catalog_growth(s1, out1, nc1, show_fit=show_fit, ylim_upload=yl_u, ylim_download=yl_d)
    plot_catalog_growth(s2, out2, nc2, show_fit=show_fit, ylim_upload=yl_u, ylim_download=yl_d)
    print(f"Wrote {out1}")
    print(f"Wrote {out2} (y-limits match {out1.name})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
