// Purpose: Tests for TokenFallbackTupleSpace.

package tuplespace

import (
	"context"
	"errors"
	"testing"
)

type mockValueStore struct {
	getFunc func(ctx context.Context, key string) ([]byte, error)
	putFunc func(ctx context.Context, key string, value []byte) error
}

func (m *mockValueStore) PutValue(ctx context.Context, key string, value []byte, opts ...interface{}) error {
	if m.putFunc != nil {
		return m.putFunc(ctx, key, value)
	}
	return nil
}

func (m *mockValueStore) GetValue(ctx context.Context, key string, opts ...interface{}) ([]byte, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, key)
	}
	return nil, errors.New("not found")
}

type mockFallbackTS struct {
	readFunc func(string) ([]byte, error)
}

func (m *mockFallbackTS) TsPut(tpname string, tpvalue []byte) (int, error) { return 0, nil }
func (m *mockFallbackTS) TsGet(tpname string) ([]byte, error)               { return nil, nil }
func (m *mockFallbackTS) TsRead(tpname string) ([]byte, error) {
	if m.readFunc != nil {
		return m.readFunc(tpname)
	}
	return nil, nil
}

func TestTokenFallbackTupleSpace_TsRead_HexKeyFromTokenStore(t *testing.T) {
	hexKey64 := "0000000000000000000000000000000000000000000000000000000000000001"
	store := &mockValueStore{
		getFunc: func(ctx context.Context, key string) ([]byte, error) {
			if len(key) > len(tokenNamespace) && key[:len(tokenNamespace)] == tokenNamespace {
				return []byte("token-data"), nil
			}
			return nil, errors.New("not found")
		},
	}
	fallback := &mockFallbackTS{}
	ts := NewTokenFallbackTupleSpace(store, fallback)
	data, err := ts.TsRead(hexKey64)
	if err != nil {
		t.Fatalf("TsRead: %v", err)
	}
	if string(data) != "token-data" {
		t.Errorf("got %q", data)
	}
}

func TestTokenFallbackTupleSpace_TsRead_NonHexKeyFallsBack(t *testing.T) {
	store := &mockValueStore{
		getFunc: func(ctx context.Context, key string) ([]byte, error) {
			return nil, errors.New("not in token store")
		},
	}
	fallback := &mockFallbackTS{
		readFunc: func(tpname string) ([]byte, error) {
			if tpname == "some-tuple-name" {
				return []byte("tuple-value"), nil
			}
			return nil, nil
		},
	}
	ts := NewTokenFallbackTupleSpace(store, fallback)
	data, err := ts.TsRead("some-tuple-name")
	if err != nil {
		t.Fatalf("TsRead: %v", err)
	}
	if string(data) != "tuple-value" {
		t.Errorf("got %q", data)
	}
}

func TestTokenFallbackTupleSpace_TsPut_HexKeyWritesToTokenStore(t *testing.T) {
	hexKey64 := "0000000000000000000000000000000000000000000000000000000000000001"
	putKey := ""
	store := &mockValueStore{
		putFunc: func(ctx context.Context, key string, value []byte) error {
			putKey = key
			return nil
		},
	}
	fallback := &mockFallbackTS{}
	ts := NewTokenFallbackTupleSpace(store, fallback)
	_, err := ts.TsPut(hexKey64, []byte("token-data"))
	if err != nil {
		t.Fatalf("TsPut: %v", err)
	}
	if putKey != tokenNamespace+hexKey64 {
		t.Errorf("expected PutValue key %q, got %q", tokenNamespace+hexKey64, putKey)
	}
}

func TestTokenFallbackTupleSpace_TsPut_NonHexKeyDelegatesToFallback(t *testing.T) {
	store := &mockValueStore{}
	fallback := &mockFallbackTS{} // TsPut returns 0, nil
	ts := NewTokenFallbackTupleSpace(store, fallback)
	_, err := ts.TsPut("some-tuple-name", []byte("val"))
	if err != nil {
		t.Fatalf("TsPut: %v", err)
	}
}

func TestTokenFallbackTupleSpace_TsRead_ShortHexNotTokenLookup(t *testing.T) {
	store := &mockValueStore{
		getFunc: func(ctx context.Context, key string) ([]byte, error) {
			t.Error("token store should not be called for short key")
			return nil, errors.New("not found")
		},
	}
	fallback := &mockFallbackTS{
		readFunc: func(tpname string) ([]byte, error) {
			return []byte("fallback-val"), nil
		},
	}
	ts := NewTokenFallbackTupleSpace(store, fallback)
	data, err := ts.TsRead("short")
	if err != nil {
		t.Fatalf("TsRead: %v", err)
	}
	if string(data) != "fallback-val" {
		t.Errorf("got %q", data)
	}
}
