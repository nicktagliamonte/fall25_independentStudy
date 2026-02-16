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
        print(f"Loaded aggregated download data: {len(download_df)} rows")
    else:
        # Load individual files
        download_files = sorted(results_path.glob("download_n*.csv"))
        if download_files:
            download_dfs = []
            for f in download_files:
                df = pd.read_csv(f)
                # Extract node_count from filename if not present
                if 'node_count' not in df.columns:
                    node_count = int(f.stem.split('_n')[1])
                    df['node_count'] = node_count
                download_dfs.append(df)
            if download_dfs:
                download_df = pd.concat(download_dfs, ignore_index=True)
                print(f"Loaded download data from {len(download_files)} files: {len(download_df)} rows")
    
    return upload_df, download_df

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
    
    return plots

def generate_download_plots(download_df):
    """Generate download latency comparison plots"""
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
    
    if len(download_df) == 0:
        return plots
    
    # Convert payload_size to readable format
    download_df['payload_size_str'] = download_df['payload_size'].apply(format_bytes)
    
    # 1. Box plot: TTFB by system and payload size
    fig, ax = plt.subplots(figsize=(14, 8))
    sns.boxplot(data=download_df, x='payload_size_str', y='ttfb_ms', 
               hue='system', ax=ax)
    ax.set_title('Download Time-to-First-Byte (TTFB) Comparison', fontsize=14, fontweight='bold')
    ax.set_xlabel('Payload Size', fontsize=12)
    ax.set_ylabel('TTFB (ms)', fontsize=12)
    ax.legend(title='System', fontsize=10)
    plt.xticks(rotation=45, ha='right')
    plots['download_ttfb_box'] = plot_to_base64(fig)
    
    # 2. Box plot: Total download time by system and payload size
    fig, ax = plt.subplots(figsize=(14, 8))
    sns.boxplot(data=download_df, x='payload_size_str', y='total_ms', 
               hue='system', ax=ax)
    ax.set_title('Total Download Time Comparison', fontsize=14, fontweight='bold')
    ax.set_xlabel('Payload Size', fontsize=12)
    ax.set_ylabel('Total Time (ms)', fontsize=12)
    ax.legend(title='System', fontsize=10)
    plt.xticks(rotation=45, ha='right')
    plots['download_total_box'] = plot_to_base64(fig)
    
    # 3. Line chart: Mean TTFB by payload size
    fig, ax = plt.subplots(figsize=(12, 6))
    mean_ttfb = download_df.groupby(['system', 'payload_size'])['ttfb_ms'].mean().reset_index()
    for system in download_df['system'].unique():
        system_data = mean_ttfb[mean_ttfb['system'] == system]
        ax.plot(system_data['payload_size'], system_data['ttfb_ms'], 
               marker='o', label=system, linewidth=2, markersize=8)
    ax.set_xlabel('Payload Size (bytes)', fontsize=12)
    ax.set_ylabel('Mean TTFB (ms)', fontsize=12)
    ax.set_title('Download TTFB: Mean by Payload Size', fontsize=14, fontweight='bold')
    ax.legend(fontsize=10)
    ax.set_xscale('log')
    ax.grid(True, alpha=0.3)
    plots['download_ttfb_line'] = plot_to_base64(fig)
    
    # 4. Line chart: Mean total time by payload size
    fig, ax = plt.subplots(figsize=(12, 6))
    mean_total = download_df.groupby(['system', 'payload_size'])['total_ms'].mean().reset_index()
    for system in download_df['system'].unique():
        system_data = mean_total[mean_total['system'] == system]
        ax.plot(system_data['payload_size'], system_data['total_ms'], 
               marker='o', label=system, linewidth=2, markersize=8)
    ax.set_xlabel('Payload Size (bytes)', fontsize=12)
    ax.set_ylabel('Mean Total Time (ms)', fontsize=12)
    ax.set_title('Download Total Time: Mean by Payload Size', fontsize=14, fontweight='bold')
    ax.legend(fontsize=10)
    ax.set_xscale('log')
    ax.grid(True, alpha=0.3)
    plots['download_total_line'] = plot_to_base64(fig)
    
    return plots

def generate_statistics_tables(upload_df, download_df):
    """Generate HTML tables with statistics"""
    tables = {}
    
    if upload_df is not None and len(upload_df) > 0:
        # Filter out errors
        upload_df_clean = upload_df[upload_df['latency_ms'] != 'ERROR'].copy()
        upload_df_clean['latency_ms'] = pd.to_numeric(upload_df_clean['latency_ms'], errors='coerce')
        upload_df_clean = upload_df_clean.dropna(subset=['latency_ms'])
        
        if len(upload_df_clean) > 0:
            # Statistics by system and payload size
            stats_rows = []
            for system in sorted(upload_df_clean['system'].unique()):
                for payload_size in sorted(upload_df_clean['payload_size'].unique()):
                    subset = upload_df_clean[
                        (upload_df_clean['system'] == system) & 
                        (upload_df_clean['payload_size'] == payload_size)
                    ]
                    if len(subset) > 0:
                        stats = calculate_statistics(subset, 'latency_ms')
                        stats_rows.append({
                            'System': system,
                            'Payload Size': format_bytes(payload_size),
                            'Count': stats['count'],
                            'Mean (ms)': f"{stats['mean']:.2f}",
                            'Median (ms)': f"{stats['median']:.2f}",
                            'Std Dev (ms)': f"{stats['std']:.2f}",
                            'Min (ms)': f"{stats['min']:.2f}",
                            'Max (ms)': f"{stats['max']:.2f}",
                            'P95 (ms)': f"{stats['p95']:.2f}",
                            'P99 (ms)': f"{stats['p99']:.2f}",
                        })
            
            upload_stats_df = pd.DataFrame(stats_rows)
            tables['upload_stats'] = upload_stats_df.to_html(index=False, classes='stats-table', table_id='upload-stats')
    
    if download_df is not None and len(download_df) > 0:
        # Filter out errors
        download_df_clean = download_df[
            (download_df['ttfb_ms'] != 'ERROR') & 
            (download_df['total_ms'] != 'ERROR')
        ].copy()
        download_df_clean['ttfb_ms'] = pd.to_numeric(download_df_clean['ttfb_ms'], errors='coerce')
        download_df_clean['total_ms'] = pd.to_numeric(download_df_clean['total_ms'], errors='coerce')
        download_df_clean = download_df_clean.dropna(subset=['ttfb_ms', 'total_ms'])
        
        if len(download_df_clean) > 0:
            # TTFB statistics
            stats_rows_ttfb = []
            for system in sorted(download_df_clean['system'].unique()):
                for payload_size in sorted(download_df_clean['payload_size'].unique()):
                    subset = download_df_clean[
                        (download_df_clean['system'] == system) & 
                        (download_df_clean['payload_size'] == payload_size)
                    ]
                    if len(subset) > 0:
                        stats = calculate_statistics(subset, 'ttfb_ms')
                        stats_rows_ttfb.append({
                            'System': system,
                            'Payload Size': format_bytes(payload_size),
                            'Count': stats['count'],
                            'Mean (ms)': f"{stats['mean']:.2f}",
                            'Median (ms)': f"{stats['median']:.2f}",
                            'Std Dev (ms)': f"{stats['std']:.2f}",
                            'P95 (ms)': f"{stats['p95']:.2f}",
                            'P99 (ms)': f"{stats['p99']:.2f}",
                        })
            
            download_ttfb_stats_df = pd.DataFrame(stats_rows_ttfb)
            tables['download_ttfb_stats'] = download_ttfb_stats_df.to_html(index=False, classes='stats-table', table_id='download-ttfb-stats')
            
            # Total time statistics
            stats_rows_total = []
            for system in sorted(download_df_clean['system'].unique()):
                for payload_size in sorted(download_df_clean['payload_size'].unique()):
                    subset = download_df_clean[
                        (download_df_clean['system'] == system) & 
                        (download_df_clean['payload_size'] == payload_size)
                    ]
                    if len(subset) > 0:
                        stats = calculate_statistics(subset, 'total_ms')
                        stats_rows_total.append({
                            'System': system,
                            'Payload Size': format_bytes(payload_size),
                            'Count': stats['count'],
                            'Mean (ms)': f"{stats['mean']:.2f}",
                            'Median (ms)': f"{stats['median']:.2f}",
                            'Std Dev (ms)': f"{stats['std']:.2f}",
                            'P95 (ms)': f"{stats['p95']:.2f}",
                            'P99 (ms)': f"{stats['p99']:.2f}",
                        })
            
            download_total_stats_df = pd.DataFrame(stats_rows_total)
            tables['download_total_stats'] = download_total_stats_df.to_html(index=False, classes='stats-table', table_id='download-total-stats')
    
    return tables

def generate_html_report(results_dir, upload_df, download_df, plots, tables):
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
"""
        if 'upload_stats' in tables:
            html_content += f"""
    <h3>Statistical Summary</h3>
    {tables['upload_stats']}
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
    upload_df, download_df = load_data(results_path)
    
    if upload_df is None and download_df is None:
        print("Error: No CSV files found in results directory", file=sys.stderr)
        sys.exit(1)
    
    print("\nGenerating plots...")
    upload_plots = generate_upload_plots(upload_df)
    download_plots = generate_download_plots(download_df)
    plots = {**upload_plots, **download_plots}
    print(f"Generated {len(plots)} plots")
    
    print("\nCalculating statistics...")
    tables = generate_statistics_tables(upload_df, download_df)
    print(f"Generated {len(tables)} statistics tables")
    
    print("\nGenerating HTML report...")
    html_content = generate_html_report(str(results_path), upload_df, download_df, plots, tables)
    
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
