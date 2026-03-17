#!/usr/bin/env bash
# Purpose: Generate comprehensive markdown report from test results
# Usage: ./scripts/analysis/generate_swarm_report.sh [--results-dir <dir>] [--output <file>]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Optional: source results_dir.sh if present (for RESULTS_DIR preset)
# Script works without it via --results-dir or artifacts/swarm_comparison_tests

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
    # Calculate stats for our system (latency_ms is column 4)
    our_stats=$(grep "^our_system," "$upload_file" 2>/dev/null | awk -F',' '$4 != "ERROR" && $4 != "" {print $4}' | sort -n | awk '
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
    
    # Calculate stats for Swarm (latency_ms is column 4)
    swarm_stats=$(grep "^swarm," "$upload_file" 2>/dev/null | awk -F',' '$4 != "ERROR" && $4 != "" {print $4}' | sort -n | awk '
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
    # Group by system and payload size (latency_ms is column 4)
    awk -F',' '
      NR == 1 { next }
      $4 != "ERROR" && $4 != "" {
        sys = $1
        payload = $2
        latency = $4
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

    # Upload throughput table (batch: total_bytes/total_batch_s when total_batch_ms present)
    cat <<EOF

| System | Payload Size | Batch Size | Mean Latency (ms) | Batch Throughput (MB/s) | Samples |
|--------|--------------|------------|-------------------|-------------------------|---------|
EOF
    awk -F',' '
      NR == 1 { next }
      {
        if (NF >= 7 && $6 != "ERROR" && $6 != "" && $7 != "" && $7+0 > 0) {
          sys = $1; payload = $3; batch = ($4+0) ? $4+0 : 1; latency = $6; total_ms = $7+0
        } else if (NF >= 6 && $5 != "ERROR" && $5 != "" && $6 != "" && $6+0 > 0) {
          sys = $1; payload = $2; batch = ($3+0) ? $3+0 : 1; latency = $5; total_ms = $6+0
        } else if (NF >= 5 && $4 != "ERROR" && $4 != "") {
          sys = $1; payload = $2; batch = 1; latency = $4; total_ms = $4
        } else {
          next
        }
        key = sys "," payload "," batch
        count[key]++
        latency_sum[key] += latency
        total_ms_sum[key] += total_ms
        payload_val[key] = payload
        batch_val[key] = batch
      }
      END {
        for (key in count) {
          split(key, parts, ",")
          sys = parts[1]
          payload = payload_val[key]
          batch = batch_val[key]
          n = count[key]
          mean_latency = latency_sum[key] / n
          mean_total_ms = total_ms_sum[key] / n
          total_bytes = payload * batch
          batch_throughput = (total_bytes / (mean_total_ms / 1000)) / 1000000
          if (payload >= 1048576) {
            payload_str = sprintf("%.1f MB", payload / 1048576)
          } else if (payload >= 1024) {
            payload_str = sprintf("%.1f KB", payload / 1024)
          } else {
            payload_str = sprintf("%d B", payload)
          }
          printf "| %s | %s | %d | %.2f | %.2f | %d |\n", sys, payload_str, batch, mean_latency, batch_throughput, n
        }
      }
    ' "$upload_file" | sort
  else
    echo "| *No upload test results found* | | | | |"
  fi

  cat <<EOF

**Raw Data**: [Upload Results]($(basename "$RESULTS_DIR")/$(basename "$upload_file" 2>/dev/null || echo "upload_results.csv"))

---

### Download Throughput Test

EOF

  # Download results: prefer aggregated, then cold/warm files
  download_agg_file="$RESULTS_DIR/download_aggregated.csv"
  download_file=""
  if [[ -f "$download_agg_file" ]]; then
    download_file="$download_agg_file"
  else
    for f in "$RESULTS_DIR"/download_n*_cold.csv "$RESULTS_DIR"/download_n*_warm.csv \
             "$OUR_SYSTEM_DIR"/*download*.csv "$SWARM_DIR"/*download*.csv \
             "$COMPARISON_DIR"/*download*.csv "$RESULTS_DIR"/*download*.csv; do
      if [[ -f "$f" ]]; then
        download_file="$f"
        break
      fi
    done
  fi

  if [[ -n "$download_file" && -f "$download_file" ]]; then
    # Check if file has cache_mode (5th field before ttfb/total)
    has_cache_mode=false
    if head -1 "$download_file" | grep -q "cache_mode"; then
      has_cache_mode=true
    fi

    if [[ "$has_cache_mode" == "true" ]]; then
      cat <<EOF
| System | Payload Size | Cache Mode | Mean TTFB (ms) | Mean Total (ms) | Throughput (MB/s) | Samples |
|--------|--------------|------------|----------------|-----------------|-------------------|---------|
EOF
      awk -F',' '
        NR == 1 { next }
        NF >= 6 && $5 != "ERROR" && $6 != "ERROR" && $5 != "" && $6 != "" {
          sys = $1
          payload = $2
          cache = $4
          ttfb = $5
          total = $6
          key = sys "," payload "," cache
          ttfb_sum[key] += ttfb
          total_sum[key] += total
          payload_val[key] = payload
          cache_val[key] = cache
          count[key]++
        }
        END {
          for (key in count) {
            split(key, parts, ",")
            sys = parts[1]
            payload = payload_val[key]
            cache = cache_val[key]
            n = count[key]
            mean_ttfb = ttfb_sum[key] / n
            mean_total = total_sum[key] / n
            payload_mb = payload / 1048576
            time_sec = mean_total / 1000
            throughput = (time_sec > 0) ? payload_mb / time_sec : 0
            if (payload >= 1048576) payload_str = sprintf("%.1f MB", payload / 1048576)
            else if (payload >= 1024) payload_str = sprintf("%.1f KB", payload / 1024)
            else payload_str = sprintf("%d B", payload)
            printf "| %s | %s | %s | %.2f | %.2f | %.2f | %d |\n",
              sys, payload_str, cache, mean_ttfb, mean_total, throughput, n
          }
        }
      ' "$download_file" | sort
    else
      cat <<EOF
| System | Payload Size | Mean TTFB (ms) | Mean Total (ms) | Throughput (MB/s) | Samples |
|--------|--------------|----------------|-----------------|-------------------|---------|
EOF
      awk -F',' '
        NR == 1 { next }
        NF >= 6 && $5 != "ERROR" && $6 != "ERROR" && $5 != "" && $6 != "" {
          sys = $1
          payload = $2
          ttfb = $5
          total = $6
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
            payload_mb = payload / 1048576
            time_sec = mean_total / 1000
            throughput = (time_sec > 0) ? payload_mb / time_sec : 0
            if (payload >= 1048576) payload_str = sprintf("%.1f MB", payload / 1048576)
            else if (payload >= 1024) payload_str = sprintf("%.1f KB", payload / 1024)
            else payload_str = sprintf("%d B", payload)
            printf "| %s | %s | %.2f | %.2f | %.2f | %d |\n",
              sys, payload_str, mean_ttfb, mean_total, throughput, n
          }
        }
      ' "$download_file" | sort
    fi

    # Cold vs Warm comparison (when cache_mode present)
    if [[ "$has_cache_mode" == "true" ]]; then
      cat <<EOF

#### Cold vs Warm Cache Comparison

Cold: content not in local cache (DHT lookup + fetch). Warm: content in cache (prime get + measured get).

| System | Payload Size | Cold TTFB (ms) | Warm TTFB (ms) | Cold Total (ms) | Warm Total (ms) | Speedup (cold→warm) |
|--------|--------------|----------------|----------------|-----------------|-----------------|---------------------|
EOF
      awk -F',' '
        NR == 1 { next }
        NF >= 6 && $5 != "ERROR" && $6 != "ERROR" && $5 != "" && $6 != "" {
          sys = $1
          payload = $2
          cache = $4
          ttfb = $5
          total = $6
          key = sys "," payload "," cache
          ttfb_sum[key] += ttfb
          total_sum[key] += total
          count[key]++
          payload_val[key] = payload
          cache_val[key] = cache
        }
        END {
          for (key in count) {
            split(key, parts, ",")
            sys = parts[1]
            payload = payload_val[key]
            cache = cache_val[key]
            cold_key = sys "," payload ",cold"
            warm_key = sys "," payload ",warm"
            if (!(cold_key in count) || !(warm_key in count)) continue
            n_cold = count[cold_key]
            n_warm = count[warm_key]
            mean_ttfb_cold = ttfb_sum[cold_key] / n_cold
            mean_ttfb_warm = ttfb_sum[warm_key] / n_warm
            mean_total_cold = total_sum[cold_key] / n_cold
            mean_total_warm = total_sum[warm_key] / n_warm
            speedup = (mean_total_warm > 0) ? mean_total_cold / mean_total_warm : 0
            if (payload >= 1048576) payload_str = sprintf("%.1f MB", payload / 1048576)
            else if (payload >= 1024) payload_str = sprintf("%.1f KB", payload / 1024)
            else payload_str = sprintf("%d B", payload)
            printf "| %s | %s | %.2f | %.2f | %.2f | %.2f | %.2fx |\n",
              sys, payload_str, mean_ttfb_cold, mean_ttfb_warm,
              mean_total_cold, mean_total_warm, speedup
          }
        }
      ' "$download_file" | sort -t'|' -k2 -k1
    fi

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
  
  # Network hops test
  hops_file=""
  for f in "$RESULTS_DIR"/network_hops*.csv; do
    if [[ -f "$f" ]]; then
      hops_file="$f"
      break
    fi
  done

  if [[ -n "$hops_file" && -f "$hops_file" ]]; then
    cat <<EOF
### Network Hops Test

DHT lookup hop count per operation (system, operation, payload_size, hops). vn-IPFS reports this metric; Swarm does not.

| System | Operation | Payload Size | Mean Hops | Median Hops | Min | Max | Samples |
|--------|-----------|--------------|-----------|-------------|-----|-----|---------|
EOF
    awk -F',' '
      NR == 1 { next }
      $5 != "" && $5 ~ /^[0-9]/ {
        key = $1 "," $2 "," $3
        n = count[key]++
        sum[key] += $5
        values[key] = (values[key] == "" ? $5 : values[key] " " $5)
      }
      END {
        for (key in count) {
          split(key, parts, ",")
          sys = parts[1]
          op = parts[2]
          payload = parts[3]
          n = count[key]
          mean = sum[key] / n
          split(values[key], h, " ")
          asort(h)
          if (n % 2 == 0) {
            median = (h[n/2] + h[n/2+1]) / 2
          } else {
            median = h[(n+1)/2]
          }
          min_val = h[1]
          max_val = h[n]
          if (payload >= 1048576) {
            payload_str = sprintf("%.1f MB", payload / 1048576)
          } else if (payload >= 1024) {
            payload_str = sprintf("%.1f KB", payload / 1024)
          } else {
            payload_str = sprintf("%d B", payload)
          }
          printf "| %s | %s | %s | %.1f | %.1f | %.0f | %.0f | %d |\n",
            sys, op, payload_str, mean, median, min_val, max_val, n
        }
      }
    ' "$hops_file" | sort
    cat <<EOF

**Raw Data**: [Network Hops]($(basename "$RESULTS_DIR")/$(basename "$hops_file"))

---
EOF
  fi

  # Lookup complexity test (O(log N) verification)
  lookup_complexity_file=""
  for f in "$RESULTS_DIR"/lookup_complexity*.csv; do
    if [[ -f "$f" ]]; then
      lookup_complexity_file="$f"
      break
    fi
  done

  if [[ -n "$lookup_complexity_file" && -f "$lookup_complexity_file" ]]; then
    cat <<EOF
### Lookup Complexity Test (O(log N) Verification)

Hops vs node count. Slope ~1 in hops vs log10(N) verifies O(log N). vn-IPFS reports hops; Swarm does not.

| System | Node Count | Operation | Mean Hops | Median Hops | Samples |
|--------|------------|-----------|-----------|-------------|---------|
EOF
    awk -F',' '
      NR == 1 { next }
      $4 != "" && $4 != "N/A" && $4 ~ /^[0-9]/ {
        key = $1 "," $2 "," $3
        n = count[key]++
        sum[key] += $4
        values[key] = (values[key] == "" ? $4 : values[key] " " $4)
      }
      END {
        for (key in count) {
          split(key, parts, ",")
          sys = parts[1]
          nodes = parts[2]
          op = parts[3]
          n = count[key]
          mean = sum[key] / n
          split(values[key], h, " ")
          asort(h)
          if (n % 2 == 0) {
            median = (h[n/2] + h[n/2+1]) / 2
          } else {
            median = h[(n+1)/2]
          }
          printf "| %s | %s | %s | %.2f | %.2f | %d |\n", sys, nodes, op, mean, median, n
        }
      }
    ' "$lookup_complexity_file" | sort -t'|' -k2 -n -k1 -k3

    # O(log N) regression: hops ~ log10(N). Slope ~1 and high R² support O(log N).
    cat <<EOF

#### O(log N) Regression (hops vs log10(N))

| System | Operation | Slope | R² | Interpretation |
|--------|-----------|-------|-----|----------------|
EOF
    awk -F',' '
      NR == 1 { next }
      $4 != "" && $4 != "N/A" && $4 ~ /^[0-9]/ {
        key = $1 "," $2 "," $3
        n[key]++
        sum[key] += $4
      }
      END {
        for (key in n) {
          split(key, p, ",")
          sys = p[1]
          nodes = p[2] + 0
          op = p[3]
          mean = sum[key] / n[key]
          logN = (nodes > 0) ? log(nodes) / log(10) : 0
          so = sys "," op
          nn[so]++
          sx[so] += logN
          sy[so] += mean
          sxy[so] += logN * mean
          sxx[so] += logN * logN
          syy[so] += mean * mean
        }
        for (so in nn) {
          n = nn[so]
          if (n < 2) {
            split(so, p, ",")
            printf "| %s | %s | N/A | N/A | too few points |\n", p[1], p[2]
            continue
          }
          denom = n * sxx[so] - sx[so] * sx[so]
          if (denom == 0) {
            split(so, p, ",")
            printf "| %s | %s | N/A | N/A | degenerate |\n", p[1], p[2]
            continue
          }
          slope = (n * sxy[so] - sx[so] * sy[so]) / denom
          denom_r2 = (n * sxx[so] - sx[so] * sx[so]) * (n * syy[so] - sy[so] * sy[so])
          num_r2 = n * sxy[so] - sx[so] * sy[so]; r2 = (denom_r2 > 0) ? (num_r2 * num_r2) / denom_r2 : 0
          interp = (slope > 0.3 && slope < 3 && r2 > 0.5) ? "O(log N) consistent" : "check scaling"
          split(so, p, ",")
          printf "| %s | %s | %.2f | %.2f | %s |\n", p[1], p[2], slope, r2, interp
        }
      }
    ' "$lookup_complexity_file" | sort -t'|' -k1,1 -k3,3

    cat <<EOF

**Raw Data**: [Lookup Complexity]($(basename "$RESULTS_DIR")/$(basename "$lookup_complexity_file"))

---
EOF
  fi

  # Scaling comparison: vn-IPFS vs Swarm — latency vs log10(N)
  upload_agg="$RESULTS_DIR/upload_aggregated.csv"
  download_agg="$RESULTS_DIR/download_aggregated.csv"
  if [[ -f "$upload_agg" && -f "$download_agg" ]]; then
    cat <<EOF
### Scaling Comparison (vn-IPFS vs Swarm)

Latency vs node count. Slope near 0 = good scaling (O(log N) or better). Higher slope = stronger N dependence.

| System | Upload Slope | Upload R² | Download Slope | Download R² |
|--------|--------------|-----------|----------------|-------------|
EOF
    (
      # Upload: mean latency per (system, node_count)
      awk -F',' '
        NR==1 { next }
        $6 != "" && $6 != "ERROR" && $6 ~ /^[0-9.]/ {
          key = $1 "," $2
          sum[key] += $6
          n[key]++
        }
        END {
          for (k in n) {
            split(k, p, ",")
            printf "%s\t%s\t%.2f\n", p[1], p[2], sum[k]/n[k]
          }
        }
      ' "$upload_agg" > "$RESULTS_DIR/.upload_scale"
      # Download: mean total_ms per (system, node_count), cold only if cache_mode present
      awk -F',' '
        NR==1 { next }
        NF>=7 && $7 != "" && $7 != "ERROR" && $7 ~ /^[0-9.]/ {
          cold = (NF>=7 && $5=="cold") ? 1 : (NF<7 ? 1 : 0)
          if (cold) {
            key = $1 "," $2
            sum[key] += $7
            n[key]++
          }
        }
        NF>=6 && NF<7 && $6 != "" && $6 != "ERROR" && $6 ~ /^[0-9.]/ {
          key = $1 "," $2
          sum[key] += $6
          n[key]++
        }
        END {
          for (k in n) {
            split(k, p, ",")
            printf "%s\t%s\t%.2f\n", p[1], p[2], sum[k]/n[k]
          }
        }
      ' "$download_agg" > "$RESULTS_DIR/.download_scale"
      # Regression helper
      regress() {
        local f="$1"
        while IFS=$'\t' read -r sys nodes mean; do
          [[ -z "$sys" ]] && continue
          logn=$(echo "scale=6; l($nodes)/l(10)" | bc -l 2>/dev/null || echo "0")
          echo "$sys $logn $mean"
        done < "$f" | awk '
          { sys=$1; x=$2; y=$3
            n[sys]++; sx[sys]+=x; sy[sys]+=y; sxy[sys]+=x*y; sxx[sys]+=x*x; syy[sys]+=y*y }
          END {
            for (s in n) {
              nn=n[s]
              denom=nn*sxx[s]-sx[s]*sx[s]
              if (denom==0) { slope="N/A"; r2="N/A" }
              else {
                slope=(nn*sxy[s]-sx[s]*sy[s])/denom
                d2=(nn*sxx[s]-sx[s]*sx[s])*(nn*syy[s]-sy[s]*sy[s])
                num=nn*sxy[s]-sx[s]*sy[s]
                r2=(d2>0)?(num*num/d2):0
              }
              printf "%s %.2f %.2f\n", s, slope+0, r2+0
            }
          }
        '
      }
      upload_reg=$(regress "$RESULTS_DIR/.upload_scale")
      download_reg=$(regress "$RESULTS_DIR/.download_scale")
      for sys in our_system swarm; do
        us=$(echo "$upload_reg" | awk -v s="$sys" '$1==s {printf "%.2f %.2f", $2, $3}')
        ds=$(echo "$download_reg" | awk -v s="$sys" '$1==s {printf "%.2f %.2f", $2, $3}')
        [[ -z "$us" ]] && us="N/A N/A"
        [[ -z "$ds" ]] && ds="N/A N/A"
        printf "| %s | %s | %s | %s | %s |\n" "$sys" $(echo $us | cut -d' ' -f1) $(echo $us | cut -d' ' -f2) $(echo $ds | cut -d' ' -f1) $(echo $ds | cut -d' ' -f2)
      done
      rm -f "$RESULTS_DIR/.upload_scale" "$RESULTS_DIR/.download_scale"
    )
    cat <<EOF

**Interpretation**: Slope ≈ 0 indicates latency does not grow with N (good). Positive slope indicates some N-dependence.

---
EOF
  fi

  # Isolated lookup latency (token routing vs provider discovery)
  lookup_latency_file=""
  for f in "$RESULTS_DIR"/lookup_latency*.csv; do
    if [[ -f "$f" ]]; then
      lookup_latency_file="$f"
      break
    fi
  done
  if [[ -n "$lookup_latency_file" && -f "$lookup_latency_file" ]]; then
    cat <<EOF
### Lookup Latency (Token Routing vs Provider Discovery)

vn-IPFS: isolated GetToken latency (/lookup). Swarm: TTFB as lookup proxy (discovery before first byte).

| System | Mean Lookup (ms) | Median Hops | Samples |
|--------|------------------|-------------|---------|
EOF
    awk -F',' '
      NR == 1 { next }
      $3 != "FAILED" && $3 != "" && $3 ~ /^[0-9.]/ {
        key = $1
        n[key]++; sum[key] += $3 + 0
        if ($4 != "" && $4 != "N/A" && $4 ~ /^[0-9]/) {
          hops_n[key]++; hops_sum[key] += $4 + 0
        }
      }
      END {
        for (k in n) {
          mean = sum[k] / n[k]
          median_hops = (hops_n[k] > 0) ? sprintf("%.1f", hops_sum[k] / hops_n[k]) : "N/A"
          printf "| %s | %.2f | %s | %d |\n", k, mean, median_hops, n[k]
        }
      }
    ' "$lookup_latency_file" | sort
    cat <<EOF

**Raw Data**: [Lookup Latency]($(basename "$RESULTS_DIR")/$(basename "$lookup_latency_file"))

---
EOF
  fi

  # Routing overhead (token vs provider announce)
  if [[ -f "$RESULTS_DIR/routing_overhead_results.csv" ]]; then
    cat <<EOF
### Token Routing vs Provider Announcement Overhead

Message counts per operation. vn-IPFS: token lookup. Swarm: provider announcements + retrieval.

| System | Operation | Message Count | Overhead Type |
|--------|-----------|---------------|---------------|
EOF
    tail -n +2 "$RESULTS_DIR/routing_overhead_results.csv" | awk -F',' '{ printf "| %s | %s | %s | %s |\n", $1, $2, $3, $4 }'
    cat <<EOF

---
EOF
  fi

  # Resource usage (CPU/memory)
  resource_file=""
  for f in "$RESULTS_DIR"/resource_usage*.csv; do
    if [[ -f "$f" ]]; then
      resource_file="$f"
      break
    fi
  done

  if [[ -n "$resource_file" && -f "$resource_file" ]]; then
    cat <<EOF
### Resource Usage (CPU/Memory)

CPU and memory usage during tests (mean/peak per system). Columns: timestamp,container,cpu_pct,mem_usage_mb.

| System | Samples | CPU Mean % | CPU Peak % | Mem Mean (MB) | Mem Peak (MB) |
|--------|---------|------------|------------|---------------|---------------|
EOF
    awk -F',' '
      NR == 1 { next }
      $2 != "" {
        c = $2
        sys = (c ~ /^fall25-/) ? "our_system" : ((c ~ /^swarm-/) ? "swarm" : "other")
        if (sys == "other") next
        cpu = $3 + 0
        mem = $4 + 0
        count[sys]++
        cpu_sum[sys] += cpu
        mem_sum[sys] += mem
        if (cpu > cpu_max[sys]) cpu_max[sys] = cpu
        if (mem > mem_max[sys]) mem_max[sys] = mem
      }
      END {
        for (sys in count) {
          n = count[sys]
          cpu_mean = (n > 0) ? cpu_sum[sys] / n : 0
          mem_mean = (n > 0) ? mem_sum[sys] / n : 0
          printf "| %s | %d | %.2f | %.2f | %.2f | %.2f |\n",
            sys, n, cpu_mean, cpu_max[sys]+0, mem_mean, mem_max[sys]+0
        }
      }
    ' "$resource_file" | sort
    cat <<EOF

**Raw Data**: [Resource Usage]($(basename "$RESULTS_DIR")/$(basename "$resource_file"))

---
EOF
  fi

  # Storage efficiency test
  storage_eff_file=""
  for f in "$RESULTS_DIR"/storage_efficiency*.csv; do
    if [[ -f "$f" ]]; then
      storage_eff_file="$f"
      break
    fi
  done

  if [[ -n "$storage_eff_file" && -f "$storage_eff_file" ]]; then
    cat <<EOF
### Storage Efficiency Test

Disk usage and efficiency ratio per system. Columns: system, payload_size, nodes, disk_bytes, efficiency_ratio.

| System | Payload Size | Nodes | Disk Bytes | Efficiency Ratio |
|--------|--------------|-------|------------|------------------|
EOF
    tail -n +2 "$storage_eff_file" | awk -F',' '{
      sys = $1
      payload = $2
      nodes = $3
      disk = $4
      eff = $5
      if (payload >= 1048576) payload_str = sprintf("%.1f MB", payload / 1048576)
      else if (payload >= 1024) payload_str = sprintf("%.1f KB", payload / 1024)
      else payload_str = sprintf("%d B", payload)
      printf "| %s | %s | %s | %s | %s |\n", sys, payload_str, nodes, disk, eff
    }'
    cat <<EOF

**Raw Data**: [Storage Efficiency]($(basename "$RESULTS_DIR")/$(basename "$storage_eff_file"))

---
EOF
  fi

  # Partition recovery test
  partition_recovery_file=""
  for f in "$OUR_SYSTEM_DIR"/*partition_recovery*.csv "$SWARM_DIR"/*partition_recovery*.csv "$COMPARISON_DIR"/*partition_recovery*.csv "$RESULTS_DIR"/*partition_recovery*.csv; do
    if [[ -f "$f" ]]; then
      partition_recovery_file="$f"
      break
    fi
  done

  if [[ -n "$partition_recovery_file" && -f "$partition_recovery_file" ]]; then
    cat <<EOF
### Partition Recovery Test

Time from network reconnect until content available on previously partitioned nodes. Columns: system, node_count, partition_size, recovery_time_s.

| System | Node Count | Partition Size | Recovery Time (s) |
|--------|------------|----------------|-------------------|
EOF
    tail -n +2 "$partition_recovery_file" | awk -F',' '{
      sys = $1
      nodes = $2
      part = $3
      time = $4
      printf "| %s | %s | %s | %s |\n", sys, nodes, part, time
    }'
    cat <<EOF

**Raw Data**: [Partition Recovery]($(basename "$RESULTS_DIR")/$(basename "$partition_recovery_file"))

---
EOF
  fi

  # Concurrent read/write test
  concurrent_file=""
  for f in "$RESULTS_DIR"/concurrent_results.csv; do
    if [[ -f "$f" ]]; then
      concurrent_file="$f"
      break
    fi
  done

  if [[ -n "$concurrent_file" && -f "$concurrent_file" ]]; then
    cat <<EOF
### Concurrent Read/Write Test

N parallel uploads and M parallel downloads. Test matrix: 1w/0r, 5w/5r, 10w/10r. Columns: system, concurrent_writes, concurrent_reads, throughput_mbps, p99_latency_ms.

| System | Concurrent Writes | Concurrent Reads | Throughput (MB/s) | p99 Latency (ms) |
|--------|-------------------|------------------|-------------------|------------------|
EOF
    tail -n +2 "$concurrent_file" | awk -F',' '{
      sys = $1
      w = $2
      r = $3
      thr = $4
      p99 = $5
      printf "| %s | %s | %s | %s | %s |\n", sys, w, r, thr, p99
    }'
    cat <<EOF

**Raw Data**: [Concurrent Results]($(basename "$RESULTS_DIR")/concurrent_results.csv)

#### Lock Overhead Comparison

vn-IPFS uses write locking; Swarm uses chunk push without locks. p99_ratio > 1 or tput_ratio < 1 suggests lock overhead.

| Concurrency | vn-IPFS p99 | Swarm p99 | p99_ratio | vn-IPFS tput | Swarm tput | tput_ratio |
|-------------|-------------|-----------|-----------|--------------|------------|------------|
EOF
    python3 - "$concurrent_file" << 'PYEOF'
rows = list(csv.DictReader(p.open()))
by_key = {}
for r in rows:
  k = (r.get('concurrent_writes',''), r.get('concurrent_reads',''))
  if k not in by_key: by_key[k] = {}
  s = r.get('system','')
  by_key[k][s] = {'p99': float(r.get('p99_latency_ms',0) or 0), 'thr': float(r.get('throughput_mbps',0) or 0)}
for (w,r) in sorted(by_key.keys(), key=lambda x: (int(x[0] or 0), int(x[1] or 0))):
  d = by_key[(w,r)]
  vn, sw = d.get('our_system',{}), d.get('swarm',{})
  vp, sp = vn.get('p99',0), sw.get('p99',0)
  vt, st = vn.get('thr',0), sw.get('thr',0)
  r99 = vp/sp if sp > 0 else 0
  rthr = vt/st if st > 0 else 0
  print(f'| {w}w/{r}r | {vp:.2f} | {sp:.2f} | {r99:.2f} | {vt:.2f} | {st:.2f} | {rthr:.2f} |')
PYEOF
    cat <<EOF

---
EOF
  fi

  # Replication speed test
  replication_file=""
  for f in "$RESULTS_DIR"/replication*.csv; do
    if [[ -f "$f" ]]; then
      replication_file="$f"
      break
    fi
  done

  if [[ -n "$replication_file" && -f "$replication_file" ]]; then
    cat <<EOF
### Replication Speed Test

Time to reach R replicas after put. Columns: system, payload_size, nodes, replicas_target, time_to_R_s.

| System | Payload Size | Nodes | Replicas Target | Time to R (s) |
|--------|--------------|-------|-----------------|---------------|
EOF
    tail -n +2 "$replication_file" | awk -F',' '{
      sys = $1
      payload = $2
      nodes = $3
      target = $4
      time_r = $5
      if (payload >= 1048576) payload_str = sprintf("%.1f MB", payload / 1048576)
      else if (payload >= 1024) payload_str = sprintf("%.1f KB", payload / 1024)
      else payload_str = sprintf("%d B", payload)
      printf "| %s | %s | %s | %s | %s |\n", sys, payload_str, nodes, target, time_r
    }'
    cat <<EOF

**Raw Data**: [Replication Results]($(basename "$RESULTS_DIR")/$(basename "$replication_file"))

---
EOF
  fi

  # Replication distribution (N/M/F)
  if [[ -f "$RESULTS_DIR/replication_distribution.csv" ]]; then
    cat <<EOF
### Replication Distribution (N/M/F)

vn-IPFS: Near/Midrange/FarFlung. Swarm: chunk-based (N/A).

| System | Node Count | Near | Midrange | FarFlung |
|--------|------------|------|----------|----------|
EOF
    tail -n +2 "$RESULTS_DIR/replication_distribution.csv" | awk -F',' '{ printf "| %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5 }'
    cat <<EOF

---
EOF
  fi

  # Repair time after node failure
  if [[ -f "$RESULTS_DIR/repair_time_results.csv" ]]; then
    cat <<EOF
### Repair Time (After Node Failure)

Time to restore R replicas after stopping one node.

| System | Node Count | Repair Time (s) |
|--------|------------|-----------------|
EOF
    tail -n +2 "$RESULTS_DIR/repair_time_results.csv" | awk -F',' '{ printf "| %s | %s | %s |\n", $1, $2, $3 }'
    cat <<EOF

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
- **Storage Efficiency**: \`storage_efficiency_results.csv\` (when available)
- **Replication Speed**: \`replication_results.csv\` (when available)
- **Partition Recovery**: \`partition_recovery_results.csv\` (when available)
- **Lookup Complexity**: \`lookup_complexity_results.csv\` or \`lookup_complexity.csv\` (when available)
- **Concurrent Read/Write**: \`concurrent_results.csv\` (when available)
- **Resource Usage**: \`resource_usage.csv\` (when available)
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
