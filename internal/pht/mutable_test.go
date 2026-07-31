package pht

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
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

type laggingStore struct {
	mu      sync.Mutex
	visible map[string][]byte
	latest  map[string][]byte
}

func (s *laggingStore) PutValue(
	_ context.Context,
	key string,
	value []byte,
	_ ...interface{},
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest == nil {
		s.latest = make(map[string][]byte)
	}
	s.latest[key] = append([]byte(nil), value...)
	return nil
}

func (s *laggingStore) GetValue(
	_ context.Context,
	key string,
	_ ...interface{},
) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.visible[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *laggingStore) publishLatest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.visible = make(map[string][]byte, len(s.latest))
	for key, value := range s.latest {
		s.visible[key] = append([]byte(nil), value...)
	}
}

func TestMutableIndexPreservesWritesWhenDHTReadsLag(t *testing.T) {
	ctx := context.Background()
	store := &laggingStore{}
	index, err := NewMutableIndex(store)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if err := index.Insert(ctx, fmt.Sprintf("task:lagging:%03d", i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	store.publishLatest()
	rows, err := ExecutePrefixQuery(ctx, store, "task:lagging:")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 40 {
		t.Fatalf("rows after delayed DHT visibility = %d, want 40", len(rows))
	}
}

func TestRegexQueryScansAndFiltersNames(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	index, err := NewMutableIndex(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"task:image:dataset-a:001",
		"task:text:dataset-a:002",
		"task:audio:dataset-a:003",
	} {
		if err := index.Insert(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	rows, stats, err := RegexQueryDHTWithStats(
		ctx,
		store,
		`task:(image|text):dataset-a:[0-9]+`,
	)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(rows)
	if len(rows) != 2 ||
		rows[0] != "task:image:dataset-a:001" ||
		rows[1] != "task:text:dataset-a:002" {
		t.Fatalf("regex rows = %#v", rows)
	}
	if stats.Candidates != 3 || stats.Matches != 2 || stats.NodesFetched == 0 {
		t.Fatalf("regex stats = %+v", stats)
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

type transientMissingStore struct {
	ValueStore
	mu        sync.Mutex
	key       string
	remaining int
}

func (s *transientMissingStore) GetValue(
	ctx context.Context,
	key string,
	opts ...interface{},
) ([]byte, error) {
	s.mu.Lock()
	if key == s.key && s.remaining > 0 {
		s.remaining--
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	s.mu.Unlock()
	return s.ValueStore.GetValue(ctx, key, opts...)
}

func TestMutableIndexAdoptFenceRetriesTransientlyMissingChild(t *testing.T) {
	ctx := context.Background()
	base := &mockStore{}
	index, err := NewMutableIndex(base)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if err := index.Insert(ctx, fmt.Sprintf("task:adopt-retry:%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	root, err := GetNode(ctx, base, "")
	if err != nil {
		t.Fatal(err)
	}
	var childPrefix string
	for segment := range root.Children {
		childPrefix = root.Prefix + segment
		break
	}
	if childPrefix == "" {
		t.Fatal("test tree did not split")
	}
	transient := &transientMissingStore{
		ValueStore: base,
		key:        dhtKey(childPrefix),
		remaining:  2,
	}
	restarted, err := NewMutableIndex(transient)
	if err != nil {
		t.Fatal(err)
	}
	fence := WriteFence{Epoch: 9, Writer: "owner-new"}
	if err := restarted.AdoptFence(ctx, fence); err != nil {
		t.Fatalf("AdoptFence after transient missing child: %v", err)
	}
	assertStoredFence(t, ctx, base, "", fence)
}

func TestMutableIndexAdoptFenceDiscardsPriorWriterCache(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{}
	first, err := NewMutableIndex(store)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewMutableIndex(store)
	if err != nil {
		t.Fatal(err)
	}
	fence1 := WriteFence{Epoch: 1, Writer: "owner-a"}
	fence2 := WriteFence{Epoch: 2, Writer: "owner-b"}
	fence3 := WriteFence{Epoch: 3, Writer: "owner-a"}
	if err := first.InsertFenced(ctx, "task:cache:001", fence1); err != nil {
		t.Fatal(err)
	}
	if err := second.AdoptFence(ctx, fence2); err != nil {
		t.Fatal(err)
	}
	if err := second.InsertFenced(ctx, "task:cache:002", fence2); err != nil {
		t.Fatal(err)
	}
	if err := first.AdoptFence(ctx, fence3); err != nil {
		t.Fatal(err)
	}
	if err := first.InsertFenced(ctx, "task:cache:003", fence3); err != nil {
		t.Fatal(err)
	}
	rows, err := ExecutePrefixQuery(ctx, store, "task:cache:")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(rows)
	if len(rows) != 3 ||
		rows[0] != "task:cache:001" ||
		rows[1] != "task:cache:002" ||
		rows[2] != "task:cache:003" {
		t.Fatalf("rows after authority returned to prior writer = %#v", rows)
	}
}

func TestMutableIndexAdoptFenceFailsClosedForPersistentlyMissingChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base := &mockStore{}
	root := NewInternal("")
	root.Children["t"] = nil
	if err := PutNode(ctx, base, root); err != nil {
		t.Fatal(err)
	}
	index, err := NewMutableIndex(base)
	if err != nil {
		t.Fatal(err)
	}
	err = index.AdoptFence(ctx, WriteFence{Epoch: 9, Writer: "owner-new"})
	if err == nil {
		t.Fatal("AdoptFence succeeded with a persistently missing child")
	}
	storedRoot, getErr := GetNode(context.Background(), base, "")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if storedRoot.Epoch != 0 || storedRoot.Writer != "" {
		t.Fatalf("root fence changed after failed adoption: %+v", storedRoot)
	}
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
