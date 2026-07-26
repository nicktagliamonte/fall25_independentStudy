# Locking API

Key-level locks provide write mutual exclusion. Locks are keyed by `Key = hash(data)`. Reads are lock-free; only Put and Delete acquire locks when `KeyLockManager` is configured.

---

## Concepts

| Term      | Description                                              |
|-----------|----------------------------------------------------------|
| Key       | 64 hex chars (SHA256 of data). Lock scope.               |
| Holder    | peer.ID of the lock owner.                               |
| TTL       | Time-to-live. Expired locks are treated as released.     |
| Lock key  | `/locks/` + hex(key) in the backing store.               |

---

## KeyLockManager

### Creation

```go
// DHT-backed: shared across peers (distributed lock)
mgr := storage.NewKeyLockManager(dht)

// Datastore-backed: single-node or test
mgr := storage.NewKeyLockManagerFromDatastore(datastore)

// With custom default TTL
mgr := storage.NewKeyLockManager(dht, storage.WithDefaultTTL(10*time.Minute))
```

### Options

| Option               | Description              |
|----------------------|--------------------------|
| WithDefaultTTL(ttl)  | Default TTL (default: 5m)|

---

## Core Operations

### AcquireLock

Acquire a lock on a key. Fails immediately if locked by another peer.

```go
err := mgr.AcquireLock(ctx, key, holder, ttl)
```

- **key**: Key to lock.
- **holder**: peer.ID of the caller.
- **ttl**: Lock duration. Use `0` for manager default.

**Errors:**
- `ErrLockHeldByAnother`: Another peer holds the lock.
- `key cannot be zero`

### AcquireLockWithRetry

Acquire with exponential backoff on `ErrLockHeldByAnother`.

```go
err := mgr.AcquireLockWithRetry(ctx, key, holder, ttl, cfg)
```

- **ttl**: Lock duration. Use `0` for manager default.
- **cfg**: Optional `LockRetryConfig`. `nil` uses defaults.

**LockRetryConfig:**

| Field          | Default | Description                    |
|----------------|---------|--------------------------------|
| InitialBackoff | 50ms    | First sleep between retries    |
| MaxBackoff     | 5s      | Cap on backoff                 |
| Timeout        | 30s     | Fail if not acquired by then   |

**Errors:**
- `ErrLockTimeout`: Not acquired within timeout.
- Context cancellation stops retries.

### ReleaseLock

Release the lock if the holder matches.

```go
err := mgr.ReleaseLock(ctx, key, holder)
```

- No-op if no lock or lock expired.
- Returns error if held by a different peer.

### ExtendLock

Extend TTL of an existing lock held by the caller.

```go
err := mgr.ExtendLock(ctx, key, holder, ttl)
```

**Errors:**
- `no active lock to extend`
- `lock held by <other>`

### IsLocked

Check whether a key is locked and who holds it.

```go
locked, holder := mgr.IsLocked(ctx, key)
```

Returns `(false, "")` if unlocked or expired.

---

## Errors

| Error                  | Retriable | Description                          |
|------------------------|-----------|--------------------------------------|
| ErrLockHeldByAnother   | Yes       | Another peer holds the lock          |
| ErrLockTimeout         | No        | Lock not acquired within timeout     |
| key cannot be zero     | No        | Invalid key                          |
| lock held by X, not Y  | No        | Release/Extend by non-holder         |

---

## Integration with Put/Delete

Locking is **opt-in**: `PutBlock`/`DeleteBlock` only acquire locks when both `Stack.KeyLockManager` and `Stack.Host` are non-nil. If either is unset, Put/Delete proceed without any locking at all (no mutual exclusion), which is the default for single-writer or test setups that never construct a `KeyLockManager`.

When `Stack.KeyLockManager` and `Stack.Host` are set:

- **PutBlock**: Acquires lock (with retry if `PutLockRetryConfig` set), writes block, releases lock.
- **DeleteBlock**: Acquires lock for key (from routing table), deletes block, releases lock.

Lock holder = `Host.ID()`.

Put uses `PutLockOpts`:

```go
type PutLockOpts struct {
    Manager     *KeyLockManager
    Holder      peer.ID
    RetryConfig *LockRetryConfig  // nil = defaults
}
```

Stack integration:

```go
stack.KeyLockManager = storage.NewKeyLockManagerFromDatastore(stack.Datastore)
stack.PutLockRetryConfig = &storage.LockRetryConfig{
    Timeout: 15 * time.Second,
}
```

---

## Lock Structure (Internal)

```json
{
  "key": "<64 hex chars>",
  "lock_holder": "<peer.ID>",
  "acquired_at_ns": 1234567890000000000,
  "expires_at_ns": 1234567890000000000,
  "version": 1
}
```

---

## Reads

Reads do **not** acquire locks. `GetBlock`, `GetBlockByKey`, and DirectFetch are lock-free. Multiple concurrent reads are allowed.
