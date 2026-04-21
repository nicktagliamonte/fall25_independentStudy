#!/usr/bin/env python3
# Purpose: Plot catalog_growth CSV (upload/download vs files_on_network): coerce errors to missing,
#          drop transient spikes (Hampel + neighbor midpoint), fill gaps with linear interpolation.
#          Optional --fit: least-squares line on cleaned points per series (same color, dashed).
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


def plot_catalog_growth(
    series_by_label: dict[str, pd.DataFrame],
    out_path: Path,
    node_count: int | None,
    *,
    show_fit: bool = False,
) -> None:
    fig, axes = plt.subplots(2, 1, figsize=(9, 6), sharex=True, constrained_layout=True)
    colors = plt.rcParams["axes.prop_cycle"].by_key()["color"]
    labels = list(series_by_label.keys())
    multi = len(labels) > 1

    for ax, col, ylabel in zip(
        axes,
        ("upload_ms", "download_total_ms"),
        ("Upload (ms)", "Download (ms)"),
    ):
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
        nargs="?",
        default=Path("test_results/catalog_growth_512/catalog_growth_n50.csv"),
        help="Input CSV path",
    )
    ap.add_argument(
        "-o",
        "--output",
        type=Path,
        default=None,
        help="Output PNG (default: <csv_stem>_latency.png beside the CSV)",
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
    csv_path = args.csv.resolve()
    if not csv_path.is_file():
        print(f"Error: not a file: {csv_path}", file=sys.stderr)
        return 1

    out = args.output
    if out is None:
        out = csv_path.parent / f"{csv_path.stem}_latency.png"
    else:
        out = out.resolve()

    win = max(5, int(args.window) | 1)

    series_map, node_count = load_series_by_system(
        csv_path,
        window=win,
        n_sigma=max(1.0, float(args.n_sigma)),
        min_abs_ms=max(0.5, float(args.min_abs_ms)),
        system_filter=args.system,
    )
    plot_catalog_growth(series_map, out, node_count, show_fit=bool(args.fit))
    print(f"Wrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
