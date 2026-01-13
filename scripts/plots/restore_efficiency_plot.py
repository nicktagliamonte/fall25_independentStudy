#!/usr/bin/env python3
"""
Generate Panel C: Restore Efficiency vs Network Size with error bars.
Shows mean restores per node ± 95% CI across multiple runs.
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


def load_restore_efficiency_data(results_dir):
    """
    Load restore efficiency (restores per node) for all runs, grouped by node count.
    Returns dict: {node_count: [restores_per_node...]}
    """
    runs_file = Path(results_dir) / 'runs.txt'
    if not runs_file.exists():
        print(f"ERROR: runs.txt not found in {results_dir}", file=sys.stderr)
        return {}
    
    efficiency_by_n = defaultdict(list)
    
    with open(runs_file, 'r') as f:
        for line in f:
            line = line.strip()
            if not line or '|' not in line:
                continue
            
            n_str, run_id = line.split('|', 1)
            n = int(n_str)
            run_dir = Path(f"artifacts/runs/{run_id}")
            
            # Try to load from final_metrics.jsonl first
            final_metrics_file = run_dir / 'final_metrics.jsonl'
            if final_metrics_file.exists():
                metrics_by_node = {}
                with open(final_metrics_file, 'r') as mf:
                    for mline in mf:
                        mline = mline.strip()
                        if not mline:
                            continue
                        try:
                            data = json.loads(mline)
                            node_id = data.get('node_id')
                            if node_id is not None:
                                metrics_by_node[node_id] = data
                        except json.JSONDecodeError:
                            continue
                
                if metrics_by_node:
                    # Calculate restores per node
                    total_restores = sum(m.get('restores_started', 0) for m in metrics_by_node.values())
                    num_nodes = len(metrics_by_node)
                    if num_nodes > 0:
                        restores_per_node = total_restores / num_nodes
                        efficiency_by_n[n].append(restores_per_node)
                    else:
                        print(f"WARNING: No node metrics for N={n}, RUN_ID={run_id}", file=sys.stderr)
                else:
                    print(f"WARNING: Could not load final metrics for N={n}, RUN_ID={run_id}", file=sys.stderr)
            else:
                # Fallback: try to calculate from raw metrics.jsonl
                metrics_file = run_dir / 'raw' / 'metrics.jsonl'
                if metrics_file.exists():
                    # Get the last snapshot for each node
                    last_metrics = {}
                    with open(metrics_file, 'r') as mf:
                        for mline in mf:
                            mline = mline.strip()
                            if not mline:
                                continue
                            try:
                                data = json.loads(mline)
                                node_id = data.get('node_id')
                                if node_id is not None:
                                    # Keep the latest entry for each node
                                    if node_id not in last_metrics:
                                        last_metrics[node_id] = data
                                    else:
                                        if data.get('ts', 0) > last_metrics[node_id].get('ts', 0):
                                            last_metrics[node_id] = data
                            except json.JSONDecodeError:
                                continue
                    
                    if last_metrics:
                        total_restores = sum(m.get('restores_started', 0) for m in last_metrics.values())
                        num_nodes = len(last_metrics)
                        if num_nodes > 0:
                            restores_per_node = total_restores / num_nodes
                            efficiency_by_n[n].append(restores_per_node)
                        else:
                            print(f"WARNING: No metrics found for N={n}, RUN_ID={run_id}", file=sys.stderr)
                    else:
                        print(f"WARNING: Could not load metrics for N={n}, RUN_ID={run_id}", file=sys.stderr)
                else:
                    print(f"WARNING: No metrics file found for N={n}, RUN_ID={run_id}", file=sys.stderr)
    
    return efficiency_by_n


def create_restore_efficiency_plot(efficiency_by_n, output_path):
    """
    Create Panel C: Restore Efficiency vs Network Size with error bars.
    """
    if not HAS_MATPLOTLIB:
        print("ERROR: matplotlib not available", file=sys.stderr)
        return False
    
    # Sort by node count
    sorted_n = sorted(efficiency_by_n.keys())
    if not sorted_n:
        print("ERROR: No restore efficiency data found", file=sys.stderr)
        return False
    
    # Calculate statistics for each node count
    n_values = []
    means = []
    ci_lowers = []
    ci_uppers = []
    stds = []
    counts = []
    
    for n in sorted_n:
        values = efficiency_by_n[n]
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
    
    # Filter out zero values (missing data) for cleaner plot
    non_zero_indices = [i for i, m in enumerate(means) if m > 0.01]
    
    if not non_zero_indices:
        print("\n" + "=" * 80, file=sys.stderr)
        print("SKIPPING Restore Efficiency Plot", file=sys.stderr)
        print("=" * 80, file=sys.stderr)
        print("All restore efficiency values are zero.", file=sys.stderr)
        print("This indicates restore operations were not completed during the test.", file=sys.stderr)
        print("", file=sys.stderr)
        print("The restore efficiency plot requires restore jobs to be submitted and", file=sys.stderr)
        print("completed. Since no restore data is available, this plot is skipped.", file=sys.stderr)
        print("=" * 80 + "\n", file=sys.stderr)
        return False
    
    # Need at least 2 data points for a meaningful plot
    if len(non_zero_indices) < 2:
        print("\n" + "=" * 80, file=sys.stderr)
        print("SKIPPING Restore Efficiency Plot", file=sys.stderr)
        print("=" * 80, file=sys.stderr)
        print(f"Only {len(non_zero_indices)} data point(s) with restore data found.", file=sys.stderr)
        print("Need at least 2 data points to generate a meaningful scaling plot.", file=sys.stderr)
        print("=" * 80 + "\n", file=sys.stderr)
        return False
    
    # Create plot with better layout
    fig, ax = plt.subplots(1, 1, figsize=(9, 7))
    
    # Filter to only non-zero values
    n_array = np.array([n_values[i] for i in non_zero_indices])
    means_array = np.array([means[i] for i in non_zero_indices])
    ci_lower_array = np.array([ci_lowers[i] for i in non_zero_indices])
    ci_upper_array = np.array([ci_uppers[i] for i in non_zero_indices])
    counts_filtered = [counts[i] for i in non_zero_indices]
    
    # Calculate error bar values (distance from mean)
    yerr_lower = means_array - ci_lower_array
    yerr_upper = ci_upper_array - means_array
    yerr = np.array([yerr_lower, yerr_upper])
    
    # Calculate dynamic y-axis range
    y_max = max(ci_uppers) if len(ci_uppers) > 0 else 1.0
    y_max = max(y_max * 1.15, 1.0)  # Add padding, ensure at least 1.0
    
    # Plot with error bars
    ax.errorbar(n_array, means_array, yerr=yerr, 
                fmt='o-', color='#9b59b6', linewidth=2, markersize=8,
                capsize=5, capthick=2, elinewidth=2, label='Mean ± 95% CI')
    
    # Only add reference line if we have data near 1.0
    if y_max >= 0.8:
        ax.axhline(y=1.0, color='#95a5a6', linestyle='--', alpha=0.3, linewidth=1.5, label='Perfect efficiency (1.0)')
    
    # Set linear scale for both axes
    ax.set_xlabel('Number of Nodes', fontsize=12)
    ax.set_ylabel('Restores per Node', fontsize=12, labelpad=10)
    ax.set_title('C) Restore Efficiency', fontsize=14, fontweight='bold', loc='left')
    ax.set_ylim([0, y_max])
    ax.grid(alpha=0.2, linestyle='--')
    
    # Add text annotation showing node count and number of runs (clearer labels)
    for i, (n, count) in enumerate(zip(n_array, counts_filtered)):
        label_text = f'N={int(n)}\n(n={count})'
        ax.text(n, means_array[i] + ci_upper_array[i] + y_max * 0.02, label_text,
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
    print("Restore Efficiency Statistics")
    print("=" * 80)
    print(f"{'N':<8} {'Mean':<12} {'Std':<12} {'95% CI':<25} {'Runs':<8}")
    print("-" * 80)
    for i, n in enumerate(n_values):
        ci_str = f"[{ci_lowers[i]:.3f}, {ci_uppers[i]:.3f}]"
        print(f"{n:<8} {means[i]:<12.3f} {stds[i]:<12.3f} {ci_str:<25} {counts[i]:<8}")
    print("=" * 80 + "\n")
    
    return True


def main():
    parser = argparse.ArgumentParser(description='Generate restore efficiency plot with error bars')
    parser.add_argument('results_dir', help='Convergence test results directory')
    parser.add_argument('--output', '-o', default=None,
                       help='Output path for plot (default: results_dir/restore_efficiency_plot.png)')
    
    args = parser.parse_args()
    
    results_dir = Path(args.results_dir)
    if not results_dir.exists():
        print(f"ERROR: Results directory not found: {results_dir}", file=sys.stderr)
        sys.exit(1)
    
    # Determine output path
    if args.output:
        output_path = Path(args.output)
    else:
        output_path = results_dir / 'restore_efficiency_plot.png'
    output_path.parent.mkdir(parents=True, exist_ok=True)
    
    # Load restore efficiency data
    print(f"Loading restore efficiency data from {results_dir}...")
    efficiency_by_n = load_restore_efficiency_data(results_dir)
    
    if not efficiency_by_n:
        print("ERROR: No restore efficiency data found", file=sys.stderr)
        sys.exit(1)
    
    # Create plot
    print(f"Generating restore efficiency plot...")
    if create_restore_efficiency_plot(efficiency_by_n, output_path):
        print("Done!")
    else:
        # Don't exit with error - just skip the plot if data is insufficient
        print("Plot generation skipped due to insufficient data.", file=sys.stderr)
        # Remove the empty/bad plot file if it was created
        if output_path.exists():
            output_path.unlink()
        sys.exit(0)  # Exit successfully since skipping is acceptable


if __name__ == '__main__':
    main()

