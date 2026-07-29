# Tarsus stability audit

This is an internal evidence record for the manuscript and experiment campaign.

## DHT/token integration flakes

Observed failures:

- `TestPartitionAndRecovery` intermittently failed because the token put ran
  with an empty DHT routing table.
- `TestReadWithoutLockMultipleConcurrentReadsSucceed` intermittently failed
  because the reader could not retrieve the token.
- Repeated execution reproduced two `TestPartitionAndRecovery` failures in ten
  runs.

Root cause:

- Tests created one DHT before its peer was serving the DHT protocol, connected
  later, did not bootstrap again, and used fixed sleeps as a readiness proxy.
- Rapid repeated host creation could also produce a transient first-dial Noise
  negotiation failure, which the tests treated as terminal.

Correction:

- Added a bounded connect/retry, post-protocol bootstrap, and observable
  routing-table readiness helper.
- Replaced fixed token-propagation sleeps with polling for the required token
  and provider count.

Evidence after correction:

- The two affected tests passed 10 consecutive combined runs.
- The complete `pkg/node` package passed five consecutive runs.
- The entire repository passed three consecutive uncached test runs.
- The repaired paths passed under Go's race detector.

## Invalid O(log N) timing assertion

The former `TestVerifyOLogNComplexity` inferred asymptotic behavior from eight
warm-cache wall-clock samples per network size on one physical host. It used
all-to-all local connections and scheduler-sensitive latency ratios. This was
not valid complexity evidence and occasionally failed due to timing noise.

Correction:

- Retained multi-size lookup as a functional smoke test and non-normative
  latency log.
- Removed wall-clock asymptotic pass/fail assertions.
- The paper attributes expected logarithmic lookup to Kademlia and reports
  measured routing work separately.

## Startup availability advertisement

Observed failure:

- Nodes attempted one storage-availability tuple write before their DHTs were
  ready, discarded the error, and waited 30 seconds before trying again.
- Docker smoke runs exposed the failure in lifetime mutation counters.

Correction:

- Startup advertisement now runs in a cancellable loop with bounded
  exponential retry and an explicit log message, followed by the existing
  periodic refresh after success.
- Campaign validation now requires every node to have a neighbor and polls
  until at least one distinct indexed availability name exists per configured
  node.

Evidence after correction:

- A three-node Docker preflight observed the expected transient initial
  failures, then waited as the indexed offer count converged from two to three.
- The cell proceeded only after all three offers were indexed, then populated
  100/100 workload tuples with exactly 100 attributed mutations and zero
  workload failures.

