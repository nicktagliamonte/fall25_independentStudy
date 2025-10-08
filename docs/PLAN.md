# SNG‑40 Block Transfer Plan

1. **Go vs CGO**
   A Go rewrite beats CGO here because the hot path is networking + concurrency: staying pure-Go keeps libp2p/bitswap in their native ecosystem, avoids CGO’s build/toolchain pain (per-platform compilers, linking troubles, static builds), sidesteps threading/stack handoff overhead between Go↔C (which kills goroutine scheduling and complicates perf [I ran into a ton of this over the summer, it kind of killed that work]), and makes memory, profiling, and debugging uniform across the stack. Alternatives if a full rewrite is too much: (1) keep a narrow C/C++ compute core and expose a tiny, synchronous FFI while doing all I/O/libp2p in Go; (2) split at a process boundary (gRPC/IPC) so each side stays idiomatic with clean versioning and crash isolation; (3) use CGO only for leaf calls with strict data copies, not streaming paths

2. **Libraries & Wiring**

   * **libp2p** for P2P (transport, security, multiplexing).
   * **boxo/bitswap** for block exchange + blockstore.
     These get wired together into a small `BlockService` interface (Put/Get/Provide).
     In the libraries and wiring layer, libp2p provides the transport substrate: we spin up a host with TCP/QUIC, wrap it in a secure channel (Noise or TLS) for authenticated, encrypted sessions, and then add a multiplexer like Yamux so multiple logical streams can share a single connection (or sng40 multiplexing, depending on how part (1) goes). On top of those streams is boxo/bitswap, which handles block exchange and coordinates wantlists/ledgers across peers. You back bitswap with a blockstore (in-mem or file-based), and expose it all through a slim BlockService API. That way the whole stack is layered: transports → security → mux → streams → bitswap → blockstore → SNG-40 integration.

3. **Integrating SNG‑40**
   To integrate SNG-40’s proprietary discovery layer, ACAN, we treat it as the replacement for IPFS’s DHT/provider system. Instead of publishing and querying providers through a global DHT, Bitswap’s “who has this CID?” calls are wired into ACAN’s Tuple Space APIs. When a node stores a block, it issues an announce into ACAN; when another node needs that block, it queries ACAN to get a peer list, then opens libp2p streams to those peers and pulls data via Bitswap. This keeps discovery fully under SNG-40’s control while letting libp2p and boxo handle the transport and block exchange layers unchanged.

4. **Replication Layer**
   The replication layer builds on top of block exchange by enforcing my N-Hop replication algorithm. When a node issues a replication request into the tuple space, the request includes a target replication count and an exclusion list of its own peers. Any node that is not on that list can take the request, decrement the replication counter, append its own peerlist to the exclusion list, and then re-publish the updated request back into the tuple space. After doing so, it pulls the file via Bitswap and stores it locally. This hop-by-hop handoff guarantees that replication fans out across disjoint neighborhoods rather than circling among direct peers, driving diversity and resilience without relying on global coordination.

5. **Milestones**

   * M0 — Go vs CGO decision & spike: Build a tiny CGO echo vs pure-Go echo over libp2p to measure context-switch/throughput overhead; decide “Go-only” unless a narrow compute FFI is justified. Report on the bench.
   * M1 — Libraries & wiring: Stand up libp2p Host (TCP/QUIC + Noise/TLS + Yamux), add boxo/bitswap with an in-mem then file-backed blockstore, expose a minimal BlockService (Put/Get/Provide). Demo single-CID transfer + basic metrics.
   * M2 — Integrate SNG-40 (ACAN): Implement Announce/Query adapters; plug ACAN into bitswap’s provider path with a small provider cache. Show DHT-free fetch across 5 nodes; add allowlist and basic rate-limits.
   * M3 — Replication (N-Hop): Define tuple-space schemas; implement worker loop (exclusion list handling, counter decrement, re-put semantics); property tests to prove no immediate-neighbor cycles; 10-node fan-out demo with success criteria.
   * M4 — Hardening & perf: Pin/GC policy, backpressure/concurrency tuning, failure injection (loss/churn/slow peers), dashboards + runbook. Target: 100 MB replication ≤2× CAR baseline; ≥95% success under 15% loss.

5. **Timeline**

   * Week 1 — M0 (Go vs CGO): Run spike tests comparing CGO vs pure Go; finalize decision note; set up repo + libp2p host scaffold.
   * Week 2 — M1 (Libraries & Wiring): Wire libp2p (TCP/QUIC + Noise/TLS + Yamux) with boxo/bitswap + blockstore; expose BlockService; demo single-CID transfer.
   * Week 3 — M2 (Integrate ACAN): Implement announce/query adapters; hook ACAN into Bitswap provider path; demo multi-node fetch without DHT.
   * Week 4 — M3 (Replication, N-Hop): Build tuple-space worker logic (exclusion list, counter decrement, re-put); validate with small replication tests.
   * Week 5 — M4 (Hardening & Perf): Add pin/GC, backpressure tuning, failure injection (loss/churn), metrics dashboards; deliver 10-node demo + runbook.

6. Policy: No DHT

- This work explicitly forbids the use of distributed hash tables for discovery or routing. All provider discovery and replication must be done via alternative substrates (tuple space/ACAN, explicit peer lists, relays/rendezvous) and MUST NOT rely on Kademlia or any DHT derivative. This constraint is foundational and non-negotiable.