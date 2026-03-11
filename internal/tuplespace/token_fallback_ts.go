// Purpose: TupleSpace that tries token lookup (/tokens/) first, then falls back to DHT tuple space.
// Enables Gateway.Query to return token data for exact-key lookups while supporting tuple space for other patterns.
// Per planTwo Phase 5.3: Gateway handles token routing; token keys are 32-byte hash = 64 hex chars.

package tuplespace

import (
	"context"
	"errors"
)

const tokenNamespace = "/tokens/"

// TokenFallbackTupleSpace implements TupleSpace by checking /tokens/ first for hex keys, then delegating.
var _ TupleSpace = (*TokenFallbackTupleSpace)(nil)

type TokenFallbackTupleSpace struct {
	store   ValueStore
	fallback TupleSpace
}

// NewTokenFallbackTupleSpace creates a TupleSpace that reads tokens for exact hex keys, else falls back.
func NewTokenFallbackTupleSpace(store ValueStore, fallback TupleSpace) *TokenFallbackTupleSpace {
	return &TokenFallbackTupleSpace{store: store, fallback: fallback}
}

// TsPut writes to /tokens/ for hex keys (token storage), else delegates to fallback.
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

// TsGet delegates to fallback; tokens are not consumed via TsGet.
func (t *TokenFallbackTupleSpace) TsGet(tpname string) ([]byte, error) {
	if t.fallback == nil {
		return nil, errors.New("fallback required for TsGet")
	}
	return t.fallback.TsGet(tpname)
}

// TsRead checks /tokens/ for hex keys (64 chars), else delegates to fallback.
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
