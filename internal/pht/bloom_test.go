// Purpose: Tests for Bloom filter.

package pht

import "testing"

func TestBloomFilterAddContains(t *testing.T) {
	bf := NewBloomFilter(1024, 7)
	bf.AddString("forest")
	if !bf.ContainsString("forest") {
		t.Error("Contains should be true for added item")
	}
}

func TestBloomFilterEmpty(t *testing.T) {
	bf := NewBloomFilter(1024, 7)
	if bf.ContainsString("missing") {
		t.Error("Contains should be false for empty filter")
	}
}

func TestBloomFilterSizeAndHashCount(t *testing.T) {
	bf := NewBloomFilter(256, 5)
	if bf.Size() < 256 {
		t.Errorf("Size want >= 256, got %d", bf.Size())
	}
	if bf.HashCount() != 5 {
		t.Errorf("HashCount want 5, got %d", bf.HashCount())
	}
}

func TestBloomFilterDefaultParams(t *testing.T) {
	bf := NewBloomFilter(0, 0)
	if bf.Size() <= 0 {
		t.Error("zero size should use default")
	}
	if bf.HashCount() <= 0 {
		t.Error("zero hash count should use default")
	}
}

func TestBloomFilterBytes(t *testing.T) {
	bf := NewBloomFilter(1024, 7)
	bf.Add([]byte{1, 2, 3})
	if !bf.Contains([]byte{1, 2, 3}) {
		t.Error("Contains bytes should be true for added item")
	}
}

func TestExtractNGrams(t *testing.T) {
	got := ExtractNGrams("forest", 3)
	want := []string{"for", "ore", "res", "est"}
	if len(got) != len(want) {
		t.Fatalf("ExtractNGrams forest: want %d, got %d", len(want), len(got))
	}
	for i, s := range want {
		if got[i] != s {
			t.Errorf("ExtractNGrams[%d]: want %q, got %q", i, s, got[i])
		}
	}
}

func TestBuildNodeBloom(t *testing.T) {
	leaf := NewLeaf("ab")
	leaf.Entries = []string{"ab1", "ab2", "forest"}
	BuildNodeBloom(leaf, 3, 1024, 7)
	if leaf.Bloom == nil {
		t.Fatal("BuildNodeBloom should set Bloom on leaf")
	}
	if !leaf.Bloom.ContainsString("for") {
		t.Error("Bloom should contain n-gram for from forest")
	}
}

func TestBuildNodeBloomInternal(t *testing.T) {
	root := buildTestTree()
	BuildNodeBloom(root, 3, 1024, 7)
	if root.Bloom == nil {
		t.Fatal("BuildNodeBloom should set Bloom on internal")
	}
}

func TestBloomFilterMarshalUnmarshal(t *testing.T) {
	bf := NewBloomFilter(256, 5)
	bf.AddString("test")
	data, err := bf.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	b2, err := NewBloomFilterFromBinary(data)
	if err != nil {
		t.Fatalf("NewBloomFilterFromBinary: %v", err)
	}
	if !b2.ContainsString("test") {
		t.Error("unmarshaled filter should contain test")
	}
}

func TestBloomContainsAll(t *testing.T) {
	bf := NewBloomFilter(1024, 7)
	for _, ng := range []string{"for", "ore", "res", "est"} {
		bf.AddString(ng)
	}
	if !BloomContainsAll(bf, []string{"for", "ore", "res"}) {
		t.Error("BloomContainsAll should be true when all ngrams present")
	}
	if !BloomContainsAll(bf, []string{"for", "ore", "res", "est"}) {
		t.Error("BloomContainsAll should be true for full set")
	}
	if BloomContainsAll(bf, []string{"for", "xyz"}) {
		t.Error("BloomContainsAll should be false when any ngram missing")
	}
	if !BloomContainsAll(nil, []string{"a"}) {
		t.Error("BloomContainsAll(nil, ...) returns true to avoid pruning")
	}
	if !BloomContainsAll(bf, nil) && !BloomContainsAll(bf, []string{}) {
		t.Error("BloomContainsAll with empty ngrams returns true")
	}
}

func TestPruneByBloom(t *testing.T) {
	ngrams := []string{"for", "ore"}
	a := NewLeaf("a")
	a.Entries = []string{"forest"}
	BuildNodeBloom(a, 3, 1024, 7)
	b := NewLeaf("b")
	b.Entries = []string{"xyz"}
	BuildNodeBloom(b, 3, 1024, 7)
	c := NewLeaf("c")
	c.Entries = []string{"before"}
	BuildNodeBloom(c, 3, 1024, 7)
	nodes := []*Node{a, b, c}
	kept := PruneByBloom(nodes, ngrams)
	if len(kept) != 2 {
		t.Errorf("PruneByBloom: want 2 kept (a,c), got %d", len(kept))
	}
	seen := make(map[string]bool)
	for _, n := range kept {
		seen[n.Prefix] = true
	}
	if !seen["a"] || !seen["c"] || seen["b"] {
		t.Errorf("PruneByBloom: want a,c kept, b pruned; got %v", seen)
	}
	all := PruneByBloom(nodes, nil)
	if len(all) != 3 {
		t.Errorf("PruneByBloom with nil ngrams: want all 3, got %d", len(all))
	}
}

func TestOptimalBloomParams(t *testing.T) {
	m, k := OptimalBloomParams(500, 0.01)
	if m <= 0 || k <= 0 {
		t.Errorf("OptimalBloomParams(500, 0.01): want positive m,k, got %d,%d", m, k)
	}
	p := EstimatedFalsePositiveRate(m, k, 500)
	if p < 0.001 || p > 0.05 {
		t.Errorf("EstimatedFalsePositiveRate for optimal params: want ~0.01, got %v", p)
	}
	m2, k2 := OptimalBloomParams(10000, 0.01)
	if m2 < m || k2 < 1 {
		t.Errorf("more items should yield larger filter: got m=%d k=%d", m2, k2)
	}
}

func TestEstimatedFalsePositiveRate(t *testing.T) {
	p := EstimatedFalsePositiveRate(4096, 6, 500)
	if p <= 0 || p >= 0.1 {
		t.Errorf("EstimatedFalsePositiveRate(4096,6,500): expect ~0.01, got %v", p)
	}
	p0 := EstimatedFalsePositiveRate(1024, 7, 10)
	if p0 <= 0 || p0 >= 0.001 {
		t.Errorf("sparse filter: expect very low FP, got %v", p0)
	}
	p1 := EstimatedFalsePositiveRate(100, 5, 1000)
	if p1 < 0.5 {
		t.Errorf("overloaded filter: expect high FP, got %v", p1)
	}
}

func TestExtractNGramsEdgeCases(t *testing.T) {
	if ExtractNGrams("ab", 3) != nil {
		t.Error("string shorter than n should return nil")
	}
	if ExtractNGrams("abc", 0) != nil {
		t.Error("n=0 should return nil")
	}
	if ExtractNGrams("abc", -1) != nil {
		t.Error("n<0 should return nil")
	}
	got := ExtractNGrams("abc", 3)
	if len(got) != 1 || got[0] != "abc" {
		t.Errorf("exact match: want [abc], got %v", got)
	}
	got = ExtractNGrams("ab", 2)
	if len(got) != 1 || got[0] != "ab" {
		t.Errorf("len=n: want [ab], got %v", got)
	}
}
