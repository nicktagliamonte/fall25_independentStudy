// Package tuplespace implements the Tarsus coordination plane.
//
// NativeTupleSpace is the in-process multiset implementation. Distributed
// exact-name operations route to a deterministic overlay owner, which stores
// a versioned durable record containing tuple instances and retained mutation
// results. The owner serializes Put, non-consuming Read, and consuming Get.
// Lease-bearing epoch fences allow a successor to adopt committed state after
// bounded crash handoff.
//
// Router classifies query shapes. Exact names resolve directly to owners;
// prefix and substring patterns use hash-sharded PHT indexes and Bloom
// summaries to discover candidate names before authoritative owner
// verification. Index entries are hints and never grant permission to consume
// a tuple.
//
// TokenFallbackTupleSpace resolves content-provider records stored under the
// token namespace without moving block bytes through the tuple space. Content
// transfer and hash verification are handled by the storage/direct-fetch
// packages.
//
// P2PTupleSpace is a compatibility client for the historical TSH daemon. It is
// optional and is not used by the default native Tarsus path.
package tuplespace
