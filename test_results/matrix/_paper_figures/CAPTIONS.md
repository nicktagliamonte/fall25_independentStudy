# Matrix paper figures

Generated from `/Users/garrett/Projects/Temple/Shi/vnIPFS/fall25_independentStudy/test_results_20260414_042505` with `_n<N>_i10_{vnipfs,swarm}` layout.
Node counts included: 10, 50, 100.

**Not plotted (non-comparable or omitted by design):** routing_overhead, repair_time, replication, replication_distribution.

## Figure notes

- **fig01 / fig02 (download warm):** Same-node cached GET after upload; LAN microbenchmark. Compares mean `total_ms` / `ttfb_ms` by payload size. Not predictive of wide-area behavior.

- **fig03 (upload):** Batch size **1** mean latency only; Swarm client path may differ from vn-IPFS Docker/exec path.

- **fig04 (concurrent):** Throughput as reported by the harness for each load label.

- **fig05 (storage):** Uses `efficiency_ratio`; **definitions differ between stacks**. Node counts excluded from this plot: [10].

- **fig06 (lookup complexity):** **vn-IPFS matrix cells**; `lookup_complexity_results.csv` from Docker. Does not prove asymptotics by itself — pair with analysis.

## Files

- `fig01_download_warm_total_ms.png`
- `fig02_download_warm_ttfb_ms.png`
- `fig03_upload_latency_batch1_mean.png`
- `fig04_concurrent_throughput.png`
