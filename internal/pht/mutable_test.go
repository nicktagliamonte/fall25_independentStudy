package pht

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
)

func TestMutableIndexInsertSplitAndQuery(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	index, err := NewMutableIndex(store)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 40; i++ {
		key := fmt.Sprintf("task:image:dataset-a:%03d", i)
		if err := index.Insert(ctx, key); err != nil {
			t.Fatalf("insert %q: %v", key, err)
		}
	}
	// Inserting an existing name must not duplicate the index entry.
	if err := index.Insert(ctx, "task:image:dataset-a:001"); err != nil {
		t.Fatal(err)
	}

	got, err := ExecutePrefixQuery(ctx, store, "task:image:dataset-a:01")
	if err != nil {
		t.Fatalf("prefix query: %v", err)
	}
	sort.Strings(got)
	if len(got) != 10 || got[0] != "task:image:dataset-a:010" || got[9] != "task:image:dataset-a:019" {
		t.Fatalf("prefix results = %#v", got)
	}

	got, err = ExecuteSubstringQuery(ctx, store, "dataset-a:02", 0)
	if err != nil {
		t.Fatalf("substring query: %v", err)
	}
	sort.Strings(got)
	if len(got) != 10 || got[0] != "task:image:dataset-a:020" || got[9] != "task:image:dataset-a:029" {
		t.Fatalf("substring results = %#v", got)
	}
}

func TestMutableIndexDeletePreservesOtherEntries(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	index, _ := NewMutableIndex(store)
	for _, key := range []string{
		"task:image:dataset-a:001",
		"task:image:dataset-a:002",
		"task:text:dataset-a:001",
	} {
		if err := index.Insert(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	if err := index.Delete(ctx, "task:image:dataset-a:001"); err != nil {
		t.Fatal(err)
	}
	got, err := ExecutePrefixQuery(ctx, store, "task:image:")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "task:image:dataset-a:002" {
		t.Fatalf("remaining image entries = %#v", got)
	}
	got, err = ExecuteSubstringQuery(ctx, store, "dataset-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if len(got) != 2 {
		t.Fatalf("remaining substring entries = %#v", got)
	}
}

func TestMutableIndexConcurrentInsertIsLossless(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	index, _ := NewMutableIndex(store)
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		go func(i int) {
			errs <- index.Insert(ctx, fmt.Sprintf("task:concurrent:%03d", i))
		}(i)
	}
	for i := 0; i < 64; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	got, err := ExecutePrefixQuery(ctx, store, "task:concurrent:")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 64 {
		t.Fatalf("concurrent entries = %d, want 64", len(got))
	}
}

func TestMutableIndexFencesStaleWriterAndMigratesExistingEntry(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	index, err := NewMutableIndex(store)
	if err != nil {
		t.Fatal(err)
	}
	first := WriteFence{Epoch: 1, Writer: "owner-a"}
	second := WriteFence{Epoch: 2, Writer: "owner-b"}
	if err := index.InsertFenced(ctx, "task:fenced:001", first); err != nil {
		t.Fatal(err)
	}
	// Reasserting an existing key under the new epoch must migrate the PHT
	// record even though it does not add a duplicate entry.
	if err := index.InsertFenced(ctx, "task:fenced:001", second); err != nil {
		t.Fatal(err)
	}
	root, err := GetNode(ctx, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if root.Epoch != second.Epoch || root.Writer != second.Writer {
		t.Fatalf("root fence = (%d,%q), want (%d,%q)", root.Epoch, root.Writer, second.Epoch, second.Writer)
	}
	if err := index.InsertFenced(ctx, "task:fenced:002", first); !errors.Is(err, ErrStaleWriteFence) {
		t.Fatalf("stale writer error = %v, want ErrStaleWriteFence", err)
	}
	rows, err := ExecutePrefixQuery(ctx, store, "task:fenced:")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0] != "task:fenced:001" {
		t.Fatalf("rows after rejected stale write = %#v", rows)
	}
}

func TestMutableIndexAdoptFenceMigratesEntireTree(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	index, err := NewMutableIndex(store)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if err := index.Insert(ctx, fmt.Sprintf("task:adopt:%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	fence := WriteFence{Epoch: 9, Writer: "owner-new"}
	if err := index.AdoptFence(ctx, fence); err != nil {
		t.Fatal(err)
	}
	assertStoredFence(t, ctx, store, "", fence)
}

func assertStoredFence(
	t *testing.T,
	ctx context.Context,
	store ValueStore,
	prefix string,
	want WriteFence,
) {
	t.Helper()
	node, err := GetNode(ctx, store, prefix)
	if err != nil {
		t.Fatal(err)
	}
	got := WriteFence{Epoch: node.Epoch, Writer: node.Writer}
	if got != want {
		t.Fatalf("node %q fence = %+v, want %+v", prefix, got, want)
	}
	for segment := range node.Children {
		assertStoredFence(t, ctx, store, prefix+segment, want)
	}
}

func TestMutableIndexQueryStatsReportDirectWork(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	index, _ := NewMutableIndex(store)
	for i := 0; i < 40; i++ {
		if err := index.Insert(ctx, fmt.Sprintf("task:image:%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	prefixRows, prefixStats, err := PrefixQueryDHTWithStats(ctx, store, "task:image:01")
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixRows) != 10 || prefixStats.NodesFetched == 0 ||
		prefixStats.Candidates != 10 || prefixStats.Matches != 10 {
		t.Fatalf("prefix rows/stats = %d, %+v", len(prefixRows), prefixStats)
	}
	substringRows, substringStats, err := ExecuteSubstringQueryWithStats(ctx, store, "image:02", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(substringRows) != 10 || substringStats.NodesFetched == 0 ||
		substringStats.BranchesConsidered == 0 || substringStats.Matches != 10 {
		t.Fatalf("substring rows/stats = %d, %+v", len(substringRows), substringStats)
	}
}

func TestSubstringQueryBloomAblationPreservesResultsAndExposesWork(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	index, _ := NewMutableIndex(store)
	for i := 0; i < 80; i++ {
		if err := index.Insert(ctx, fmt.Sprintf("task:dataset:%03d", i)); err != nil {
			t.Fatal(err)
		}
	}

	prunedRows, prunedStats, err := ExecuteSubstringQueryWithStatsAndPruning(ctx, store, "not-present", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	fullRows, fullStats, err := ExecuteSubstringQueryWithStatsAndPruning(ctx, store, "not-present", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(prunedRows) != len(fullRows) {
		t.Fatalf("ablation changed results: pruned=%v full=%v", prunedRows, fullRows)
	}
	if prunedStats.BranchesPruned == 0 {
		t.Fatalf("expected pruning work, stats=%+v", prunedStats)
	}
	if fullStats.BranchesPruned != 0 || fullStats.Candidates <= prunedStats.Candidates {
		t.Fatalf("expected unpruned traversal to inspect more candidates: pruned=%+v full=%+v", prunedStats, fullStats)
	}
}
