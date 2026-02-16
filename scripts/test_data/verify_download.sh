#!/usr/bin/env bash
set -euo pipefail

# Purpose: Verify downloaded content matches original files
# Usage: ./scripts/test_data/verify_download.sh [options]
#   --original <file>    Original file path
#   --downloaded <file>  Downloaded file path
#   --hash <hash>        Expected MD5 hash (optional, will compute if not provided)
#   --manifest <file>    Manifest CSV file (alternative to --original)
#   --output <file>      Output verification report (default: verification_report.csv)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Default values
ORIGINAL_FILE=""
DOWNLOADED_FILE=""
EXPECTED_HASH=""
MANIFEST_FILE=""
OUTPUT_FILE="verification_report.csv"
VERIFY_HASH=true

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --original)
      ORIGINAL_FILE="$2"
      shift 2
      ;;
    --downloaded)
      DOWNLOADED_FILE="$2"
      shift 2
      ;;
    --hash)
      EXPECTED_HASH="$2"
      shift 2
      ;;
    --manifest)
      MANIFEST_FILE="$2"
      shift 2
      ;;
    --output)
      OUTPUT_FILE="$2"
      shift 2
      ;;
    --no-hash-check)
      VERIFY_HASH=false
      shift
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --original <file>    Original file path"
      echo "  --downloaded <file>  Downloaded file path"
      echo "  --hash <hash>        Expected MD5 hash (optional)"
      echo "  --manifest <file>    Manifest CSV file (alternative to --original)"
      echo "  --output <file>      Output verification report (default: verification_report.csv)"
      echo "  --no-hash-check      Skip hash verification (only compare file sizes)"
      echo ""
      echo "Examples:"
      echo "  # Verify single file"
      echo "  $0 --original artifacts/test_data/test_file_binary_1KB --downloaded /tmp/downloaded.bin"
      echo ""
      echo "  # Verify using manifest"
      echo "  $0 --manifest artifacts/test_data/manifest.csv --downloaded /tmp/downloaded.bin"
      echo ""
      echo "  # Verify with expected hash"
      echo "  $0 --original file.bin --downloaded file_downloaded.bin --hash abc123..."
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Check for required tools
if [[ "$VERIFY_HASH" == "true" ]]; then
  if ! command -v md5sum >/dev/null 2>&1 && ! command -v md5 >/dev/null 2>&1; then
    echo -e "${YELLOW}Warning: md5sum/md5 not found. Hash verification disabled.${NC}" >&2
    VERIFY_HASH=false
  fi
fi

# Function to compute hash
compute_hash() {
  local file="$1"
  if [[ ! -f "$file" ]]; then
    echo ""
    return 1
  fi
  
  if command -v md5sum >/dev/null 2>&1; then
    md5sum "$file" | awk '{print $1}'
  elif command -v md5 >/dev/null 2>&1; then
    md5 -q "$file"
  else
    echo ""
    return 1
  fi
}

# Function to get file size
get_file_size() {
  local file="$1"
  if [[ ! -f "$file" ]]; then
    echo "0"
    return 1
  fi
  
  stat -c%s "$file" 2>/dev/null || stat -f%z "$file" 2>/dev/null || echo "0"
}

# Function to verify single file
verify_file() {
  local original="$1"
  local downloaded="$2"
  local expected_hash="${3:-}"
  local filename=$(basename "$original")
  
  local status="PASS"
  local errors=()
  local warnings=()
  
  # Check if files exist
  if [[ ! -f "$original" ]]; then
    echo "ERROR: Original file not found: $original" >&2
    return 1
  fi
  
  if [[ ! -f "$downloaded" ]]; then
    echo "ERROR: Downloaded file not found: $downloaded" >&2
    return 1
  fi
  
  # Compare file sizes
  local original_size=$(get_file_size "$original")
  local downloaded_size=$(get_file_size "$downloaded")
  
  if [[ $original_size -ne $downloaded_size ]]; then
    status="FAIL"
    errors+=("Size mismatch: original=$original_size bytes, downloaded=$downloaded_size bytes")
  fi
  
  # Verify hash if enabled
  if [[ "$VERIFY_HASH" == "true" ]]; then
    # Get expected hash from original or use provided hash
    if [[ -z "$expected_hash" ]]; then
      expected_hash=$(compute_hash "$original")
    fi
    
    local downloaded_hash=$(compute_hash "$downloaded")
    
    if [[ -z "$expected_hash" || -z "$downloaded_hash" ]]; then
      warnings+=("Could not compute hash")
    elif [[ "$expected_hash" != "$downloaded_hash" ]]; then
      status="FAIL"
      errors+=("Hash mismatch: expected=$expected_hash, got=$downloaded_hash")
    fi
  fi
  
  # Byte-by-byte comparison (for detailed verification)
  if [[ "$status" == "PASS" ]]; then
    if ! cmp -s "$original" "$downloaded" 2>/dev/null; then
      status="FAIL"
      errors+=("Byte comparison failed (files differ)")
    fi
  fi
  
  # Output result
  local error_msg=""
  if [[ ${#errors[@]} -gt 0 ]]; then
    error_msg=$(IFS='; '; echo "${errors[*]}")
  fi
  
  local warning_msg=""
  if [[ ${#warnings[@]} -gt 0 ]]; then
    warning_msg=$(IFS='; '; echo "${warnings[*]}")
  fi
  
  echo "$filename,$original,$downloaded,$original_size,$downloaded_size,$status,$error_msg,$warning_msg"
  
  # Print status
  if [[ "$status" == "PASS" ]]; then
    echo -e "  ${GREEN}✓${NC} $filename: Verified"
    return 0
  else
    echo -e "  ${RED}✗${NC} $filename: FAILED"
    for error in "${errors[@]}"; do
      echo -e "    ${RED}Error:${NC} $error"
    done
    for warning in "${warnings[@]}"; do
      echo -e "    ${YELLOW}Warning:${NC} $warning"
    done
    return 1
  fi
}

# Function to verify from manifest
verify_from_manifest() {
  local manifest="$1"
  local downloaded_dir="$2"
  
  if [[ ! -f "$manifest" ]]; then
    echo "Error: Manifest file not found: $manifest" >&2
    return 1
  fi
  
  if [[ ! -d "$downloaded_dir" ]]; then
    echo "Error: Downloaded directory not found: $downloaded_dir" >&2
    return 1
  fi
  
  local manifest_dir=$(dirname "$manifest")
  local total=0
  local passed=0
  local failed=0
  
  # Read manifest (skip header)
  tail -n +2 "$manifest" | while IFS=',' read -r filename type size size_formatted expected_hash; do
    # Clean up fields (remove quotes if present)
    filename=$(echo "$filename" | sed 's/^"//;s/"$//')
    expected_hash=$(echo "$expected_hash" | sed 's/^"//;s/"$//')
    
    local original_file="$manifest_dir/$filename"
    local downloaded_file="$downloaded_dir/$filename"
    
    # Try alternative names if exact match not found
    if [[ ! -f "$downloaded_file" ]]; then
      # Try without directory prefix
      downloaded_file="$downloaded_dir/$(basename "$filename")"
    fi
    
    if [[ ! -f "$downloaded_file" ]]; then
      echo -e "  ${YELLOW}⚠${NC} $filename: Downloaded file not found"
      echo "$filename,$original_file,$downloaded_file,0,0,NOT_FOUND,Downloaded file not found," >> "$OUTPUT_FILE"
      failed=$((failed + 1))
      continue
    fi
    
    total=$((total + 1))
    if verify_file "$original_file" "$downloaded_file" "$expected_hash" >> "$OUTPUT_FILE"; then
      passed=$((passed + 1))
    else
      failed=$((failed + 1))
    fi
  done
  
  echo ""
  echo "=========================================="
  echo "Verification Summary"
  echo "=========================================="
  echo "Total files: $total"
  echo -e "${GREEN}Passed: $passed${NC}"
  if [[ $failed -gt 0 ]]; then
    echo -e "${RED}Failed: $failed${NC}"
  else
    echo "Failed: $failed"
  fi
}

# Initialize output file
echo "filename,original_path,downloaded_path,original_size,downloaded_size,status,errors,warnings" > "$OUTPUT_FILE"

echo "=========================================="
echo "Data Verification"
echo "=========================================="

# Verify from manifest if provided
if [[ -n "$MANIFEST_FILE" ]]; then
  if [[ -z "$DOWNLOADED_FILE" ]]; then
    echo "Error: --downloaded required when using --manifest" >&2
    exit 1
  fi
  
  echo "Manifest: $MANIFEST_FILE"
  echo "Downloaded directory: $DOWNLOADED_FILE"
  echo ""
  
  verify_from_manifest "$MANIFEST_FILE" "$DOWNLOADED_FILE"
  
# Verify single file
elif [[ -n "$ORIGINAL_FILE" && -n "$DOWNLOADED_FILE" ]]; then
  echo "Original: $ORIGINAL_FILE"
  echo "Downloaded: $DOWNLOADED_FILE"
  if [[ -n "$EXPECTED_HASH" ]]; then
    echo "Expected hash: $EXPECTED_HASH"
  fi
  echo ""
  
  verify_file "$ORIGINAL_FILE" "$DOWNLOADED_FILE" "$EXPECTED_HASH" >> "$OUTPUT_FILE"
  
else
  echo "Error: Must provide either --original/--downloaded or --manifest/--downloaded" >&2
  exit 1
fi

echo ""
echo "Verification report saved to: $OUTPUT_FILE"
