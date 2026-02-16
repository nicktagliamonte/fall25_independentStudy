#!/usr/bin/env bash
set -euo pipefail

# Purpose: Generate test files of various types and sizes for testing
# Usage: ./scripts/test_data/generate_test_files.sh [options]
#   --sizes <list>      Comma-separated file sizes in bytes (default: 1024,10240,102400,1048576)
#   --types <list>      Comma-separated types: binary,text,json (default: binary,text,json)
#   --output-dir <dir>  Output directory (default: artifacts/test_data)
#   --prefix <str>      Filename prefix (default: test_file)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Default values
SIZES="1024,10240,102400,1048576"  # 1KB, 10KB, 100KB, 1MB
TYPES="binary,text,json"
OUTPUT_DIR="artifacts/test_data"
PREFIX="test_file"

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --sizes)
      SIZES="$2"
      shift 2
      ;;
    --types)
      TYPES="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --prefix)
      PREFIX="$2"
      shift 2
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --sizes <list>      Comma-separated file sizes in bytes (default: 1024,10240,102400,1048576)"
      echo "  --types <list>      Comma-separated types: binary,text,json (default: binary,text,json)"
      echo "  --output-dir <dir>  Output directory (default: artifacts/test_data)"
      echo "  --prefix <str>      Filename prefix (default: test_file)"
      echo ""
      echo "Examples:"
      echo "  # Generate default test files"
      echo "  $0"
      echo ""
      echo "  # Generate only binary files of specific sizes"
      echo "  $0 --types binary --sizes 1024,1048576,10485760"
      echo ""
      echo "  # Custom output directory"
      echo "  $0 --output-dir /tmp/test_data"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Check for required tools
if ! command -v md5sum >/dev/null 2>&1 && ! command -v md5 >/dev/null 2>&1; then
  echo "Warning: md5sum/md5 not found. Hash generation will be skipped." >&2
fi

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Convert comma-separated lists to arrays
IFS=',' read -ra SIZE_ARRAY <<< "$SIZES"
IFS=',' read -ra TYPE_ARRAY <<< "$TYPES"

echo "=========================================="
echo "Test Data Generator"
echo "=========================================="
echo "Sizes: ${SIZE_ARRAY[*]} bytes"
echo "Types: ${TYPE_ARRAY[*]}"
echo "Output directory: $OUTPUT_DIR"
echo ""

# Function to format bytes
format_bytes() {
  local bytes=$1
  if [[ $bytes -lt 1024 ]]; then
    echo "${bytes}B"
  elif [[ $bytes -lt 1048576 ]]; then
    echo "$((bytes / 1024))KB"
  elif [[ $bytes -lt 1073741824 ]]; then
    echo "$((bytes / 1048576))MB"
  else
    echo "$((bytes / 1073741824))GB"
  fi
}

# Function to generate hash
generate_hash() {
  local file="$1"
  if command -v md5sum >/dev/null 2>&1; then
    md5sum "$file" | awk '{print $1}'
  elif command -v md5 >/dev/null 2>&1; then
    md5 -q "$file"
  else
    echo "no_hash_available"
  fi
}

# Function to generate random binary file
generate_binary_file() {
  local size=$1
  local output_file="$2"
  
  dd if=/dev/urandom of="$output_file" bs=1 count="$size" 2>/dev/null
}

# Function to generate text file with patterns
generate_text_file() {
  local size=$1
  local output_file="$2"
  
  # Generate text with repeating patterns
  local pattern="ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghijklmnopqrstuvwxyz"
  local pattern_len=${#pattern}
  local iterations=$((size / pattern_len + 1))
  
  > "$output_file"
  for i in $(seq 1 $iterations); do
    echo -n "$pattern" >> "$output_file"
  done
  
  # Truncate to exact size
  if command -v truncate >/dev/null 2>&1; then
    truncate -s "$size" "$output_file" 2>/dev/null
  else
    # Fallback: use head to limit size
    head -c "$size" "$output_file" > "${output_file}.tmp" 2>/dev/null && mv "${output_file}.tmp" "$output_file"
  fi
}

# Function to generate structured JSON file
generate_json_file() {
  local size=$1
  local output_file="$2"
  
  # Generate JSON with repeating structure
  # Use Python for reliable JSON generation
  python3 <<PYTHON_SCRIPT
import json
import random
import time
import sys

size = $size
output_file = "$output_file"

# Calculate how many entries we can fit
# Each entry is approximately: {"id":N,"data":"...","timestamp":N,"value":N},
# Estimate ~150 bytes per entry including comma
entry_size = 150
num_entries = max(1, size // entry_size)

entries = []
current_size = 2  # "[" and "]"

for i in range(num_entries):
    # Generate data to fill remaining space
    remaining = size - current_size - 50  # Reserve for closing bracket and structure
    if remaining <= 0:
        break
    
    # Generate random base64 data
    import base64
    import os
    data_bytes = os.urandom(min(remaining // 2, 1000))
    data = base64.b64encode(data_bytes).decode('ascii')[:remaining // 2]
    
    entry = {
        "id": i,
        "data": data,
        "timestamp": int(time.time()),
        "value": random.randint(0, 10000)
    }
    
    entry_json = json.dumps(entry)
    entry_size_actual = len(entry_json) + 1  # +1 for comma
    
    if current_size + entry_size_actual > size - 1:  # -1 for closing bracket
        break
    
    entries.append(entry)
    current_size += entry_size_actual

# Write JSON array
with open(output_file, 'w') as f:
    f.write('[')
    for i, entry in enumerate(entries):
        if i > 0:
            f.write(',')
        f.write(json.dumps(entry))
    f.write(']')

# Truncate to exact size if needed
with open(output_file, 'r+b') as f:
    f.seek(0, 2)
    actual_size = f.tell()
    if actual_size > size:
        f.truncate(size)
PYTHON_SCRIPT
}

# Generate manifest file for verification
MANIFEST_FILE="$OUTPUT_DIR/manifest.csv"
echo "filename,type,size_bytes,size_formatted,md5_hash" > "$MANIFEST_FILE"

# Generate files
TOTAL_FILES=0
for size in "${SIZE_ARRAY[@]}"; do
  for type in "${TYPE_ARRAY[@]}"; do
    local size_formatted=$(format_bytes "$size")
    local filename="${PREFIX}_${type}_${size_formatted}"
    local output_file="$OUTPUT_DIR/$filename"
    
    echo -e "${BLUE}Generating ${type} file: ${size_formatted}...${NC}"
    
    case "$type" in
      binary)
        generate_binary_file "$size" "$output_file"
        ;;
      text)
        generate_text_file "$size" "$output_file"
        ;;
      json)
        generate_json_file "$size" "$output_file"
        ;;
      *)
        echo "Warning: Unknown type '$type', skipping" >&2
        continue
        ;;
    esac
    
    # Verify file was created and has correct size
    if [[ ! -f "$output_file" ]]; then
      echo "Error: Failed to generate $output_file" >&2
      continue
    fi
    
    local actual_size=$(stat -c%s "$output_file" 2>/dev/null || stat -f%z "$output_file" 2>/dev/null || echo "0")
    if [[ $actual_size -ne $size ]]; then
      echo "Warning: File size mismatch. Expected: $size, Got: $actual_size" >&2
    fi
    
    # Generate hash
    local hash=$(generate_hash "$output_file")
    
    # Add to manifest
    echo "$filename,$type,$size,$size_formatted,$hash" >> "$MANIFEST_FILE"
    
    TOTAL_FILES=$((TOTAL_FILES + 1))
    echo -e "  ${GREEN}✓${NC} Created: $filename ($size_formatted, MD5: $hash)"
  done
done

echo ""
echo "=========================================="
echo "Generation Complete"
echo "=========================================="
echo "Total files generated: $TOTAL_FILES"
echo "Output directory: $OUTPUT_DIR"
echo "Manifest file: $MANIFEST_FILE"
echo ""
echo "To verify files:"
echo "  cat $MANIFEST_FILE"
echo ""
echo "To use in tests:"
echo "  export TEST_DATA_DIR=\"$OUTPUT_DIR\""
