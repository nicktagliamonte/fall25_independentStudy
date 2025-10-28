# SNG‑40 Block Transfer Plan

3. **Integrating SNG‑40**
   To integrate SNG-40’s proprietary discovery layer, ACAN, we treat it as the replacement for IPFS’s DHT/provider system. Instead of publishing and querying providers through a global DHT, Bitswap’s “who has this CID?” calls are wired into ACAN’s Tuple Space APIs. When a node stores a block, it issues an announce into ACAN; when another node needs that block, it queries ACAN to get a peer list, then opens libp2p streams to those peers and pulls data via Bitswap. This keeps discovery fully under SNG-40’s control while letting libp2p and boxo handle the transport and block exchange layers unchanged.

4. **Replication Layer**
   The replication layer builds on top of block exchange by enforcing my N-Hop replication algorithm. When a node issues a replication request into the tuple space, the request includes a target replication count and an exclusion list of its own peers. Any node that is not on that list can take the request, decrement the replication counter, append its own peerlist to the exclusion list, and then re-publish the updated request back into the tuple space. After doing so, it pulls the file via Bitswap and stores it locally. This hop-by-hop handoff guarantees that replication fans out across disjoint neighborhoods rather than circling among direct peers, driving diversity and resilience without relying on global coordination.

5. Policy: No DHT

- This work explicitly forbids the use of distributed hash tables for discovery or routing. All provider discovery and replication must be done via alternative substrates (tuple space/ACAN, explicit peer lists, relays/rendezvous) and MUST NOT rely on Kademlia or any DHT derivative. This constraint is foundational and non-negotiable.