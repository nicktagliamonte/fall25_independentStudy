#!/usr/bin/env bash
# Purpose: Generate comprehensive markdown report from test results
# Usage: ./scripts/analysis/generate_swarm_report.sh [--results-dir <dir>] [--output <file>]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source utilities
source "$ROOT_DIR/scripts/utils/results_dir.sh" 2>/dev/null || true

# Default values
RESULTS_DIR="${RESULTS_DIR:-}"
OUTPUT_FILE=""
REPORT_TITLE="Swarm Comparison Test Report"

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --results-dir)
      RESULTS_DIR="$2"
      shift 2
      ;;
    --output)
      OUTPUT_FILE="$2"
      shift 2
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --results-dir <dir>  Results directory (default: latest in artifacts/swarm_comparison_tests)"
      echo "  --output <file>      Output markdown file (default: <results-dir>/REPORT.md)"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Find latest results directory if not specified
if [[ -z "$RESULTS_DIR" ]]; then
  if [[ -d "$ROOT_DIR/artifacts/swarm_comparison_tests" ]]; then
    RESULTS_DIR=$(find "$ROOT_DIR/artifacts/swarm_comparison_tests" -mindepth 1 -maxdepth 1 -type d | sort -r | head -1)
  fi
fi

if [[ -z "$RESULTS_DIR" || ! -d "$RESULTS_DIR" ]]; then
  echo "Error: Results directory not found. Specify with --results-dir" >&2
  exit 1
fi

# Set output file
if [[ -z "$OUTPUT_FILE" ]]; then
  OUTPUT_FILE="$RESULTS_DIR/REPORT.md"
fi

# Subdirectories
OUR_SYSTEM_DIR="$RESULTS_DIR/our_system"
SWARM_DIR="$RESULTS_DIR/swarm"
COMPARISON_DIR="$RESULTS_DIR/comparison"
PLOTS_DIR="$RESULTS_DIR/plots"
LOGS_DIR="$RESULTS_DIR/logs"

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}Generating report from: $RESULTS_DIR${NC}"
echo -e "${BLUE}Output: $OUTPUT_FILE${NC}"

# Function to calculate statistics from CSV
calculate_stats() {
  local csv_file="$1"
  local value_column="$2"
  
  if [[ ! -f "$csv_file" ]]; then
    echo "N/A"
    return
  fi
  
  # Use awk to calculate basic statistics
  local stats=$(awk -F',' -v col="$value_column" '
    NR == 1 {
      for (i=1; i<=NF; i++) {
        if ($i == col) col_idx = i
      }
      next
    }
    col_idx && $col_idx != "" && $col_idx != "ERROR" && $col_idx !~ /^[^0-9]/ {
      values[++n] = $col_idx
      sum += $col_idx
      if (n == 1 || $col_idx < min) min = $col_idx
      if (n == 1 || $col_idx > max) max = $col_idx
    }
    END {
      if (n == 0) {
        print "N/A"
        exit
      }
      mean = sum / n
      # Calculate median
      asort(values)
      if (n % 2 == 0) {
        median = (values[n/2] + values[n/2+1]) / 2
      } else {
        median = values[(n+1)/2]
      }
      # Calculate stddev
      sum_sq_diff = 0
      for (i=1; i<=n; i++) {
        diff = values[i] - mean
        sum_sq_diff += diff * diff
      }
      stddev = sqrt(sum_sq_diff / n)
      printf "%.2f,%.2f,%.2f,%.2f,%.2f,%d", mean, median, stddev, min, max, n
    }
  ' "$csv_file")
  
  echo "$stats"
}

# Function to format number with units
format_number() {
  local num="$1"
  local unit="${2:-}"
  
  if [[ "$num" == "N/A" ]]; then
    echo "N/A"
    return
  fi
  
  # Format based on magnitude
  if (( $(echo "$num >= 1000" | bc -l 2>/dev/null || echo 0) )); then
    printf "%.2f%s" "$(echo "scale=2; $num / 1000" | bc -l)" "k$unit"
  else
    printf "%.2f%s" "$num" "$unit"
  fi
}

# Function to get test metadata
get_test_metadata() {
  local metadata_file="$RESULTS_DIR/metadata.json"
  if [[ -f "$metadata_file" ]]; then
    if command -v jq >/dev/null 2>&1; then
      jq -r '.[0] | "\(.timestamp) | Run ID: \(.run_id)"' "$metadata_file" 2>/dev/null || echo "Unknown"
    else
      echo "Unknown"
    fi
  else
    # Extract from directory name
    basename "$RESULTS_DIR" | sed 's/_/ /g'
  fi
}

# Start generating report
{
  cat <<EOF
# $REPORT_TITLE

**Generated**: $(date '+%Y-%m-%d %H:%M:%S')  
**Test Run**: $(get_test_metadata)  
**Results Directory**: \`$(basename "$RESULTS_DIR")\`

---

## Executive Summary

This report presents a comprehensive comparison between our distributed storage system and Ethereum Swarm (Bee v0.5.8) across multiple performance metrics including upload latency, download throughput, content replication, and network convergence.

### Key Findings

EOF

  # Analyze upload results (check subdirectories and root)
  upload_file=""
  for f in "$OUR_SYSTEM_DIR"/*upload*.csv "$SWARM_DIR"/*upload*.csv "$COMPARISON_DIR"/*upload*.csv "$RESULTS_DIR"/*upload*.csv; do
    if [[ -f "$f" ]]; then
      upload_file="$f"
      break
    fi
  done
  
  if [[ -n "$upload_file" && -f "$upload_file" ]]; then
    # Calculate stats for our system
    our_stats=$(grep "^our_system," "$upload_file" 2>/dev/null | awk -F',' '{print $NF}' | grep -v "ERROR" | sort -n | awk '
      {
        values[NR] = $1
        sum += $1
      }
      END {
        if (NR > 0) {
          mean = sum / NR
          asort(values)
          if (NR % 2 == 0) {
            median = (values[NR/2] + values[NR/2+1]) / 2
          } else {
            median = values[(NR+1)/2]
          }
          printf "%.2f,%.2f", mean, median
        } else {
          printf "N/A,N/A"
        }
      }
    ')
    
    # Calculate stats for Swarm
    swarm_stats=$(grep "^swarm," "$upload_file" 2>/dev/null | awk -F',' '{print $NF}' | grep -v "ERROR" | sort -n | awk '
      {
        values[NR] = $1
        sum += $1
      }
      END {
        if (NR > 0) {
          mean = sum / NR
          asort(values)
          if (NR % 2 == 0) {
            median = (values[NR/2] + values[NR/2+1]) / 2
          } else {
            median = values[(NR+1)/2]
          }
          printf "%.2f,%.2f", mean, median
        } else {
          printf "N/A,N/A"
        }
      }
    ')
    
    if [[ "$our_stats" != "N/A,N/A" && "$swarm_stats" != "N/A,N/A" ]]; then
      our_mean=$(echo "$our_stats" | cut -d',' -f1)
      swarm_mean=$(echo "$swarm_stats" | cut -d',' -f1)
      improvement=$(echo "scale=1; (($swarm_mean - $our_mean) / $swarm_mean) * 100" | bc -l 2>/dev/null || echo "0")
      
      cat <<EOF
- **Upload Latency**: Our system shows $(printf "%.1f%%" "$improvement") $(if (( $(echo "$improvement > 0" | bc -l 2>/dev/null || echo 0) )); then echo "improvement"; else echo "overhead"; fi) compared to Swarm
  - Our system: $(printf "%.2f" "$our_mean") ms (mean)
  - Swarm: $(printf "%.2f" "$swarm_mean") ms (mean)

EOF
    fi
  fi
  
  # Analyze replication results
  repl_file=""
  for f in "$OUR_SYSTEM_DIR"/*replication*.csv "$SWARM_DIR"/*replication*.csv "$COMPARISON_DIR"/*replication*.csv; do
    if [[ -f "$f" ]]; then
      repl_file="$f"
      break
    fi
  done
  
  if [[ -n "$repl_file" && -f "$repl_file" ]]; then
    cat <<EOF
- **Content Replication**: See detailed results in [Replication Propagation](#replication-propagation-test) section

EOF
  fi
  
  cat <<EOF
### Test Configuration

- **Test Systems**: Our System vs Ethereum Swarm (Bee v0.5.8)
- **Results Location**: \`$(basename "$RESULTS_DIR")\`
- **Raw Data**: Available in subdirectories (\`our_system/\`, \`swarm/\`, \`comparison/\`)

---

## Detailed Results

### Upload Latency Test

EOF

  # Upload latency table
  cat <<EOF
| System | Payload Size | Mean (ms) | Median (ms) | Std Dev | Min (ms) | Max (ms) | Samples |
|--------|--------------|-----------|-------------|---------|----------|----------|---------|
EOF

  if [[ -n "$upload_file" && -f "$upload_file" ]]; then
    # Group by system and payload size
    awk -F',' '
      NR == 1 { next }
      $NF != "ERROR" && $NF != "" {
        sys = $1
        payload = $2
        latency = $NF
        key = sys "," payload
        values[key] = values[key] " " latency
        count[key]++
        sum[key] += latency
      }
      END {
        for (key in values) {
          split(key, parts, ",")
          sys = parts[1]
          payload = parts[2]
          n = count[key]
          mean = sum[key] / n
          
          # Calculate other stats
          split(values[key], latencies, " ")
          asort(latencies)
          if (n % 2 == 0) {
            median = (latencies[n/2] + latencies[n/2+1]) / 2
          } else {
            median = latencies[(n+1)/2]
          }
          
          min_val = latencies[1]
          max_val = latencies[n]
          
          # Calculate stddev
          sum_sq_diff = 0
          for (i=1; i<=n; i++) {
            diff = latencies[i] - mean
            sum_sq_diff += diff * diff
          }
          stddev = sqrt(sum_sq_diff / n)
          
          # Format payload size
          if (payload >= 1048576) {
            payload_str = sprintf("%.1f MB", payload / 1048576)
          } else if (payload >= 1024) {
            payload_str = sprintf("%.1f KB", payload / 1024)
          } else {
            payload_str = sprintf("%d B", payload)
          }
          
          printf "| %s | %s | %.2f | %.2f | %.2f | %.2f | %.2f | %d |\n",
            sys, payload_str, mean, median, stddev, min_val, max_val, n
        }
      }
    ' "$upload_file" | sort
  else
    echo "| *No upload test results found* | | | | | | | |"
  fi
  
  cat <<EOF

**Raw Data**: [Upload Results]($(basename "$RESULTS_DIR")/$(basename "$upload_file" 2>/dev/null || echo "upload_results.csv"))

---

### Download Throughput Test

EOF

  # Download results (check subdirectories and root)
  download_file=""
  for f in "$OUR_SYSTEM_DIR"/*download*.csv "$SWARM_DIR"/*download*.csv "$COMPARISON_DIR"/*download*.csv "$RESULTS_DIR"/*download*.csv; do
    if [[ -f "$f" ]]; then
      download_file="$f"
      break
    fi
  done
  
  if [[ -n "$download_file" && -f "$download_file" ]]; then
    cat <<EOF
| System | Payload Size | Mean TTFB (ms) | Mean Total (ms) | Throughput (MB/s) | Samples |
|--------|--------------|----------------|-----------------|-------------------|---------|
EOF
    
    awk -F',' '
      NR == 1 { next }
      $(NF-1) != "ERROR" && $NF != "ERROR" && $(NF-1) != "" && $NF != "" {
        sys = $1
        payload = $2
        ttfb = $(NF-1)
        total = $NF
        key = sys "," payload
        ttfb_sum[key] += ttfb
        total_sum[key] += total
        payload_val[key] = payload
        count[key]++
      }
      END {
        for (key in count) {
          split(key, parts, ",")
          sys = parts[1]
          payload = payload_val[key]
          n = count[key]
          mean_ttfb = ttfb_sum[key] / n
          mean_total = total_sum[key] / n
          
          # Calculate throughput (MB/s)
          payload_mb = payload / 1048576
          time_sec = mean_total / 1000
          throughput = payload_mb / time_sec
          
          # Format payload
          if (payload >= 1048576) {
            payload_str = sprintf("%.1f MB", payload / 1048576)
          } else if (payload >= 1024) {
            payload_str = sprintf("%.1f KB", payload / 1024)
          } else {
            payload_str = sprintf("%d B", payload)
          }
          
          printf "| %s | %s | %.2f | %.2f | %.2f | %d |\n",
            sys, payload_str, mean_ttfb, mean_total, throughput, n
        }
      }
    ' "$download_file" | sort
    
    cat <<EOF

**Raw Data**: [Download Results]($(basename "$RESULTS_DIR")/$(basename "$download_file" 2>/dev/null || echo "download_results.csv"))

---
EOF
  else
    cat <<EOF
*No download test results found*

---
EOF
  fi
  
  # Replication propagation test (check subdirectories and root)
  repl_file=""
  for f in "$OUR_SYSTEM_DIR"/*replication*.csv "$SWARM_DIR"/*replication*.csv "$COMPARISON_DIR"/*replication*.csv "$RESULTS_DIR"/*replication*.csv; do
    if [[ -f "$f" ]]; then
      repl_file="$f"
      break
    fi
  done
  
  if [[ -n "$repl_file" && -f "$repl_file" ]]; then
    cat <<EOF
### Replication Propagation Test

Measures the time for content to propagate across nodes in the network.

| System | Nodes | Time to 50% (s) | Time to 90% (s) | Time to 100% (s) |
|--------|-------|-----------------|-----------------|------------------|
EOF
    
    tail -n +2 "$repl_file" | awk -F',' '{
      printf "| %s | %d | %.2f | %.2f | %.2f |\n", $1, $2, $3, $4, $5
    }'
    
    cat <<EOF

**Raw Data**: [Replication Results]($(basename "$RESULTS_DIR")/$(basename "$repl_file" 2>/dev/null || echo "replication_propagation.csv"))

---
EOF
  fi
  
  # Network convergence test (check subdirectories and root)
  conv_file=""
  for f in "$OUR_SYSTEM_DIR"/*convergence*.csv "$SWARM_DIR"/*convergence*.csv "$COMPARISON_DIR"/*convergence*.csv "$RESULTS_DIR"/*convergence*.csv; do
    if [[ -f "$f" ]]; then
      conv_file="$f"
      break
    fi
  done
  
  if [[ -n "$conv_file" && -f "$conv_file" ]]; then
    cat <<EOF
### Network Convergence Test

Measures the time for a new node to integrate into the network.

| System | Nodes | Time to K Neighbors (s) | Time to Discovery (s) | Time to Stable (s) |
|--------|-------|------------------------|----------------------|-------------------|
EOF
    
    tail -n +2 "$conv_file" | awk -F',' '{
      printf "| %s | %d | %.2f | %.2f | %.2f |\n", $1, $2, $3, $4, $5
    }'
    
    cat <<EOF

**Raw Data**: [Convergence Results]($(basename "$RESULTS_DIR")/$(basename "$conv_file" 2>/dev/null || echo "network_convergence.csv"))

---
EOF
  fi
  
  # Performance comparisons
  cat <<EOF
## Performance Comparisons

### Upload Latency Comparison

EOF

  if [[ -n "$upload_file" && -f "$upload_file" ]]; then
    # Calculate overall comparison
    our_overall=$(grep "^our_system," "$upload_file" 2>/dev/null | awk -F',' '{print $NF}' | grep -v "ERROR" | awk '{sum+=$1; n++} END {if(n>0) print sum/n; else print "N/A"}')
    swarm_overall=$(grep "^swarm," "$upload_file" 2>/dev/null | awk -F',' '{print $NF}' | grep -v "ERROR" | awk '{sum+=$1; n++} END {if(n>0) print sum/n; else print "N/A"}')
    
    if [[ "$our_overall" != "N/A" && "$swarm_overall" != "N/A" ]]; then
      improvement=$(echo "scale=1; (($swarm_overall - $our_overall) / $swarm_overall) * 100" | bc -l 2>/dev/null || echo "0")
      faster_system="Our System"
      if (( $(echo "$improvement < 0" | bc -l 2>/dev/null || echo 0) )); then
        faster_system="Swarm"
        improvement=$(echo "scale=1; $improvement * -1" | bc -l)
      fi
      
      cat <<EOF
- **Overall Performance**: $faster_system is $(printf "%.1f%%" "$improvement") faster on average
- **Mean Latency**: Our System: $(printf "%.2f" "$our_overall") ms | Swarm: $(printf "%.2f" "$swarm_overall") ms

EOF
    fi
  fi
  
  cat <<EOF
### Visualizations

EOF

  # List available plots
  if [[ -d "$PLOTS_DIR" ]]; then
    has_plots=false
    for plot in "$PLOTS_DIR"/*.png "$PLOTS_DIR"/*.jpg "$PLOTS_DIR"/*.svg; do
      if [[ -f "$plot" ]]; then
        has_plots=true
        plot_name=$(basename "$plot")
        cat <<EOF
- [$(basename "$plot_name" .png .jpg .svg | sed 's/_/ /g' | sed 's/\b\(.\)/\u\1/g')]($(basename "$RESULTS_DIR")/plots/$plot_name)

EOF
      fi
    done
    
    if [[ "$has_plots" == "false" ]]; then
      cat <<EOF
*No plots generated. Run the analysis script to generate visualizations.*

EOF
    fi
  else
    cat <<EOF
*Plots directory not found.*

EOF
  fi
  
  # Conclusions and recommendations
  cat <<EOF
## Conclusions and Recommendations

### Key Takeaways

1. **Upload Performance**: Based on the test results, our system $(if [[ -n "$upload_file" && "$our_overall" != "N/A" && "$swarm_overall" != "N/A" ]]; then
      if (( $(echo "$our_overall < $swarm_overall" | bc -l 2>/dev/null || echo 0) )); then
        echo "demonstrates superior upload latency"
      else
        echo "shows comparable upload latency"
      fi
    else
      echo "performance characteristics"
    fi) compared to Swarm.

2. **Replication**: Content replication times vary based on network topology and node count. See detailed results above.

3. **Network Convergence**: Both systems demonstrate different convergence characteristics. Our system $(if [[ -n "$conv_file" ]]; then echo "shows"; else echo "would show"; fi) specific convergence patterns based on the test configuration.

### Recommendations

- **For Production Use**: Consider the trade-offs between latency, throughput, and network overhead
- **Scaling**: Test with larger node counts to validate performance at scale
- **Network Conditions**: Run tests under various network conditions to assess robustness
- **Further Analysis**: Review detailed logs in \`logs/\` directory for deeper insights

### Next Steps

1. Review detailed logs: \`logs/test.log\` and \`logs/errors.log\`
2. Analyze visualizations in \`plots/\` directory
3. Compare results across different test runs
4. Consider running extended tests for statistical significance

---

## Appendix

### File Structure

\`\`\`
$(basename "$RESULTS_DIR")/
├── our_system/          # Our system test results
├── swarm/              # Swarm test results  
├── comparison/         # Aggregated comparison data
├── plots/              # Generated visualizations
├── logs/               # Test execution logs
└── REPORT.md           # This report
\`\`\`

### Raw Data Files

- **Our System Results**: \`our_system/\`
- **Swarm Results**: \`swarm/\`
- **Comparison Data**: \`comparison/\`
- **Test Logs**: \`logs/\`

### Generated Visualizations

EOF

  if [[ -d "$PLOTS_DIR" ]]; then
    has_plots=false
    for plot in "$PLOTS_DIR"/*.png "$PLOTS_DIR"/*.jpg "$PLOTS_DIR"/*.svg; do
      if [[ -f "$plot" ]]; then
        has_plots=true
        echo "- \`$(basename "$plot")\`"
      fi
    done
    if [[ "$has_plots" == "false" ]]; then
      echo "*No plots available*"
    fi
  else
    echo "*No plots directory found*"
  fi
  
  cat <<EOF

---

**Report Generated**: $(date '+%Y-%m-%d %H:%M:%S')  
**Script Version**: 1.0

EOF

} > "$OUTPUT_FILE"

echo -e "${GREEN}Report generated successfully: $OUTPUT_FILE${NC}"
