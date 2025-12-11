#!/usr/bin/env python3
"""
Plot failure/repair scaling behavior.
Shows how repair time scales with data size and network size.
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


def load_repair_data(csv_path):
    """Load repair data from CSV."""
    repairs = []
    
    if not csv_path.exists():
        print(f"Error: {csv_path} not found", file=sys.stderr)
        return repairs
    
    with open(csv_path, 'r') as f:
        reader = csv.DictReader(f)
        for row in reader:
            try:
                repair = {
                    'run_id': row['run_id'],
                    'victim_id': int(row['victim_id']),
                    'donor_id': int(row['donor_id']),
                    'shutdown_duration': float(row['shutdown_duration_s']),
                    'restart_duration': float(row['restart_duration_s']),
                    'snapshot_duration': float(row['snapshot_duration_s']),
                    'restore_duration': float(row['restore_duration_s']),
                    'total_duration': float(row['total_duration_s']),
                    'cid_count': int(row['cid_count']),
                    'restore_ok': int(row['restore_ok']),
                    'restore_failed': int(row['restore_failed']),
                    'restore_bytes': int(row['restore_bytes']),
                }
                repairs.append(repair)
            except (ValueError, KeyError) as e:
                continue
    
    return repairs


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


def create_scaling_plot(repairs, output_path, run_id=None):
    """Create scaling plot for repair operations."""
    if not repairs:
        print("No repair data found", file=sys.stderr)
        return
    
    # Add node counts
    for repair in repairs:
        if 'node_count' not in repair:
            repair['node_count'] = get_node_count_from_run(repair['run_id'])
    
    # Filter valid repairs
    valid_repairs = [r for r in repairs if r['cid_count'] > 0 and r['restore_bytes'] > 0]
    
    if not valid_repairs:
        print("No valid repair data found", file=sys.stderr)
        return
    
    # Extract data
    cid_counts = [r['cid_count'] for r in valid_repairs]
    restore_bytes = [r['restore_bytes'] for r in valid_repairs]
    restore_durations = [r['restore_duration'] for r in valid_repairs]
    total_durations = [r['total_duration'] for r in valid_repairs]
    snapshot_durations = [r['snapshot_duration'] for r in valid_repairs]
    node_counts = [r['node_count'] for r in valid_repairs]
    success_rates = [r['restore_ok'] / (r['restore_ok'] + r['restore_failed']) if (r['restore_ok'] + r['restore_failed']) > 0 else 0 
                     for r in valid_repairs]
    
    # Create plot
    fig, axes = plt.subplots(2, 2, figsize=(16, 12))
    
    # Plot 1: Restore duration vs CID count
    ax1 = axes[0, 0]
    ax1.scatter(cid_counts, restore_durations, s=100, alpha=0.7, color='steelblue', label='Restore Duration')
    if len(cid_counts) > 1:
        import numpy as np
        z = np.polyfit(cid_counts, restore_durations, 1)
        p = np.poly1d(z)
        ax1.plot(cid_counts, p(cid_counts), "b--", alpha=0.5, linewidth=2, 
                label=f'Trend: {z[0]:.3f}n + {z[1]:.2f}')
    ax1.set_xlabel('Number of CIDs', fontsize=11, fontweight='bold')
    ax1.set_ylabel('Restore Duration (seconds)', fontsize=11, fontweight='bold')
    ax1.set_title('Restore Time vs Data Size', fontsize=12, fontweight='bold')
    ax1.legend()
    ax1.grid(True, alpha=0.3)
    ax1.set_yscale('log')
    ax1.set_xscale('log')
    
    # Plot 2: Restore duration vs bytes
    ax2 = axes[0, 1]
    ax2.scatter(restore_bytes, restore_durations, s=100, alpha=0.7, color='coral', label='Restore Duration')
    if len(restore_bytes) > 1:
        z = np.polyfit(restore_bytes, restore_durations, 1)
        p = np.poly1d(z)
        ax2.plot(restore_bytes, p(restore_bytes), "r--", alpha=0.5, linewidth=2,
                label=f'Trend: {z[0]:.6f}n + {z[1]:.2f}')
    ax2.set_xlabel('Bytes Restored', fontsize=11, fontweight='bold')
    ax2.set_ylabel('Restore Duration (seconds)', fontsize=11, fontweight='bold')
    ax2.set_title('Restore Time vs Data Volume', fontsize=12, fontweight='bold')
    ax2.legend()
    ax2.grid(True, alpha=0.3)
    ax2.set_yscale('log')
    ax2.set_xscale('log')
    
    # Plot 3: Total repair duration vs network size
    ax3 = axes[1, 0]
    if any(n > 0 for n in node_counts):
        valid_node_data = [(n, d) for n, d in zip(node_counts, total_durations) if n > 0]
        if valid_node_data:
            node_nums, durations = zip(*valid_node_data)
            ax3.scatter(node_nums, durations, s=100, alpha=0.7, color='green', label='Total Repair Duration')
            if len(node_nums) > 1:
                z = np.polyfit(node_nums, durations, 1)
                p = np.poly1d(z)
                ax3.plot(node_nums, p(node_nums), "g--", alpha=0.5, linewidth=2,
                        label=f'Trend: {z[0]:.3f}n + {z[1]:.2f}')
            ax3.set_xlabel('Network Size (nodes)', fontsize=11, fontweight='bold')
            ax3.set_ylabel('Total Repair Duration (seconds)', fontsize=11, fontweight='bold')
            ax3.set_title('Repair Time vs Network Size', fontsize=12, fontweight='bold')
            ax3.legend()
            ax3.grid(True, alpha=0.3)
            ax3.set_yscale('log')
            ax3.set_xscale('log')
    else:
        ax3.text(0.5, 0.5, 'No network size data', transform=ax3.transAxes, 
                ha='center', va='center', fontsize=12)
        ax3.set_title('Repair Time vs Network Size', fontsize=12, fontweight='bold')
    
    # Plot 4: Success rate vs data size
    ax4 = axes[1, 1]
    ax4.scatter(cid_counts, success_rates, s=100, alpha=0.7, color='purple', label='Success Rate')
    ax4.axhline(y=1.0, color='green', linestyle='--', alpha=0.5, label='100% Success')
    ax4.set_xlabel('Number of CIDs', fontsize=11, fontweight='bold')
    ax4.set_ylabel('Restore Success Rate', fontsize=11, fontweight='bold')
    ax4.set_title('Repair Reliability', fontsize=12, fontweight='bold')
    ax4.set_ylim(-0.05, 1.05)
    ax4.legend()
    ax4.grid(True, alpha=0.3)
    ax4.set_xscale('log')
    
    title = f'Failure/Repair Scaling Analysis'
    if run_id:
        title += f' (Run {run_id})'
    plt.suptitle(title, fontsize=14, fontweight='bold')
    plt.tight_layout()
    plt.subplots_adjust(top=0.93)
    
    output_path.parent.mkdir(parents=True, exist_ok=True)
    plt.savefig(output_path, dpi=300, bbox_inches='tight')
    print(f"Repair scaling plot saved to: {output_path}")


def main():
    parser = argparse.ArgumentParser(description='Plot failure/repair scaling behavior')
    parser.add_argument('run_id', nargs='?', help='Run ID (optional, for single run)')
    parser.add_argument('--csv', help='Path to repair.csv (default: auto-detect)')
    parser.add_argument('--output', help='Output plot path (default: auto-detect)')
    
    args = parser.parse_args()
    
    if args.run_id:
        run_dir = Path(f"artifacts/runs/{args.run_id}")
        if not run_dir.exists():
            print(f"Error: Run directory {run_dir} not found", file=sys.stderr)
            sys.exit(1)
        
        csv_path = run_dir / "repair.csv" if not args.csv else Path(args.csv)
        output_path = run_dir / "plots" / "repair_scaling.png" if not args.output else Path(args.output)
    else:
        csv_path = Path(args.csv) if args.csv else None
        if not csv_path or not csv_path.exists():
            print("Error: Need --csv or run_id to locate repair.csv", file=sys.stderr)
            sys.exit(1)
        output_path = Path(args.output) if args.output else csv_path.parent / "repair_scaling.png"
    
    print(f"Loading repair data from {csv_path}...")
    repairs = load_repair_data(csv_path)
    
    if not repairs:
        print("No repair data found", file=sys.stderr)
        sys.exit(1)
    
    print(f"Loaded {len(repairs)} repair records")
    create_scaling_plot(repairs, output_path, args.run_id)
    print("Done!")


if __name__ == '__main__':
    main()

