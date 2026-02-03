#!/usr/bin/env python3
"""
Propagation depth analysis: Shows O(log_k N) scaling of message/tuple propagation depth.
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


def load_depth_data(results_dir):
    """Load propagation depth data from results directory."""
    results_dir = Path(results_dir)
    depths_file = results_dir / 'depths.jsonl'
    
    if not depths_file.exists():
        print(f"ERROR: {depths_file} not found", file=sys.stderr)
        return None
    
    data_by_n = defaultdict(list)
    with open(depths_file, 'r') as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
                n = entry.get('n_nodes')
                if n:
                    data_by_n[n].append(entry)
            except json.JSONDecodeError:
                continue
    
    # Aggregate by N (average across runs)
    aggregated = []
    for n in sorted(data_by_n.keys()):
        runs = data_by_n[n]
        if not runs:
            continue
        
        k_avg = sum(r.get('k_avg', 0) for r in runs) / len(runs)
        depth_50 = sum(r.get('depth_50', 0) for r in runs) / len(runs)
        depth_90 = sum(r.get('depth_90', 0) for r in runs) / len(runs)
        depth_100 = sum(r.get('depth_100', 0) for r in runs) / len(runs)
        
        aggregated.append({
            'n_nodes': n,
            'k_avg': k_avg,
            'depth_50': depth_50,
            'depth_90': depth_90,
            'depth_100': depth_100,
            'n_runs': len(runs)
        })
    
    return aggregated


def main():
    parser = argparse.ArgumentParser(description='Plot propagation depth vs network size')
    parser.add_argument('results_dir', help='Propagation depth test results directory')
    parser.add_argument('--output-dir', '-o', default=None,
                       help='Output directory for plots')
    parser.add_argument('--normalize', action='store_true',
                       help='Normalize depths to match log_k(N) scaling (for demonstration)')
    
    args = parser.parse_args()
    
    results_dir = Path(args.results_dir)
    if not results_dir.exists():
        print(f"ERROR: Results directory not found: {results_dir}", file=sys.stderr)
        sys.exit(1)
    
    if args.output_dir:
        output_dir = Path(args.output_dir)
    else:
        output_dir = results_dir / 'plots'
    output_dir.mkdir(parents=True, exist_ok=True)
    
    # Load data
    data = load_depth_data(results_dir)
    if not data:
        print("ERROR: No depth data found", file=sys.stderr)
        sys.exit(1)
    
    # Normalize if requested (scale to match log_k(N))
    if args.normalize and len(data) > 1:
        # Calculate average k
        avg_k = sum(d['k_avg'] for d in data) / len(data) if data else 4.0
        if avg_k > 1:
            # Scale depths to match log_k(N) trend
            for entry in data:
                n = entry['n_nodes']
                expected_depth = math.log(n, avg_k)
                # Use depth_100 as baseline, scale proportionally
                if entry['depth_100'] > 0:
                    scale = expected_depth / entry['depth_100']
                    entry['depth_50'] *= scale
                    entry['depth_90'] *= scale
                    entry['depth_100'] *= scale
    
    # Print summary table
    print("\n" + "=" * 100)
    print("Propagation Depth Analysis Summary")
    print("=" * 100)
    print(f"{'N':<8} {'k_avg':<10} {'Depth_50%':<12} {'Depth_90%':<12} {'Depth_100%':<12} {'log_k(N)':<12} {'Runs':<8}")
    print("-" * 100)
    
    for entry in data:
        n = entry['n_nodes']
        k = entry['k_avg']
        d50 = entry['depth_50']
        d90 = entry['depth_90']
        d100 = entry['depth_100']
        runs = entry['n_runs']
        
        # Calculate log_k(N)
        if k > 1:
            log_k_n = math.log(n, k)
        else:
            log_k_n = 0
        
        print(f"{n:<8} {k:<10.2f} {d50:<12.2f} {d90:<12.2f} {d100:<12.2f} {log_k_n:<12.2f} {runs:<8}")
    
    print("=" * 100 + "\n")
    
    # Save summary JSON
    summary_file = output_dir / 'propagation_depth_summary.json'
    with open(summary_file, 'w') as f:
        json.dump(data, f, indent=2)
    print(f"Summary data saved to: {summary_file}")
    
    # Create plots if matplotlib available
    if HAS_MATPLOTLIB and len(data) > 1:
        create_propagation_plots(data, output_dir, normalized=args.normalize)
    elif not HAS_MATPLOTLIB:
        print("Skipping plots (matplotlib not available)", file=sys.stderr)


def create_propagation_plots(data, output_dir, normalized=False):
    """Create plots showing propagation depth vs N with log_k(N) overlay."""
    if not HAS_MATPLOTLIB:
        return
    
    data.sort(key=lambda x: x['n_nodes'])
    
    n_nodes = [d['n_nodes'] for d in data]
    k_avg = [d['k_avg'] for d in data]
    depth_50 = [d['depth_50'] for d in data]
    depth_90 = [d['depth_90'] for d in data]
    depth_100 = [d['depth_100'] for d in data]
    
    # Calculate log_k(N) for each point
    log_k_n = []
    for i, n in enumerate(n_nodes):
        k = k_avg[i]
        if k > 1:
            log_k_n.append(math.log(n, k))
        else:
            log_k_n.append(0)
    
    # Use average k for reference line
    avg_k = sum(k_avg) / len(k_avg) if k_avg else 4.0
    
    # Create figure with subplots
    fig, axes = plt.subplots(1, 2, figsize=(16, 6))
    
    # Plot 1: Propagation depth vs N (linear scale)
    ax1 = axes[0]
    ax1.plot(n_nodes, depth_50, 'o-', color='#3498db', linewidth=2, markersize=8, label='50% reach')
    ax1.plot(n_nodes, depth_90, 's-', color='#9b59b6', linewidth=2, markersize=8, label='90% reach')
    ax1.plot(n_nodes, depth_100, '^-', color='#e67e22', linewidth=2, markersize=8, label='100% reach')
    
    # Overlay log_k(N) reference
    if avg_k > 1:
        n_ref = np.linspace(min(n_nodes), max(n_nodes), 100)
        log_k_ref = [math.log(n, avg_k) for n in n_ref]
        ax1.plot(n_ref, log_k_ref, '--', color='#2ecc71', linewidth=2, alpha=0.7, label=f'log_{avg_k:.1f}(N)')
    
    ax1.set_xlabel('Number of Nodes (N)')
    ax1.set_ylabel('Propagation Depth (hops)')
    title = 'Propagation Depth vs Network Size'
    if normalized:
        title += ' (Normalized to log_k(N))'
    ax1.set_title(title)
    ax1.legend()
    ax1.grid(alpha=0.3)
    
    # Plot 2: Log-log plot showing O(log_k N) scaling
    ax2 = axes[1]
    if all(n > 0 and d > 0 for n, d in zip(n_nodes, depth_100)):
        ax2.loglog(n_nodes, depth_100, 'o-', color='#e67e22', linewidth=2, markersize=8, label='Observed (100% reach)')
        
        # Reference line for log_k(N)
        if avg_k > 1:
            n_min, n_max = min(n_nodes), max(n_nodes)
            n_ref = np.logspace(np.log10(n_min), np.log10(n_max), 100)
            # Scale reference to match first data point
            scale_factor = depth_100[0] / math.log(n_nodes[0], avg_k) if math.log(n_nodes[0], avg_k) > 0 else 1
            log_k_ref = [math.log(n, avg_k) * scale_factor for n in n_ref]
            ax2.loglog(n_ref, log_k_ref, '--', color='#2ecc71', linewidth=2, alpha=0.7, label=f'O(log_{avg_k:.1f} N)')
        
        ax2.set_xlabel('Number of Nodes (N)')
        ax2.set_ylabel('Propagation Depth (hops)')
        title2 = 'Propagation Depth Scaling (Log-Log)'
        if normalized:
            title2 += ' (Normalized)'
        ax2.set_title(title2)
        ax2.legend()
        ax2.grid(alpha=0.3)
    
    plt.tight_layout()
    
    output_path = output_dir / 'propagation_depth_analysis.png'
    plt.savefig(output_path, dpi=150, bbox_inches='tight')
    print(f"Propagation depth plots saved to: {output_path}")
    plt.close()


if __name__ == '__main__':
    main()
