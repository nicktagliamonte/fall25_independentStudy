// Purpose: Tests for PHT node structure and recursive prefix hashing.

package pht

import (
	"bytes"
	"reflect"
	"sort"
	"testing"
)

func TestNewLeaf(t *testing.T) {
	n := NewLeaf("ab")
	if n == nil {
		t.Fatal("NewLeaf returned nil")
	}
	if n.Kind != KindLeaf {
		t.Errorf("Kind want Leaf, got %v", n.Kind)
	}
	if n.Prefix != "ab" {
		t.Errorf("Prefix want ab, got %q", n.Prefix)
	}
	if n.Entries == nil {
		t.Error("Entries nil")
	}
	if len(n.Entries) != 0 {
		t.Errorf("Entries want empty, got len %d", len(n.Entries))
	}
	if n.Children != nil {
		t.Error("Leaf should not have Children")
	}
	if !n.IsLeaf() {
		t.Error("IsLeaf should be true")
	}
	if n.IsInternal() {
		t.Error("IsInternal should be false")
	}
}

func TestNewInternal(t *testing.T) {
	n := NewInternal("x")
	if n == nil {
		t.Fatal("NewInternal returned nil")
	}
	if n.Kind != KindInternal {
		t.Errorf("Kind want Internal, got %v", n.Kind)
	}
	if n.Prefix != "x" {
		t.Errorf("Prefix want x, got %q", n.Prefix)
	}
	if n.Children == nil {
		t.Error("Children nil")
	}
	if len(n.Children) != 0 {
		t.Errorf("Children want empty, got len %d", len(n.Children))
	}
	if n.Entries != nil {
		t.Error("Internal should not have Entries")
	}
	if n.IsLeaf() {
		t.Error("IsLeaf should be false")
	}
	if !n.IsInternal() {
		t.Error("IsInternal should be true")
	}
}

func TestNodeNilSafety(t *testing.T) {
	var n *Node
	if n.IsLeaf() {
		t.Error("nil node IsLeaf should be false")
	}
	if n.IsInternal() {
		t.Error("nil node IsInternal should be false")
	}
}

func TestHashPrefix(t *testing.T) {
	h0 := HashPrefix("")
	if len(h0) != 32 {
		t.Errorf("HashPrefix empty: want 32 bytes, got %d", len(h0))
	}
	h1 := HashPrefix("a")
	if len(h1) != 32 {
		t.Errorf("HashPrefix: want 32 bytes, got %d", len(h1))
	}
	if bytes.Equal(h0, h1) {
		t.Error("HashPrefix: different inputs should produce different hashes")
	}
	h2 := HashPrefix("a")
	if !bytes.Equal(h1, h2) {
		t.Error("HashPrefix: same input should produce same hash")
	}
}

func TestIndexKey(t *testing.T) {
	hashes := IndexKey("abc")
	if len(hashes) != 4 {
		t.Errorf("IndexKey abc: want 4 hashes (empty, a, ab, abc), got %d", len(hashes))
	}
	seen := make(map[string]bool)
	for i, h := range hashes {
		if len(h) != 32 {
			t.Errorf("IndexKey hash %d: want 32 bytes, got %d", i, len(h))
		}
		key := string(h)
		if seen[key] {
			t.Errorf("IndexKey: duplicate hash at index %d", i)
		}
		seen[key] = true
	}
}

func TestIndexKeyEmpty(t *testing.T) {
	hashes := IndexKey("")
	if len(hashes) != 1 {
		t.Errorf("IndexKey empty: want 1 hash, got %d", len(hashes))
	}
}

func TestIndexKeyDeterministic(t *testing.T) {
	a := IndexKey("image_001.png")
	b := IndexKey("image_001.png")
	if len(a) != len(b) {
		t.Fatalf("length mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Errorf("IndexKey not deterministic at index %d", i)
		}
	}
}

func buildTestTree() *Node {
	root := NewInternal("")
	a := NewInternal("a")
	ab := NewLeaf("ab")
	ab.Entries = []string{"ab1", "ab2"}
	ac := NewLeaf("ac")
	ac.Entries = []string{"ac1"}
	root.Children["a"] = a
	a.Children["b"] = ab
	a.Children["c"] = ac
	return root
}

func TestNavigate(t *testing.T) {
	root := buildTestTree()

	if Navigate(root, "") != root {
		t.Error("Navigate empty prefix should return root")
	}
	n := Navigate(root, "a")
	if n == nil || n.Prefix != "a" {
		t.Errorf("Navigate a: want node with prefix a, got %v", n)
	}
	n = Navigate(root, "ab")
	if n == nil || n.Prefix != "ab" || !n.IsLeaf() {
		t.Errorf("Navigate ab: want leaf with prefix ab, got %v", n)
	}
	n = Navigate(root, "ac")
	if n == nil || n.Prefix != "ac" || !n.IsLeaf() {
		t.Errorf("Navigate ac: want leaf with prefix ac, got %v", n)
	}
	if Navigate(root, "x") != nil {
		t.Error("Navigate x: should return nil (no path)")
	}
	if Navigate(root, "abc") != nil {
		t.Error("Navigate abc: should return nil (ab is leaf, no children)")
	}
	if Navigate(nil, "a") != nil {
		t.Error("Navigate with nil root should return nil")
	}
}

func TestCollectUnder(t *testing.T) {
	root := buildTestTree()

	got := CollectUnder(nil)
	if got != nil {
		t.Errorf("CollectUnder nil: want nil, got %v", got)
	}
	got = CollectUnder(Navigate(root, "ab"))
	sort.Strings(got)
	want := []string{"ab1", "ab2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CollectUnder ab: want %v, got %v", want, got)
	}
	got = CollectUnder(Navigate(root, "ac"))
	want = []string{"ac1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CollectUnder ac: want %v, got %v", want, got)
	}
	got = CollectUnder(Navigate(root, "a"))
	sort.Strings(got)
	want = []string{"ab1", "ab2", "ac1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CollectUnder a: want %v, got %v", want, got)
	}
}

func TestPrefixQuery(t *testing.T) {
	root := buildTestTree()

	got := PrefixQuery(root, "ab")
	sort.Strings(got)
	want := []string{"ab1", "ab2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PrefixQuery ab: want %v, got %v", want, got)
	}
	got = PrefixQuery(root, "a")
	sort.Strings(got)
	want = []string{"ab1", "ab2", "ac1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PrefixQuery a: want %v, got %v", want, got)
	}
	got = PrefixQuery(root, "x")
	if got != nil {
		t.Errorf("PrefixQuery x: want nil, got %v", got)
	}
	got = PrefixQuery(root, "")
	sort.Strings(got)
	want = []string{"ab1", "ab2", "ac1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PrefixQuery empty: want %v, got %v", want, got)
	}
}

func TestSplitLeaf(t *testing.T) {
	leaf := NewLeaf("a")
	for i := 0; i < MAX_BUCKET_SIZE+5; i++ {
		leaf.Entries = append(leaf.Entries, "a"+string(rune('a'+i%26)))
	}
	SplitLeaf(leaf)
	if leaf.IsLeaf() {
		t.Error("SplitLeaf should convert to internal")
	}
	if len(leaf.Children) == 0 {
		t.Error("SplitLeaf should create children")
	}
	got := CollectUnder(leaf)
	if len(got) != MAX_BUCKET_SIZE+5 {
		t.Errorf("SplitLeaf: want %d entries under, got %d", MAX_BUCKET_SIZE+5, len(got))
	}
}

func TestSplitLeafNoOp(t *testing.T) {
	leaf := NewLeaf("x")
	leaf.Entries = []string{"x1", "x2"}
	SplitLeaf(leaf)
	if !leaf.IsLeaf() {
		t.Error("SplitLeaf should not split leaf under threshold")
	}
	if len(leaf.Entries) != 2 {
		t.Errorf("SplitLeaf: entries should remain, got %d", len(leaf.Entries))
	}
}

func TestSplitLeafRecursive(t *testing.T) {
	leaf := NewLeaf("")
	for i := 0; i < 50; i++ {
		leaf.Entries = append(leaf.Entries, "a"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	SplitLeaf(leaf)
	if leaf.IsLeaf() {
		t.Error("SplitLeaf should convert to internal")
	}
	got := CollectUnder(leaf)
	if len(got) != 50 {
		t.Errorf("SplitLeaf: want 50 entries, got %d", len(got))
	}
}
