// Purpose: Tests for query parser.

package pht

import (
	"context"
	"sort"
	"testing"
)

func TestRankPrefixMatches(t *testing.T) {
	keys := []string{"image_123", "image_1", "image_99"}
	got := RankPrefixMatches(keys, "image_")
	if len(got) != 3 {
		t.Fatalf("RankPrefixMatches: want 3, got %d", len(got))
	}
	if got[0].Key != "image_1" {
		t.Errorf("highest rank should be shortest key image_1, got %q (score %v)", got[0].Key, got[0].Score)
	}
	if got[0].Score <= got[1].Score || got[1].Score <= got[2].Score {
		t.Error("scores should decrease by key length")
	}
}

func TestRankSubstringMatches(t *testing.T) {
	keys := []string{"deforestation", "forest", "my_forest_file"}
	got := RankSubstringMatches(keys, "forest")
	if len(got) != 3 {
		t.Fatalf("RankSubstringMatches: want 3, got %d", len(got))
	}
	if got[0].Key != "forest" || got[0].Score != 1.0 {
		t.Errorf("exact match forest should rank first with score 1.0, got %q %.2f", got[0].Key, got[0].Score)
	}
	if got[1].Key != "deforestation" {
		t.Errorf("earlier occurrence (deforestation idx 2) should rank higher than my_forest_file (idx 3), got %q", got[1].Key)
	}
}

func TestExecuteRanked(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	root := NewInternal("")
	a := NewInternal("a")
	ab := NewLeaf("ab")
	ab.Entries = []string{"ab", "ab123", "ab12"}
	root.Children["a"] = a
	a.Children["b"] = ab
	BuildNodeBloom(root, 3, 256, 5)
	if err := PutNodeRecursive(ctx, store, root); err != nil {
		t.Fatalf("PutNodeRecursive: %v", err)
	}
	q := ParseQuery("ab*")
	got, err := ExecuteRanked(ctx, store, q)
	if err != nil {
		t.Fatalf("ExecuteRanked: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ExecuteRanked: want 3, got %d", len(got))
	}
	if got[0].Key != "ab" {
		t.Errorf("shortest prefix match (ab) should rank first, got %q", got[0].Key)
	}
}

func TestCombineResults(t *testing.T) {
	got := CombineResults(
		[]string{"a", "b"},
		[]string{"b", "c"},
		[]string{"a", "d"},
	)
	if len(got) != 4 {
		t.Errorf("CombineResults: want 4 unique, got %d", len(got))
	}
	seen := make(map[string]bool)
	for _, k := range got {
		if seen[k] {
			t.Errorf("CombineResults: duplicate %q", k)
		}
		seen[k] = true
	}
	for _, want := range []string{"a", "b", "c", "d"} {
		if !seen[want] {
			t.Errorf("CombineResults: missing %q", want)
		}
	}
	empty := CombineResults(nil, []string{})
	if len(empty) != 0 {
		t.Errorf("CombineResults(nil, {}): want empty, got %v", empty)
	}
}

func TestParseQueryExact(t *testing.T) {
	q := ParseQuery("image_001.jpg")
	if q.Kind != QueryExact {
		t.Errorf("ParseQuery image_001.jpg: want QueryExact, got %v", q.Kind)
	}
	if q.Prefix != "" || q.Substring != "" {
		t.Errorf("exact: want empty Prefix/Substring, got %q %q", q.Prefix, q.Substring)
	}
	q2 := ParseQuery("x")
	if q2.Kind != QueryExact {
		t.Errorf("ParseQuery x: want QueryExact, got %v", q2.Kind)
	}
}

func TestParseQueryPrefix(t *testing.T) {
	q := ParseQuery("image_*")
	if q.Kind != QueryPrefix {
		t.Errorf("ParseQuery image_*: want QueryPrefix, got %v", q.Kind)
	}
	if q.Prefix != "image_" {
		t.Errorf("prefix: want image_, got %q", q.Prefix)
	}
	if q.Substring != "" {
		t.Errorf("prefix query should have empty Substring, got %q", q.Substring)
	}
}

func TestParseQuerySubstring(t *testing.T) {
	q := ParseQuery("*forest*")
	if q.Kind != QuerySubstring {
		t.Errorf("ParseQuery *forest*: want QuerySubstring, got %v", q.Kind)
	}
	if q.Substring != "forest" {
		t.Errorf("substring: want forest, got %q", q.Substring)
	}
	if q.Prefix != "" {
		t.Errorf("substring query should have empty Prefix, got %q", q.Prefix)
	}
}

func TestParseQuerySubstringLeadingOnly(t *testing.T) {
	q := ParseQuery("*forest")
	if q.Kind != QuerySubstring {
		t.Errorf("ParseQuery *forest: want QuerySubstring, got %v", q.Kind)
	}
	if q.Substring != "forest" {
		t.Errorf("substring: want forest, got %q", q.Substring)
	}
}

func TestParseQueryEmpty(t *testing.T) {
	q := ParseQuery("")
	if q.Pattern != "" {
		t.Errorf("empty pattern: Pattern should be empty, got %q", q.Pattern)
	}
}

func TestExecutePrefixQuery(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	root := buildTestTree()
	if err := PutNodeRecursive(ctx, store, root); err != nil {
		t.Fatalf("PutNodeRecursive: %v", err)
	}
	got, err := ExecutePrefixQuery(ctx, store, "ab")
	if err != nil {
		t.Fatalf("ExecutePrefixQuery: %v", err)
	}
	sort.Strings(got)
	want := []string{"ab1", "ab2"}
	if len(got) != len(want) {
		t.Errorf("ExecutePrefixQuery ab: want %v, got %v", want, got)
	}
	for i, s := range want {
		if i < len(got) && got[i] != s {
			t.Errorf("ExecutePrefixQuery[%d]: want %q, got %q", i, s, got[i])
		}
	}
}

func TestExecutePrefixFromParsed(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	root := buildTestTree()
	if err := PutNodeRecursive(ctx, store, root); err != nil {
		t.Fatalf("PutNodeRecursive: %v", err)
	}
	q := ParseQuery("ab*")
	if q.Kind != QueryPrefix || q.Prefix != "ab" {
		t.Fatalf("ParseQuery ab*: want QueryPrefix/ab, got %v/%q", q.Kind, q.Prefix)
	}
	got, err := Execute(ctx, store, q)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sort.Strings(got)
	want := []string{"ab1", "ab2"}
	if len(got) != len(want) {
		t.Errorf("Execute(ab*): want %v, got %v", want, got)
	}
}

func TestExecuteSubstringQuery(t *testing.T) {
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
	got, err := ExecuteSubstringQuery(ctx, store, "forest", 3)
	if err != nil {
		t.Fatalf("ExecuteSubstringQuery: %v", err)
	}
	sort.Strings(got)
	want := []string{"forest"}
	if len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
		t.Errorf("ExecuteSubstringQuery forest: want %v, got %v", want, got)
	}
}

func TestExecuteSubstringFromParsed(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	root := NewInternal("")
	a := NewInternal("a")
	ab := NewLeaf("ab")
	ab.Entries = []string{"deforestation", "forest"}
	ac := NewLeaf("ac")
	ac.Entries = []string{"xyz"}
	root.Children["a"] = a
	a.Children["b"] = ab
	a.Children["c"] = ac
	BuildNodeBloom(root, 3, 256, 5)
	if err := PutNodeRecursive(ctx, store, root); err != nil {
		t.Fatalf("PutNodeRecursive: %v", err)
	}
	q := ParseQuery("*forest*")
	if q.Kind != QuerySubstring || q.Substring != "forest" {
		t.Fatalf("ParseQuery *forest*: want QuerySubstring/forest, got %v/%q", q.Kind, q.Substring)
	}
	got, err := Execute(ctx, store, q)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sort.Strings(got)
	want := []string{"deforestation", "forest"}
	if len(got) != len(want) {
		t.Errorf("Execute(*forest*): want %v, got %v", want, got)
	}
}
