# Mutable named-object protocol (v1)

This document is the normative specification for Tarsus mutable names. The
wire records are canonical DAG-CBOR. JSON is used only at the local HTTP
boundary; nodes decode it and re-encode the signed payload canonically before
verification or publication.

## Identifiers and normalization

`NamespaceID` is 32 cryptographically random bytes. A name is written
`tarsus://<lower-case hex NamespaceID><normalized-path>`. Paths are UTF-8,
absolute, cleaned of `.` components and repeated separators, and may not
contain NUL, `..`, a trailing slash (except `/`), or exceed 4096 bytes.

`NameID = SHA-256("tarsus-name-id-v1\x00" || NamespaceID || normalizedPath)`.
The DHT key is `/names/<lower-case hex NameID>`. Derivation is checked by every
validator, so a record cannot be moved to another key.

## Signed records

The signature domain is `tarsus-name-record-v1\x00` followed by canonical
DAG-CBOR of every record field except `signature`. A `NameRecord` contains:

- format version, NamespaceID, NameID, normalized path, object kind;
- generation and the SHA-256 hash of the preceding complete signed record;
- current immutable manifest key, or a tombstone marker;
- namespace owner key and optional delegated capability;
- replication, RTT-placement, encryption, retention, and searchability policy;
- signer public key, Unix-nanosecond timestamp, nonce, and Ed25519 signature.

Generation zero is signed directly by the namespace owner. Later records must
increment the current generation by exactly one, name the current record hash,
and be signed by the owner or by a non-expired capability granting the required
operation and path prefix. Capabilities use the independent
`tarsus-capability-v1\x00` signing domain and are always signed by the namespace
owner. Tombstones require `delete` permission; policy changes require `admin`.

An update is submitted with `expected_generation`. Its exact-name owner runs a
single compare-and-swap transaction: the expected generation and predecessor
hash must match, validation must succeed, and then the complete new signed
record becomes current. A lease can reduce wasted work but never substitutes
for this CAS. DHT selection prefers the greatest valid generation only when
the predecessor relation is valid at the authority; equal-generation forks
are rejected, not resolved by arbitrary byte ordering.

Tarsus does not promise linearizable availability across arbitrary network
partitions. The fenced exact owner may reject or delay a write during handoff.

## Policies

The default policy is seven total copies with explicit RTT-class targets
`near=3`, `middle=2`, `far=2`; strict publication; the latest three generations;
and a 24-hour collection grace. RTT classes are latency-diversity hints, not
geographic, jurisdictional, or administrative failure domains. Counts are
validated and sum exactly to the replica target.

The head is not committed until the manifest and every referenced chunk have
the required number of verified provider claims. Provider claims use the
`tarsus-provider-claim-v1\x00` domain and bind a provider identity, content key,
expiry, timestamp, nonce, and signature. A policy controller, rather than the
block store, schedules placement, repair, indexing, retention, and collection.

## Data format

Objects of at most 4 MiB are represented by one immutable encrypted block.
Larger objects are streamed as fixed 1 MiB chunks and a canonical manifest.
The manifest includes ordered chunk keys and lengths, logical size, plaintext
SHA-256 digest, encryption epoch, and authenticated key envelopes. Content
keys are SHA-256 digests of the stored ciphertext or manifest bytes.

Private data uses XChaCha20-Poly1305. Each key epoch has a random 256-bit data
encryption key. Every reader envelope uses an ephemeral X25519 exchange,
HKDF-SHA256, and XChaCha20-Poly1305. Unchanged ciphertext chunks may be reused
within an epoch. Reader membership changes create a new epoch and re-encrypt
the current object. Revocation protects future versions only.

## Directories, rename, search, and deletion

A directory manifest maps child components to `NameID`s, never to content
keys. Updating a child's content therefore does not rewrite ancestors. Rename
acquires the old and new name leases in ascending `NameID` order, creates the
new record, and tombstones the old record.

Secondary PHT/Bloom entries contain current searchable logical names only.
Historical generations and provider copies are never indexed. Results are
accepted only after fetching and verifying the current signed head. Search
responses report fanout attempted/completed and an explicit `complete` flag.
Exact lookup needs no secondary index.

Deletion publishes a signed tombstone and schedules collection after retention
and grace rules permit it. This removes the Tarsus namespace reference. Erasing
all key envelopes can provide cryptographic erasure to parties that did not
already obtain a key. Tarsus cannot force deletion of bytes, keys, or plaintext
already copied by another party.

## Locks

Lease records are scoped to one exact `NameID` or a normalized directory
subtree. Their signature domain is `tarsus-lease-v1\x00`. A lease binds scope,
holder key, fencing number, issue/expiry time, nonce, and signature. Acquire,
renew, and release are authorized operations at the exact scope owner. Staging
and strict replication happen before the short head-update lease. Byte-range
coordination is application-owned and outside this protocol.
