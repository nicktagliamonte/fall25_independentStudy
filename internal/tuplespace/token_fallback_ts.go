// Purpose: TupleSpace that tries token lookup (/tokens/) first, then falls back to DHT tuple space.
// Enables Gateway.Query to return token data for exact-key lookups while supporting tuple space for other patterns.
// Token keys are 32-byte hashes encoded as 64 hexadecimal characters.

package tuplespace

import (
	"context"
	"errors"
)

// tokenNamespace is the DHT/ValueStore key prefix under which token records
// (content-addressed block location metadata) are stored, e.g. "/tokens/<hex key>".
const tokenNamespace = "/tokens/"

// TokenFallbackTupleSpace implements TupleSpace by checking /tokens/ first for hex keys, then delegating.
var _ TupleSpace = (*TokenFallbackTupleSpace)(nil)

// TokenFallbackTupleSpace wraps a ValueStore and a fallback TupleSpace so that
// exact 64-character hex tuple names (Tarsus content keys) are served
// directly from the /tokens/ namespace of the ValueStore, while every other
// tuple name/pattern is delegated to the fallback TupleSpace (typically a
// Router). This lets Gateway.Query resolve token lookups for exact keys
// without requiring every TupleSpace implementation to understand tokens.
type TokenFallbackTupleSpace struct {
	// store is the token-specific backing store consulted for hex keys.
	store ValueStore
	// fallback is the TupleSpace used for non-hex-key operations and for
	// TsGet (tokens are never consumed via TsGet).
	fallback TupleSpace
}

// NewTokenFallbackTupleSpace creates a TupleSpace that reads tokens for exact hex keys, else falls back.
//
// Parameters:
//   - store (ValueStore): backing store used for /tokens/ reads and writes on hex keys.
//   - fallback (TupleSpace): tuple space used for all other operations.
//
// Returns:
//   - *TokenFallbackTupleSpace: the constructed wrapper.
func NewTokenFallbackTupleSpace(store ValueStore, fallback TupleSpace) *TokenFallbackTupleSpace {
	return &TokenFallbackTupleSpace{store: store, fallback: fallback}
}

// TsPut writes to /tokens/ for hex keys (token storage), else delegates to fallback.
//
// Parameters:
//   - tpname (string): tuple name; if a 64-character hex string, treated as a token key.
//   - tpvalue ([]byte): tuple payload to store.
//
// Returns:
//   - int: 0 on success when written via store; otherwise the fallback's status code,
//     or TSPUT_ER if store/fallback is unavailable or the store write failed.
//   - error: non-nil on failure (missing store/fallback, or underlying write error).
func (t *TokenFallbackTupleSpace) TsPut(tpname string, tpvalue []byte) (int, error) {
	if t.store == nil {
		return TSPUT_ER, errors.New("store required for TsPut")
	}
	if isHexKey(tpname) {
		if err := t.store.PutValue(context.Background(), tokenNamespace+tpname, tpvalue); err != nil {
			return TSPUT_ER, err
		}
		return 0, nil
	}
	if t.fallback == nil {
		return TSPUT_ER, errors.New("fallback required for TsPut")
	}
	return t.fallback.TsPut(tpname, tpvalue)
}

// TsReplace delegates singleton-record replacement for non-token tuple names.
// Token records already have key/value replacement semantics in the DHT.
func (t *TokenFallbackTupleSpace) TsReplace(tpname string, tpvalue []byte) (int, error) {
	if isHexKey(tpname) {
		return t.TsPut(tpname, tpvalue)
	}
	replacer, ok := t.fallback.(NamedTupleReplacer)
	if !ok {
		return TSPUT_ER, errors.New("fallback does not support tuple replacement")
	}
	return replacer.TsReplace(tpname, tpvalue)
}

func (t *TokenFallbackTupleSpace) CompareAndSwapExact(ctx context.Context, name string, expected, next []byte) error {
	authority, ok := t.fallback.(ExactCompareAndSwapper)
	if !ok {
		return errors.New("fallback does not support exact CAS")
	}
	return authority.CompareAndSwapExact(ctx, name, expected, next)
}
func (t *TokenFallbackTupleSpace) ReadExact(ctx context.Context, name string) ([]byte, error) {
	authority, ok := t.fallback.(ExactCompareAndSwapper)
	if !ok {
		return nil, errors.New("fallback does not support exact reads")
	}
	return authority.ReadExact(ctx, name)
}
func (t *TokenFallbackTupleSpace) IndexLogicalName(ctx context.Context, entry string) error {
	index, ok := t.fallback.(interface {
		IndexLogicalName(context.Context, string) error
	})
	if !ok {
		return errors.New("fallback does not support logical-name indexing")
	}
	return index.IndexLogicalName(ctx, entry)
}
func (t *TokenFallbackTupleSpace) DeleteLogicalName(ctx context.Context, entry string) error {
	index, ok := t.fallback.(interface {
		DeleteLogicalName(context.Context, string) error
	})
	if !ok {
		return errors.New("fallback does not support logical-name indexing")
	}
	return index.DeleteLogicalName(ctx, entry)
}
func (t *TokenFallbackTupleSpace) SearchLogicalNames(ctx context.Context, prefix, suffix string) ([]string, int, int, error) {
	index, ok := t.fallback.(interface {
		SearchLogicalNames(context.Context, string, string) ([]string, int, int, error)
	})
	if !ok {
		return nil, 0, 0, errors.New("fallback does not support logical-name indexing")
	}
	return index.SearchLogicalNames(ctx, prefix, suffix)
}

// TsPutWithMutationStats delegates non-token writes to an instrumented
// fallback and returns exact per-call index mutation work.
func (t *TokenFallbackTupleSpace) TsPutWithMutationStats(tpname string, tpvalue []byte) (int, IndexMutationStats, error) {
	if isHexKey(tpname) {
		code, err := t.TsPut(tpname, tpvalue)
		return code, IndexMutationStats{}, err
	}
	instrumented, ok := t.fallback.(interface {
		TsPutWithMutationStats(string, []byte) (int, IndexMutationStats, error)
	})
	if !ok {
		code, err := t.TsPut(tpname, tpvalue)
		return code, IndexMutationStats{}, err
	}
	return instrumented.TsPutWithMutationStats(tpname, tpvalue)
}

// TsGet delegates to fallback; tokens are not consumed via TsGet.
//
// Parameters:
//   - tpname (string): tuple name/pattern to consume.
//
// Returns:
//   - []byte: the consumed tuple data, from the fallback TupleSpace.
//   - error: non-nil if no fallback is configured or the fallback's TsGet failed.
func (t *TokenFallbackTupleSpace) TsGet(tpname string) ([]byte, error) {
	if t.fallback == nil {
		return nil, errors.New("fallback required for TsGet")
	}
	return t.fallback.TsGet(tpname)
}

// TsRead checks /tokens/ for hex keys (64 chars), else delegates to fallback.
//
// Parameters:
//   - tpname (string): tuple name/pattern; if a 64-character hex string, the
//     /tokens/ store is checked first.
//
// Returns:
//   - []byte: the tuple data, from the token store if found for a hex key,
//     otherwise from the fallback TupleSpace.
//   - error: non-nil if store/fallback are unavailable, or if both the token
//     lookup (when applicable) and the fallback lookup fail.
func (t *TokenFallbackTupleSpace) TsRead(tpname string) ([]byte, error) {
	if t.store == nil || t.fallback == nil {
		return nil, errors.New("store and fallback required")
	}
	if isHexKey(tpname) {
		data, err := t.store.GetValue(context.Background(), tokenNamespace+tpname)
		if err == nil && len(data) > 0 {
			return data, nil
		}
	}
	return t.fallback.TsRead(tpname)
}

// TsReadWithStats delegates instrumented non-token queries when the fallback
// is an IndexedTupleSpace (or another compatible implementation).
func (t *TokenFallbackTupleSpace) TsReadWithStats(tpname string) ([]byte, IndexedQueryStats, error) {
	if isHexKey(tpname) {
		data, err := t.TsRead(tpname)
		stats := IndexedQueryStats{QueryKind: "token", OwnerAttempts: 1}
		if err == nil {
			stats.VerifiedMatches = 1
		}
		return data, stats, err
	}
	instrumented, ok := t.fallback.(interface {
		TsReadWithStats(string) ([]byte, IndexedQueryStats, error)
	})
	if !ok {
		data, err := t.TsRead(tpname)
		return data, IndexedQueryStats{}, err
	}
	return instrumented.TsReadWithStats(tpname)
}

// MutationSnapshot delegates mutation counters when available.
func (t *TokenFallbackTupleSpace) MutationSnapshot() IndexMutationStats {
	instrumented, ok := t.fallback.(interface {
		MutationSnapshot() IndexMutationStats
	})
	if !ok {
		return IndexMutationStats{}
	}
	return instrumented.MutationSnapshot()
}

// isHexKey reports whether s is a 64-character lowercase/uppercase hex string,
// the shape of a Tarsus content key (SHA-256 hex digest), used to decide
// whether a tuple name should be treated as a token lookup.
//
// Parameters:
//   - s (string): candidate tuple name.
//
// Returns:
//   - bool: true if s is exactly 64 hex characters.
func isHexKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}
