#!/usr/bin/env python3
"""
Generate Figure 3: Fault Tolerance (1x2 layout)
  Panel A: Repair Time vs Network Size
  Panel B: Recovery Time Distribution (CDF)
"""

import json
import sys
import argparse
import math
import csv
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
    
    # t-distribution critical value
    if n >= 30:
        t_critical = 1.96
    elif n >= 3:
        t_table = {3: 4.303, 4: 3.182, 5: 2.776, 6: 2.571, 7: 2.447, 8: 2.365,
                   9: 2.306, 10: 2.262, 15: 2.131, 20: 2.086, 25: 2.060}
        t_critical = t_table.get(n, 2.0)
    else:
        t_critical = 2.0
    
    margin = t_critical * (std / math.sqrt(n))
    ci_lower = mean - margin
    ci_upper = mean + margin
    
    return mean, ci_lower, ci_upper, std


def calculate_percentile(values, percentile):
    """Calculate percentile value from sorted list."""
    if not values:
        return None
    sorted_vals = sorted(values)
    index = (percentile / 100.0) * (len(sorted_vals) - 1)
    lower = int(math.floor(index))
    upper = int(math.ceil(index))
    if lower == upper:
        return sorted_vals[lower]
    weight = index - lower
    return sorted_vals[lower] * (1 - weight) + sorted_vals[upper] * weight


def load_repair_data(results_dir):
    """
    Load repair time data from all runs.
    Returns dict: {node_count: [repair_times_in_seconds...]}
    """
    runs_file = Path(results_dir) / 'runs.txt'
    if not runs_file.exists():
        print(f"ERROR: runs.txt not found in {results_dir}", file=sys.stderr)
        return {}
    
    repair_by_n = defaultdict(list)
    all_repair_times = []  # For Panel B CDF
    
    with open(runs_file, 'r') as f:
        for line in f:
            line = line.strip()
            if not line or '|' not in line:
                continue
            
            parts = line.split('|')
            if len(parts) < 2:
                continue
            
            n = int(parts[0])
            run_id = parts[1]
            
            run_dir = Path(f"artifacts/runs/{run_id}")
            repair_csv = run_dir / 'repair.csv'
            
            if not repair_csv.exists():
                print(f"WARNING: repair.csv not found for N={n}, RUN_ID={run_id}", file=sys.stderr)
                continue
            
            # Read repair.csv
            try:
                with open(repair_csv, 'r') as csvfile:
                    reader = csv.DictReader(csvfile)
                    for row in reader:
                        total_duration = row.get('total_duration_s', '').strip()
                        if total_duration and total_duration != '':
                            try:
                                repair_time = float(total_duration)
                                if repair_time >= 0:
                                    repair_by_n[n].append(repair_time)
                                    all_repair_times.append(repair_time)
                            except ValueError:
                                continue
            except Exception as e:
                print(f"WARNING: Error reading repair.csv for N={n}, RUN_ID={run_id}: {e}", file=sys.stderr)
                continue
    
    return repair_by_n, all_repair_times


def create_repair_scaling_plot(repair_by_n, output_path):
    """
    Create Panel A: Repair Time vs Network Size.
    """
    if not HAS_MATPLOTLIB:
        print("ERROR: matplotlib not available", file=sys.stderr)
        return False
    
    # Sort by node count
    sorted_n = sorted(repair_by_n.keys())
    if not sorted_n:
        print("ERROR: No repair data found", file=sys.stderr)
        return False
    
    # Calculate statistics for each node count
    n_values = []
    means = []
    ci_lowers = []
    ci_uppers = []
    stds = []
    counts = []
    
    for n in sorted_n:
        values = repair_by_n[n]
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
    
    # Create plot
    fig, ax = plt.subplots(1, 1, figsize=(8, 6))
    
    # Convert to numpy arrays
    n_array = np.array(n_values)
    means_array = np.array(means)
    ci_lower_array = np.array(ci_lowers)
    ci_upper_array = np.array(ci_uppers)
    
    # Calculate error bar values
    yerr_lower = means_array - ci_lower_array
    yerr_upper = ci_upper_array - means_array
    yerr = np.array([yerr_lower, yerr_upper])
    
    # Plot with error bars
    ax.errorbar(n_array, means_array, yerr=yerr,
               fmt='o-', color='#e74c3c', linewidth=2, markersize=8,
               capsize=5, capthick=2, elinewidth=2, label='Mean ± 95% CI')
    
    ax.set_xlabel('Number of Nodes', fontsize=12)
    ax.set_ylabel('Mean Repair Time (seconds)', fontsize=12)
    ax.set_title('A) Repair Time Scaling', fontsize=14, fontweight='bold', loc='left')
    ax.grid(alpha=0.2, linestyle='--')
    
    # Add text annotation showing number of runs
    for i, (n, count) in enumerate(zip(n_values, counts)):
        ax.text(n, means[i] + ci_upper_array[i] + max(means_array) * 0.02, f'n={count}',
               ha='center', va='bottom', fontsize=9, alpha=0.7)
    
    # Add legend
    ax.legend(loc='best', fontsize=10)
    
    plt.tight_layout()
    plt.savefig(output_path, dpi=300, bbox_inches='tight')
    print(f"Panel A plot saved to: {output_path}")
    plt.close()
    
    # Print summary statistics
    print("\n" + "=" * 80)
    print("Repair Time Scaling Statistics")
    print("=" * 80)
    print(f"{'N':<8} {'Mean (s)':<12} {'Std (s)':<12} {'95% CI':<25} {'Runs':<8}")
    print("-" * 80)
    for i, n in enumerate(n_values):
        ci_str = f"[{ci_lowers[i]:.2f}, {ci_uppers[i]:.2f}]"
        print(f"{n:<8} {means[i]:<12.2f} {stds[i]:<12.2f} {ci_str:<25} {counts[i]:<8}")
    print("=" * 80 + "\n")
    
    return True


def create_recovery_cdf_plot(all_repair_times, output_path):
    """
    Create Panel B: Recovery Time Distribution (CDF).
    """
    if not HAS_MATPLOTLIB:
        print("ERROR: matplotlib not available", file=sys.stderr)
        return False
    
    if not all_repair_times:
        print("ERROR: No repair time data", file=sys.stderr)
        return False
    
    # Sort repair times
    sorted_times = sorted(all_repair_times)
    n = len(sorted_times)
    
    # Calculate CDF
    cdf_values = [(i + 1) / n for i in range(n)]
    
    # Calculate percentiles
    p50 = calculate_percentile(sorted_times, 50)
    p95 = calculate_percentile(sorted_times, 95)
    
    # Create plot
    fig, ax = plt.subplots(1, 1, figsize=(8, 6))
    
    # Plot CDF
    ax.plot(sorted_times, cdf_values, linewidth=2, color='#3498db', label='CDF')
    
    # Add percentile vertical lines
    if p50 is not None:
        ax.axvline(x=p50, color='#e74c3c', linestyle='--', linewidth=2,
                   label=f'P50 ({p50:.2f}s)')
    if p95 is not None:
        ax.axvline(x=p95, color='#f39c12', linestyle='--', linewidth=2,
                   label=f'P95 ({p95:.2f}s)')
    
    ax.set_xlabel('Recovery Time (seconds)', fontsize=12)
    ax.set_ylabel('CDF', fontsize=12)
    ax.set_title('B) Recovery Time Distribution', fontsize=14, fontweight='bold', loc='left')
    ax.set_ylim([0, 1])
    ax.set_xlim([0, max(sorted_times) * 1.1])  # Add 10% padding
    ax.grid(alpha=0.2, linestyle='--')
    ax.legend(loc='lower right', fontsize=10)
    
    plt.tight_layout()
    plt.savefig(output_path, dpi=300, bbox_inches='tight')
    print(f"Panel B plot saved to: {output_path}")
    plt.close()
    
    # Print percentile statistics
    print("\n" + "=" * 80)
    print("Recovery Time Distribution Statistics")
    print("=" * 80)
    print(f"Total repairs: {n}")
    if p50 is not None:
        print(f"P50 (median): {p50:.2f}s")
    if p95 is not None:
        print(f"P95: {p95:.2f}s")
    print(f"Min: {min(sorted_times):.2f}s")
    print(f"Max: {max(sorted_times):.2f}s")
    print("=" * 80 + "\n")
    
    return True


def main():
    parser = argparse.ArgumentParser(description='Generate fault tolerance plots')
    parser.add_argument('results_dir', help='Fault tolerance test results directory')
    parser.add_argument('--output-dir', '-o', default=None,
                       help='Output directory for plots (default: results_dir)')
    
    args = parser.parse_args()
    
    results_dir = Path(args.results_dir)
    if not results_dir.exists():
        print(f"ERROR: Results directory not found: {results_dir}", file=sys.stderr)
        sys.exit(1)
    
    # Determine output directory
    if args.output_dir:
        output_dir = Path(args.output_dir)
    else:
        output_dir = results_dir
    output_dir.mkdir(parents=True, exist_ok=True)
    
    # Load repair data
    print(f"Loading repair data from {results_dir}...")
    repair_by_n, all_repair_times = load_repair_data(results_dir)
    
    if not repair_by_n:
        print("ERROR: No repair data found", file=sys.stderr)
        sys.exit(1)
    
    # Create Panel A: Repair Time Scaling
    print(f"Generating Panel A: Repair Time Scaling...")
    panel_a_path = output_dir / 'repair_scaling.png'
    if not create_repair_scaling_plot(repair_by_n, panel_a_path):
        print("ERROR: Failed to create Panel A", file=sys.stderr)
        sys.exit(1)
    
    # Create Panel B: Recovery Time Distribution (CDF)
    print(f"Generating Panel B: Recovery Time Distribution...")
    panel_b_path = output_dir / 'recovery_cdf.png'
    if not create_recovery_cdf_plot(all_repair_times, panel_b_path):
        print("ERROR: Failed to create Panel B", file=sys.stderr)
        sys.exit(1)
    
    print("\n" + "=" * 80)
    print("Fault Tolerance Plots Complete!")
    print("=" * 80)
    print(f"Panel A (Scaling): {panel_a_path}")
    print(f"Panel B (CDF): {panel_b_path}")
    print("=" * 80 + "\n")


if __name__ == '__main__':
    main()

