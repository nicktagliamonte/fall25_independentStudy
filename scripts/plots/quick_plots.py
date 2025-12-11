#!/usr/bin/env python3
"""
Quick plots for metrics validation.
Reads metrics.jsonl and prints table + bar charts for dials and restores.
"""

import json
import sys
import argparse
from pathlib import Path
from collections import defaultdict

try:
    import matplotlib.pyplot as plt
    import matplotlib
    matplotlib.use('Agg')  # Non-interactive backend
    HAS_MATPLOTLIB = True
except ImportError:
    HAS_MATPLOTLIB = False
    print("WARNING: matplotlib not available, skipping plots. Install with: pip install matplotlib", file=sys.stderr)


def load_metrics(jsonl_path):
    """Load metrics from JSONL file, keeping latest values per node."""
    metrics_by_node = {}
    
    with open(jsonl_path, 'r') as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                data = json.loads(line)
                node_id = data.get('node_id')
                if node_id is not None:
                    # Keep latest metrics per node
                    if node_id not in metrics_by_node or data.get('ts', 0) > metrics_by_node[node_id].get('ts', 0):
                        metrics_by_node[node_id] = data
            except json.JSONDecodeError:
                continue
    
    return metrics_by_node


def load_time_series(jsonl_path):
    """Load all metrics entries as time series, grouped by node."""
    time_series = defaultdict(list)
    
    with open(jsonl_path, 'r') as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                data = json.loads(line)
                node_id = data.get('node_id')
                ts = data.get('ts', 0)
                iteration = data.get('iteration', 0)
                
                if node_id is not None and ts > 0:
                    time_series[node_id].append({
                        'ts': ts,
                        'iteration': iteration,
                        'dials_attempted': data.get('dials_attempted', 0),
                        'dials_succeeded': data.get('dials_succeeded', 0),
                        'dials_failed': data.get('dials_failed', 0),
                        'restores_started': data.get('restores_started', 0),
                        'restores_ok': data.get('restores_ok', 0),
                        'restores_failed': data.get('restores_failed', 0),
                        'restore_bytes': data.get('restore_bytes', 0),
                        'gossip_learned': data.get('gossip_learned', 0),
                    })
            except json.JSONDecodeError:
                continue
    
    # Sort by timestamp for each node
    for node_id in time_series:
        time_series[node_id].sort(key=lambda x: x['ts'])
    
    return time_series


def calculate_efficiency_metrics(time_series):
    """Calculate efficiency metrics and convergence indicators."""
    if not time_series:
        return {}
    
    # Aggregate across all nodes at each time point
    all_timestamps = set()
    for node_data in time_series.values():
        for entry in node_data:
            all_timestamps.add(entry['ts'])
    
    sorted_ts = sorted(all_timestamps)
    if len(sorted_ts) < 2:
        return {}
    
    # Calculate aggregated metrics over time
    time_aggregates = []
    for ts in sorted_ts:
        agg = {
            'ts': ts,
            'dials_attempted': 0,
            'dials_succeeded': 0,
            'dials_failed': 0,
            'restores_started': 0,
            'restores_ok': 0,
            'restores_failed': 0,
            'restore_bytes': 0,
            'gossip_learned': 0,
        }
        
        for node_data in time_series.values():
            # Find entry at or before this timestamp
            for entry in reversed(node_data):
                if entry['ts'] <= ts:
                    agg['dials_attempted'] += entry['dials_attempted']
                    agg['dials_succeeded'] += entry['dials_succeeded']
                    agg['dials_failed'] += entry['dials_failed']
                    agg['restores_started'] += entry['restores_started']
                    agg['restores_ok'] += entry['restores_ok']
                    agg['restores_failed'] += entry['restores_failed']
                    agg['restore_bytes'] += entry['restore_bytes']
                    agg['gossip_learned'] += entry['gossip_learned']
                    break
        
        time_aggregates.append(agg)
    
    # Calculate efficiency metrics
    first = time_aggregates[0]
    last = time_aggregates[-1]
    
    # Calculate rates of change (convergence indicators)
    total_time = sorted_ts[-1] - sorted_ts[0] if len(sorted_ts) > 1 else 1
    
    dials_rate = (last['dials_attempted'] - first['dials_attempted']) / total_time if total_time > 0 else 0
    restores_rate = (last['restores_started'] - first['restores_started']) / total_time if total_time > 0 else 0
    
    # Success rates
    dial_success_rate = (last['dials_succeeded'] / last['dials_attempted'] * 100) if last['dials_attempted'] > 0 else 0
    restore_success_rate = (last['restores_ok'] / last['restores_started'] * 100) if last['restores_started'] > 0 else 0
    
    # Convergence: check if metrics are stabilizing (low rate of change in recent samples)
    convergence_window = min(5, len(time_aggregates))
    if convergence_window > 1:
        recent = time_aggregates[-convergence_window:]
        recent_dials_change = abs(recent[-1]['dials_attempted'] - recent[0]['dials_attempted'])
        recent_restores_change = abs(recent[-1]['restores_started'] - recent[0]['restores_started'])
        convergence_dials = recent_dials_change < (last['dials_attempted'] * 0.05)  # < 5% change
        convergence_restores = recent_restores_change < (last['restores_started'] * 0.05) if last['restores_started'] > 0 else True
    else:
        convergence_dials = False
        convergence_restores = False
    
    return {
        'time_aggregates': time_aggregates,
        'total_time': total_time,
        'dials_rate_per_sec': dials_rate,
        'restores_rate_per_sec': restores_rate,
        'dial_success_rate_pct': dial_success_rate,
        'restore_success_rate_pct': restore_success_rate,
        'convergence_dials': convergence_dials,
        'convergence_restores': convergence_restores,
        'first_ts': sorted_ts[0],
        'last_ts': sorted_ts[-1],
    }


def aggregate_metrics(metrics_by_node):
    """Aggregate metrics across all nodes."""
    totals = defaultdict(int)
    
    for node_id, metrics in metrics_by_node.items():
        totals['dials_attempted'] += metrics.get('dials_attempted', 0)
        totals['dials_succeeded'] += metrics.get('dials_succeeded', 0)
        totals['dials_failed'] += metrics.get('dials_failed', 0)
        totals['restores_started'] += metrics.get('restores_started', 0)
        totals['restores_ok'] += metrics.get('restores_ok', 0)
        totals['restores_failed'] += metrics.get('restores_failed', 0)
        totals['restore_bytes'] += metrics.get('restore_bytes', 0)
        totals['gossip_learned'] += metrics.get('gossip_learned', 0)
    
    return totals


def print_table(metrics_by_node, totals):
    """Print formatted table of metrics."""
    print("\n" + "=" * 80)
    print("Metrics Summary Table")
    print("=" * 80)
    
    # Header
    print(f"{'Node':<6} {'Dials':<20} {'Restores':<30} {'Bytes':<12}")
    print(f"{'':<6} {'Att':<6} {'Succ':<6} {'Fail':<6} {'Start':<6} {'OK':<6} {'Fail':<6} {'Restore':<12}")
    print("-" * 80)
    
    # Per-node rows
    for node_id in sorted(metrics_by_node.keys()):
        m = metrics_by_node[node_id]
        print(f"{node_id:<6} "
              f"{m.get('dials_attempted', 0):<6} "
              f"{m.get('dials_succeeded', 0):<6} "
              f"{m.get('dials_failed', 0):<6} "
              f"{m.get('restores_started', 0):<6} "
              f"{m.get('restores_ok', 0):<6} "
              f"{m.get('restores_failed', 0):<6} "
              f"{m.get('restore_bytes', 0):<12}")
    
    # Totals row
    print("-" * 80)
    print(f"{'TOTAL':<6} "
          f"{totals['dials_attempted']:<6} "
          f"{totals['dials_succeeded']:<6} "
          f"{totals['dials_failed']:<6} "
          f"{totals['restores_started']:<6} "
          f"{totals['restores_ok']:<6} "
          f"{totals['restores_failed']:<6} "
          f"{totals['restore_bytes']:<12}")
    print("=" * 80 + "\n")


def create_plots(metrics_by_node, totals, output_dir, time_series=None, efficiency=None):
    """Create bar charts and time-series plots for dials and restores."""
    if not HAS_MATPLOTLIB:
        return
    
    output_dir = Path(output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    
    # Prepare data
    node_ids = sorted(metrics_by_node.keys())
    dials_attempted = [metrics_by_node[nid].get('dials_attempted', 0) for nid in node_ids]
    dials_succeeded = [metrics_by_node[nid].get('dials_succeeded', 0) for nid in node_ids]
    dials_failed = [metrics_by_node[nid].get('dials_failed', 0) for nid in node_ids]
    
    restores_started = [metrics_by_node[nid].get('restores_started', 0) for nid in node_ids]
    restores_ok = [metrics_by_node[nid].get('restores_ok', 0) for nid in node_ids]
    restores_failed = [metrics_by_node[nid].get('restores_failed', 0) for nid in node_ids]
    
    # Create figure with bar charts
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(14, 6))
    
    # Dials chart
    x = range(len(node_ids))
    width = 0.25
    ax1.bar([i - width for i in x], dials_attempted, width, label='Attempted', color='#3498db')
    ax1.bar(x, dials_succeeded, width, label='Succeeded', color='#2ecc71')
    ax1.bar([i + width for i in x], dials_failed, width, label='Failed', color='#e74c3c')
    ax1.set_xlabel('Node ID')
    ax1.set_ylabel('Count')
    ax1.set_title('Dial Metrics by Node')
    ax1.set_xticks(x)
    ax1.set_xticklabels([f'Node {nid}' for nid in node_ids])
    ax1.legend()
    ax1.grid(axis='y', alpha=0.3)
    
    # Restores chart
    ax2.bar([i - width for i in x], restores_started, width, label='Started', color='#3498db')
    ax2.bar(x, restores_ok, width, label='OK', color='#2ecc71')
    ax2.bar([i + width for i in x], restores_failed, width, label='Failed', color='#e74c3c')
    ax2.set_xlabel('Node ID')
    ax2.set_ylabel('Count')
    ax2.set_title('Restore Metrics by Node')
    ax2.set_xticks(x)
    ax2.set_xticklabels([f'Node {nid}' for nid in node_ids])
    ax2.legend()
    ax2.grid(axis='y', alpha=0.3)
    
    plt.tight_layout()
    
    output_path = output_dir / 'metrics_plots.png'
    plt.savefig(output_path, dpi=150, bbox_inches='tight')
    print(f"Bar charts saved to: {output_path}")
    plt.close()
    
    # Create time-series plots if we have time-series data
    if time_series and efficiency and efficiency.get('time_aggregates'):
        create_time_series_plots(time_series, efficiency, output_dir)


def create_time_series_plots(time_series, efficiency, output_dir):
    """Create time-series plots showing metrics over time and convergence."""
    if not HAS_MATPLOTLIB:
        return
    
    time_aggregates = efficiency['time_aggregates']
    first_ts = efficiency['first_ts']
    
    # Normalize timestamps to start from 0
    times = [(agg['ts'] - first_ts) for agg in time_aggregates]
    
    # Create figure with multiple subplots
    fig, axes = plt.subplots(2, 2, figsize=(16, 12))
    
    # Plot 1: Dials over time
    ax1 = axes[0, 0]
    ax1.plot(times, [a['dials_attempted'] for a in time_aggregates], 
             label='Attempted', color='#3498db', linewidth=2)
    ax1.plot(times, [a['dials_succeeded'] for a in time_aggregates], 
             label='Succeeded', color='#2ecc71', linewidth=2)
    ax1.plot(times, [a['dials_failed'] for a in time_aggregates], 
             label='Failed', color='#e74c3c', linewidth=2)
    ax1.set_xlabel('Time (seconds)')
    ax1.set_ylabel('Count')
    ax1.set_title('Dial Metrics Over Time')
    ax1.legend()
    ax1.grid(alpha=0.3)
    
    # Plot 2: Dial success rate over time
    ax2 = axes[0, 1]
    success_rates = []
    for agg in time_aggregates:
        if agg['dials_attempted'] > 0:
            rate = (agg['dials_succeeded'] / agg['dials_attempted']) * 100
        else:
            rate = 0
        success_rates.append(rate)
    ax2.plot(times, success_rates, label='Success Rate', color='#2ecc71', linewidth=2)
    ax2.axhline(y=95, color='#95a5a6', linestyle='--', alpha=0.5, label='95% target')
    ax2.set_xlabel('Time (seconds)')
    ax2.set_ylabel('Success Rate (%)')
    ax2.set_title('Dial Success Rate Over Time')
    ax2.set_ylim([0, 105])
    ax2.legend()
    ax2.grid(alpha=0.3)
    
    # Plot 3: Restores over time
    ax3 = axes[1, 0]
    ax3.plot(times, [a['restores_started'] for a in time_aggregates], 
             label='Started', color='#3498db', linewidth=2)
    ax3.plot(times, [a['restores_ok'] for a in time_aggregates], 
             label='OK', color='#2ecc71', linewidth=2)
    ax3.plot(times, [a['restores_failed'] for a in time_aggregates], 
             label='Failed', color='#e74c3c', linewidth=2)
    ax3.set_xlabel('Time (seconds)')
    ax3.set_ylabel('Count')
    ax3.set_title('Restore Metrics Over Time')
    ax3.legend()
    ax3.grid(alpha=0.3)
    
    # Plot 4: Network saturation indicators (gossip learned, cumulative activity)
    ax4 = axes[1, 1]
    ax4.plot(times, [a['gossip_learned'] for a in time_aggregates], 
             label='Gossip Learned', color='#9b59b6', linewidth=2)
    ax4_twin = ax4.twinx()
    total_activity = [a['dials_attempted'] + a['restores_started'] for a in time_aggregates]
    ax4_twin.plot(times, total_activity, label='Total Activity', color='#e67e22', linewidth=2, linestyle='--')
    ax4.set_xlabel('Time (seconds)')
    ax4.set_ylabel('Gossip Learned', color='#9b59b6')
    ax4_twin.set_ylabel('Total Activity (dials + restores)', color='#e67e22')
    ax4.set_title('Network Saturation Indicators')
    ax4.tick_params(axis='y', labelcolor='#9b59b6')
    ax4_twin.tick_params(axis='y', labelcolor='#e67e22')
    ax4.legend(loc='upper left')
    ax4_twin.legend(loc='upper right')
    ax4.grid(alpha=0.3)
    
    plt.tight_layout()
    
    output_path = Path(output_dir) / 'metrics_timeseries.png'
    plt.savefig(output_path, dpi=150, bbox_inches='tight')
    print(f"Time-series plots saved to: {output_path}")
    plt.close()


def main():
    parser = argparse.ArgumentParser(description='Generate quick plots from metrics.jsonl')
    parser.add_argument('metrics_file', help='Path to metrics.jsonl file')
    parser.add_argument('--output-dir', '-o', default=None,
                       help='Output directory for plots (default: inferred from metrics_file path)')
    parser.add_argument('--no-plots', action='store_true',
                       help='Skip plot generation (table only)')
    parser.add_argument('--save-table', action='store_true',
                       help='Save table output to file')
    
    args = parser.parse_args()
    
    metrics_path = Path(args.metrics_file)
    if not metrics_path.exists():
        print(f"ERROR: Metrics file not found: {metrics_path}", file=sys.stderr)
        sys.exit(1)
    
    # Infer output directory from metrics file path if not provided
    if args.output_dir is None:
        # Try to extract run_id from path: artifacts/runs/RUN_ID/raw/metrics.jsonl
        parts = metrics_path.parts
        if 'runs' in parts:
            runs_idx = parts.index('runs')
            if runs_idx + 1 < len(parts):
                run_id = parts[runs_idx + 1]
                args.output_dir = f"artifacts/runs/{run_id}/plots"
            else:
                args.output_dir = "artifacts/plots"
        else:
            args.output_dir = "artifacts/plots"
    
    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    
    # Load and process metrics
    metrics_by_node = load_metrics(metrics_path)
    if not metrics_by_node:
        print("WARNING: No metrics found in file", file=sys.stderr)
        sys.exit(1)
    
    totals = aggregate_metrics(metrics_by_node)
    
    # Load time-series data if available
    time_series = load_time_series(metrics_path)
    efficiency = None
    if time_series and len(time_series) > 0:
        efficiency = calculate_efficiency_metrics(time_series)
    
    # Print table (and optionally save to file)
    if args.save_table:
        table_file = output_dir / 'metrics_table.txt'
        with open(table_file, 'w') as f:
            # Redirect print_table output to file
            import io
            from contextlib import redirect_stdout
            buf = io.StringIO()
            with redirect_stdout(buf):
                print_table(metrics_by_node, totals)
            table_output = buf.getvalue()
            f.write(table_output)
            print(table_output)  # Also print to console
            print(f"Table saved to: {table_file}")
    else:
        print_table(metrics_by_node, totals)
    
    # Print efficiency metrics if available
    if efficiency:
        print("\n" + "=" * 80)
        print("Efficiency Metrics & Convergence Analysis")
        print("=" * 80)
        print(f"  Observation period: {efficiency['total_time']:.1f} seconds")
        print(f"  Dial success rate: {efficiency['dial_success_rate_pct']:.1f}%")
        print(f"  Restore success rate: {efficiency['restore_success_rate_pct']:.1f}%")
        print(f"  Dial rate: {efficiency['dials_rate_per_sec']:.2f} dials/sec")
        print(f"  Restore rate: {efficiency['restores_rate_per_sec']:.2f} restores/sec")
        print(f"  Dials converged: {'Yes' if efficiency['convergence_dials'] else 'No'}")
        print(f"  Restores converged: {'Yes' if efficiency['convergence_restores'] else 'No'}")
        print("=" * 80 + "\n")
    
    # Create plots if requested
    if not args.no_plots and HAS_MATPLOTLIB:
        create_plots(metrics_by_node, totals, args.output_dir, time_series, efficiency)
    elif not args.no_plots:
        print("Skipping plots (matplotlib not available)", file=sys.stderr)
    
    # Print summary
    summary_lines = [
        "Summary:",
        f"  Total dials attempted: {totals['dials_attempted']}",
        f"  Total dials succeeded: {totals['dials_succeeded']}",
        f"  Total dials failed: {totals['dials_failed']}",
        f"  Total restores started: {totals['restores_started']}",
        f"  Total restores OK: {totals['restores_ok']}",
        f"  Total restores failed: {totals['restores_failed']}",
        f"  Total restore bytes: {totals['restore_bytes']}"
    ]
    
    for line in summary_lines:
        print(line)
    
    # Save summary to JSON
    summary_file = output_dir / 'metrics_summary.json'
    summary_data = {
        'totals': totals,
        'per_node': {str(nid): metrics_by_node[nid] for nid in metrics_by_node.keys()}
    }
    if efficiency:
        summary_data['efficiency'] = {
            'dial_success_rate_pct': efficiency['dial_success_rate_pct'],
            'restore_success_rate_pct': efficiency['restore_success_rate_pct'],
            'dials_rate_per_sec': efficiency['dials_rate_per_sec'],
            'restores_rate_per_sec': efficiency['restores_rate_per_sec'],
            'convergence_dials': efficiency['convergence_dials'],
            'convergence_restores': efficiency['convergence_restores'],
            'total_time': efficiency['total_time'],
        }
    with open(summary_file, 'w') as f:
        json.dump(summary_data, f, indent=2)
    print(f"Summary JSON saved to: {summary_file}")


if __name__ == '__main__':
    main()

