#!/usr/bin/env python3
"""
Scaling analysis: Compare metrics across different node counts.
Shows how performance scales as network size increases.
"""

import json
import sys
import argparse
from pathlib import Path
from collections import defaultdict

try:
    import matplotlib
    matplotlib.use('Agg')  # Must set backend before importing pyplot
    import matplotlib.pyplot as plt
    import numpy as np
    HAS_MATPLOTLIB = True
except ImportError as e:
    HAS_MATPLOTLIB = False
    print(f"WARNING: matplotlib/numpy not available: {e}", file=sys.stderr)


def load_run_metrics(run_dir):
    """Load final metrics from a run directory."""
    final_metrics_file = Path(run_dir) / 'final_metrics.jsonl'
    if not final_metrics_file.exists():
        return None
    
    metrics_by_node = {}
    with open(final_metrics_file, 'r') as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                data = json.loads(line)
                node_id = data.get('node_id')
                if node_id is not None:
                    metrics_by_node[node_id] = data
            except json.JSONDecodeError:
                continue
    
    # Aggregate
    totals = {
        'dials_attempted': sum(m.get('dials_attempted', 0) for m in metrics_by_node.values()),
        'dials_succeeded': sum(m.get('dials_succeeded', 0) for m in metrics_by_node.values()),
        'dials_failed': sum(m.get('dials_failed', 0) for m in metrics_by_node.values()),
        'restores_started': sum(m.get('restores_started', 0) for m in metrics_by_node.values()),
        'restores_ok': sum(m.get('restores_ok', 0) for m in metrics_by_node.values()),
        'restores_failed': sum(m.get('restores_failed', 0) for m in metrics_by_node.values()),
        'restore_bytes': sum(m.get('restore_bytes', 0) for m in metrics_by_node.values()),
        'gossip_learned': sum(m.get('gossip_learned', 0) for m in metrics_by_node.values()),
    }
    
    # Calculate per-node averages
    num_nodes = len(metrics_by_node)
    if num_nodes > 0:
        totals['dials_per_node'] = totals['dials_attempted'] / num_nodes
        totals['restores_per_node'] = totals['restores_started'] / num_nodes
        totals['gossip_per_node'] = totals['gossip_learned'] / num_nodes
    
    return totals


def load_time_series_metrics(run_dir):
    """Load time-series metrics to calculate convergence time."""
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
                        'restores_started': data.get('restores_started', 0),
                    })
            except json.JSONDecodeError:
                continue
    
    if not time_series:
        return None
    
    # Calculate convergence time (when dials stabilize)
    all_ts = set()
    for node_data in time_series.values():
        for entry in node_data:
            all_ts.add(entry['ts'])
    
    sorted_ts = sorted(all_ts)
    if len(sorted_ts) < 2:
        return None
    
    # Aggregate at each timestamp
    convergence_data = []
    for ts in sorted_ts:
        total_dials = 0
        for node_data in time_series.values():
            for entry in reversed(node_data):
                if entry['ts'] <= ts:
                    total_dials += entry['dials_attempted']
                    break
        convergence_data.append({'ts': ts, 'total_dials': total_dials})
    
    # Find convergence point (when change rate drops)
    if len(convergence_data) < 3:
        return None
    
    first_ts = convergence_data[0]['ts']
    last_ts = convergence_data[-1]['ts']
    total_time = last_ts - first_ts
    
    # Check for convergence (low change in last 20% of time)
    convergence_window = max(3, len(convergence_data) // 5)
    recent = convergence_data[-convergence_window:]
    if len(recent) > 1:
        recent_change = abs(recent[-1]['total_dials'] - recent[0]['total_dials'])
        final_dials = convergence_data[-1]['total_dials']
        convergence_threshold = final_dials * 0.05  # 5% change threshold
        
        # Find when convergence occurred
        convergence_ts = None
        for i in range(len(convergence_data) - convergence_window, len(convergence_data)):
            if i > 0:
                change = abs(convergence_data[i]['total_dials'] - convergence_data[i-1]['total_dials'])
                if change < convergence_threshold:
                    convergence_ts = convergence_data[i]['ts']
                    break
        
        convergence_time = (convergence_ts - first_ts) if convergence_ts else total_time
    else:
        convergence_time = total_time
    
    return {
        'convergence_time': convergence_time,
        'total_time': total_time,
        'final_dials': convergence_data[-1]['total_dials'] if convergence_data else 0,
    }


def main():
    parser = argparse.ArgumentParser(description='Analyze scaling across multiple runs')
    parser.add_argument('results_dir', help='Scaling test results directory')
    parser.add_argument('--output-dir', '-o', default=None,
                       help='Output directory for plots')
    
    args = parser.parse_args()
    
    results_dir = Path(args.results_dir)
    if not results_dir.exists():
        print(f"ERROR: Results directory not found: {results_dir}", file=sys.stderr)
        sys.exit(1)
    
    # Determine output directory
    if args.output_dir:
        output_dir = Path(args.output_dir)
    else:
        output_dir = results_dir / 'plots'
    output_dir.mkdir(parents=True, exist_ok=True)
    
    # Load scaling data
    scaling_data_file = results_dir / 'scaling_data.txt'
    if not scaling_data_file.exists():
        print("ERROR: scaling_data.txt not found", file=sys.stderr)
        sys.exit(1)
    
    scaling_runs = []
    with open(scaling_data_file, 'r') as f:
        for line in f:
            line = line.strip()
            if '|' in line:
                n_str, run_id = line.split('|', 1)
                scaling_runs.append((int(n_str), run_id))
    
    if not scaling_runs:
        print("ERROR: No scaling runs found", file=sys.stderr)
        sys.exit(1)
    
    # Load metrics for each run
    scaling_metrics = []
    for n_nodes, run_id in sorted(scaling_runs):
        run_dir = Path(f"artifacts/runs/{run_id}")
        if not run_dir.exists():
            print(f"WARNING: Run directory not found: {run_dir}", file=sys.stderr)
            continue
        
        metrics = load_run_metrics(run_dir)
        time_metrics = load_time_series_metrics(run_dir)
        
        if metrics:
            combined = {
                'n_nodes': n_nodes,
                'run_id': run_id,
                **metrics
            }
            if time_metrics:
                combined.update(time_metrics)
            scaling_metrics.append(combined)
    
    if not scaling_metrics:
        print("ERROR: No metrics found for any runs", file=sys.stderr)
        sys.exit(1)
    
    # Print summary table
    print("\n" + "=" * 100)
    print("Scaling Analysis Summary")
    print("=" * 100)
    print(f"{'N':<6} {'Dials':<15} {'Dials/N':<10} {'Restores':<15} {'Restores/N':<12} {'Converge(s)':<12} {'Success%':<10}")
    print("-" * 100)
    
    for m in scaling_metrics:
        n = m['n_nodes']
        dials = m.get('dials_attempted', 0)
        dials_per_node = m.get('dials_per_node', 0)
        restores = m.get('restores_started', 0)
        restores_per_node = m.get('restores_per_node', 0)
        converge_time = m.get('convergence_time', 0)
        success_rate = (m.get('dials_succeeded', 0) / dials * 100) if dials > 0 else 0
        
        print(f"{n:<6} {dials:<15} {dials_per_node:<10.2f} {restores:<15} {restores_per_node:<12.2f} {converge_time:<12.1f} {success_rate:<10.1f}")
    
    print("=" * 100 + "\n")
    
    # Save data to JSON
    summary_file = output_dir / 'scaling_summary.json'
    with open(summary_file, 'w') as f:
        json.dump(scaling_metrics, f, indent=2)
    print(f"Scaling data saved to: {summary_file}")
    
    # Create plots if matplotlib available
    if HAS_MATPLOTLIB and len(scaling_metrics) > 1:
        create_scaling_plots(scaling_metrics, output_dir)
    elif not HAS_MATPLOTLIB:
        print("Skipping plots (matplotlib not available)", file=sys.stderr)


def create_scaling_plots(scaling_metrics, output_dir):
    """Create plots showing how metrics scale with node count."""
    if not HAS_MATPLOTLIB:
        return
    
    # Sort by node count
    scaling_metrics.sort(key=lambda x: x['n_nodes'])
    
    n_nodes = [m['n_nodes'] for m in scaling_metrics]
    dials_total = [m.get('dials_attempted', 0) for m in scaling_metrics]
    dials_per_node = [m.get('dials_per_node', 0) for m in scaling_metrics]
    restores_total = [m.get('restores_started', 0) for m in scaling_metrics]
    restores_per_node = [m.get('restores_per_node', 0) for m in scaling_metrics]
    converge_times = [m.get('convergence_time', 0) for m in scaling_metrics]
    success_rates = [(m.get('dials_succeeded', 0) / m.get('dials_attempted', 1) * 100) 
                     if m.get('dials_attempted', 0) > 0 else 0 for m in scaling_metrics]
    
    # Create figure with 1x3 subplots (convergence time, restores per node, complexity)
    fig, axes = plt.subplots(1, 3, figsize=(18, 5))
    
    # Plot 1: Convergence time vs node count
    ax1 = axes[0]
    ax1.plot(n_nodes, converge_times, 'o-', color='#e67e22', linewidth=2, markersize=8)
    ax1.set_xlabel('Number of Nodes')
    ax1.set_ylabel('Convergence Time (seconds)')
    ax1.set_title('Convergence Time vs Network Size')
    ax1.set_ylim([30, 90])
    ax1.grid(alpha=0.3)
    
    # Plot 2: Restores per node vs node count
    ax2 = axes[1]
    ax2.plot(n_nodes, restores_per_node, 'o-', color='#9b59b6', linewidth=2, markersize=8)
    ax2.set_xlabel('Number of Nodes')
    ax2.set_ylabel('Restores per Node')
    ax2.set_title('Restores per Node vs Network Size')
    ax2.grid(alpha=0.3)
    
    # Plot 3: Log-log plot for complexity analysis
    ax3 = axes[2]
    if all(x > 0 and y > 0 for x, y in zip(n_nodes, dials_total)):
        ax3.loglog(n_nodes, dials_total, 'o-', color='#3498db', linewidth=2, markersize=8, label='Total Dials')
        # Add reference lines for O(n), O(n log n), O(n^2)
        if len(n_nodes) > 1:
            n_min, n_max = min(n_nodes), max(n_nodes)
            n_ref = np.logspace(np.log10(n_min), np.log10(n_max), 100)
            ax3.loglog(n_ref, n_ref * (dials_total[0] / n_nodes[0]), '--', alpha=0.3, label='O(n)')
            ax3.loglog(n_ref, n_ref * np.log10(n_ref) * (dials_total[0] / (n_nodes[0] * np.log10(n_nodes[0]))), '--', alpha=0.3, label='O(n log n)')
            ax3.loglog(n_ref, n_ref**2 * (dials_total[0] / n_nodes[0]**2), '--', alpha=0.3, label='O(n²)')
        ax3.set_xlabel('Number of Nodes (log scale)')
        ax3.set_ylabel('Total Dials (log scale)')
        ax3.set_title('Complexity Analysis (Log-Log)')
        ax3.legend()
        ax3.grid(alpha=0.3)
    
    plt.tight_layout()
    
    output_path = output_dir / 'scaling_analysis.png'
    plt.savefig(output_path, dpi=150, bbox_inches='tight')
    print(f"Scaling plots saved to: {output_path}")
    plt.close()


if __name__ == '__main__':
    main()

