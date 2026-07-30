// Purpose: Tests for token routing PutToken and GetToken (Phase 7.1).

package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"crypto/rand"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multiaddr"
)

func TestTokenAbsentRecognizesDHTNamespaceMiss(t *testing.T) {
	if !isTokenAbsent(errors.New("DHT get token failed: no matching tuple")) {
		t.Fatal("DHT namespace miss was not recognized as an absent token")
	}
	if isTokenAbsent(errors.New("connection reset")) {
		t.Fatal("transient network failure was classified as an absent token")
	}
}

type mockTokenDHT struct {
	mu    sync.Mutex
	store map[string][]byte
}

func newMockTokenDHT() *mockTokenDHT {
	return &mockTokenDHT{store: make(map[string][]byte)}
}

func (m *mockTokenDHT) PutValue(ctx context.Context, key string, value []byte, opts ...routing.Option) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[key] = append([]byte(nil), value...)
	return nil
}

func (m *mockTokenDHT) GetValue(ctx context.Context, key string, opts ...routing.Option) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.store[key]; ok {
		return append([]byte(nil), v...), nil
	}
	return nil, routing.ErrNotFound
}

func (m *mockTokenDHT) SearchValue(ctx context.Context, key string, opts ...routing.Option) (<-chan []byte, error) {
	ch := make(chan []byte, 1)
	go func() {
		defer close(ch)
		val, err := m.GetValue(ctx, key, opts...)
		if err == nil && len(val) > 0 {
			ch <- val
		}
	}()
	return ch, nil
}

func tokenTestPeerID(t *testing.T) peer.ID {
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	pid, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey: %v", err)
	}
	return pid
}

func tokenTestMultiaddr(t *testing.T, s string) multiaddr.Multiaddr {
	ma, err := multiaddr.NewMultiaddr(s)
	if err != nil {
		t.Fatalf("NewMultiaddr %q: %v", s, err)
	}
	return ma
}

func TestPutToken_GetToken_Roundtrip(t *testing.T) {
	ctx := context.Background()
	dht := newMockTokenDHT()
	k := KeyFromData([]byte("test block"))
	pid := tokenTestPeerID(t)
	addr := tokenTestMultiaddr(t, "/ip4/127.0.0.1/tcp/4001")

	token := Token{
		Key:       k,
		Locations: []Location{{ProviderID: pid, Address: addr, RTT: 10 * time.Millisecond}},
		Timestamp: time.Now().UnixNano(),
		Version:   1,
	}

	if err := PutToken(ctx, dht, k, token); err != nil {
		t.Fatalf("PutToken: %v", err)
	}

	got, err := GetToken(ctx, dht, k)
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if !got.Key.Equal(k) {
		t.Errorf("key mismatch: got %s, want %s", got.Key.String(), k.String())
	}
	if len(got.Locations) != 1 {
		t.Fatalf("expected 1 location, got %d", len(got.Locations))
	}
	if got.Locations[0].ProviderID != pid {
		t.Errorf("provider mismatch: got %s, want %s", got.Locations[0].ProviderID, pid)
	}
	if got.Locations[0].Address.String() != addr.String() {
		t.Errorf("address mismatch: got %s, want %s", got.Locations[0].Address.String(), addr.String())
	}
	if got.Version != 1 {
		t.Errorf("version: got %d, want 1", got.Version)
	}
}

func TestPutToken_RejectsNilDHT(t *testing.T) {
	ctx := context.Background()
	k := KeyFromData([]byte("x"))
	pid := tokenTestPeerID(t)
	addr := tokenTestMultiaddr(t, "/ip4/127.0.0.1/tcp/4001")
	token := Token{
		Key:       k,
		Locations: []Location{{ProviderID: pid, Address: addr, RTT: 0}},
		Timestamp: time.Now().UnixNano(),
		Version:   1,
	}

	if err := PutToken(ctx, nil, k, token); err == nil {
		t.Error("PutToken with nil DHT should fail")
	}
}

func TestPutToken_RejectsZeroKey(t *testing.T) {
	ctx := context.Background()
	dht := newMockTokenDHT()
	pid := tokenTestPeerID(t)
	addr := tokenTestMultiaddr(t, "/ip4/127.0.0.1/tcp/4001")
	token := Token{
		Key:       KeyFromData([]byte("x")),
		Locations: []Location{{ProviderID: pid, Address: addr, RTT: 0}},
		Timestamp: time.Now().UnixNano(),
		Version:   1,
	}

	if err := PutToken(ctx, dht, Key{}, token); err == nil {
		t.Error("PutToken with zero key should fail")
	}
}

func TestGetToken_RejectsNilDHT(t *testing.T) {
	ctx := context.Background()
	k := KeyFromData([]byte("x"))
	_, err := GetToken(ctx, nil, k)
	if err == nil {
		t.Error("GetToken with nil DHT should fail")
	}
}

func TestGetToken_RejectsZeroKey(t *testing.T) {
	ctx := context.Background()
	dht := newMockTokenDHT()
	_, err := GetToken(ctx, dht, Key{})
	if err == nil {
		t.Error("GetToken with zero key should fail")
	}
}

func TestGetToken_NotFound(t *testing.T) {
	ctx := context.Background()
	dht := newMockTokenDHT()
	k := KeyFromData([]byte("nonexistent"))

	_, err := GetToken(ctx, dht, k)
	if err == nil {
		t.Error("GetToken for missing key should fail")
	}
}
