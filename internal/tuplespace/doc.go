// Package tuplespace provides tuple space implementations for vn-IPFS.
//
// A tuple space is a coordination abstraction offering Put (non-consuming
// write), Read (non-consuming lookup), and Get (consuming lookup-and-remove)
// operations over named tuples. This package defines the common TupleSpace
// interface (interface.go) and several implementations that back different
// parts of the system:
//
//   - DHTTupleSpace (dht_ts.go): backs exact-match tuple names with the
//     Kademlia DHT. This is the open storage layer: no permission checks are
//     performed, operations are O(log N), and "consumption" is emulated with
//     tombstone markers since the DHT has no native delete (cleanup relies on
//     the standard 48h libp2p TTL expiration).
//
//   - P2PTupleSpace (p2p_ts.go): backs regex/wildcard tuple names and
//     administrative/coordination operations (e.g. KYC, application
//     management) by speaking the legacy TSH (tuple space handler) wire
//     protocol over TCP. This layer is permissioned via an optional
//     PermissionChecker and supports O(log_20 k)-hop routing with O(N)
//     messaging.
//
//   - Router (router.go): implements TupleSpace by inspecting the pattern
//     shape of each call and dispatching to the right backend: exact names go
//     to the DHT tuple space; simple wildcards (prefix/substring `*`) are
//     resolved via the PHT (internal/pht) to a set of candidate names which
//     are then fetched from the DHT tuple space; complex regex patterns fall
//     through to the P2P tuple space.
//
//   - TokenFallbackTupleSpace (token_fallback_ts.go): wraps another
//     TupleSpace and adds a fast path for vn-IPFS content tokens: 64-character
//     hex keys are read/written directly against a ValueStore under the
//     /tokens/ namespace, while everything else delegates to the wrapped
//     TupleSpace. This lets Gateway.Query return token data for exact-key
//     lookups without every implementation needing to know about tokens.
//
//   - PermissionChecker (permission.go): a pluggable identity/authorization
//     hook consulted by P2PTupleSpace before administrative operations;
//     absent (nil) checkers are treated as "allow all" for backward
//     compatibility.
//
// None of the types in this package fetch or transfer block content — they
// only store and resolve tuples/tokens (locations, metadata). Retrieving
// actual block bytes is the caller's responsibility via DirectFetch.
package tuplespace
