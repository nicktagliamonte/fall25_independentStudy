#!/usr/bin/env python3
"""
Swarm Comparison Test Analysis Script

Reads CSV files from test runs and generates:
- Statistical summaries (mean, median, stddev, percentiles)
- Comparison visualizations (box plots, line charts, bar charts)
- HTML report with tables and plots
"""

import argparse
import sys
import os
from pathlib import Path
import pandas as pd
import numpy as np
import matplotlib
matplotlib.use('Agg')  # Non-interactive backend
import matplotlib.pyplot as plt
import seaborn as sns
from datetime import datetime
import base64
from io import BytesIO

# Set style
sns.set_style("whitegrid")
plt.rcParams['figure.figsize'] = (12, 6)
plt.rcParams['font.size'] = 10

def format_bytes(n):
    """Format bytes to human-readable format"""
    for unit in ['B', 'KB', 'MB', 'GB']:
        if n < 1024.0:
            return f"{n:.1f} {unit}"
        n /= 1024.0
    return f"{n:.1f} TB"

def calculate_statistics(df, value_col):
    """Calculate comprehensive statistics for a value column"""
    stats = {
        'count': len(df),
        'mean': df[value_col].mean(),
        'median': df[value_col].median(),
        'std': df[value_col].std(),
        'min': df[value_col].min(),
        'max': df[value_col].max(),
        'p25': df[value_col].quantile(0.25),
        'p75': df[value_col].quantile(0.75),
        'p90': df[value_col].quantile(0.90),
        'p95': df[value_col].quantile(0.95),
        'p99': df[value_col].quantile(0.99),
    }
    return stats

def plot_to_base64(fig):
    """Convert matplotlib figure to base64 encoded string"""
    buf = BytesIO()
    fig.savefig(buf, format='png', dpi=100, bbox_inches='tight')
    buf.seek(0)
    img_base64 = base64.b64encode(buf.read()).decode('utf-8')
    buf.close()
    plt.close(fig)
    return img_base64

def load_data(results_dir):
    """Load all CSV files from results directory"""
    results_path = Path(results_dir)
    
    # Load aggregated files if they exist
    upload_agg_file = results_path / "upload_aggregated.csv"
    download_agg_file = results_path / "download_aggregated.csv"
    
    upload_df = None
    download_df = None

    if upload_agg_file.exists():
        upload_df = pd.read_csv(upload_agg_file)
        print(f"Loaded aggregated upload data: {len(upload_df)} rows")
    else:
        # Load individual files
        upload_files = sorted(results_path.glob("upload_n*.csv"))
        if upload_files:
            upload_dfs = []
            for f in upload_files:
                df = pd.read_csv(f)
                # Extract node_count from filename if not present
                if 'node_count' not in df.columns:
                    node_count = int(f.stem.split('_n')[1])
                    df['node_count'] = node_count
                upload_dfs.append(df)
            if upload_dfs:
                upload_df = pd.concat(upload_dfs, ignore_index=True)
                print(f"Loaded upload data from {len(upload_files)} files: {len(upload_df)} rows")
    
    if download_agg_file.exists():
        download_df = pd.read_csv(download_agg_file)
        if 'cache_mode' not in download_df.columns:
            download_df['cache_mode'] = 'warm'
        print(f"Loaded aggregated download data: {len(download_df)} rows")
    else:
        # Load individual files (download_n10_warm.csv, etc.)
        download_files = sorted(results_path.glob("download_n*.csv"))
        if download_files:
            download_dfs = []
            for f in download_files:
                df = pd.read_csv(f)
                if 'node_count' not in df.columns:
                    stem_part = f.stem.split('_n')[-1]
                    node_count = int(stem_part.split('_')[0]) if stem_part.split('_')[0].isdigit() else 0
                    df['node_count'] = node_count
                if 'cache_mode' not in df.columns:
                    df['cache_mode'] = 'warm'
                download_dfs.append(df)
            if download_dfs:
                download_df = pd.concat(download_dfs, ignore_index=True)
                print(f"Loaded download data from {len(download_files)} files: {len(download_df)} rows")
    
    # Load network hops (dedicated file or extract from upload/download)
    hops_df = None
    hops_files = list(results_path.glob("network_hops*.csv")) + list(results_path.glob("*network_hops*.csv"))
    for f in hops_files:
        try:
            hf = pd.read_csv(f)
            if 'hops' in hf.columns and 'operation' in hf.columns:
                hf = hf[~hf['hops'].isin(['', 'N/A', 'ERROR'])].copy()
                hf['hops'] = pd.to_numeric(hf['hops'], errors='coerce')
                hf = hf.dropna(subset=['hops'])
                if len(hf) > 0:
                    hops_df = hf if hops_df is None else pd.concat([hops_df, hf], ignore_index=True)
                    print(f"Loaded network hops: {len(hf)} rows from {f.name}")
                break
        except Exception:
            pass
    if hops_df is None and upload_df is not None and 'hops' in upload_df.columns:
        rows = []
        for _, row in upload_df.iterrows():
            h = row.get('hops', '')
            if pd.notna(h) and str(h) not in ('', 'N/A', 'ERROR'):
                try:
                    rows.append({
                        'system': row['system'],
                        'operation': 'put',
                        'payload_size': row['payload_size'],
                        'hops': int(float(h))
                    })
                except (ValueError, TypeError):
                    pass
        if rows:
            hops_df = pd.DataFrame(rows)
            print(f"Extracted hops from upload data: {len(hops_df)} rows")
    if hops_df is None and download_df is not None and 'hops' in download_df.columns:
        rows = []
        for _, row in download_df.iterrows():
            h = row.get('hops', '')
            if pd.notna(h) and str(h) not in ('', 'N/A', 'ERROR'):
                try:
                    rows.append({
                        'system': row['system'],
                        'operation': 'get',
                        'payload_size': row['payload_size'],
                        'hops': int(float(h))
                    })
                except (ValueError, TypeError):
                    pass
        if rows:
            hd = pd.DataFrame(rows)
            hops_df = hd if hops_df is None else pd.concat([hops_df, hd], ignore_index=True)
            print(f"Extracted hops from download data: {len(hd)} rows")

    # Load storage efficiency (system, payload_size, nodes, disk_bytes, efficiency_ratio)
    storage_efficiency_df = None
    storage_eff_file = results_path / "storage_efficiency_results.csv"
    if storage_eff_file.exists():
        try:
            se = pd.read_csv(storage_eff_file)
            if all(c in se.columns for c in ['system', 'payload_size', 'nodes', 'disk_bytes', 'efficiency_ratio']):
                se = se.dropna(subset=['disk_bytes', 'efficiency_ratio'], how='all')
                se['disk_bytes'] = pd.to_numeric(se['disk_bytes'], errors='coerce')
                se['efficiency_ratio'] = pd.to_numeric(se['efficiency_ratio'], errors='coerce')
                se = se.dropna(subset=['disk_bytes', 'efficiency_ratio'])
                if len(se) > 0:
                    storage_efficiency_df = se
                    print(f"Loaded storage efficiency: {len(storage_efficiency_df)} rows")
        except Exception:
            pass

    # Load resource usage (timestamp, container, cpu_pct, mem_usage_mb)
    resource_df = None
    resource_file = results_path / "resource_usage.csv"
    if resource_file.exists():
        try:
            rf = pd.read_csv(resource_file)
            if all(c in rf.columns for c in ['timestamp', 'container', 'cpu_pct', 'mem_usage_mb']):
                rf['cpu_pct'] = pd.to_numeric(rf['cpu_pct'], errors='coerce')
                rf['mem_usage_mb'] = pd.to_numeric(rf['mem_usage_mb'], errors='coerce')
                rf = rf.dropna(subset=['cpu_pct', 'mem_usage_mb'], how='all')
                rf['system'] = rf['container'].apply(
                    lambda x: 'our_system' if str(x).startswith('fall25-') else ('swarm' if str(x).startswith('swarm-') else 'other')
                )
                rf = rf[rf['system'] != 'other']
                if len(rf) > 0:
                    resource_df = rf
                    print(f"Loaded resource usage: {len(resource_df)} rows")
        except Exception:
            pass

    # Load replication results (system, payload_size, nodes, replicas_target, time_to_R_s)
    replication_df = None
    repl_file = results_path / "replication_results.csv"
    if repl_file.exists():
        try:
            rf = pd.read_csv(repl_file)
            if all(c in rf.columns for c in ['system', 'payload_size', 'nodes', 'replicas_target', 'time_to_R_s']):
                replication_df = rf
                print(f"Loaded replication results: {len(replication_df)} rows")
        except Exception:
            pass

    partition_recovery_df = None
    part_file = results_path / "partition_recovery_results.csv"
    if part_file.exists():
        try:
            pf = pd.read_csv(part_file)
            if all(c in pf.columns for c in ['system', 'node_count', 'partition_size', 'recovery_time_s']):
                pf = pf[pf['recovery_time_s'] != 'TIMEOUT'].copy()
                pf['recovery_time_s'] = pd.to_numeric(pf['recovery_time_s'], errors='coerce')
                pf = pf.dropna(subset=['recovery_time_s'])
                if len(pf) > 0:
                    partition_recovery_df = pf
                    print(f"Loaded partition recovery results: {len(partition_recovery_df)} rows")
        except Exception:
            pass

    lookup_complexity_df = None
    for lc_name in ("lookup_complexity_results.csv", "lookup_complexity.csv"):
        lc_file = results_path / lc_name
        if not lc_file.exists():
            continue
        try:
            lc = pd.read_csv(lc_file)
            if all(c in lc.columns for c in ['system', 'node_count', 'operation', 'hops']):
                lc = lc[~lc['hops'].isin(['N/A', '', 'FAILED'])].copy()
                lc['hops'] = pd.to_numeric(lc['hops'], errors='coerce')
                lc = lc.dropna(subset=['hops'])
                if len(lc) > 0:
                    lookup_complexity_df = lc
                    print(f"Loaded lookup complexity results: {len(lookup_complexity_df)} rows from {lc_name}")
                    break
        except Exception:
            pass

    replication_distribution_df = None
    repl_dist_file = results_path / "replication_distribution.csv"
    if repl_dist_file.exists():
        try:
            rdf = pd.read_csv(repl_dist_file)
            if all(c in rdf.columns for c in ['system', 'node_count', 'near', 'midrange', 'farflung']):
                replication_distribution_df = rdf
                print(f"Loaded replication distribution: {len(replication_distribution_df)} rows")
        except Exception:
            pass

    routing_overhead_df = None
    ro_file = results_path / "routing_overhead_results.csv"
    if ro_file.exists():
        try:
            rof = pd.read_csv(ro_file)
            if all(c in rof.columns for c in ['system', 'operation', 'message_count', 'overhead_type']):
                routing_overhead_df = rof
                print(f"Loaded routing overhead: {len(routing_overhead_df)} rows")
        except Exception:
            pass

    lookup_latency_df = None
    import re
    ll_files = [results_path / "lookup_latency_results.csv"] + sorted(results_path.glob("lookup_latency_n*.csv"))
    ll_dfs = []
    for ll_file in ll_files:
        if not ll_file.exists():
            continue
        try:
            ll = pd.read_csv(ll_file)
            if 'lookup_latency_ms' not in ll.columns:
                continue
            ll = ll[~ll['lookup_latency_ms'].isin(['N/A', '', 'FAILED'])].copy()
            ll['lookup_latency_ms'] = pd.to_numeric(ll['lookup_latency_ms'], errors='coerce')
            ll = ll.dropna(subset=['lookup_latency_ms'])
            if len(ll) > 0:
                m = re.search(r'n(\d+)', ll_file.name)
                if m and 'node_count' not in ll.columns:
                    ll['node_count'] = int(m.group(1))
                ll_dfs.append(ll)
        except Exception:
            pass
    if ll_dfs:
        lookup_latency_df = pd.concat(ll_dfs, ignore_index=True)
        print(f"Loaded lookup latency: {len(lookup_latency_df)} rows")

    repair_time_df = None
    repair_file = results_path / "repair_time_results.csv"
    if repair_file.exists():
        try:
            rtf = pd.read_csv(repair_file)
            if all(c in rtf.columns for c in ['system', 'node_count', 'repair_time_s']):
                repair_time_df = rtf
                print(f"Loaded repair time results: {len(repair_time_df)} rows")
        except Exception:
            pass

    concurrent_df = None
    conc_file = results_path / "concurrent_results.csv"
    if conc_file.exists():
        try:
            cf = pd.read_csv(conc_file)
            if all(c in cf.columns for c in ['system', 'concurrent_writes', 'concurrent_reads', 'throughput_mbps', 'p99_latency_ms']):
                cf['throughput_mbps'] = pd.to_numeric(cf['throughput_mbps'], errors='coerce')
                cf['p99_latency_ms'] = pd.to_numeric(cf['p99_latency_ms'], errors='coerce')
                cf['concurrency_label'] = cf['concurrent_writes'].astype(str) + 'w/' + cf['concurrent_reads'].astype(str) + 'r'
                cf['concurrency_level'] = cf['concurrent_writes'] + cf['concurrent_reads']
                if len(cf) > 0:
                    concurrent_df = cf
                    print(f"Loaded concurrent results: {len(concurrent_df)} rows")
        except Exception:
            pass

    return upload_df, download_df, hops_df, resource_df, storage_efficiency_df, replication_df, partition_recovery_df, lookup_complexity_df, concurrent_df, replication_distribution_df, repair_time_df, routing_overhead_df, lookup_latency_df

def generate_upload_plots(upload_df):
    """Generate upload latency comparison plots"""
    plots = {}
    
    if upload_df is None or len(upload_df) == 0:
        return plots
    
    # Filter out error rows
    upload_df = upload_df[upload_df['latency_ms'] != 'ERROR'].copy()
    upload_df['latency_ms'] = pd.to_numeric(upload_df['latency_ms'], errors='coerce')
    upload_df = upload_df.dropna(subset=['latency_ms'])

    if len(upload_df) == 0:
        return plots

    # Paper / suite default: batch 10 and 20 were dropped from run_comparison (slow, noisy).
    if 'batch_size' in upload_df.columns:
        upload_df['batch_size'] = pd.to_numeric(upload_df['batch_size'], errors='coerce').fillna(1).astype(int)
        _rows_before = len(upload_df)
        upload_df = upload_df[upload_df['batch_size'].isin([1, 5])].copy()
        if len(upload_df) == 0:
            return plots
        if _rows_before > len(upload_df):
            print(f"Upload plots: batch_size restricted to {{1,5}} ({_rows_before} -> {len(upload_df)} rows)")

    # Derive throughput: batch when total_batch_ms available, else per-file
    if 'total_batch_ms' in upload_df.columns and 'batch_size' in upload_df.columns:
        upload_df['total_batch_ms'] = pd.to_numeric(upload_df['total_batch_ms'], errors='coerce')
        upload_df['batch_size'] = pd.to_numeric(upload_df['batch_size'], errors='coerce').fillna(1).astype(int)
        mask = upload_df['total_batch_ms'].notna() & (upload_df['total_batch_ms'] > 0)
        upload_df.loc[mask, 'throughput_mbps'] = (
            (upload_df.loc[mask, 'payload_size'] * upload_df.loc[mask, 'batch_size'])
            / (upload_df.loc[mask, 'total_batch_ms'] / 1000) / 1e6
        )
        upload_df.loc[~mask, 'throughput_mbps'] = (
            upload_df.loc[~mask, 'payload_size'] / (upload_df.loc[~mask, 'latency_ms'] / 1000) / 1e6
        )
    else:
        upload_df['throughput_mbps'] = upload_df['payload_size'] / (upload_df['latency_ms'] / 1000) / 1e6

    # Convert payload_size to readable format
    upload_df['payload_size_str'] = upload_df['payload_size'].apply(format_bytes)
    
    # 1. Box plot: Latency by system and payload size
    fig, ax = plt.subplots(figsize=(14, 8))
    if 'node_count' in upload_df.columns:
        # Group by system, payload_size, and node_count
        sns.boxplot(data=upload_df, x='payload_size_str', y='latency_ms', 
                   hue='system', ax=ax)
        ax.set_title('Upload Latency Comparison: Box Plot by Payload Size', fontsize=14, fontweight='bold')
    else:
        sns.boxplot(data=upload_df, x='payload_size_str', y='latency_ms', 
                   hue='system', ax=ax)
        ax.set_title('Upload Latency Comparison: Box Plot by Payload Size', fontsize=14, fontweight='bold')
    ax.set_xlabel('Payload Size', fontsize=12)
    ax.set_ylabel('Latency (ms)', fontsize=12)
    ax.legend(title='System', fontsize=10)
    plt.xticks(rotation=45, ha='right')
    plots['upload_box'] = plot_to_base64(fig)
    
    # 2. Line chart: Mean latency by payload size
    fig, ax = plt.subplots(figsize=(12, 6))
    mean_latency = upload_df.groupby(['system', 'payload_size'])['latency_ms'].mean().reset_index()
    for system in upload_df['system'].unique():
        system_data = mean_latency[mean_latency['system'] == system]
        ax.plot(system_data['payload_size'], system_data['latency_ms'], 
               marker='o', label=system, linewidth=2, markersize=8)
    ax.set_xlabel('Payload Size (bytes)', fontsize=12)
    ax.set_ylabel('Mean Latency (ms)', fontsize=12)
    ax.set_title('Upload Latency: Mean by Payload Size', fontsize=14, fontweight='bold')
    ax.legend(fontsize=10)
    ax.set_xscale('log')
    ax.grid(True, alpha=0.3)
    plots['upload_line_mean'] = plot_to_base64(fig)
    
    # 3. Bar chart: Mean latency comparison (if node_count available)
    if 'node_count' in upload_df.columns:
        fig, ax = plt.subplots(figsize=(14, 8))
        mean_by_nodes = upload_df.groupby(['system', 'node_count', 'payload_size'])['latency_ms'].mean().reset_index()
        x_pos = np.arange(len(mean_by_nodes['node_count'].unique()))
        width = 0.35
        
        node_counts = sorted(mean_by_nodes['node_count'].unique())
        payload_sizes = sorted(mean_by_nodes['payload_size'].unique())
        
        for idx, payload_size in enumerate(payload_sizes):
            our_data = mean_by_nodes[(mean_by_nodes['system'] == 'our_system') & 
                                     (mean_by_nodes['payload_size'] == payload_size)]
            swarm_data = mean_by_nodes[(mean_by_nodes['system'] == 'swarm') & 
                                      (mean_by_nodes['payload_size'] == payload_size)]
            
            our_means = [our_data[our_data['node_count'] == n]['latency_ms'].values[0] 
                        if len(our_data[our_data['node_count'] == n]) > 0 else 0 
                        for n in node_counts]
            swarm_means = [swarm_data[swarm_data['node_count'] == n]['latency_ms'].values[0] 
                          if len(swarm_data[swarm_data['node_count'] == n]) > 0 else 0 
                          for n in node_counts]
            
            x = np.arange(len(node_counts)) + idx * (width * 2 + 0.1)
            ax.bar(x - width/2, our_means, width, label=f'Our System ({format_bytes(payload_size)})', alpha=0.8)
            ax.bar(x + width/2, swarm_means, width, label=f'Swarm ({format_bytes(payload_size)})', alpha=0.8)
        
        ax.set_xlabel('Node Count', fontsize=12)
        ax.set_ylabel('Mean Latency (ms)', fontsize=12)
        ax.set_title('Upload Latency: Mean by Node Count and Payload Size', fontsize=14, fontweight='bold')
        ax.set_xticks(np.arange(len(node_counts)) + (len(payload_sizes) - 1) * (width * 2 + 0.1) / 2)
        ax.set_xticklabels(node_counts)
        ax.legend(fontsize=9, ncol=2)
        ax.grid(True, alpha=0.3, axis='y')
        plots['upload_bar_nodes'] = plot_to_base64(fig)

    # 4. Box plot: Upload throughput by system and payload size
    fig, ax = plt.subplots(figsize=(14, 8))
    sns.boxplot(data=upload_df, x='payload_size_str', y='throughput_mbps', hue='system', ax=ax)
    ax.set_xlabel('Payload Size', fontsize=12)
    ax.set_ylabel('Throughput (MB/s)', fontsize=12)
    ax.set_title('Upload Throughput Comparison by Payload Size', fontsize=14, fontweight='bold')
    ax.legend(title='System', fontsize=10)
    plt.xticks(rotation=45, ha='right')
    plots['upload_throughput_box'] = plot_to_base64(fig)

    # 5. Line chart: Mean throughput by payload size
    fig, ax = plt.subplots(figsize=(12, 6))
    mean_throughput = upload_df.groupby(['system', 'payload_size'])['throughput_mbps'].mean().reset_index()
    for system in upload_df['system'].unique():
        system_data = mean_throughput[mean_throughput['system'] == system]
        ax.plot(system_data['payload_size'], system_data['throughput_mbps'],
               marker='o', label=system, linewidth=2, markersize=8)
    ax.set_xlabel('Payload Size (bytes)', fontsize=12)
    ax.set_ylabel('Mean Throughput (MB/s)', fontsize=12)
    ax.set_title('Upload Throughput: Mean by Payload Size', fontsize=14, fontweight='bold')
    ax.legend(fontsize=10)
    ax.set_xscale('log')
    ax.grid(True, alpha=0.3)
    plots['upload_throughput_line'] = plot_to_base64(fig)

    return plots

def generate_download_plots(download_df):
    """Generate download latency comparison plots (cache_mode in CSV, typically warm)."""
    plots = {}
    
    if download_df is None or len(download_df) == 0:
        return plots
    
    # Filter out error rows
    download_df = download_df[
        (download_df['ttfb_ms'] != 'ERROR') &
        (download_df['total_ms'] != 'ERROR')
    ].copy()
    download_df['ttfb_ms'] = pd.to_numeric(download_df['ttfb_ms'], errors='coerce')
    download_df['total_ms'] = pd.to_numeric(download_df['total_ms'], errors='coerce')
    download_df = download_df.dropna(subset=['ttfb_ms', 'total_ms'])
    
    if 'cache_mode' not in download_df.columns:
        download_df['cache_mode'] = 'warm'
    
    if len(download_df) == 0:
        return plots
    
    download_df['payload_size_str'] = download_df['payload_size'].apply(format_bytes)
    
    # 1. Box plot: TTFB by system, payload size, and cache_mode
    fig, ax = plt.subplots(figsize=(14, 8))
    hue_order = ['our_system', 'swarm'] if 'our_system' in download_df['system'].values else None
    sns.boxplot(data=download_df, x='payload_size_str', y='ttfb_ms',
                hue='system', ax=ax)
    ax.set_title('Download TTFB by System and Payload Size', fontsize=14, fontweight='bold')
    ax.set_xlabel('Payload Size', fontsize=12)
    ax.set_ylabel('TTFB (ms)', fontsize=12)
    ax.legend(title='System', fontsize=10)
    plt.xticks(rotation=45, ha='right')
    plots['download_ttfb_box'] = plot_to_base64(fig)
    
    # 2. Box plot: TTFB by cache_mode (usually single mode: warm)
    fig, ax = plt.subplots(figsize=(12, 6))
    sns.boxplot(data=download_df, x='cache_mode', y='ttfb_ms', hue='system', ax=ax)
    ax.set_title('Download TTFB by Cache Mode', fontsize=14, fontweight='bold')
    ax.set_xlabel('Cache Mode', fontsize=12)
    ax.set_ylabel('TTFB (ms)', fontsize=12)
    ax.legend(title='System', fontsize=10)
    plots['download_ttfb_cold_warm'] = plot_to_base64(fig)
    
    # 3. Box plot: Total download time by system and payload size
    fig, ax = plt.subplots(figsize=(14, 8))
    sns.boxplot(data=download_df, x='payload_size_str', y='total_ms',
                hue='system', ax=ax)
    ax.set_title('Total Download Time by System and Payload Size', fontsize=14, fontweight='bold')
    ax.set_xlabel('Payload Size', fontsize=12)
    ax.set_ylabel('Total Time (ms)', fontsize=12)
    ax.legend(title='System', fontsize=10)
    plt.xticks(rotation=45, ha='right')
    plots['download_total_box'] = plot_to_base64(fig)
    
    # 4. Line chart: Mean TTFB by payload size (facet by cache_mode)
    group_cols = ['system', 'payload_size']
    if 'cache_mode' in download_df.columns:
        group_cols.append('cache_mode')
    mean_ttfb = download_df.groupby(group_cols)['ttfb_ms'].mean().reset_index()
    fig, ax = plt.subplots(figsize=(12, 6))
    for system in download_df['system'].unique():
        for cm in download_df['cache_mode'].unique():
            sub = mean_ttfb[(mean_ttfb['system'] == system) & (mean_ttfb['cache_mode'] == cm)]
            if len(sub) > 0:
                ax.plot(sub['payload_size'], sub['ttfb_ms'], marker='o',
                       label=f'{system} ({cm})', linewidth=2, markersize=8)
    ax.set_xlabel('Payload Size (bytes)', fontsize=12)
    ax.set_ylabel('Mean TTFB (ms)', fontsize=12)
    ax.set_title('Download TTFB by Payload Size and Cache Mode', fontsize=14, fontweight='bold')
    ax.legend(fontsize=9)
    ax.set_xscale('log')
    ax.grid(True, alpha=0.3)
    plots['download_ttfb_line'] = plot_to_base64(fig)
    
    # 5. Line chart: Mean total time by payload size (facet by cache_mode)
    mean_total = download_df.groupby(group_cols)['total_ms'].mean().reset_index()
    fig, ax = plt.subplots(figsize=(12, 6))
    for system in download_df['system'].unique():
        for cm in download_df['cache_mode'].unique():
            sub = mean_total[(mean_total['system'] == system) & (mean_total['cache_mode'] == cm)]
            if len(sub) > 0:
                ax.plot(sub['payload_size'], sub['total_ms'], marker='o',
                       label=f'{system} ({cm})', linewidth=2, markersize=8)
    ax.set_xlabel('Payload Size (bytes)', fontsize=12)
    ax.set_ylabel('Mean Total Time (ms)', fontsize=12)
    ax.set_title('Download Total Time: Cold vs Warm by Payload Size', fontsize=14, fontweight='bold')
    ax.legend(fontsize=9)
    ax.set_xscale('log')
    ax.grid(True, alpha=0.3)
    plots['download_total_line'] = plot_to_base64(fig)
    
    return plots

def generate_network_hops_plots(hops_df):
    """Generate network hops comparison plots (system, operation, payload_size, hops)"""
    plots = {}
    if hops_df is None or len(hops_df) == 0:
        return plots
    hops_df = hops_df.copy()
    hops_df['payload_size_str'] = hops_df['payload_size'].apply(format_bytes)
    if 'operation' not in hops_df.columns:
        return plots
    fig, ax = plt.subplots(figsize=(14, 8))
    order = hops_df.groupby('payload_size_str')['payload_size'].first().sort_values().index.tolist()
    sns.boxplot(data=hops_df, x='payload_size_str', y='hops', hue='system',
                order=order if order else None, ax=ax)
    ax.set_title('Network Hops: DHT Lookup Hops by System and Payload Size', fontsize=14, fontweight='bold')
    ax.set_xlabel('Payload Size', fontsize=12)
    ax.set_ylabel('Hops', fontsize=12)
    ax.legend(title='System', fontsize=10)
    plt.xticks(rotation=45, ha='right')
    plots['network_hops_box'] = plot_to_base64(fig)
    fig2, ax2 = plt.subplots(figsize=(12, 6))
    mean_hops = hops_df.groupby(['system', 'operation', 'payload_size'])['hops'].mean().reset_index()
    for (sys, op), grp in mean_hops.groupby(['system', 'operation']):
        ax2.plot(grp['payload_size'], grp['hops'], marker='o', label=f'{sys} ({op})', linewidth=2, markersize=8)
    ax2.set_xlabel('Payload Size (bytes)', fontsize=12)
    ax2.set_ylabel('Mean Hops', fontsize=12)
    ax2.set_title('Network Hops: Mean by Operation and Payload Size', fontsize=14, fontweight='bold')
    ax2.legend(fontsize=10)
    ax2.set_xscale('log')
    ax2.grid(True, alpha=0.3)
    plots['network_hops_line'] = plot_to_base64(fig2)
    return plots

def generate_resource_plots(resource_df):
    """Generate CPU/memory resource usage plots (mean/peak per system)"""
    plots = {}
    if resource_df is None or len(resource_df) == 0:
        return plots
    rdf = resource_df.copy()
    rdf['ts'] = pd.to_datetime(rdf['timestamp'], errors='coerce')
    rdf = rdf.dropna(subset=['ts'])
    if len(rdf) == 0:
        return plots

    # Box plot: CPU by system
    fig, ax = plt.subplots(figsize=(10, 6))
    sns.boxplot(data=rdf, x='system', y='cpu_pct', ax=ax)
    ax.set_title('CPU Usage (%) by System', fontsize=14, fontweight='bold')
    ax.set_xlabel('System', fontsize=12)
    ax.set_ylabel('CPU %', fontsize=12)
    plots['resource_cpu_box'] = plot_to_base64(fig)

    # Box plot: Memory by system
    fig, ax = plt.subplots(figsize=(10, 6))
    sns.boxplot(data=rdf, x='system', y='mem_usage_mb', ax=ax)
    ax.set_title('Memory Usage (MB) by System', fontsize=14, fontweight='bold')
    ax.set_xlabel('System', fontsize=12)
    ax.set_ylabel('Memory (MB)', fontsize=12)
    plots['resource_mem_box'] = plot_to_base64(fig)

    # Line chart: CPU over time (mean per timestamp, by system)
    fig, ax = plt.subplots(figsize=(12, 6))
    for system in rdf['system'].unique():
        sub = rdf[rdf['system'] == system].groupby('ts')['cpu_pct'].mean().reset_index()
        ax.plot(sub['ts'], sub['cpu_pct'], label=system, linewidth=2)
    ax.set_xlabel('Time', fontsize=12)
    ax.set_ylabel('Mean CPU %', fontsize=12)
    ax.set_title('CPU Usage Over Time (mean per sample)', fontsize=14, fontweight='bold')
    ax.legend(fontsize=10)
    ax.grid(True, alpha=0.3)
    plt.xticks(rotation=45, ha='right')
    plots['resource_cpu_time'] = plot_to_base64(fig)

    # Line chart: Memory over time
    fig, ax = plt.subplots(figsize=(12, 6))
    for system in rdf['system'].unique():
        sub = rdf[rdf['system'] == system].groupby('ts')['mem_usage_mb'].mean().reset_index()
        ax.plot(sub['ts'], sub['mem_usage_mb'], label=system, linewidth=2)
    ax.set_xlabel('Time', fontsize=12)
    ax.set_ylabel('Mean Memory (MB)', fontsize=12)
    ax.set_title('Memory Usage Over Time (mean per sample)', fontsize=14, fontweight='bold')
    ax.legend(fontsize=10)
    ax.grid(True, alpha=0.3)
    plt.xticks(rotation=45, ha='right')
    plots['resource_mem_time'] = plot_to_base64(fig)

    return plots

def generate_replication_plots(replication_df):
    """Generate replication speed comparison (time to R replicas)"""
    plots = {}
    if replication_df is None or len(replication_df) == 0:
        return plots
    rf = replication_df.copy()
    rf['time_to_R_s'] = pd.to_numeric(rf['time_to_R_s'], errors='coerce')
    rf = rf.dropna(subset=['time_to_R_s'])
    rf = rf[rf['time_to_R_s'] > 0]
    if len(rf) == 0:
        return plots
    rf['payload_size_str'] = rf['payload_size'].apply(format_bytes)
    fig, ax = plt.subplots(figsize=(10, 6))
    sns.barplot(data=rf, x='payload_size_str', y='time_to_R_s', hue='system', ax=ax)
    ax.set_xlabel('Payload Size', fontsize=12)
    ax.set_ylabel('Time to R Replicas (s)', fontsize=12)
    ax.set_title('Replication Speed: Time to Reach Target Replicas', fontsize=14, fontweight='bold')
    ax.legend(title='System')
    plt.xticks(rotation=45, ha='right')
    plots['replication_bar'] = plot_to_base64(fig)
    return plots

def generate_lookup_complexity_plots(lookup_complexity_df):
    """Plots for lookup hop counts vs N. Uses operation=lookup (CSV from lookup_complexity_test.sh)."""
    plots = {}
    if lookup_complexity_df is None or len(lookup_complexity_df) == 0:
        return plots
    lc = lookup_complexity_df.copy()
    lc_plot = lc[lc['operation'] == 'lookup'].copy()
    if len(lc_plot) == 0:
        lc_plot = lc[lc['operation'] == 'get'].copy()
    if len(lc_plot) == 0:
        return plots
    agg = lc_plot.groupby(['system', 'node_count'])['hops'].agg(['mean', 'std', 'count']).reset_index()
    agg['log_N'] = np.log10(agg['node_count'].clip(lower=1))
    fig, ax = plt.subplots(figsize=(10, 6))
    for system in agg['system'].unique():
        sub = agg[agg['system'] == system].sort_values('node_count')
        slope_str = ''
        if len(sub) > 1:
            slope, intercept = np.polyfit(sub['log_N'], sub['mean'], 1)
            slope_str = f' (slope={slope:.2f})'
        ax.plot(sub['node_count'], sub['mean'], 'o-', label=f'{system}{slope_str}', linewidth=2, markersize=8)
    ax.set_xscale('log')
    ax.set_xlabel('Node Count (N)', fontsize=12)
    ax.set_ylabel('Mean hops (lookup)', fontsize=12)
    ax.set_title(
        'Lookup: mean DHT query-event hops vs N (Docker vn-IPFS; ideal O(log N) approximate)',
        fontsize=12,
        fontweight='bold',
    )
    ax.legend(fontsize=9)
    ax.grid(True, alpha=0.3)
    plots['lookup_complexity_hops_vs_n'] = plot_to_base64(fig)
    fig2, ax2 = plt.subplots(figsize=(10, 6))
    for system in agg['system'].unique():
        sub = agg[agg['system'] == system].sort_values('log_N')
        slope_str = ''
        if len(sub) > 1:
            slope, intercept = np.polyfit(sub['log_N'], sub['mean'], 1)
            ax2.plot(sub['log_N'], slope * sub['log_N'] + intercept, '--', alpha=0.5)
            slope_str = f' slope={slope:.2f}'
        ax2.plot(sub['log_N'], sub['mean'], 'o-', label=f'{system}{slope_str}', linewidth=2, markersize=8)
    ax2.set_xlabel('log10(N)', fontsize=12)
    ax2.set_ylabel('Mean hops (lookup)', fontsize=12)
    ax2.set_title('Lookup: hops vs log10(N) (slope ~1 is textbook ideal, not guaranteed here)', fontsize=12, fontweight='bold')
    ax2.legend(fontsize=9)
    ax2.grid(True, alpha=0.3)
    plots['lookup_complexity_log_n'] = plot_to_base64(fig2)

    # Log-log plot: O(log N) vs O(N) reference (slope 1 in log-log = O(N))
    fig3, ax3 = plt.subplots(figsize=(10, 6))
    node_vals = np.array(sorted(agg['node_count'].unique()))
    node_vals = node_vals[node_vals >= 1]
    for system in agg['system'].unique():
        sub = agg[agg['system'] == system].sort_values('node_count')
        if len(sub) < 2:
            ax3.plot(sub['node_count'], sub['mean'], 'o-', label=system, linewidth=2, markersize=8)
            continue
        ax3.plot(sub['node_count'], sub['mean'], 'o-', label=system, linewidth=2, markersize=8)
        slope, intercept = np.polyfit(sub['log_N'], sub['mean'], 1)
        ax3.plot(sub['node_count'], slope * sub['log_N'] + intercept, '--', alpha=0.6, label=f'{system} O(log N) fit')
    if len(node_vals) >= 2:
        y0 = max(agg['mean'].min(), 0.5)
        n0 = max(node_vals.min(), 1)
        o_n_ref = y0 * (node_vals.astype(float) / n0)
        ax3.plot(node_vals, o_n_ref, ':', color='gray', linewidth=2, label='O(N) reference')
    ax3.set_xscale('log')
    ax3.set_yscale('log')
    ax3.set_xlabel('Node Count N (log scale)', fontsize=12)
    ax3.set_ylabel('Mean Hops (log scale)', fontsize=12)
    ax3.set_title('Log-log: compare to O(N) reference; flat hops vs N is common when measurement is noisy', fontsize=12, fontweight='bold')
    ax3.legend(fontsize=9)
    ax3.grid(True, alpha=0.3)
    ax3.set_ylim(bottom=0.5)
    plots['lookup_complexity_loglog'] = plot_to_base64(fig3)
    return plots

def generate_concurrent_plots(concurrent_df):
    """Generate throughput vs concurrency and p99 vs concurrency plots"""
    plots = {}
    if concurrent_df is None or len(concurrent_df) == 0:
        return plots
    cdf = concurrent_df.dropna(subset=['throughput_mbps', 'p99_latency_ms'])
    if len(cdf) == 0:
        return plots

    fig, ax = plt.subplots(figsize=(10, 6))
    for system in cdf['system'].unique():
        sub = cdf[cdf['system'] == system].sort_values('concurrency_level')
        ax.plot(sub['concurrency_label'], sub['throughput_mbps'], 'o-', label=system, linewidth=2, markersize=8)
    ax.set_xlabel('Concurrency (writes/reads)', fontsize=12)
    ax.set_ylabel('Throughput (MB/s)', fontsize=12)
    ax.set_title('Throughput vs Concurrency', fontsize=14, fontweight='bold')
    ax.legend()
    ax.grid(True, alpha=0.3)
    plt.xticks(rotation=45, ha='right')
    plots['concurrent_throughput_vs_concurrency'] = plot_to_base64(fig)

    fig2, ax2 = plt.subplots(figsize=(10, 6))
    for system in cdf['system'].unique():
        sub = cdf[cdf['system'] == system].sort_values('concurrency_level')
        ax2.plot(sub['concurrency_label'], sub['p99_latency_ms'], 'o-', label=system, linewidth=2, markersize=8)
    ax2.set_xlabel('Concurrency (writes/reads)', fontsize=12)
    ax2.set_ylabel('p99 Latency (ms)', fontsize=12)
    ax2.set_title('p99 Latency vs Concurrency', fontsize=14, fontweight='bold')
    ax2.legend()
    ax2.grid(True, alpha=0.3)
    plt.xticks(rotation=45, ha='right')
    plots['concurrent_p99_vs_concurrency'] = plot_to_base64(fig2)

    return plots

def generate_partition_recovery_plots(partition_recovery_df):
    """Generate partition recovery time comparison (system, node_count, partition_size, recovery_time_s)"""
    plots = {}
    if partition_recovery_df is None or len(partition_recovery_df) == 0:
        return plots
    prf = partition_recovery_df.copy()
    fig, ax = plt.subplots(figsize=(10, 6))
    sns.barplot(data=prf, x='partition_size', y='recovery_time_s', hue='system', ax=ax)
    ax.set_xlabel('Partition Size (nodes disconnected)', fontsize=12)
    ax.set_ylabel('Recovery Time (s)', fontsize=12)
    ax.set_title('Partition Recovery: Time from Reconnect Until Content on Partitioned Nodes', fontsize=14, fontweight='bold')
    ax.legend(title='System')
    plots['partition_recovery_bar'] = plot_to_base64(fig)
    return plots

def generate_storage_efficiency_plots(storage_efficiency_df):
    """Generate storage efficiency bar chart"""
    plots = {}
    if storage_efficiency_df is None or len(storage_efficiency_df) == 0:
        return plots
    se = storage_efficiency_df.dropna(subset=['efficiency_ratio'])
    if len(se) == 0:
        return plots
    fig, ax = plt.subplots(figsize=(10, 6))
    x = np.arange(len(se))
    colors = ['#3498db' if s == 'our_system' else '#e74c3c' for s in se['system']]
    ax.bar(x, se['efficiency_ratio'], color=colors)
    ax.set_xticks(x)
    ax.set_xticklabels([f"{row['system']}\n{format_bytes(row['payload_size'])}" for _, row in se.iterrows()], rotation=0, ha='center')
    ax.set_ylabel('Efficiency Ratio', fontsize=12)
    ax.set_title('Storage Efficiency: (payload_size * replication) / disk_bytes', fontsize=14, fontweight='bold')
    from matplotlib.patches import Patch
    ax.legend(handles=[Patch(facecolor='#3498db', label='our_system'), Patch(facecolor='#e74c3c', label='swarm')])
    plots['storage_efficiency_bar'] = plot_to_base64(fig)
    return plots

def generate_statistics_tables(upload_df, download_df, hops_df=None, resource_df=None, storage_efficiency_df=None, replication_df=None, partition_recovery_df=None, lookup_complexity_df=None, concurrent_df=None, replication_distribution_df=None, repair_time_df=None, routing_overhead_df=None, lookup_latency_df=None):
    """Generate HTML tables with statistics"""
    tables = {}
    
    if upload_df is not None and len(upload_df) > 0:
        # Filter out errors
        upload_df_clean = upload_df[upload_df['latency_ms'] != 'ERROR'].copy()
        upload_df_clean['latency_ms'] = pd.to_numeric(upload_df_clean['latency_ms'], errors='coerce')
        upload_df_clean = upload_df_clean.dropna(subset=['latency_ms'])
        
        if len(upload_df_clean) > 0:
            if 'batch_size' in upload_df_clean.columns:
                upload_df_clean['batch_size'] = pd.to_numeric(upload_df_clean['batch_size'], errors='coerce').fillna(1).astype(int)
                upload_df_clean = upload_df_clean[upload_df_clean['batch_size'].isin([1, 5])].copy()
        if len(upload_df_clean) > 0:
            # Derive throughput: batch (total_bytes/total_batch_s) when available
            if 'total_batch_ms' in upload_df_clean.columns and 'batch_size' in upload_df_clean.columns:
                upload_df_clean['total_batch_ms'] = pd.to_numeric(upload_df_clean['total_batch_ms'], errors='coerce')
                upload_df_clean['batch_size'] = pd.to_numeric(upload_df_clean['batch_size'], errors='coerce').fillna(1).astype(int)
                mask = upload_df_clean['total_batch_ms'].notna() & (upload_df_clean['total_batch_ms'] > 0)
                upload_df_clean.loc[mask, 'throughput_mbps'] = (
                    (upload_df_clean.loc[mask, 'payload_size'] * upload_df_clean.loc[mask, 'batch_size'])
                    / (upload_df_clean.loc[mask, 'total_batch_ms'] / 1000) / 1e6
                )
                upload_df_clean.loc[~mask, 'throughput_mbps'] = (
                    upload_df_clean.loc[~mask, 'payload_size'] / (upload_df_clean.loc[~mask, 'latency_ms'] / 1000) / 1e6
                )
            else:
                upload_df_clean['throughput_mbps'] = (
                    upload_df_clean['payload_size'] / (upload_df_clean['latency_ms'] / 1000) / 1e6
                )
            # Statistics by system, payload size, and batch_size when present
            stats_rows = []
            tput_stats_rows = []
            group_cols = ['system', 'payload_size']
            if 'batch_size' in upload_df_clean.columns:
                group_cols.append('batch_size')
            for keys, subset in upload_df_clean.groupby(group_cols):
                if len(subset) == 0:
                    continue
                keys = keys if isinstance(keys, tuple) else (keys,)
                system = keys[0]
                payload_size = keys[1]
                batch_size = int(keys[2]) if len(keys) > 2 else 1
                stats = calculate_statistics(subset, 'latency_ms')
                tput_stats = calculate_statistics(subset, 'throughput_mbps')
                batch_label = f" (batch={batch_size})" if batch_size > 1 else ""
                stats_rows.append({
                    'System': system,
                    'Payload Size': format_bytes(payload_size) + batch_label,
                    'Count': stats['count'],
                    'Mean (ms)': f"{stats['mean']:.2f}",
                    'Median (ms)': f"{stats['median']:.2f}",
                    'Std Dev (ms)': f"{stats['std']:.2f}",
                    'Min (ms)': f"{stats['min']:.2f}",
                    'Max (ms)': f"{stats['max']:.2f}",
                    'P95 (ms)': f"{stats['p95']:.2f}",
                    'P99 (ms)': f"{stats['p99']:.2f}",
                    'Batch Throughput (MB/s)': f"{tput_stats['mean']:.2f}",
                })
                tput_stats_rows.append({
                    'System': system,
                    'Payload Size': format_bytes(payload_size) + batch_label,
                    'Count': tput_stats['count'],
                    'Mean (MB/s)': f"{tput_stats['mean']:.2f}",
                    'Median (MB/s)': f"{tput_stats['median']:.2f}",
                    'Std Dev': f"{tput_stats['std']:.2f}",
                    'Min (MB/s)': f"{tput_stats['min']:.2f}",
                    'Max (MB/s)': f"{tput_stats['max']:.2f}",
                })

            upload_stats_df = pd.DataFrame(stats_rows)
            tables['upload_stats'] = upload_stats_df.to_html(index=False, classes='stats-table', table_id='upload-stats')
            if tput_stats_rows:
                upload_throughput_df = pd.DataFrame(tput_stats_rows)
                tables['upload_throughput_stats'] = upload_throughput_df.to_html(index=False, classes='stats-table', table_id='upload-throughput-stats')
    
    if download_df is not None and len(download_df) > 0:
        # Filter out errors
        download_df_clean = download_df[
            (download_df['ttfb_ms'] != 'ERROR') &
            (download_df['total_ms'] != 'ERROR')
        ].copy()
        download_df_clean['ttfb_ms'] = pd.to_numeric(download_df_clean['ttfb_ms'], errors='coerce')
        download_df_clean['total_ms'] = pd.to_numeric(download_df_clean['total_ms'], errors='coerce')
        download_df_clean = download_df_clean.dropna(subset=['ttfb_ms', 'total_ms'])
        if 'cache_mode' not in download_df_clean.columns:
            download_df_clean['cache_mode'] = 'warm'
        
        if len(download_df_clean) > 0:
            # TTFB statistics (group by system, payload_size, cache_mode)
            stats_rows_ttfb = []
            for system in sorted(download_df_clean['system'].unique()):
                for payload_size in sorted(download_df_clean['payload_size'].unique()):
                    for cache_mode in sorted(download_df_clean['cache_mode'].unique()):
                        subset = download_df_clean[
                            (download_df_clean['system'] == system) &
                            (download_df_clean['payload_size'] == payload_size) &
                            (download_df_clean['cache_mode'] == cache_mode)
                        ]
                        if len(subset) > 0:
                            stats = calculate_statistics(subset, 'ttfb_ms')
                            stats_rows_ttfb.append({
                                'System': system,
                                'Payload Size': format_bytes(payload_size),
                                'Cache Mode': cache_mode,
                                'Count': stats['count'],
                                'Mean (ms)': f"{stats['mean']:.2f}",
                                'Median (ms)': f"{stats['median']:.2f}",
                                'Std Dev (ms)': f"{stats['std']:.2f}",
                                'P95 (ms)': f"{stats['p95']:.2f}",
                                'P99 (ms)': f"{stats['p99']:.2f}",
                            })
            
            download_ttfb_stats_df = pd.DataFrame(stats_rows_ttfb)
            tables['download_ttfb_stats'] = download_ttfb_stats_df.to_html(index=False, classes='stats-table', table_id='download-ttfb-stats')
            
            # Total time statistics (group by system, payload_size, cache_mode)
            stats_rows_total = []
            for system in sorted(download_df_clean['system'].unique()):
                for payload_size in sorted(download_df_clean['payload_size'].unique()):
                    for cache_mode in sorted(download_df_clean['cache_mode'].unique()):
                        subset = download_df_clean[
                            (download_df_clean['system'] == system) &
                            (download_df_clean['payload_size'] == payload_size) &
                            (download_df_clean['cache_mode'] == cache_mode)
                        ]
                        if len(subset) > 0:
                            stats = calculate_statistics(subset, 'total_ms')
                            stats_rows_total.append({
                                'System': system,
                                'Payload Size': format_bytes(payload_size),
                                'Cache Mode': cache_mode,
                                'Count': stats['count'],
                                'Mean (ms)': f"{stats['mean']:.2f}",
                                'Median (ms)': f"{stats['median']:.2f}",
                                'Std Dev (ms)': f"{stats['std']:.2f}",
                                'P95 (ms)': f"{stats['p95']:.2f}",
                                'P99 (ms)': f"{stats['p99']:.2f}",
                            })
            
            download_total_stats_df = pd.DataFrame(stats_rows_total)
            tables['download_total_stats'] = download_total_stats_df.to_html(index=False, classes='stats-table', table_id='download-total-stats')
    if hops_df is not None and len(hops_df) > 0:
        stats_rows = []
        for system in sorted(hops_df['system'].unique()):
            for op in sorted(hops_df.get('operation', pd.Series(['get'])).unique()):
                subset = hops_df[(hops_df['system'] == system)]
                if 'operation' in hops_df.columns:
                    subset = subset[subset['operation'] == op]
                for payload_size in sorted(subset['payload_size'].unique()):
                    sub = subset[subset['payload_size'] == payload_size]
                    if len(sub) > 0:
                        stats = calculate_statistics(sub, 'hops')
                        stats_rows.append({
                            'System': system,
                            'Operation': op,
                            'Payload Size': format_bytes(payload_size),
                            'Count': stats['count'],
                            'Mean': f"{stats['mean']:.1f}",
                            'Median': f"{stats['median']:.1f}",
                            'Min': f"{stats['min']:.0f}",
                            'Max': f"{stats['max']:.0f}",
                            'P95': f"{stats['p95']:.1f}",
                        })
        if stats_rows:
            tables['network_hops_stats'] = pd.DataFrame(stats_rows).to_html(index=False, classes='stats-table', table_id='network-hops-stats')
    if resource_df is not None and len(resource_df) > 0:
        stats_rows = []
        for system in sorted(resource_df['system'].unique()):
            sub = resource_df[resource_df['system'] == system]
            cpu_mean = sub['cpu_pct'].mean()
            cpu_peak = sub['cpu_pct'].max()
            mem_mean = sub['mem_usage_mb'].mean()
            mem_peak = sub['mem_usage_mb'].max()
            stats_rows.append({
                'System': system,
                'Samples': len(sub),
                'CPU Mean %': f"{cpu_mean:.2f}",
                'CPU Peak %': f"{cpu_peak:.2f}",
                'Mem Mean (MB)': f"{mem_mean:.2f}",
                'Mem Peak (MB)': f"{mem_peak:.2f}",
            })
        if stats_rows:
            tables['resource_usage_stats'] = pd.DataFrame(stats_rows).to_html(index=False, classes='stats-table', table_id='resource-usage-stats')
    if storage_efficiency_df is not None and len(storage_efficiency_df) > 0:
        se_display = storage_efficiency_df.copy()
        se_display['disk_bytes'] = se_display['disk_bytes'].apply(lambda x: format_bytes(int(x)) if pd.notna(x) else '')
        se_display['efficiency_ratio'] = se_display['efficiency_ratio'].apply(lambda x: f"{x:.4f}" if pd.notna(x) else '')
        tables['storage_efficiency'] = se_display.to_html(index=False, classes='stats-table', table_id='storage-efficiency')
    if replication_df is not None and len(replication_df) > 0:
        repl_display = replication_df.copy()
        tables['replication'] = repl_display.to_html(index=False, classes='stats-table', table_id='replication')
    if replication_distribution_df is not None and len(replication_distribution_df) > 0:
        tables['replication_distribution'] = replication_distribution_df.to_html(index=False, classes='stats-table', table_id='replication-distribution')
    if repair_time_df is not None and len(repair_time_df) > 0:
        tables['repair_time'] = repair_time_df.to_html(index=False, classes='stats-table', table_id='repair-time')
    if partition_recovery_df is not None and len(partition_recovery_df) > 0:
        pr_display = partition_recovery_df.copy()
        pr_display['recovery_time_s'] = pr_display['recovery_time_s'].apply(lambda x: f"{x:.2f}" if pd.notna(x) else str(x))
        tables['partition_recovery'] = pr_display.to_html(index=False, classes='stats-table', table_id='partition-recovery')
    if lookup_complexity_df is not None and len(lookup_complexity_df) > 0:
        lc_agg = lookup_complexity_df.groupby(['system', 'node_count', 'operation'])['hops'].agg(['mean', 'median', 'count']).reset_index()
        lc_agg['mean'] = lc_agg['mean'].apply(lambda x: f"{x:.2f}" if pd.notna(x) else '')
        lc_agg['median'] = lc_agg['median'].apply(lambda x: f"{x:.2f}" if pd.notna(x) else '')
        tables['lookup_complexity'] = lc_agg.to_html(index=False, classes='stats-table', table_id='lookup-complexity')
    if concurrent_df is not None and len(concurrent_df) > 0:
        conc_display = concurrent_df[['system', 'concurrent_writes', 'concurrent_reads', 'throughput_mbps', 'p99_latency_ms']].copy()
        tables['concurrent'] = conc_display.to_html(index=False, classes='stats-table', table_id='concurrent')
        # Lock-overhead analysis: vn-IPFS uses write locking; Swarm uses chunk push without locks.
        # Compare p99_ratio (vnipfs/swarm) and throughput_ratio per concurrency level.
        cf = concurrent_df.dropna(subset=['throughput_mbps', 'p99_latency_ms'])
        systems = cf['system'].unique()
        if 'our_system' in systems and 'swarm' in systems:
            rows = []
            for _, grp in cf.groupby(['concurrent_writes', 'concurrent_reads']):
                our_grp = grp[grp['system'] == 'our_system']
                sw_grp = grp[grp['system'] == 'swarm']
                if our_grp.empty or sw_grp.empty:
                    continue
                our = our_grp.iloc[0]
                sw = sw_grp.iloc[0]
                conc_label = f"{int(our['concurrent_writes'])}w/{int(our['concurrent_reads'])}r"
                p99_ratio = our['p99_latency_ms'] / sw['p99_latency_ms'] if sw['p99_latency_ms'] > 0 else float('nan')
                tput_ratio = our['throughput_mbps'] / sw['throughput_mbps'] if sw['throughput_mbps'] > 0 else float('nan')
                rows.append({
                    'concurrency': conc_label,
                    'p99_vnipfs_ms': f"{our['p99_latency_ms']:.2f}",
                    'p99_swarm_ms': f"{sw['p99_latency_ms']:.2f}",
                    'p99_ratio': f"{p99_ratio:.2f}" if not np.isnan(p99_ratio) else '-',
                    'tput_vnipfs_mbps': f"{our['throughput_mbps']:.2f}",
                    'tput_swarm_mbps': f"{sw['throughput_mbps']:.2f}",
                    'tput_ratio': f"{tput_ratio:.2f}" if not np.isnan(tput_ratio) else '-',
                })
            if rows:
                lo_df = pd.DataFrame(rows)
                tables['lock_overhead'] = lo_df.to_html(index=False, classes='stats-table', table_id='lock-overhead')
    if routing_overhead_df is not None and len(routing_overhead_df) > 0:
        tables['routing_overhead'] = routing_overhead_df.to_html(index=False, classes='stats-table', table_id='routing-overhead')
    if download_df is not None and lookup_latency_df is not None and len(download_df) > 0 and len(lookup_latency_df) > 0:
        dd = download_df.copy()
        if 'total_ms' not in dd.columns and 'ttfb_ms' in dd.columns:
            dd['total_ms'] = dd['ttfb_ms']
        dd['total_ms'] = pd.to_numeric(dd['total_ms'], errors='coerce')
        dd = dd.dropna(subset=['total_ms'])
        cache_col = dd['cache_mode'] if 'cache_mode' in dd.columns else pd.Series(['warm'] * len(dd))
        our_dl = dd[(dd['system'] == 'our_system') & (cache_col.isin(['warm', 'cold']))]
        ll = lookup_latency_df
        if 'system' in lookup_latency_df.columns:
            ll = lookup_latency_df[lookup_latency_df['system'] == 'our_system']
        if len(our_dl) > 0 and len(ll) > 0:
            mean_lookup = ll['lookup_latency_ms'].mean()
            lookup_by_n = ll.groupby('node_count')['lookup_latency_ms'].mean() if 'node_count' in ll.columns and ll['node_count'].notna().any() else None
            rows = []
            for (nc, payload), grp in our_dl.groupby(['node_count', 'payload_size']):
                mean_total = grp['total_ms'].mean()
                lk = float(lookup_by_n.get(nc, mean_lookup)) if lookup_by_n is not None else mean_lookup
                fetch_ms = max(1.0, mean_total - lk)
                eff = (float(payload) / (fetch_ms / 1000)) / 1e6
                rows.append({'node_count': nc, 'payload_bytes': int(payload), 'mean_fetch_ms': f"{fetch_ms:.2f}", 'direct_fetch_mbps': f"{eff:.2f}"})
            if rows:
                tables['direct_fetch_efficiency'] = pd.DataFrame(rows).to_html(index=False, classes='stats-table', table_id='direct-fetch-efficiency')
    return tables

def generate_html_report(results_dir, upload_df, download_df, plots, tables, hops_df=None, resource_df=None, storage_efficiency_df=None, replication_df=None, partition_recovery_df=None, lookup_complexity_df=None, concurrent_df=None, replication_distribution_df=None, repair_time_df=None, routing_overhead_df=None, lookup_latency_df=None):
    """Generate HTML report with all statistics and visualizations"""
    
    html_content = f"""<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Swarm Comparison Test Analysis Report</title>
    <style>
        body {{
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            line-height: 1.6;
            color: #333;
            max-width: 1400px;
            margin: 0 auto;
            padding: 20px;
            background-color: #f5f5f5;
        }}
        h1 {{
            color: #2c3e50;
            border-bottom: 3px solid #3498db;
            padding-bottom: 10px;
        }}
        h2 {{
            color: #34495e;
            margin-top: 30px;
            border-bottom: 2px solid #ecf0f1;
            padding-bottom: 5px;
        }}
        h3 {{
            color: #7f8c8d;
            margin-top: 20px;
        }}
        .header-info {{
            background-color: #ecf0f1;
            padding: 15px;
            border-radius: 5px;
            margin-bottom: 20px;
        }}
        .plot-container {{
            background-color: white;
            padding: 20px;
            margin: 20px 0;
            border-radius: 5px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }}
        .plot-container img {{
            max-width: 100%;
            height: auto;
            display: block;
            margin: 0 auto;
        }}
        .stats-table {{
            width: 100%;
            border-collapse: collapse;
            margin: 20px 0;
            background-color: white;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }}
        .stats-table th {{
            background-color: #3498db;
            color: white;
            padding: 12px;
            text-align: left;
            font-weight: bold;
        }}
        .stats-table td {{
            padding: 10px;
            border-bottom: 1px solid #ecf0f1;
        }}
        .stats-table tr:nth-child(even) {{
            background-color: #f8f9fa;
        }}
        .stats-table tr:hover {{
            background-color: #e8f4f8;
        }}
        .summary {{
            background-color: #d5f4e6;
            padding: 15px;
            border-radius: 5px;
            margin: 20px 0;
            border-left: 4px solid #27ae60;
        }}
    </style>
</head>
<body>
    <h1>Swarm Comparison Test Analysis Report</h1>
    
    <div class="header-info">
        <p><strong>Generated:</strong> {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}</p>
        <p><strong>Results Directory:</strong> {results_dir}</p>
    </div>
"""
    
    # Upload section
    if upload_df is not None and len(upload_df) > 0:
        html_content += """
    <h2>Upload Latency Analysis</h2>
    <p><strong>Note:</strong> These charts are <em>wall-clock upload latency</em> (ms) and throughput, not DHT hop counts.
    Hop counts from routing appear under <em>Network Hops</em> and <em>Lookup Complexity</em>. Do not infer routing depth from upload time alone.</p>
"""
        if 'upload_stats' in tables:
            html_content += f"""
    <h3>Statistical Summary (Latency)</h3>
    {tables['upload_stats']}
"""
        if 'upload_throughput_stats' in tables:
            html_content += f"""
    <h3>Upload Throughput Statistics (MB/s)</h3>
    {tables['upload_throughput_stats']}
"""
        if 'upload_box' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Box Plot: Latency Distribution by Payload Size</h3>
        <img src="data:image/png;base64,{plots['upload_box']}" alt="Upload Latency Box Plot">
    </div>
"""
        if 'upload_line_mean' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Line Chart: Mean Latency by Payload Size</h3>
        <img src="data:image/png;base64,{plots['upload_line_mean']}" alt="Upload Latency Line Chart">
    </div>
"""
        if 'upload_bar_nodes' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Bar Chart: Mean Latency by Node Count</h3>
        <img src="data:image/png;base64,{plots['upload_bar_nodes']}" alt="Upload Latency Bar Chart">
    </div>
"""
        if 'upload_throughput_box' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Box Plot: Upload Throughput by Payload Size</h3>
        <img src="data:image/png;base64,{plots['upload_throughput_box']}" alt="Upload Throughput Box Plot">
    </div>
"""
        if 'upload_throughput_line' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Line Chart: Mean Upload Throughput by Payload Size</h3>
        <img src="data:image/png;base64,{plots['upload_throughput_line']}" alt="Upload Throughput Line Chart">
    </div>
"""
    else:
        html_content += """
    <h2>Upload Latency Analysis</h2>
    <p class="summary">No upload data available.</p>
"""
    
    # Download section
    if download_df is not None and len(download_df) > 0:
        html_content += """
    <h2>Download Latency Analysis</h2>
"""
        if 'download_ttfb_stats' in tables:
            html_content += f"""
    <h3>Time-to-First-Byte (TTFB) Statistical Summary</h3>
    {tables['download_ttfb_stats']}
"""
        if 'download_total_stats' in tables:
            html_content += f"""
    <h3>Total Download Time Statistical Summary</h3>
    {tables['download_total_stats']}
"""
        if 'download_ttfb_box' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Box Plot: TTFB Distribution by Payload Size</h3>
        <img src="data:image/png;base64,{plots['download_ttfb_box']}" alt="Download TTFB Box Plot">
    </div>
"""
        if 'download_ttfb_cold_warm' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Cold vs Warm Cache: TTFB Comparison</h3>
        <img src="data:image/png;base64,{plots['download_ttfb_cold_warm']}" alt="Download TTFB by cache mode">
    </div>
"""
        if 'download_total_box' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Box Plot: Total Download Time Distribution by Payload Size</h3>
        <img src="data:image/png;base64,{plots['download_total_box']}" alt="Download Total Time Box Plot">
    </div>
"""
        if 'download_ttfb_line' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Line Chart: Mean TTFB by Payload Size</h3>
        <img src="data:image/png;base64,{plots['download_ttfb_line']}" alt="Download TTFB Line Chart">
    </div>
"""
        if 'download_total_line' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Line Chart: Mean Total Download Time by Payload Size</h3>
        <img src="data:image/png;base64,{plots['download_total_line']}" alt="Download Total Time Line Chart">
    </div>
"""
    else:
        html_content += """
    <h2>Download Latency Analysis</h2>
    <p class="summary">No download data available.</p>
"""
    
    if hops_df is not None and len(hops_df) > 0 and 'network_hops_stats' in tables:
        html_content += """
    <h2>Network Hops Analysis</h2>
    <p>DHT <em>hop count</em> (query events) per operation — not upload/download latency. vn-IPFS reports hops; Swarm does not expose this metric.</p>
"""
        html_content += f"""
    <h3>Network Hops: Statistics by System, Operation, Payload Size</h3>
    {tables['network_hops_stats']}
"""
        if 'network_hops_box' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Box Plot: Hops Distribution by Payload Size</h3>
        <img src="data:image/png;base64,{plots['network_hops_box']}" alt="Network Hops Box Plot">
    </div>
"""
        if 'network_hops_line' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Line Chart: Mean Hops by Operation and Payload Size</h3>
        <img src="data:image/png;base64,{plots['network_hops_line']}" alt="Network Hops Line Chart">
    </div>
"""
    if resource_df is not None and len(resource_df) > 0 and 'resource_usage_stats' in tables:
        html_content += """
    <h2>Resource Usage Analysis</h2>
    <p>CPU and memory usage during tests (mean/peak per system).</p>
"""
        html_content += f"""
    <h3>Resource Usage: Mean and Peak per System</h3>
    {tables['resource_usage_stats']}
"""
        if 'resource_cpu_box' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Box Plot: CPU Usage by System</h3>
        <img src="data:image/png;base64,{plots['resource_cpu_box']}" alt="Resource CPU Box Plot">
    </div>
"""
        if 'resource_mem_box' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Box Plot: Memory Usage by System</h3>
        <img src="data:image/png;base64,{plots['resource_mem_box']}" alt="Resource Memory Box Plot">
    </div>
"""
        if 'resource_cpu_time' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>CPU Usage Over Time</h3>
        <img src="data:image/png;base64,{plots['resource_cpu_time']}" alt="Resource CPU Over Time">
    </div>
"""
        if 'resource_mem_time' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Memory Usage Over Time</h3>
        <img src="data:image/png;base64,{plots['resource_mem_time']}" alt="Resource Memory Over Time">
    </div>
"""
    if storage_efficiency_df is not None and len(storage_efficiency_df) > 0 and 'storage_efficiency' in tables:
        html_content += """
    <h2>Storage Efficiency Analysis</h2>
    <p>Disk usage and efficiency ratio: (payload_size * replication_count) / disk_bytes. Higher ratio is better.</p>
"""
        html_content += f"""
    <h3>Storage Efficiency: system, payload_size, nodes, disk_bytes, efficiency_ratio</h3>
    {tables['storage_efficiency']}
"""
        if 'storage_efficiency_bar' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Bar Chart: Efficiency Ratio by System</h3>
        <img src="data:image/png;base64,{plots['storage_efficiency_bar']}" alt="Storage Efficiency Bar Chart">
    </div>
"""
    if replication_df is not None and len(replication_df) > 0 and 'replication' in tables:
        html_content += """
    <h2>Replication Speed Analysis</h2>
    <p>Time to reach R replicas after put. Lower is better.</p>
"""
        html_content += f"""
    <h3>Replication Results: system, payload_size, nodes, replicas_target, time_to_R_s</h3>
    {tables['replication']}
"""
        if 'replication_bar' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Replication Speed: Time to R Replicas</h3>
        <img src="data:image/png;base64,{plots['replication_bar']}" alt="Replication Speed Bar Chart">
    </div>
"""
    if partition_recovery_df is not None and len(partition_recovery_df) > 0 and 'partition_recovery' in tables:
        html_content += """
    <h2>Partition Recovery Analysis</h2>
    <p>Time from network reconnect until content available on previously partitioned nodes. Lower is better.</p>
"""
        html_content += f"""
    <h3>Partition Recovery: system, node_count, partition_size, recovery_time_s</h3>
    {tables['partition_recovery']}
"""
        if 'partition_recovery_bar' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Partition Recovery Time by System and Partition Size</h3>
        <img src="data:image/png;base64,{plots['partition_recovery_bar']}" alt="Partition Recovery Bar Chart">
    </div>
"""
    if lookup_complexity_df is not None and len(lookup_complexity_df) > 0 and 'lookup_complexity' in tables:
        html_content += """
    <h2>Lookup Complexity</h2>
    <p>Rows use <code>operation=lookup</code> from <code>lookup_complexity_test.sh</code> (vn-IPFS Docker, cold <code>lookup-key</code>).</p>
"""
        html_content += f"""
    <h3>Lookup Complexity: system, node_count, operation, hops (mean/median)</h3>
    {tables['lookup_complexity']}
"""
        if 'lookup_complexity_log_n' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Hops vs log10(N)</h3>
        <img src="data:image/png;base64,{plots['lookup_complexity_log_n']}" alt="Lookup Complexity Hops vs log N">
    </div>
"""
        if 'lookup_complexity_hops_vs_n' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Hops vs N (log scale)</h3>
        <img src="data:image/png;base64,{plots['lookup_complexity_hops_vs_n']}" alt="Lookup Complexity Hops vs N">
    </div>
"""
        if 'lookup_complexity_loglog' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Log-log hops vs N vs O(N) reference line</h3>
        <img src="data:image/png;base64,{plots['lookup_complexity_loglog']}" alt="Log-Log hops vs N">
    </div>
"""
    if routing_overhead_df is not None and len(routing_overhead_df) > 0 and 'routing_overhead' in tables:
        html_content += """
    <h2>Token Routing vs Provider Announcement Overhead</h2>
    <p>Message counts for put/get. vn-IPFS uses token lookup; Swarm uses provider announcements + retrieval.</p>
"""
        html_content += f"""
    <h3>Routing Overhead: system, operation, message_count, overhead_type</h3>
    {tables['routing_overhead']}
"""
    if 'direct_fetch_efficiency' in tables:
        html_content += """
    <h2>Direct Fetch Efficiency</h2>
    <p>vn-IPFS: fetch phase only (total get time minus token lookup). Higher MB/s is better.</p>
"""
        html_content += f"""
    <h3>Direct Fetch: payload_bytes / fetch_time → MB/s</h3>
    {tables['direct_fetch_efficiency']}
"""
    if concurrent_df is not None and len(concurrent_df) > 0 and 'concurrent' in tables:
        html_content += """
    <h2>Concurrent Read/Write Performance</h2>
    <p>Aggregate throughput and p99 latency under N parallel uploads and M parallel downloads. Test matrix: 1w/0r, 5w/5r, 10w/10r.</p>
"""
        html_content += f"""
    <h3>Concurrent Results: system, concurrent_writes, concurrent_reads, throughput_mbps, p99_latency_ms</h3>
    {tables['concurrent']}
"""
        if 'concurrent_throughput_vs_concurrency' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>Throughput vs Concurrency</h3>
        <img src="data:image/png;base64,{plots['concurrent_throughput_vs_concurrency']}" alt="Throughput vs Concurrency">
    </div>
"""
        if 'concurrent_p99_vs_concurrency' in plots:
            html_content += f"""
    <div class="plot-container">
        <h3>p99 Latency vs Concurrency</h3>
        <img src="data:image/png;base64,{plots['concurrent_p99_vs_concurrency']}" alt="p99 Latency vs Concurrency">
    </div>
"""
        if 'lock_overhead' in tables:
            html_content += """
    <h3>Lock Overhead Comparison</h3>
    <p>vn-IPFS uses write locking for consistency; Swarm uses chunk-based push without explicit locks.
    p99_ratio &gt; 1 indicates higher tail latency for vn-IPFS (possible lock wait). tput_ratio &lt; 1 indicates lower throughput (lock serialization).</p>
"""
            html_content += f"""
    {tables['lock_overhead']}
"""
    
    html_content += """
</body>
</html>
"""
    
    return html_content

def main():
    parser = argparse.ArgumentParser(
        description='Analyze Swarm comparison test results and generate HTML report'
    )
    parser.add_argument(
        'results_dir',
        help='Directory containing test result CSV files'
    )
    parser.add_argument(
        '--output',
        '-o',
        default=None,
        help='Output HTML file path (default: <results_dir>/analysis_report.html)'
    )
    
    args = parser.parse_args()
    
    results_path = Path(args.results_dir)
    if not results_path.exists():
        print(f"Error: Results directory does not exist: {results_path}", file=sys.stderr)
        sys.exit(1)
    
    print(f"Loading data from: {results_path}")
    upload_df, download_df, hops_df, resource_df, storage_efficiency_df, replication_df, partition_recovery_df, lookup_complexity_df, concurrent_df, replication_distribution_df, repair_time_df, routing_overhead_df, lookup_latency_df = load_data(results_path)
    
    if upload_df is None and download_df is None and hops_df is None and resource_df is None and storage_efficiency_df is None and replication_df is None and partition_recovery_df is None and lookup_complexity_df is None:
        print("Error: No CSV files found in results directory", file=sys.stderr)
        sys.exit(1)
    
    print("\nGenerating plots...")
    upload_plots = generate_upload_plots(upload_df)
    download_plots = generate_download_plots(download_df)
    hops_plots = generate_network_hops_plots(hops_df)
    resource_plots = generate_resource_plots(resource_df)
    storage_eff_plots = generate_storage_efficiency_plots(storage_efficiency_df)
    replication_plots = generate_replication_plots(replication_df)
    partition_recovery_plots = generate_partition_recovery_plots(partition_recovery_df)
    lookup_complexity_plots = generate_lookup_complexity_plots(lookup_complexity_df)
    concurrent_plots = generate_concurrent_plots(concurrent_df)
    plots = {**upload_plots, **download_plots, **hops_plots, **resource_plots, **storage_eff_plots, **replication_plots, **partition_recovery_plots, **lookup_complexity_plots, **concurrent_plots}
    print(f"Generated {len(plots)} plots")
    
    print("\nCalculating statistics...")
    tables = generate_statistics_tables(upload_df, download_df, hops_df, resource_df, storage_efficiency_df, replication_df, partition_recovery_df, lookup_complexity_df, concurrent_df, replication_distribution_df, repair_time_df, routing_overhead_df, lookup_latency_df)
    print(f"Generated {len(tables)} statistics tables")
    
    print("\nGenerating HTML report...")
    html_content = generate_html_report(str(results_path), upload_df, download_df, plots, tables, hops_df, resource_df, storage_efficiency_df, replication_df, partition_recovery_df, lookup_complexity_df, concurrent_df, replication_distribution_df, repair_time_df, routing_overhead_df, lookup_latency_df)
    
    # Determine output path
    if args.output:
        output_path = Path(args.output)
    else:
        output_path = results_path / "analysis_report.html"
    
    output_path.write_text(html_content)
    print(f"\n✓ Report generated: {output_path}")
    print(f"  Open in browser: file://{output_path.absolute()}")

if __name__ == '__main__':
    main()
