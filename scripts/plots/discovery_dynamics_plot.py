#!/usr/bin/env python3
"""
Generate Figure 2: Peer Discovery Dynamics (1x2 layout)
  Panel A: Discovery Time Distribution (CDF)
  Panel B: Discovery Rate vs Network Size
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


def load_discovery_data(results_dir):
    """
    Load discovery time data from all runs.
    Returns dict: {(node_count, k): [discovery_times_in_seconds...]}
    """
    runs_file = Path(results_dir) / 'runs.txt'
    if not runs_file.exists():
        print(f"ERROR: runs.txt not found in {results_dir}", file=sys.stderr)
        return {}
    
    discovery_by_nk = defaultdict(list)
    
    with open(runs_file, 'r') as f:
        for line in f:
            line = line.strip()
            if not line or '|' not in line:
                continue
            
            parts = line.split('|')
            if len(parts) < 3:
                continue
            
            n = int(parts[0])
            run_id = parts[1]
            k = int(parts[2])
            
            run_dir = Path(f"artifacts/runs/{run_id}")
            discovery_csv = run_dir / 'discovery.csv'
            
            if not discovery_csv.exists():
                print(f"WARNING: discovery.csv not found for N={n}, K={k}, RUN_ID={run_id}", file=sys.stderr)
                continue
            
            # Read discovery.csv
            try:
                with open(discovery_csv, 'r') as csvfile:
                    reader = csv.DictReader(csvfile)
                    for row in reader:
                        ts_k_ns = row.get('ts_k_ns', '').strip()
                        ts_start_ns = row.get('ts_start_ns', '').strip()
                        
                        # Skip if ts_k_ns is empty (node didn't reach K neighbors)
                        if not ts_k_ns or ts_k_ns == '':
                            continue
                        
                        # Calculate relative time if both timestamps are available
                        if ts_start_ns and ts_start_ns != '':
                            try:
                                ts_k_val = float(ts_k_ns)
                                ts_start_val = float(ts_start_ns)
                                # Calculate relative time in seconds
                                ts_k_sec = (ts_k_val - ts_start_val) / 1e9
                                if ts_k_sec >= 0:  # Only include valid times
                                    discovery_by_nk[(n, k)].append(ts_k_sec)
                            except (ValueError, TypeError) as e:
                                print(f"WARNING: Error parsing timestamps: {e}", file=sys.stderr)
                                continue
                        else:
                            # Fallback: assume ts_k_ns is already relative (shouldn't happen but handle gracefully)
                            try:
                                ts_k_sec = float(ts_k_ns) / 1e9
                                if ts_k_sec >= 0:
                                    discovery_by_nk[(n, k)].append(ts_k_sec)
                            except (ValueError, TypeError):
                                continue
            except Exception as e:
                print(f"WARNING: Error reading discovery.csv for N={n}, K={k}, RUN_ID={run_id}: {e}", file=sys.stderr)
                continue
    
    return discovery_by_nk


def create_cdf_plot(discovery_times, output_path, k_value=3):
    """
    Create Panel A: Discovery Time Distribution (CDF).
    """
    if not HAS_MATPLOTLIB:
        print("ERROR: matplotlib not available", file=sys.stderr)
        return False
    
    if not discovery_times:
        print("ERROR: No discovery time data", file=sys.stderr)
        return False
    
    # Sort discovery times
    sorted_times = sorted(discovery_times)
    n = len(sorted_times)
    
    # Calculate CDF
    cdf_values = [(i + 1) / n for i in range(n)]
    
    # Calculate median
    if n % 2 == 0:
        median = (sorted_times[n // 2 - 1] + sorted_times[n // 2]) / 2
    else:
        median = sorted_times[n // 2]
    
    # Create plot
    fig, ax = plt.subplots(1, 1, figsize=(8, 6))
    
    # Plot CDF
    ax.plot(sorted_times, cdf_values, linewidth=2, color='#3498db', label='CDF')
    
    # Add median vertical line
    ax.axvline(x=median, color='#e74c3c', linestyle='--', linewidth=2, 
               label=f'Median ({median:.2f}s)')
    
    ax.set_xlabel('Time to Discover K Neighbors (seconds)', fontsize=12)
    ax.set_ylabel('CDF', fontsize=12)
    ax.set_title(f'A) Discovery Time Distribution (K={k_value})', fontsize=14, fontweight='bold', loc='left')
    ax.set_ylim([0, 1])
    ax.set_xlim([0, max(sorted_times) * 1.1])  # Add 10% padding
    ax.grid(alpha=0.2, linestyle='--')
    ax.legend(loc='lower right', fontsize=10)
    
    plt.tight_layout()
    plt.savefig(output_path, dpi=300, bbox_inches='tight')
    print(f"Panel A plot saved to: {output_path}")
    plt.close()
    
    return True


def create_scaling_plot(discovery_by_nk, output_path, k_values=[3, 5]):
    """
    Create Panel B: Discovery Rate vs Network Size.
    """
    if not HAS_MATPLOTLIB:
        print("ERROR: matplotlib not available", file=sys.stderr)
        return False
    
    # Organize data by K value
    k_data = {}
    for k in k_values:
        k_data[k] = {}
        for (n, k_val), times in discovery_by_nk.items():
            if k_val == k and times:
                k_data[k][n] = times
    
    if not k_data:
        print("ERROR: No discovery data found", file=sys.stderr)
        return False
    
    # Create plot
    fig, ax = plt.subplots(1, 1, figsize=(8, 6))
    
    # Colors for different K values
    colors = {'3': '#e67e22', '5': '#9b59b6'}
    markers = {'3': 'o', '5': 's'}
    
    for k in k_values:
        if k not in k_data or not k_data[k]:
            continue
        
        # Sort by node count
        sorted_n = sorted(k_data[k].keys())
        n_values = []
        means = []
        ci_lowers = []
        ci_uppers = []
        
        for n in sorted_n:
            times = k_data[k][n]
            mean, ci_lower, ci_upper, std = calculate_ci(times)
            if mean is not None:
                n_values.append(n)
                means.append(mean)
                ci_lowers.append(ci_lower)
                ci_uppers.append(ci_upper)
        
        if not n_values:
            continue
        
        # Convert to numpy arrays
        n_array = np.array(n_values)
        means_array = np.array(means)
        ci_lower_array = np.array(ci_lowers)
        ci_upper_array = np.array(ci_uppers)
        
        # Calculate error bars
        yerr_lower = means_array - ci_lower_array
        yerr_upper = ci_upper_array - means_array
        yerr = np.array([yerr_lower, yerr_upper])
        
        # Plot with error bars
        color = colors.get(str(k), '#3498db')
        marker = markers.get(str(k), 'o')
        ax.errorbar(n_array, means_array, yerr=yerr,
                   fmt=f'{marker}-', color=color, linewidth=2, markersize=8,
                   capsize=5, capthick=2, elinewidth=2, label=f'K={k}')
    
    ax.set_xlabel('Number of Nodes', fontsize=12)
    ax.set_ylabel('Mean Time to Discover K Neighbors (seconds)', fontsize=12)
    ax.set_title('B) Discovery Rate Scaling', fontsize=14, fontweight='bold', loc='left')
    ax.grid(alpha=0.2, linestyle='--')
    ax.legend(loc='best', fontsize=10)
    
    plt.tight_layout()
    plt.savefig(output_path, dpi=300, bbox_inches='tight')
    print(f"Panel B plot saved to: {output_path}")
    plt.close()
    
    return True


def main():
    parser = argparse.ArgumentParser(description='Generate discovery dynamics plots')
    parser.add_argument('results_dir', help='Discovery test results directory')
    parser.add_argument('--output-dir', '-o', default=None,
                       help='Output directory for plots (default: results_dir)')
    parser.add_argument('--k', default='3', help='K value for Panel A CDF (default: 3)')
    
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
    
    k_value = int(args.k)
    
    # Load discovery data
    print(f"Loading discovery data from {results_dir}...")
    discovery_by_nk = load_discovery_data(results_dir)
    
    if not discovery_by_nk:
        print("ERROR: No discovery data found", file=sys.stderr)
        sys.exit(1)
    
    # Collect all discovery times for Panel A (for specified K)
    all_discovery_times = []
    for (n, k), times in discovery_by_nk.items():
        if k == k_value:
            all_discovery_times.extend(times)
    
    if not all_discovery_times:
        print(f"ERROR: No discovery data found for K={k_value}", file=sys.stderr)
        sys.exit(1)
    
    # Create Panel A: CDF plot
    print(f"Generating Panel A: Discovery Time Distribution (K={k_value})...")
    panel_a_path = output_dir / f'discovery_cdf_k{k_value}.png'
    if not create_cdf_plot(all_discovery_times, panel_a_path, k_value):
        print("ERROR: Failed to create Panel A", file=sys.stderr)
        sys.exit(1)
    
    # Determine K values for Panel B (use all available K values, or default to 3,5)
    available_k_values = sorted(set(k for (n, k) in discovery_by_nk.keys()))
    if not available_k_values:
        print("ERROR: No K values found in data", file=sys.stderr)
        sys.exit(1)
    
    # Use available K values, but prefer 3 and 5 if available
    k_values_for_b = []
    for k in [3, 5]:
        if k in available_k_values:
            k_values_for_b.append(k)
    # Add any other K values if 3 and 5 aren't available
    for k in available_k_values:
        if k not in k_values_for_b:
            k_values_for_b.append(k)
            if len(k_values_for_b) >= 2:
                break
    
    # Create Panel B: Scaling plot
    print(f"Generating Panel B: Discovery Rate Scaling (K={k_values_for_b})...")
    panel_b_path = output_dir / 'discovery_scaling.png'
    if not create_scaling_plot(discovery_by_nk, panel_b_path, k_values_for_b):
        print("ERROR: Failed to create Panel B", file=sys.stderr)
        sys.exit(1)
    
    print("\n" + "=" * 80)
    print("Discovery Dynamics Plots Complete!")
    print("=" * 80)
    print(f"Panel A (CDF): {panel_a_path}")
    print(f"Panel B (Scaling): {panel_b_path}")
    print("=" * 80 + "\n")


if __name__ == '__main__':
    main()

