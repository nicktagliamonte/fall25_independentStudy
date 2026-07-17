# Swarm Comparison Test Report

**Generated**: 2026-04-14 16:44:03  
**Test Run**: test results 20260414 042505  
**Results Directory**: `test_results_20260414_042505`

---

## Executive Summary

This report presents a comprehensive comparison between our distributed storage system and Ethereum Swarm (Bee v0.5.8) across multiple performance metrics including upload latency, download throughput, content replication, and network convergence.

### Key Findings

### Test Configuration

- **Test Systems**: Our System vs Ethereum Swarm (Bee v0.5.8)
- **Results Location**: `test_results_20260414_042505`
- **Raw Data**: Available in subdirectories (`our_system/`, `swarm/`, `comparison/`)

---

## Detailed Results

### Upload Latency Test

| System | Payload Size | Mean (ms) | Median (ms) | Std Dev | Min (ms) | Max (ms) | Samples |
|--------|--------------|-----------|-------------|---------|----------|----------|---------|
| 2026-04-14T08:43:56Z | 0.0 MB | 17.10 | 17.10 | 0.00 | 17.10 | 17.10 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 19.11 | 19.11 | 0.00 | 19.11 | 19.11 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 19.11 | 19.11 | 0.00 | 19.11 | 19.11 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 19.18 | 19.18 | 0.00 | 19.18 | 19.18 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 19.38 | 19.38 | 0.00 | 19.38 | 19.38 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 19.42 | 19.42 | 0.00 | 19.42 | 19.42 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 19.44 | 19.44 | 0.00 | 19.44 | 19.44 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 19.48 | 19.48 | 0.00 | 19.48 | 19.48 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 19.70 | 19.70 | 0.00 | 19.70 | 19.70 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 203.60 | 203.60 | 0.00 | 203.60 | 203.60 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 21.57 | 21.57 | 0.00 | 21.57 | 21.57 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 23.78 | 23.78 | 0.00 | 23.78 | 23.78 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 25.87 | 25.87 | 0.00 | 25.87 | 25.87 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 26.11 | 26.11 | 0.00 | 26.11 | 26.11 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 27.68 | 27.68 | 0.00 | 27.68 | 27.68 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 27.90 | 27.90 | 0.00 | 27.90 | 27.90 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 29.77 | 29.77 | 0.00 | 29.77 | 29.77 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 29.78 | 29.78 | 0.00 | 29.78 | 29.78 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 29.80 | 29.80 | 0.00 | 29.80 | 29.80 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 29.86 | 29.86 | 0.00 | 29.86 | 29.86 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 30.79 | 30.79 | 0.00 | 30.79 | 30.79 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 32.24 | 32.24 | 0.00 | 32.24 | 32.24 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 35.98 | 35.98 | 0.00 | 35.98 | 35.98 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 37.95 | 37.95 | 0.00 | 37.95 | 37.95 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 38.09 | 38.09 | 0.00 | 38.09 | 38.09 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 38.87 | 38.87 | 0.00 | 38.87 | 38.87 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 42.21 | 42.21 | 0.00 | 42.21 | 42.21 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 42.46 | 42.46 | 0.00 | 42.46 | 42.46 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 43.21 | 43.21 | 0.00 | 43.21 | 43.21 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 43.60 | 43.60 | 0.00 | 43.60 | 43.60 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 43.99 | 43.99 | 0.00 | 43.99 | 43.99 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 44.09 | 44.09 | 0.00 | 44.09 | 44.09 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 44.27 | 44.27 | 0.00 | 44.27 | 44.27 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 44.46 | 44.46 | 0.00 | 44.46 | 44.46 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 44.60 | 44.60 | 0.00 | 44.60 | 44.60 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 44.62 | 44.62 | 0.00 | 44.62 | 44.62 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 44.67 | 44.67 | 0.00 | 44.67 | 44.67 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 44.88 | 44.88 | 0.00 | 44.88 | 44.88 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 45.40 | 45.40 | 0.00 | 45.40 | 45.40 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 45.79 | 45.79 | 0.00 | 45.79 | 45.79 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 46.34 | 46.34 | 0.00 | 46.34 | 46.34 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 46.43 | 46.43 | 0.00 | 46.43 | 46.43 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 47.11 | 47.11 | 0.00 | 47.11 | 47.11 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 48.01 | 48.01 | 0.00 | 48.01 | 48.01 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 49.00 | 49.00 | 0.00 | 49.00 | 49.00 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 51.31 | 51.31 | 0.00 | 51.31 | 51.31 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 51.38 | 51.38 | 0.00 | 51.38 | 51.38 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 52.44 | 52.44 | 0.00 | 52.44 | 52.44 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 52.83 | 52.83 | 0.00 | 52.83 | 52.83 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 53.45 | 53.45 | 0.00 | 53.45 | 53.45 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 53.77 | 53.77 | 0.00 | 53.77 | 53.77 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 54.00 | 54.00 | 0.00 | 54.00 | 54.00 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 54.20 | 54.20 | 0.00 | 54.20 | 54.20 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 54.62 | 54.62 | 0.00 | 54.62 | 54.62 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 54.67 | 54.67 | 0.00 | 54.67 | 54.67 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 56.41 | 56.41 | 0.00 | 56.41 | 56.41 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 56.67 | 56.67 | 0.00 | 56.67 | 56.67 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 56.70 | 56.70 | 0.00 | 56.70 | 56.70 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 56.80 | 56.80 | 0.00 | 56.80 | 56.80 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 57.05 | 57.05 | 0.00 | 57.05 | 57.05 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 57.46 | 57.46 | 0.00 | 57.46 | 57.46 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 57.54 | 57.54 | 0.00 | 57.54 | 57.54 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 57.69 | 57.69 | 0.00 | 57.69 | 57.69 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 58.43 | 58.43 | 0.00 | 58.43 | 58.43 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 59.15 | 59.15 | 0.00 | 59.15 | 59.15 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 59.40 | 59.40 | 0.00 | 59.40 | 59.40 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 60.81 | 60.81 | 0.00 | 60.81 | 60.81 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 60.84 | 60.84 | 0.00 | 60.84 | 60.84 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 61.22 | 61.22 | 0.00 | 61.22 | 61.22 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 61.74 | 61.74 | 0.00 | 61.74 | 61.74 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 61.77 | 61.77 | 0.00 | 61.77 | 61.77 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 62.07 | 62.07 | 0.00 | 62.07 | 62.07 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 63.22 | 63.22 | 0.00 | 63.22 | 63.22 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 63.43 | 63.43 | 0.00 | 63.43 | 63.43 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 64.25 | 64.25 | 0.00 | 64.25 | 64.25 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 64.29 | 64.29 | 0.00 | 64.29 | 64.29 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 64.77 | 64.77 | 0.00 | 64.77 | 64.77 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 64.86 | 64.86 | 0.00 | 64.86 | 64.86 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 65.26 | 65.26 | 0.00 | 65.26 | 65.26 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 65.50 | 65.50 | 0.00 | 65.50 | 65.50 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 65.87 | 65.87 | 0.00 | 65.87 | 65.87 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 66.16 | 66.16 | 0.00 | 66.16 | 66.16 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 66.31 | 66.31 | 0.00 | 66.31 | 66.31 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 67.55 | 67.55 | 0.00 | 67.55 | 67.55 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 67.69 | 67.69 | 0.00 | 67.69 | 67.69 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 67.80 | 67.80 | 0.00 | 67.80 | 67.80 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 68.45 | 68.45 | 0.00 | 68.45 | 68.45 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 68.82 | 68.82 | 0.00 | 68.82 | 68.82 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 69.51 | 69.51 | 0.00 | 69.51 | 69.51 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 70.39 | 70.39 | 0.00 | 70.39 | 70.39 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 70.49 | 70.49 | 0.00 | 70.49 | 70.49 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 71.28 | 71.28 | 0.00 | 71.28 | 71.28 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 71.63 | 71.63 | 0.00 | 71.63 | 71.63 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 71.92 | 71.92 | 0.00 | 71.92 | 71.92 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 72.71 | 72.71 | 0.00 | 72.71 | 72.71 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 73.06 | 73.06 | 0.00 | 73.06 | 73.06 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 73.08 | 73.08 | 0.00 | 73.08 | 73.08 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 74.06 | 74.06 | 0.00 | 74.06 | 74.06 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 74.52 | 74.52 | 0.00 | 74.52 | 74.52 | 1 |
| 2026-04-14T08:43:56Z | 0.0 MB | 79.88 | 79.88 | 0.00 | 79.88 | 79.88 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 19.35 | 19.35 | 0.00 | 19.35 | 19.35 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 23.11 | 23.11 | 0.00 | 23.11 | 23.11 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 36.41 | 36.41 | 0.00 | 36.41 | 36.41 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 42.49 | 42.49 | 0.00 | 42.49 | 42.49 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 44.59 | 44.59 | 0.00 | 44.59 | 44.59 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 45.00 | 45.00 | 0.00 | 45.00 | 45.00 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 45.60 | 45.60 | 0.00 | 45.60 | 45.60 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 47.09 | 47.09 | 0.00 | 47.09 | 47.09 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 48.00 | 48.00 | 0.00 | 48.00 | 48.00 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 48.88 | 48.88 | 0.00 | 48.88 | 48.88 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 49.55 | 49.55 | 0.00 | 49.55 | 49.55 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 49.86 | 49.86 | 0.00 | 49.86 | 49.86 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 52.98 | 52.98 | 0.00 | 52.98 | 52.98 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 53.44 | 53.44 | 0.00 | 53.44 | 53.44 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 53.82 | 53.82 | 0.00 | 53.82 | 53.82 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 54.43 | 54.43 | 0.00 | 54.43 | 54.43 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 55.27 | 55.27 | 0.00 | 55.27 | 55.27 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 57.04 | 57.04 | 0.00 | 57.04 | 57.04 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 57.43 | 57.43 | 0.00 | 57.43 | 57.43 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 58.14 | 58.14 | 0.00 | 58.14 | 58.14 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 59.32 | 59.32 | 0.00 | 59.32 | 59.32 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 59.61 | 59.61 | 0.00 | 59.61 | 59.61 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 60.75 | 60.75 | 0.00 | 60.75 | 60.75 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 60.84 | 60.84 | 0.00 | 60.84 | 60.84 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 61.89 | 61.89 | 0.00 | 61.89 | 61.89 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 62.38 | 62.38 | 0.00 | 62.38 | 62.38 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 62.85 | 62.85 | 0.00 | 62.85 | 62.85 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 62.93 | 62.93 | 0.00 | 62.93 | 62.93 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 63.14 | 63.14 | 0.00 | 63.14 | 63.14 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 63.16 | 63.16 | 0.00 | 63.16 | 63.16 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 63.29 | 63.29 | 0.00 | 63.29 | 63.29 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 63.54 | 63.54 | 0.00 | 63.54 | 63.54 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 64.79 | 64.79 | 0.00 | 64.79 | 64.79 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 65.47 | 65.47 | 0.00 | 65.47 | 65.47 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 65.59 | 65.59 | 0.00 | 65.59 | 65.59 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 66.89 | 66.89 | 0.00 | 66.89 | 66.89 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 67.55 | 67.55 | 0.00 | 67.55 | 67.55 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 68.65 | 68.65 | 0.00 | 68.65 | 68.65 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 71.05 | 71.05 | 0.00 | 71.05 | 71.05 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 71.57 | 71.57 | 0.00 | 71.57 | 71.57 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 72.01 | 72.01 | 0.00 | 72.01 | 72.01 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 74.56 | 74.56 | 0.00 | 74.56 | 74.56 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 77.38 | 77.38 | 0.00 | 77.38 | 77.38 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 78.64 | 78.64 | 0.00 | 78.64 | 78.64 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 82.27 | 82.27 | 0.00 | 82.27 | 82.27 | 1 |
| 2026-04-14T08:47:21Z | 0.0 MB | 96.09 | 96.09 | 0.00 | 96.09 | 96.09 | 1 |

| System | Payload Size | Batch Size | Mean Latency (ms) | Batch Throughput (MB/s) | Samples |
|--------|--------------|------------|-------------------|-------------------------|---------|

**Raw Data**: [Upload Results](test_results_20260414_042505/resource_usage_upload_n100.csv)

---

### Download Throughput Test

| System | Payload Size | Cache Mode | Mean TTFB (ms) | Mean Total (ms) | Throughput (MB/s) | Samples |
|--------|--------------|------------|----------------|-----------------|-------------------|---------|
| our_system | 100 B | 1 | 0.00 | 0.37 | 0.26 | 4 |
| our_system | 100 B | 10 | 0.00 | 0.69 | 0.14 | 4 |
| our_system | 100 B | 100 | 0.00 | 0.52 | 0.18 | 4 |
| our_system | 100 B | 11 | 0.00 | 0.53 | 0.18 | 4 |
| our_system | 100 B | 12 | 0.00 | 0.33 | 0.29 | 4 |
| our_system | 100 B | 13 | 0.00 | 0.33 | 0.29 | 4 |
| our_system | 100 B | 14 | 0.00 | 0.39 | 0.25 | 4 |
| our_system | 100 B | 15 | 0.00 | 0.84 | 0.11 | 4 |
| our_system | 100 B | 16 | 0.00 | 0.62 | 0.15 | 4 |
| our_system | 100 B | 17 | 0.00 | 0.33 | 0.29 | 4 |
| our_system | 100 B | 18 | 0.00 | 0.57 | 0.17 | 4 |
| our_system | 100 B | 19 | 0.00 | 0.37 | 0.26 | 4 |
| our_system | 100 B | 2 | 0.00 | 0.50 | 0.19 | 4 |
| our_system | 100 B | 20 | 0.00 | 0.63 | 0.15 | 4 |
| our_system | 100 B | 21 | 0.00 | 0.31 | 0.31 | 4 |
| our_system | 100 B | 22 | 0.00 | 0.39 | 0.24 | 4 |
| our_system | 100 B | 23 | 0.00 | 0.32 | 0.30 | 4 |
| our_system | 100 B | 24 | 0.00 | 0.36 | 0.26 | 4 |
| our_system | 100 B | 25 | 0.00 | 0.33 | 0.29 | 4 |
| our_system | 100 B | 26 | 0.00 | 0.41 | 0.23 | 4 |
| our_system | 100 B | 27 | 0.00 | 1.68 | 0.06 | 4 |
| our_system | 100 B | 28 | 0.00 | 0.60 | 0.16 | 4 |
| our_system | 100 B | 29 | 0.00 | 0.61 | 0.16 | 4 |
| our_system | 100 B | 3 | 0.00 | 0.34 | 0.28 | 4 |
| our_system | 100 B | 30 | 0.00 | 0.65 | 0.15 | 4 |
| our_system | 100 B | 31 | 0.00 | 0.41 | 0.23 | 4 |
| our_system | 100 B | 32 | 0.00 | 0.32 | 0.30 | 4 |
| our_system | 100 B | 33 | 0.00 | 0.34 | 0.28 | 4 |
| our_system | 100 B | 34 | 0.00 | 0.34 | 0.28 | 4 |
| our_system | 100 B | 35 | 0.00 | 0.47 | 0.20 | 4 |
| our_system | 100 B | 36 | 0.00 | 0.46 | 0.21 | 4 |
| our_system | 100 B | 37 | 0.00 | 0.37 | 0.26 | 4 |
| our_system | 100 B | 38 | 0.00 | 0.52 | 0.18 | 4 |
| our_system | 100 B | 39 | 0.00 | 0.61 | 0.16 | 4 |
| our_system | 100 B | 4 | 0.00 | 1.98 | 0.05 | 4 |
| our_system | 100 B | 40 | 0.00 | 0.98 | 0.10 | 4 |
| our_system | 100 B | 41 | 0.00 | 0.38 | 0.25 | 4 |
| our_system | 100 B | 42 | 0.00 | 0.41 | 0.23 | 4 |
| our_system | 100 B | 43 | 0.00 | 0.53 | 0.18 | 4 |
| our_system | 100 B | 44 | 0.00 | 0.55 | 0.17 | 4 |
| our_system | 100 B | 45 | 0.00 | 0.80 | 0.12 | 4 |
| our_system | 100 B | 46 | 0.00 | 0.57 | 0.17 | 4 |
| our_system | 100 B | 47 | 0.00 | 0.44 | 0.21 | 4 |
| our_system | 100 B | 48 | 0.00 | 0.39 | 0.24 | 4 |
| our_system | 100 B | 49 | 0.00 | 0.44 | 0.21 | 4 |
| our_system | 100 B | 5 | 0.00 | 0.42 | 0.23 | 4 |
| our_system | 100 B | 50 | 0.00 | 0.33 | 0.29 | 4 |
| our_system | 100 B | 51 | 0.00 | 0.40 | 0.24 | 4 |
| our_system | 100 B | 52 | 0.00 | 0.83 | 0.11 | 4 |
| our_system | 100 B | 53 | 0.00 | 0.84 | 0.11 | 4 |
| our_system | 100 B | 54 | 0.00 | 0.44 | 0.21 | 4 |
| our_system | 100 B | 55 | 0.00 | 0.77 | 0.12 | 4 |
| our_system | 100 B | 56 | 0.00 | 0.38 | 0.25 | 4 |
| our_system | 100 B | 57 | 0.00 | 0.38 | 0.25 | 4 |
| our_system | 100 B | 58 | 0.00 | 0.41 | 0.23 | 4 |
| our_system | 100 B | 59 | 0.00 | 0.44 | 0.22 | 4 |
| our_system | 100 B | 6 | 0.00 | 0.31 | 0.31 | 4 |
| our_system | 100 B | 60 | 0.00 | 0.36 | 0.26 | 4 |
| our_system | 100 B | 61 | 0.00 | 0.50 | 0.19 | 4 |
| our_system | 100 B | 62 | 0.00 | 0.90 | 0.11 | 4 |
| our_system | 100 B | 63 | 0.00 | 1.04 | 0.09 | 4 |
| our_system | 100 B | 64 | 0.00 | 0.51 | 0.19 | 4 |
| our_system | 100 B | 65 | 0.00 | 0.37 | 0.26 | 4 |
| our_system | 100 B | 66 | 0.00 | 0.34 | 0.28 | 4 |
| our_system | 100 B | 67 | 0.00 | 0.38 | 0.25 | 4 |
| our_system | 100 B | 68 | 0.00 | 0.49 | 0.19 | 4 |
| our_system | 100 B | 69 | 0.00 | 0.44 | 0.22 | 4 |
| our_system | 100 B | 7 | 0.00 | 0.47 | 0.20 | 4 |
| our_system | 100 B | 70 | 0.00 | 0.59 | 0.16 | 4 |
| our_system | 100 B | 71 | 0.00 | 0.38 | 0.25 | 4 |
| our_system | 100 B | 72 | 0.00 | 0.32 | 0.29 | 4 |
| our_system | 100 B | 73 | 0.00 | 0.39 | 0.25 | 4 |
| our_system | 100 B | 74 | 0.00 | 0.67 | 0.14 | 4 |
| our_system | 100 B | 75 | 0.00 | 0.64 | 0.15 | 4 |
| our_system | 100 B | 76 | 0.00 | 0.36 | 0.26 | 4 |
| our_system | 100 B | 77 | 0.00 | 0.36 | 0.27 | 4 |
| our_system | 100 B | 78 | 0.00 | 1.43 | 0.07 | 4 |
| our_system | 100 B | 79 | 0.00 | 0.46 | 0.21 | 4 |
| our_system | 100 B | 8 | 0.00 | 0.45 | 0.21 | 4 |
| our_system | 100 B | 80 | 0.00 | 0.92 | 0.10 | 4 |
| our_system | 100 B | 81 | 0.00 | 0.42 | 0.23 | 4 |
| our_system | 100 B | 82 | 0.00 | 0.47 | 0.20 | 4 |
| our_system | 100 B | 83 | 0.00 | 0.41 | 0.23 | 4 |
| our_system | 100 B | 84 | 0.00 | 0.40 | 0.24 | 4 |
| our_system | 100 B | 85 | 0.00 | 0.71 | 0.13 | 4 |
| our_system | 100 B | 86 | 0.00 | 1.08 | 0.09 | 4 |
| our_system | 100 B | 87 | 0.00 | 0.31 | 0.30 | 4 |
| our_system | 100 B | 88 | 0.00 | 0.82 | 0.12 | 4 |
| our_system | 100 B | 89 | 0.00 | 0.32 | 0.29 | 4 |
| our_system | 100 B | 9 | 0.00 | 0.33 | 0.29 | 4 |
| our_system | 100 B | 90 | 0.00 | 0.37 | 0.26 | 4 |
| our_system | 100 B | 91 | 0.00 | 0.31 | 0.30 | 4 |
| our_system | 100 B | 92 | 0.00 | 0.35 | 0.27 | 4 |
| our_system | 100 B | 93 | 0.00 | 0.41 | 0.23 | 4 |
| our_system | 100 B | 94 | 0.00 | 0.40 | 0.24 | 4 |
| our_system | 100 B | 95 | 0.00 | 0.66 | 0.15 | 4 |
| our_system | 100 B | 96 | 0.00 | 0.71 | 0.13 | 4 |
| our_system | 100 B | 97 | 0.00 | 0.53 | 0.18 | 4 |
| our_system | 100 B | 98 | 0.00 | 0.85 | 0.11 | 4 |
| our_system | 100 B | 99 | 0.00 | 0.37 | 0.26 | 4 |
| our_system | 50 B | 1 | 0.00 | 0.73 | 0.07 | 4 |
| our_system | 50 B | 10 | 0.00 | 0.35 | 0.14 | 4 |
| our_system | 50 B | 100 | 0.00 | 0.48 | 0.10 | 4 |
| our_system | 50 B | 11 | 0.00 | 0.35 | 0.13 | 4 |
| our_system | 50 B | 12 | 0.00 | 0.37 | 0.13 | 4 |
| our_system | 50 B | 13 | 0.00 | 0.38 | 0.13 | 4 |
| our_system | 50 B | 14 | 0.00 | 0.36 | 0.13 | 4 |
| our_system | 50 B | 15 | 0.00 | 0.47 | 0.10 | 4 |
| our_system | 50 B | 16 | 0.00 | 0.34 | 0.14 | 4 |
| our_system | 50 B | 17 | 0.00 | 0.55 | 0.09 | 4 |
| our_system | 50 B | 18 | 0.00 | 0.84 | 0.06 | 4 |
| our_system | 50 B | 19 | 0.00 | 0.41 | 0.12 | 4 |
| our_system | 50 B | 2 | 0.00 | 0.43 | 0.11 | 4 |
| our_system | 50 B | 20 | 0.00 | 0.50 | 0.10 | 4 |
| our_system | 50 B | 21 | 0.00 | 0.41 | 0.12 | 4 |
| our_system | 50 B | 22 | 0.00 | 0.32 | 0.15 | 4 |
| our_system | 50 B | 23 | 0.00 | 0.70 | 0.07 | 4 |
| our_system | 50 B | 24 | 0.00 | 0.65 | 0.07 | 4 |
| our_system | 50 B | 25 | 0.00 | 0.85 | 0.06 | 4 |
| our_system | 50 B | 26 | 0.00 | 0.59 | 0.08 | 4 |
| our_system | 50 B | 27 | 0.00 | 0.48 | 0.10 | 4 |
| our_system | 50 B | 28 | 0.00 | 0.33 | 0.15 | 4 |
| our_system | 50 B | 29 | 0.00 | 0.45 | 0.11 | 4 |
| our_system | 50 B | 3 | 0.00 | 0.71 | 0.07 | 4 |
| our_system | 50 B | 30 | 0.00 | 0.33 | 0.14 | 4 |
| our_system | 50 B | 31 | 0.00 | 0.58 | 0.08 | 4 |
| our_system | 50 B | 32 | 0.00 | 0.41 | 0.11 | 4 |
| our_system | 50 B | 33 | 0.00 | 0.38 | 0.12 | 4 |
| our_system | 50 B | 34 | 0.00 | 0.40 | 0.12 | 4 |
| our_system | 50 B | 35 | 0.00 | 0.70 | 0.07 | 4 |
| our_system | 50 B | 36 | 0.00 | 1.51 | 0.03 | 4 |
| our_system | 50 B | 37 | 0.00 | 0.46 | 0.10 | 4 |
| our_system | 50 B | 38 | 0.00 | 0.93 | 0.05 | 4 |
| our_system | 50 B | 39 | 0.00 | 0.36 | 0.13 | 4 |
| our_system | 50 B | 4 | 0.00 | 0.34 | 0.14 | 4 |
| our_system | 50 B | 40 | 0.00 | 0.40 | 0.12 | 4 |
| our_system | 50 B | 41 | 0.00 | 0.67 | 0.07 | 4 |
| our_system | 50 B | 42 | 0.00 | 0.73 | 0.07 | 4 |
| our_system | 50 B | 43 | 0.00 | 0.59 | 0.08 | 4 |
| our_system | 50 B | 44 | 0.00 | 1.04 | 0.05 | 4 |
| our_system | 50 B | 45 | 0.00 | 1.20 | 0.04 | 4 |
| our_system | 50 B | 46 | 0.00 | 0.31 | 0.16 | 4 |
| our_system | 50 B | 47 | 0.00 | 0.42 | 0.11 | 4 |
| our_system | 50 B | 48 | 0.00 | 0.52 | 0.09 | 4 |
| our_system | 50 B | 49 | 0.00 | 0.53 | 0.09 | 4 |
| our_system | 50 B | 5 | 0.00 | 0.40 | 0.12 | 4 |
| our_system | 50 B | 50 | 0.00 | 0.39 | 0.12 | 4 |
| our_system | 50 B | 51 | 0.00 | 0.71 | 0.07 | 4 |
| our_system | 50 B | 52 | 0.00 | 0.37 | 0.13 | 4 |
| our_system | 50 B | 53 | 0.00 | 0.33 | 0.14 | 4 |
| our_system | 50 B | 54 | 0.00 | 1.19 | 0.04 | 4 |
| our_system | 50 B | 55 | 0.00 | 0.55 | 0.09 | 4 |
| our_system | 50 B | 56 | 0.00 | 0.50 | 0.09 | 4 |
| our_system | 50 B | 57 | 0.00 | 0.50 | 0.10 | 4 |
| our_system | 50 B | 58 | 0.00 | 0.41 | 0.12 | 4 |
| our_system | 50 B | 59 | 0.00 | 0.47 | 0.10 | 4 |
| our_system | 50 B | 6 | 0.00 | 2.57 | 0.02 | 4 |
| our_system | 50 B | 60 | 0.00 | 0.59 | 0.08 | 4 |
| our_system | 50 B | 61 | 0.00 | 0.57 | 0.08 | 4 |
| our_system | 50 B | 62 | 0.00 | 0.98 | 0.05 | 4 |
| our_system | 50 B | 63 | 0.00 | 1.05 | 0.05 | 4 |
| our_system | 50 B | 64 | 0.00 | 2.21 | 0.02 | 4 |
| our_system | 50 B | 65 | 0.00 | 0.35 | 0.13 | 4 |
| our_system | 50 B | 66 | 0.00 | 0.48 | 0.10 | 4 |
| our_system | 50 B | 67 | 0.00 | 0.36 | 0.13 | 4 |
| our_system | 50 B | 68 | 0.00 | 1.33 | 0.04 | 4 |
| our_system | 50 B | 69 | 0.00 | 0.52 | 0.09 | 4 |
| our_system | 50 B | 7 | 0.00 | 0.34 | 0.14 | 4 |
| our_system | 50 B | 70 | 0.00 | 0.59 | 0.08 | 4 |
| our_system | 50 B | 71 | 0.00 | 1.82 | 0.03 | 4 |
| our_system | 50 B | 72 | 0.00 | 1.40 | 0.03 | 4 |
| our_system | 50 B | 73 | 0.00 | 0.62 | 0.08 | 4 |
| our_system | 50 B | 74 | 0.00 | 0.53 | 0.09 | 4 |
| our_system | 50 B | 75 | 0.00 | 0.47 | 0.10 | 4 |
| our_system | 50 B | 76 | 0.00 | 0.53 | 0.09 | 4 |
| our_system | 50 B | 77 | 0.00 | 0.59 | 0.08 | 4 |
| our_system | 50 B | 78 | 0.00 | 0.62 | 0.08 | 4 |
| our_system | 50 B | 79 | 0.00 | 0.48 | 0.10 | 4 |
| our_system | 50 B | 8 | 0.00 | 0.60 | 0.08 | 4 |
| our_system | 50 B | 80 | 0.00 | 1.31 | 0.04 | 4 |
| our_system | 50 B | 81 | 0.00 | 0.32 | 0.15 | 4 |
| our_system | 50 B | 82 | 0.00 | 0.35 | 0.13 | 4 |
| our_system | 50 B | 83 | 0.00 | 0.77 | 0.06 | 4 |
| our_system | 50 B | 84 | 0.00 | 0.38 | 0.12 | 4 |
| our_system | 50 B | 85 | 0.00 | 0.66 | 0.07 | 4 |
| our_system | 50 B | 86 | 0.00 | 0.41 | 0.12 | 4 |
| our_system | 50 B | 87 | 0.00 | 4.90 | 0.01 | 4 |
| our_system | 50 B | 88 | 0.00 | 0.40 | 0.12 | 4 |
| our_system | 50 B | 89 | 0.00 | 0.50 | 0.10 | 4 |
| our_system | 50 B | 9 | 0.00 | 0.32 | 0.15 | 4 |
| our_system | 50 B | 90 | 0.00 | 0.33 | 0.14 | 4 |
| our_system | 50 B | 91 | 0.00 | 1.09 | 0.04 | 4 |
| our_system | 50 B | 92 | 0.00 | 0.82 | 0.06 | 4 |
| our_system | 50 B | 93 | 0.00 | 0.43 | 0.11 | 4 |
| our_system | 50 B | 94 | 0.00 | 0.39 | 0.12 | 4 |
| our_system | 50 B | 95 | 0.00 | 1.09 | 0.04 | 4 |
| our_system | 50 B | 96 | 0.00 | 0.65 | 0.07 | 4 |
| our_system | 50 B | 97 | 0.00 | 0.39 | 0.12 | 4 |
| our_system | 50 B | 98 | 0.00 | 0.34 | 0.14 | 4 |
| our_system | 50 B | 99 | 0.00 | 0.34 | 0.14 | 4 |

**Raw Data**: [Download Results](test_results_20260414_042505/download_aggregated.csv)

---
### Scaling Comparison (vn-IPFS vs Swarm)

Latency vs node count. Slope near 0 = good scaling (O(log N) or better). Higher slope = stronger N dependence.

| System | Upload Slope | Upload R² | Download Slope | Download R² |
|--------|--------------|-----------|----------------|-------------|
| our_system | 13.65 | 1.00 | 0.96 | 1.00 |
| swarm | N/A | N/A | N/A | N/A |

**Interpretation**: Slope ≈ 0 indicates latency does not grow with N (good). Positive slope indicates some N-dependence.

---
### Token Routing vs Provider Announcement Overhead

Message counts per operation. vn-IPFS: token lookup. Swarm: provider announcements + retrieval.

| System | Operation | Message Count | Overhead Type |
|--------|-----------|---------------|---------------|
| our_system | put | 0 | token_lookup |
| our_system | get | 1 | token_lookup |

---
### Resource Usage (CPU/Memory)

CPU and memory usage during tests (mean/peak per system). Columns: timestamp,container,cpu_pct,mem_usage_mb.

| System | Samples | CPU Mean % | CPU Peak % | Mem Mean (MB) | Mem Peak (MB) |
|--------|---------|------------|------------|---------------|---------------|
| our_system | 146 | 1.04 | 35.07 | 54.02 | 203.60 |

**Raw Data**: [Resource Usage](test_results_20260414_042505/resource_usage_upload_n100.csv)

---
### Storage Efficiency Test

Disk usage and efficiency ratio per system. Columns: system, payload_size, nodes, disk_bytes, efficiency_ratio.

| System | Payload Size | Nodes | Disk Bytes | Efficiency Ratio |
|--------|--------------|-------|------------|------------------|
| our_system | 64.0 KB | 100 | 2464333 | .0265 |

**Raw Data**: [Storage Efficiency](test_results_20260414_042505/storage_efficiency_results.csv)

---
### Concurrent Read/Write Test

N parallel uploads and M parallel downloads. Test matrix: 1w/0r, 5w/5r, 10w/10r. Columns: system, concurrent_writes, concurrent_reads, throughput_mbps, p99_latency_ms.

| System | Concurrent Writes | Concurrent Reads | Throughput (MB/s) | p99 Latency (ms) |
|--------|-------------------|------------------|-------------------|------------------|
| our_system | 1 | 0 | .55 | 80.126000000 |
| our_system | 5 | 5 | .60 | 110.984000000 |
| our_system | 10 | 10 | .60 | 192.024000000 |

**Raw Data**: [Concurrent Results](test_results_20260414_042505/concurrent_results.csv)

#### Lock Overhead Comparison

vn-IPFS uses write locking; Swarm uses chunk push without locks. p99_ratio > 1 or tput_ratio < 1 suggests lock overhead.

| Concurrency | vn-IPFS p99 | Swarm p99 | p99_ratio | vn-IPFS tput | Swarm tput | tput_ratio |
|-------------|-------------|-----------|-----------|--------------|------------|------------|
| 1w/0r | 80.13 | 0.00 | 0.00 | 0.55 | 0.00 | 0.00 |
| 5w/5r | 110.98 | 0.00 | 0.00 | 0.60 | 0.00 | 0.00 |
| 10w/10r | 192.02 | 0.00 | 0.00 | 0.60 | 0.00 | 0.00 |

---
### Replication Speed Test

Time to reach R replicas after put. Columns: system, payload_size, nodes, replicas_target, time_to_R_s.

| System | Payload Size | Nodes | Replicas Target | Time to R (s) |
|--------|--------------|-------|-----------------|---------------|
| our_system | 50 B | 1 | 0 | 0 |
| swarm | 50 B | N/A | N/A | N/A |
| our_system | 100 B | 1 | 0 | 0 |
| swarm | 100 B | N/A | N/A | N/A |

**Raw Data**: [Replication Results](test_results_20260414_042505/replication_distribution.csv)

---
### Replication Distribution (N/M/F)

vn-IPFS: Near/Midrange/FarFlung. Swarm: chunk-based (N/A).

| System | Node Count | Near | Midrange | FarFlung |
|--------|------------|------|----------|----------|
| our_system | 50 | 1 | 0 | 0 |
| swarm | 50 | N/A | N/A | N/A |
| our_system | 100 | 1 | 0 | 0 |
| swarm | 100 | N/A | N/A | N/A |

---
### Repair Time (After Node Failure)

Time to restore R replicas after stopping one node.

| System | Node Count | Repair Time (s) |
|--------|------------|-----------------|
| our_system | 50 | SKIP |
| swarm | 50 | SKIP |
| our_system | 100 | SKIP |
| swarm | 100 | SKIP |

---
### Replication Propagation Test

Measures the time for content to propagate across nodes in the network.

| System | Nodes | Time to 50% (s) | Time to 90% (s) | Time to 100% (s) |
|--------|-------|-----------------|-----------------|------------------|
| our_system | 50 | 1.00 | 0.00 | 0.00 |
| swarm | 50 | 0.00 | 0.00 | 0.00 |
| our_system | 100 | 1.00 | 0.00 | 0.00 |
| swarm | 100 | 0.00 | 0.00 | 0.00 |

**Raw Data**: [Replication Results](test_results_20260414_042505/replication_distribution.csv)

---
## Performance Comparisons

### Upload Latency Comparison

### Visualizations

*Plots directory not found.*

## Conclusions and Recommendations

### Key Takeaways

1. **Upload Performance**: Based on the test results, our system performance characteristics compared to Swarm.

2. **Replication**: Content replication times vary based on network topology and node count. See detailed results above.

3. **Network Convergence**: Both systems demonstrate different convergence characteristics. Our system would show specific convergence patterns based on the test configuration.

### Recommendations

- **For Production Use**: Consider the trade-offs between latency, throughput, and network overhead
- **Scaling**: Test with larger node counts to validate performance at scale
- **Network Conditions**: Run tests under various network conditions to assess robustness
- **Further Analysis**: Review detailed logs in `logs/` directory for deeper insights

### Next Steps

1. Review detailed logs: `logs/test.log` and `logs/errors.log`
2. Analyze visualizations in `plots/` directory
3. Compare results across different test runs
4. Consider running extended tests for statistical significance

---

## Appendix

### File Structure

```
test_results_20260414_042505/
├── our_system/          # Our system test results
├── swarm/              # Swarm test results  
├── comparison/         # Aggregated comparison data
├── plots/              # Generated visualizations
├── logs/               # Test execution logs
└── REPORT.md           # This report
```

### Raw Data Files

- **Our System Results**: `our_system/`
- **Swarm Results**: `swarm/`
- **Comparison Data**: `comparison/`
- **Storage Efficiency**: `storage_efficiency_results.csv` (when available)
- **Replication Speed**: `replication_results.csv` (when available)
- **Partition Recovery**: `partition_recovery_results.csv` (when available)
- **Lookup Complexity**: `lookup_complexity_results.csv` or `lookup_complexity.csv` (when available)
- **Concurrent Read/Write**: `concurrent_results.csv` (when available)
- **Resource Usage**: `resource_usage.csv` (when available)
- **Test Logs**: `logs/`

### Generated Visualizations

*No plots directory found*

---

**Report Generated**: 2026-04-14 16:44:04  
**Script Version**: 1.0

