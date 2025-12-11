#!/usr/bin/env python3
"""
Plot partition/merge scaling behavior.
Shows how partition and merge durations scale with network size.
"""

import csv
import sys
import argparse
from pathlib import Path
from collections import defaultdict

try:
    import matplotlib.pyplot as plt
    import matplotlib
    matplotlib.use('Agg')
    HAS_MATPLOTLIB = True
except ImportError:
    HAS_MATPLOTLIB = False
    print("ERROR: matplotlib not available. Install with: pip install matplotlib", file=sys.stderr)
    sys.exit(1)


def load_partition_data(csv_path):
    """Load partition/merge data from CSV."""
    runs = defaultdict(lambda: {'partition': None, 'merge': None, 'groups': None, 'node_count': 0})
    
    if not csv_path.exists():
        print(f"Error: {csv_path} not found", file=sys.stderr)
        return runs
    
    with open(csv_path, 'r') as f:
        reader = csv.DictReader(f)
        for row in reader:
            run_id = row['run_id']
            phase = row['phase']
            duration = float(row['duration_s'])
            groups = row.get('groups', '')
            
            if phase == 'partition':
                runs[run_id]['partition'] = duration
                runs[run_id]['groups'] = groups
            elif phase == 'merge':
                runs[run_id]['merge'] = duration
            
            # Estimate node count from groups (e.g., "1-5,6-10" = 10 nodes)
            if groups and runs[run_id]['node_count'] == 0:
                max_node = 0
                for group in groups.split(','):
                    if '-' in group:
                        parts = group.split('-')
                        if len(parts) == 2:
                            try:
                                max_node = max(max_node, int(parts[1]))
                            except ValueError:
                                pass
                runs[run_id]['node_count'] = max_node
    
    return runs


def get_node_count_from_run(run_id):
    """Try to get node count from nodes.json."""
    nodes_json = Path(f"artifacts/runs/{run_id}/nodes.json")
    if nodes_json.exists():
        try:
            import json
            with open(nodes_json, 'r') as f:
                data = json.load(f)
                return len(data) if isinstance(data, list) else 0
        except:
            pass
    return 0


def create_scaling_plot(runs_data, output_path, run_id=None):
    """Create scaling plot for partition/merge."""
    if not runs_data:
        print("No partition data found", file=sys.stderr)
        return
    
    # Collect data points
    node_counts = []
    partition_durations = []
    merge_durations = []
    total_durations = []
    
    for run_id_key, data in runs_data.items():
        if data['partition'] is None or data['merge'] is None:
            continue
        
        node_count = data['node_count']
        if node_count == 0:
            node_count = get_node_count_from_run(run_id_key)
        
        if node_count > 0:
            node_counts.append(node_count)
            partition_durations.append(data['partition'])
            merge_durations.append(data['merge'])
            total_durations.append(data['partition'] + data['merge'])
    
    if not node_counts:
        print("No valid data points found", file=sys.stderr)
        return
    
    # Sort by node count
    sorted_data = sorted(zip(node_counts, partition_durations, merge_durations, total_durations))
    node_counts, partition_durations, merge_durations, total_durations = zip(*sorted_data)
    
    # Create plot
    fig, axes = plt.subplots(2, 2, figsize=(16, 12))
    
    # Plot 1: Partition duration vs network size
    ax1 = axes[0, 0]
    ax1.scatter(node_counts, partition_durations, s=100, alpha=0.7, color='red', label='Partition Duration')
    if len(node_counts) > 1:
        # Fit trend line
        import numpy as np
        z = np.polyfit(node_counts, partition_durations, 1)
        p = np.poly1d(z)
        ax1.plot(node_counts, p(node_counts), "r--", alpha=0.5, linewidth=2, label=f'Trend: {z[0]:.3f}n + {z[1]:.2f}')
    ax1.set_xlabel('Network Size (nodes)', fontsize=11, fontweight='bold')
    ax1.set_ylabel('Partition Duration (seconds)', fontsize=11, fontweight='bold')
    ax1.set_title('Partition Duration Scaling', fontsize=12, fontweight='bold')
    ax1.legend()
    ax1.grid(True, alpha=0.3)
    ax1.set_yscale('log')
    ax1.set_xscale('log')
    
    # Plot 2: Merge duration vs network size
    ax2 = axes[0, 1]
    ax2.scatter(node_counts, merge_durations, s=100, alpha=0.7, color='green', label='Merge Duration')
    if len(node_counts) > 1:
        z = np.polyfit(node_counts, merge_durations, 1)
        p = np.poly1d(z)
        ax2.plot(node_counts, p(node_counts), "g--", alpha=0.5, linewidth=2, label=f'Trend: {z[0]:.3f}n + {z[1]:.2f}')
    ax2.set_xlabel('Network Size (nodes)', fontsize=11, fontweight='bold')
    ax2.set_ylabel('Merge Duration (seconds)', fontsize=11, fontweight='bold')
    ax2.set_title('Merge Duration Scaling', fontsize=12, fontweight='bold')
    ax2.legend()
    ax2.grid(True, alpha=0.3)
    ax2.set_yscale('log')
    ax2.set_xscale('log')
    
    # Plot 3: Total duration vs network size
    ax3 = axes[1, 0]
    ax3.scatter(node_counts, total_durations, s=100, alpha=0.7, color='blue', label='Total Duration')
    if len(node_counts) > 1:
        z = np.polyfit(node_counts, total_durations, 1)
        p = np.poly1d(z)
        ax3.plot(node_counts, p(node_counts), "b--", alpha=0.5, linewidth=2, label=f'Trend: {z[0]:.3f}n + {z[1]:.2f}')
    ax3.set_xlabel('Network Size (nodes)', fontsize=11, fontweight='bold')
    ax3.set_ylabel('Total Duration (seconds)', fontsize=11, fontweight='bold')
    ax3.set_title('Total Duration Scaling', fontsize=12, fontweight='bold')
    ax3.legend()
    ax3.grid(True, alpha=0.3)
    ax3.set_yscale('log')
    ax3.set_xscale('log')
    
    # Plot 4: Ratio (merge/partition) vs network size
    ax4 = axes[1, 1]
    ratios = [m/p if p > 0 else 0 for m, p in zip(merge_durations, partition_durations)]
    ax4.scatter(node_counts, ratios, s=100, alpha=0.7, color='purple', label='Merge/Partition Ratio')
    ax4.axhline(y=1.0, color='gray', linestyle='--', alpha=0.5, label='Equal duration')
    ax4.set_xlabel('Network Size (nodes)', fontsize=11, fontweight='bold')
    ax4.set_ylabel('Merge/Partition Ratio', fontsize=11, fontweight='bold')
    ax4.set_title('Recovery Efficiency', fontsize=12, fontweight='bold')
    ax4.legend()
    ax4.grid(True, alpha=0.3)
    ax4.set_xscale('log')
    
    title = f'Partition/Merge Scaling Analysis'
    if run_id:
        title += f' (Run {run_id})'
    plt.suptitle(title, fontsize=14, fontweight='bold')
    plt.tight_layout()
    plt.subplots_adjust(top=0.93)
    
    output_path.parent.mkdir(parents=True, exist_ok=True)
    plt.savefig(output_path, dpi=300, bbox_inches='tight')
    print(f"Partition scaling plot saved to: {output_path}")


def main():
    parser = argparse.ArgumentParser(description='Plot partition/merge scaling behavior')
    parser.add_argument('run_id', nargs='?', help='Run ID (optional, for single run)')
    parser.add_argument('--csv', help='Path to partition.csv (default: auto-detect)')
    parser.add_argument('--output', help='Output plot path (default: auto-detect)')
    
    args = parser.parse_args()
    
    if args.run_id:
        run_dir = Path(f"artifacts/runs/{args.run_id}")
        if not run_dir.exists():
            print(f"Error: Run directory {run_dir} not found", file=sys.stderr)
            sys.exit(1)
        
        csv_path = run_dir / "partition.csv" if not args.csv else Path(args.csv)
        output_path = run_dir / "plots" / "partition_scaling.png" if not args.output else Path(args.output)
    else:
        # Aggregate across all runs
        csv_path = Path(args.csv) if args.csv else None
        if not csv_path or not csv_path.exists():
            print("Error: Need --csv or run_id to locate partition.csv", file=sys.stderr)
            sys.exit(1)
        output_path = Path(args.output) if args.output else csv_path.parent / "partition_scaling.png"
    
    print(f"Loading partition data from {csv_path}...")
    runs_data = load_partition_data(csv_path)
    
    if not runs_data:
        print("No partition data found", file=sys.stderr)
        sys.exit(1)
    
    print(f"Loaded data for {len(runs_data)} runs")
    create_scaling_plot(runs_data, output_path, args.run_id)
    print("Done!")


if __name__ == '__main__':
    main()

