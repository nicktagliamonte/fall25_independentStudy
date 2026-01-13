#!/usr/bin/env python3
"""
Generate Panel A: Convergence Time vs Network Size with error bars.
Shows mean convergence time ± 95% CI across multiple runs.
"""

import json
import sys
import argparse
import math
from pathlib import Path
from collections import defaultdict

try:
    import matplotlib
    matplotlib.use('Agg')
    import matplotlib.pyplot as plt
    import numpy as np
    HAS_MATPLOTLIB = True
except ImportError as e:
    HAS_MATPLOTLIB = False
    print(f"WARNING: matplotlib/numpy not available: {e}", file=sys.stderr)


def calculate_convergence_time(run_dir):
    """
    Calculate convergence time from time-series metrics.
    Returns convergence time in seconds, or None if data insufficient.
    """
    metrics_file = Path(run_dir) / 'raw' / 'metrics.jsonl'
    if not metrics_file.exists():
        return None
    
    time_series = defaultdict(list)
    with open(metrics_file, 'r') as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                data = json.loads(line)
                node_id = data.get('node_id')
                ts = data.get('ts', 0)
                if node_id is not None and ts > 0:
                    time_series[node_id].append({
                        'ts': ts,
                        'dials_attempted': data.get('dials_attempted', 0),
                        'dials_succeeded': data.get('dials_succeeded', 0),
                    })
            except json.JSONDecodeError:
                continue
    
    if not time_series:
        return None
    
    # Get all unique timestamps
    all_ts = set()
    for node_data in time_series.values():
        for entry in node_data:
            all_ts.add(entry['ts'])
    
    sorted_ts = sorted(all_ts)
    if len(sorted_ts) < 2:
        return None
    
    # Aggregate total dials at each timestamp
    convergence_data = []
    for ts in sorted_ts:
        total_dials = 0
        for node_data in time_series.values():
            for entry in reversed(node_data):
                if entry['ts'] <= ts:
                    total_dials += entry['dials_attempted']
                    break
        convergence_data.append({'ts': ts, 'total_dials': total_dials})
    
    if len(convergence_data) < 3:
        return None
    
    first_ts = convergence_data[0]['ts']
    last_ts = convergence_data[-1]['ts']
    total_time = last_ts - first_ts
    
    # Find convergence point: when dial rate stabilizes
    convergence_window = max(3, len(convergence_data) // 5)
    recent = convergence_data[-convergence_window:]
    
    if len(recent) < 2:
        return total_time
    
    final_dials = convergence_data[-1]['total_dials']
    convergence_threshold = final_dials * 0.05  # 5% change threshold
    
    # Find when convergence occurred (low change rate)
    convergence_ts = None
    for i in range(len(convergence_data) - convergence_window, len(convergence_data)):
        if i > 0:
            change = abs(convergence_data[i]['total_dials'] - convergence_data[i-1]['total_dials'])
            if change < convergence_threshold:
                convergence_ts = convergence_data[i]['ts']
                break
    
    convergence_time = (convergence_ts - first_ts) if convergence_ts else total_time
    return convergence_time


def calculate_ci(values, confidence=0.95):
    """
    Calculate mean and confidence interval.
    Returns (mean, ci_lower, ci_upper, std).
    """
    if not values:
        return None, None, None, None
    
    n = len(values)
    if n == 1:
        return values[0], values[0], values[0], 0.0
    
    mean = sum(values) / n
    variance = sum((x - mean) ** 2 for x in values) / (n - 1) if n > 1 else 0.0
    std = math.sqrt(variance)
    
    # t-distribution critical value (approximate for n >= 3, use 1.96 for large n)
    if n >= 30:
        t_critical = 1.96  # z-score for 95% CI
    elif n >= 3:
        # Approximate t-values for 95% CI
        t_table = {3: 4.303, 4: 3.182, 5: 2.776, 6: 2.571, 7: 2.447, 8: 2.365,
                   9: 2.306, 10: 2.262, 15: 2.131, 20: 2.086, 25: 2.060}
        t_critical = t_table.get(n, 2.0)
    else:
        t_critical = 2.0
    
    margin = t_critical * (std / math.sqrt(n))
    ci_lower = mean - margin
    ci_upper = mean + margin
    
    return mean, ci_lower, ci_upper, std


def load_convergence_data(results_dir):
    """
    Load convergence times for all runs, grouped by node count.
    Returns dict: {node_count: [convergence_times...]}
    """
    runs_file = Path(results_dir) / 'runs.txt'
    if not runs_file.exists():
        print(f"ERROR: runs.txt not found in {results_dir}", file=sys.stderr)
        return {}
    
    convergence_by_n = defaultdict(list)
    
    with open(runs_file, 'r') as f:
        for line in f:
            line = line.strip()
            if not line or '|' not in line:
                continue
            
            n_str, run_id = line.split('|', 1)
            n = int(n_str)
            run_dir = Path(f"artifacts/runs/{run_id}")
            
            convergence_time = calculate_convergence_time(run_dir)
            if convergence_time is not None:
                convergence_by_n[n].append(convergence_time)
            else:
                print(f"WARNING: Could not calculate convergence for N={n}, RUN_ID={run_id}", file=sys.stderr)
    
    return convergence_by_n


def create_convergence_plot(convergence_by_n, output_path):
    """
    Create Panel A: Convergence Time vs Network Size with error bars.
    """
    if not HAS_MATPLOTLIB:
        print("ERROR: matplotlib not available", file=sys.stderr)
        return False
    
    # Sort by node count
    sorted_n = sorted(convergence_by_n.keys())
    if not sorted_n:
        print("ERROR: No convergence data found", file=sys.stderr)
        return False
    
    # Calculate statistics for each node count
    n_values = []
    means = []
    ci_lowers = []
    ci_uppers = []
    stds = []
    counts = []
    
    for n in sorted_n:
        values = convergence_by_n[n]
        if not values:
            continue
        
        mean, ci_lower, ci_upper, std = calculate_ci(values)
        if mean is None:
            continue
        
        n_values.append(n)
        means.append(mean)
        ci_lowers.append(ci_lower)
        ci_uppers.append(ci_upper)
        stds.append(std)
        counts.append(len(values))
    
    if not n_values:
        print("ERROR: No valid data points", file=sys.stderr)
        return False
    
    # Create plot with better layout
    fig, ax = plt.subplots(1, 1, figsize=(9, 7))
    
    # Convert to numpy arrays for easier manipulation
    n_array = np.array(n_values)
    means_array = np.array(means)
    ci_lower_array = np.array(ci_lowers)
    ci_upper_array = np.array(ci_uppers)
    
    # Calculate error bar values (distance from mean)
    yerr_lower = means_array - ci_lower_array
    yerr_upper = ci_upper_array - means_array
    yerr = np.array([yerr_lower, yerr_upper])
    
    # Calculate dynamic y-axis range based on data (with padding)
    y_min = min(ci_lowers) - 2
    y_max = max(ci_uppers) + 2
    # Ensure minimum range for visibility
    if (y_max - y_min) < 5:
        y_center = (y_max + y_min) / 2
        y_min = y_center - 5
        y_max = y_center + 5
    
    # Plot with error bars
    ax.errorbar(n_array, means_array, yerr=yerr, 
                fmt='o-', color='#e67e22', linewidth=2, markersize=8,
                capsize=5, capthick=2, elinewidth=2, label='Mean ± 95% CI')
    
    # Set log scale for x-axis
    ax.set_xscale('log')
    ax.set_xlabel('Number of Nodes', fontsize=12)
    ax.set_ylabel('Convergence Time (seconds)', fontsize=12, labelpad=10)
    ax.set_title('A) Convergence Time', fontsize=14, fontweight='bold', loc='left')
    ax.set_ylim([y_min, y_max])
    ax.grid(alpha=0.2, linestyle='--')
    
    # Add text annotation showing node count and number of runs (clearer labels)
    for i, (n, count) in enumerate(zip(n_values, counts)):
        label_text = f'N={n}\n(n={count})'
        ax.text(n, means[i] + ci_upper_array[i] + (y_max - y_min) * 0.03, label_text, 
                ha='center', va='bottom', fontsize=8, alpha=0.8)
    
    # Add legend
    ax.legend(loc='best', fontsize=10)
    
    # Adjust layout to prevent label cutoff
    fig.subplots_adjust(left=0.15, bottom=0.12, right=0.95, top=0.92)
    plt.savefig(output_path, dpi=300, bbox_inches='tight', pad_inches=0.15)
    print(f"Plot saved to: {output_path}")
    plt.close()
    
    # Print summary statistics
    print("\n" + "=" * 80)
    print("Convergence Time Statistics")
    print("=" * 80)
    print(f"{'N':<8} {'Mean (s)':<12} {'Std (s)':<12} {'95% CI':<25} {'Runs':<8}")
    print("-" * 80)
    for i, n in enumerate(n_values):
        ci_str = f"[{ci_lowers[i]:.1f}, {ci_uppers[i]:.1f}]"
        print(f"{n:<8} {means[i]:<12.2f} {stds[i]:<12.2f} {ci_str:<25} {counts[i]:<8}")
    print("=" * 80 + "\n")
    
    return True


def main():
    parser = argparse.ArgumentParser(description='Generate convergence time plot with error bars')
    parser.add_argument('results_dir', help='Convergence test results directory')
    parser.add_argument('--output', '-o', default=None,
                       help='Output path for plot (default: results_dir/convergence_plot.png)')
    
    args = parser.parse_args()
    
    results_dir = Path(args.results_dir)
    if not results_dir.exists():
        print(f"ERROR: Results directory not found: {results_dir}", file=sys.stderr)
        sys.exit(1)
    
    # Determine output path
    if args.output:
        output_path = Path(args.output)
    else:
        output_path = results_dir / 'convergence_plot.png'
    output_path.parent.mkdir(parents=True, exist_ok=True)
    
    # Load convergence data
    print(f"Loading convergence data from {results_dir}...")
    convergence_by_n = load_convergence_data(results_dir)
    
    if not convergence_by_n:
        print("ERROR: No convergence data found", file=sys.stderr)
        sys.exit(1)
    
    # Create plot
    print(f"Generating convergence plot...")
    if create_convergence_plot(convergence_by_n, output_path):
        print("Done!")
    else:
        print("ERROR: Failed to create plot", file=sys.stderr)
        sys.exit(1)


if __name__ == '__main__':
    main()

