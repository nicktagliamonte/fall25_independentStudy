// Purpose: Tests for PHT DHT integration.

package pht

import (
	"context"
	"sort"
	"sync"
	"testing"
)

type mockStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *mockStore) PutValue(ctx context.Context, key string, value []byte, opts ...interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	m.data[key] = append([]byte(nil), value...)
	return nil
}

func (m *mockStore) GetValue(ctx context.Context, key string, opts ...interface{}) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

var ErrNotFound = &notFoundError{}

type notFoundError struct{}

func (e *notFoundError) Error() string   { return "not found" }
func (e *notFoundError) Is(t error) bool { return t == ErrNotFound }

func TestPutGetNode(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}

	leaf := NewLeaf("ab")
	leaf.Entries = []string{"ab1", "ab2"}
	if err := PutNode(ctx, store, leaf); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	got, err := GetNode(ctx, store, "ab")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Prefix != "ab" || !got.IsLeaf() {
		t.Errorf("GetNode: want leaf prefix ab, got %v", got)
	}
	if len(got.Entries) != 2 {
		t.Errorf("GetNode: want 2 entries, got %d", len(got.Entries))
	}
}

func TestPutGetInternalNode(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}

	internal := NewInternal("a")
	internal.Children["b"] = NewLeaf("ab")
	internal.Children["c"] = NewLeaf("ac")
	if err := PutNode(ctx, store, internal); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	got, err := GetNode(ctx, store, "a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Prefix != "a" || !got.IsInternal() {
		t.Errorf("GetNode: want internal prefix a, got kind=%v", got.Kind)
	}
	if len(got.Children) != 2 {
		t.Errorf("GetNode: want 2 children, got %d", len(got.Children))
	}
}

func TestPutNodeRecursiveAndCollectUnderDHT(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}

	root := buildTestTree()
	if err := PutNodeRecursive(ctx, store, root); err != nil {
		t.Fatalf("PutNodeRecursive: %v", err)
	}
	got, err := CollectUnderDHT(ctx, store, root)
	if err != nil {
		t.Fatalf("CollectUnderDHT: %v", err)
	}
	sort.Strings(got)
	want := []string{"ab1", "ab2", "ac1"}
	if len(got) != len(want) {
		t.Errorf("CollectUnderDHT: want %d, got %d", len(want), len(got))
	}
	for i, s := range want {
		if i < len(got) && got[i] != s {
			t.Errorf("CollectUnderDHT[%d]: want %q, got %q", i, s, got[i])
		}
	}
}

func TestPrefixQueryDHT(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	root := buildTestTree()
	if err := PutNodeRecursive(ctx, store, root); err != nil {
		t.Fatalf("PutNodeRecursive: %v", err)
	}
	got, err := PrefixQueryDHT(ctx, store, "ab")
	if err != nil {
		t.Fatalf("PrefixQueryDHT: %v", err)
	}
	sort.Strings(got)
	want := []string{"ab1", "ab2"}
	if len(got) != len(want) {
		t.Errorf("PrefixQueryDHT ab: want %v, got %v", want, got)
	}
}

func TestPutGetNodeWithBloom(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}

	leaf := NewLeaf("ab")
	leaf.Entries = []string{"ab1", "forest"}
	BuildNodeBloom(leaf, 3, 256, 5)
	if err := PutNode(ctx, store, leaf); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	got, err := GetNode(ctx, store, "ab")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Bloom == nil {
		t.Fatal("GetNode: Bloom should be restored")
	}
	if !got.Bloom.ContainsString("for") {
		t.Error("restored Bloom should contain n-gram for")
	}
}

func TestCollectUnderDHTWithPrune(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}

	root := NewInternal("")
	a := NewInternal("a")
	ab := NewLeaf("ab")
	ab.Entries = []string{"ab1", "forest"}
	ac := NewLeaf("ac")
	ac.Entries = []string{"ac1", "xyz"}
	root.Children["a"] = a
	a.Children["b"] = ab
	a.Children["c"] = ac
	BuildNodeBloom(root, 3, 256, 5)
	if err := PutNodeRecursive(ctx, store, root); err != nil {
		t.Fatalf("PutNodeRecursive: %v", err)
	}

	ngrams := ExtractNGrams("forest", 3)
	got, err := CollectUnderDHTWithPrune(ctx, store, root, ngrams)
	if err != nil {
		t.Fatalf("CollectUnderDHTWithPrune: %v", err)
	}
	sort.Strings(got)
	want := []string{"ab1", "forest"}
	if len(got) != len(want) {
		t.Errorf("CollectUnderDHTWithPrune *forest*: want %v, got %v", want, got)
	}
	for i, s := range want {
		if i < len(got) && got[i] != s {
			t.Errorf("CollectUnderDHTWithPrune[%d]: want %q, got %q", i, s, got[i])
		}
	}

	gotAll, _ := CollectUnderDHT(ctx, store, root)
	sort.Strings(gotAll)
	if len(gotAll) != 4 {
		t.Errorf("CollectUnderDHT without prune: want 4 keys, got %v", gotAll)
	}
}
