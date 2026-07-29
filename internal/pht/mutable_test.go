package pht

import (
	"context"
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
