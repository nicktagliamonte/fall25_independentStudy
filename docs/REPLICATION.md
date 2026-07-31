# Replication and repair

Tarsus stores immutable content locally, publishes a provider-location token,
and then creates opportunistic replicas. Tuple metadata coordinates placement;
bulk bytes move directly between peers and are accepted only after content-key
verification.

The default target is seven providers. Placement attempts three near, two
middle, and two far copies using observed round-trip time. These classes are a
best-effort latency-diversity objective, not proof of geographic or
administrative independence. If a class lacks candidates, placement may use a
healthy candidate from another class without exceeding the target.

Periodic audits:

1. resolve and probe the recorded providers;
2. retain healthy providers and require repeated evidence before removing an
   unreachable location;
3. elect one reachable repair coordinator;
4. prefer replacements from missing RTT classes; and
5. fill any remaining count deficit from another healthy advertised peer.

The source provider is published before asynchronous copies. Exactly one
coordinator publishes each completed transfer, avoiding lost provider updates
between transfer endpoints.

Replica count is an availability policy, not a Byzantine quorum. Repair
requires a reachable source and suitable advertised peers. Same-host container
tests validate mechanism behavior but cannot establish regional independence;
that claim requires multi-host failure experiments.
