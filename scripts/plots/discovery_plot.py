#!/usr/bin/env python3
"""
Plot peer discovery timeline per node with nanosecond precision.
Reads discovery_events.csv and creates time-series visualization.
"""

import csv
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
    print("ERROR: matplotlib not available. Install with: pip install matplotlib", file=sys.stderr)
    sys.exit(1)


def load_discovery_events(csv_path):
    """Load discovery events from CSV with nanosecond timestamps."""
    events_by_node = defaultdict(list)
    
    if not csv_path.exists():
        print(f"Error: {csv_path} not found", file=sys.stderr)
        return events_by_node
    
    with open(csv_path, 'r') as f:
        reader = csv.DictReader(f)
        for row in reader:
            try:
                node_id = int(row['node_id'])
                neighbor_peer = row['neighbor_peer']
                discovery_order = int(row['discovery_order'])
                ts_ns = int(row['ts_ns'])
                ts_relative_ns = int(row['ts_relative_ns'])
                
                events_by_node[node_id].append({
                    'neighbor_peer': neighbor_peer,
                    'discovery_order': discovery_order,
                    'ts_ns': ts_ns,
                    'ts_relative_ns': ts_relative_ns,
                    'ts_relative_s': ts_relative_ns / 1e9,  # Convert to seconds
                })
            except (ValueError, KeyError) as e:
                continue
    
    # Sort by timestamp for each node
    for node_id in events_by_node:
        events_by_node[node_id].sort(key=lambda x: x['ts_relative_ns'])
    
    return events_by_node


def create_discovery_plot(events_by_node, output_path, run_id):
    """Create time-series plot showing discovery events per node."""
    if not events_by_node:
        print("No discovery events found", file=sys.stderr)
        return
    
    # Create figure with subplots
    num_nodes = len(events_by_node)
    fig, axes = plt.subplots(num_nodes, 1, figsize=(14, max(8, num_nodes * 1.5)), sharex=True)
    
    if num_nodes == 1:
        axes = [axes]
    
    # Get global time range
    all_times = []
    for events in events_by_node.values():
        for event in events:
            all_times.append(event['ts_relative_s'])
    
    if not all_times:
        print("No timestamps found", file=sys.stderr)
        return
    
    min_time = min(all_times)
    max_time = max(all_times)
    time_range = max_time - min_time if max_time > min_time else 1.0
    
    # Plot each node
    for idx, (node_id, events) in enumerate(sorted(events_by_node.items())):
        ax = axes[idx]
        
        if not events:
            ax.text(0.5, 0.5, f'Node {node_id}: No discoveries', 
                   transform=ax.transAxes, ha='center', va='center')
            ax.set_ylabel(f'Node {node_id}')
            continue
        
        # Extract data
        times = [e['ts_relative_s'] for e in events]
        orders = [e['discovery_order'] for e in events]
        peers = [e['neighbor_peer'][:12] + '...' if len(e['neighbor_peer']) > 12 else e['neighbor_peer'] 
                for e in events]
        
        # Plot discovery events as scatter points
        colors = plt.cm.tab10(range(len(events)))
        scatter = ax.scatter(times, orders, c=range(len(events)), cmap='tab10', 
                           s=100, alpha=0.7, edgecolors='black', linewidths=1)
        
        # Annotate each point with peer ID (abbreviated)
        for i, (t, o, p) in enumerate(zip(times, orders, peers)):
            ax.annotate(f'{o}: {p}', (t, o), xytext=(5, 5), textcoords='offset points',
                       fontsize=7, alpha=0.8)
        
        # Draw step function showing cumulative discoveries
        step_times = [min_time] + times + [max_time]
        step_counts = [0] + orders + [orders[-1]]
        ax.step(step_times, step_counts, where='post', linestyle='--', 
               alpha=0.3, color='gray', linewidth=1)
        
        ax.set_ylabel(f'Node {node_id}\nDiscovery Order', fontsize=10)
        ax.grid(True, alpha=0.3)
        ax.set_ylim(-0.5, max(orders) + 0.5 if orders else 1)
        
        # Add statistics
        if len(events) > 0:
            first_time = events[0]['ts_relative_s']
            last_time = events[-1]['ts_relative_s']
            duration = last_time - first_time if len(events) > 1 else 0
            stats_text = f'Total: {len(events)} | Duration: {duration:.6f}s | First: {first_time:.6f}s'
            ax.text(0.02, 0.98, stats_text, transform=ax.transAxes,
                   fontsize=8, verticalalignment='top', 
                   bbox=dict(boxstyle='round', facecolor='wheat', alpha=0.5))
    
    # Set common x-axis
    axes[-1].set_xlabel('Time (seconds from start)', fontsize=11)
    axes[-1].set_xlim(min_time - time_range * 0.05, max_time + time_range * 0.05)
    
    # Format x-axis to show microseconds
    from matplotlib.ticker import FuncFormatter
    def time_formatter(x, pos):
        return f'{x:.6f}'
    axes[-1].xaxis.set_major_formatter(FuncFormatter(time_formatter))
    
    plt.suptitle(f'Peer Discovery Timeline (Run {run_id})\nNanosecond Precision', 
                fontsize=14, fontweight='bold')
    plt.tight_layout()
    plt.subplots_adjust(top=0.95)
    
    # Save plot
    output_path.parent.mkdir(parents=True, exist_ok=True)
    plt.savefig(output_path, dpi=300, bbox_inches='tight')
    print(f"Discovery plot saved to: {output_path}")


def create_summary_plot(events_by_node, output_path, run_id):
    """Create summary plot showing all nodes on same timeline."""
    if not events_by_node:
        return
    
    fig, ax = plt.subplots(figsize=(14, 8))
    
    # Get global time range
    all_times = []
    for events in events_by_node.values():
        for event in events:
            all_times.append(event['ts_relative_s'])
    
    if not all_times:
        return
    
    min_time = min(all_times)
    max_time = max(all_times)
    
    # Plot each node's discoveries
    colors = plt.cm.tab10(range(len(events_by_node)))
    for idx, (node_id, events) in enumerate(sorted(events_by_node.items())):
        if not events:
            continue
        
        times = [e['ts_relative_s'] for e in events]
        orders = [e['discovery_order'] for e in events]
        
        # Plot as line with markers
        ax.plot(times, orders, marker='o', label=f'Node {node_id}', 
               color=colors[idx % len(colors)], linewidth=2, markersize=6, alpha=0.7)
    
    ax.set_xlabel('Time (seconds from start)', fontsize=12)
    ax.set_ylabel('Cumulative Neighbors Discovered', fontsize=12)
    ax.set_title(f'Peer Discovery Summary - All Nodes (Run {run_id})', fontsize=14, fontweight='bold')
    ax.legend(loc='best', ncol=2)
    ax.grid(True, alpha=0.3)
    
    # Format x-axis
    from matplotlib.ticker import FuncFormatter
    def time_formatter(x, pos):
        return f'{x:.6f}'
    ax.xaxis.set_major_formatter(FuncFormatter(time_formatter))
    
    plt.tight_layout()
    
    summary_path = output_path.parent / f"{output_path.stem}_summary{output_path.suffix}"
    plt.savefig(summary_path, dpi=300, bbox_inches='tight')
    print(f"Summary plot saved to: {summary_path}")


def create_scaling_plot(events_by_node, output_path, run_id):
    """Create aggregated scaling plot showing average discovery time by order."""
    if not events_by_node:
        return
    
    # Aggregate by discovery order
    by_order = defaultdict(list)
    for node_id, events in events_by_node.items():
        for event in events:
            order = event['discovery_order']
            time_s = event['ts_relative_s']
            by_order[order].append(time_s)
    
    if not by_order:
        return
    
    # Calculate statistics per order
    orders = sorted(by_order.keys())
    avg_times = []
    median_times = []
    min_times = []
    max_times = []
    std_times = []
    counts = []
    
    for order in orders:
        times = by_order[order]
        avg_times.append(sum(times) / len(times))
        sorted_times = sorted(times)
        median_times.append(sorted_times[len(sorted_times) // 2])
        min_times.append(min(times))
        max_times.append(max(times))
        mean = sum(times) / len(times)
        variance = sum((t - mean) ** 2 for t in times) / len(times)
        std_times.append(variance ** 0.5)
        counts.append(len(times))
    
    # Create plot
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(16, 6))
    
    # Plot 1: Average discovery time by order with error bars
    x_pos = list(range(len(orders)))
    ax1.errorbar(x_pos, avg_times, yerr=std_times, fmt='o-', capsize=5, capthick=2,
                linewidth=2, markersize=8, label='Mean ± Std Dev', color='steelblue')
    ax1.plot(x_pos, median_times, 's-', linewidth=2, markersize=6, 
              
            label='Median', color='coral', alpha=0.7)
    ax1.fill_between(x_pos, min_times, max_times, alpha=0.2, color='gray', label='Min-Max Range')
    
    ax1.set_xlabel('Discovery Order (N-th peer)', fontsize=12, fontweight='bold')
    ax1.set_ylabel('Time to Discover (seconds)', fontsize=12, fontweight='bold')
    ax1.set_title(f'Average Discovery Time by Order\n(Run {run_id}, {len(events_by_node)} nodes)', 
                 fontsize=13, fontweight='bold')
    ax1.set_xticks(x_pos)
    ax1.set_xticklabels([f'{o}' for o in orders])
    ax1.legend(loc='upper left')
    ax1.grid(True, alpha=0.3)
    ax1.set_yscale('log')  # Log scale to show scaling behavior
    
    # Add sample count annotations
    for i, (x, count) in enumerate(zip(x_pos, counts)):
        ax1.annotate(f'n={count}', (x, avg_times[i]), 
                    xytext=(0, 10), textcoords='offset points',
                    fontsize=8, ha='center', alpha=0.7)
    
    # Plot 2: Cumulative discovery time (how long to discover N peers total)
    cumulative_times = []
    for order in orders:
        # For each order, get the max time across all nodes (when last node reached this order)
        times = by_order[order]
        cumulative_times.append(max(times))
    
    ax2.plot(orders, cumulative_times, 'o-', linewidth=3, markersize=10, 
            color='darkgreen', label='Time to discover N peers (worst case)')
    
    # Also show average cumulative
    avg_cumulative = []
    for order in orders:
        times = by_order[order]
        avg_cumulative.append(sum(times) / len(times))
    ax2.plot(orders, avg_cumulative, 's--', linewidth=2, markersize=8,
            color='lightgreen', alpha=0.7, label='Average time to discover N peers')
    
    ax2.set_xlabel('Number of Peers Discovered', fontsize=12, fontweight='bold')
    ax2.set_ylabel('Cumulative Time (seconds)', fontsize=12, fontweight='bold')
    ax2.set_title(f'Cumulative Discovery Time\n(Scaling Behavior)', 
                 fontsize=13, fontweight='bold')
    ax2.legend(loc='upper left')
    ax2.grid(True, alpha=0.3)
    ax2.set_yscale('log')
    ax2.set_xscale('log')
    
    # Add complexity analysis annotations
    if len(orders) >= 3:
        # Check if it's roughly linear, log, or quadratic
        first_half = orders[:len(orders)//2]
        second_half = orders[len(orders)//2:]
        first_avg = sum(avg_cumulative[:len(orders)//2]) / len(first_half)
        second_avg = sum(avg_cumulative[len(orders)//2:]) / len(second_half)
        ratio = second_avg / first_avg if first_avg > 0 else 1
        
        if ratio < 1.5:
            complexity = "~O(n)"
        elif ratio < 2.5:
            complexity = "~O(n log n)"
        else:
            complexity = "~O(n²) or worse"
        
        ax2.text(0.05, 0.95, f'Scaling: {complexity}', transform=ax2.transAxes,
                fontsize=11, verticalalignment='top',
                bbox=dict(boxstyle='round', facecolor='wheat', alpha=0.8))
    
    plt.tight_layout()
    
    scaling_path = output_path.parent / f"{output_path.stem}_scaling{output_path.suffix}"
    plt.savefig(scaling_path, dpi=300, bbox_inches='tight')
    print(f"Scaling plot saved to: {scaling_path}")


def main():
    parser = argparse.ArgumentParser(description='Plot peer discovery timeline')
    parser.add_argument('run_id', help='Run ID (directory name under artifacts/runs/)')
    parser.add_argument('--events-csv', help='Path to discovery_events.csv (default: auto-detect)')
    parser.add_argument('--output', help='Output plot path (default: auto-detect)')
    parser.add_argument('--scaling-only', action='store_true', help='Only generate scaling plot')
    
    args = parser.parse_args()
    
    run_dir = Path(f"artifacts/runs/{args.run_id}")
    if not run_dir.exists():
        print(f"Error: Run directory {run_dir} not found", file=sys.stderr)
        sys.exit(1)
    
    # Find events CSV
    if args.events_csv:
        events_csv = Path(args.events_csv)
    else:
        events_csv = run_dir / "discovery_events.csv"
    
    if not events_csv.exists():
        print(f"Error: {events_csv} not found. Run discovery.sh first.", file=sys.stderr)
        sys.exit(1)
    
    # Load events
    print(f"Loading discovery events from {events_csv}...")
    events_by_node = load_discovery_events(events_csv)
    
    if not events_by_node:
        print("No discovery events found", file=sys.stderr)
        sys.exit(1)
    
    print(f"Loaded {sum(len(events) for events in events_by_node.values())} discovery events across {len(events_by_node)} nodes")
    
    # Determine output path
    if args.output:
        output_path = Path(args.output)
    else:
        plots_dir = run_dir / "plots"
        plots_dir.mkdir(parents=True, exist_ok=True)
        output_path = plots_dir / "discovery_timeline.png"
    
    # Create scaling plot (always)
    print("Creating scaling behavior plot...")
    create_scaling_plot(events_by_node, output_path, args.run_id)
    
    if not args.scaling_only:
        # Create detailed plots
        print("Creating discovery timeline plot...")
        create_discovery_plot(events_by_node, output_path, args.run_id)
        
        print("Creating summary plot...")
        create_summary_plot(events_by_node, output_path, args.run_id)
    
    print("Done!")


if __name__ == '__main__':
    main()

