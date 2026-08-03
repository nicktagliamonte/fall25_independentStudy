package tuplespace

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// Exact-name CAS is shared with the mutable-name head path. Keep its conflict
// behavior covered alongside the tuple compatibility semantics.
func TestNativeExactCASOneWinner(t *testing.T) {
	space := NewNativeTupleSpace()
	if err := space.CompareAndSwapExact(context.Background(), "__name__", nil, []byte("g0")); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var conflicts atomic.Int32
	var wg sync.WaitGroup
	for _, next := range [][]byte{[]byte("g1-a"), []byte("g1-b")} {
		next := next
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := space.CompareAndSwapExact(context.Background(), "__name__", []byte("g0"), next)
			if err == nil {
				wins.Add(1)
			} else if errors.Is(err, ErrTupleCASConflict) {
				conflicts.Add(1)
			} else {
				t.Errorf("CAS: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins.Load(), conflicts.Load())
	}
}

func TestNativeTupleSpacePutReadGet(t *testing.T) {
	ts := NewNativeTupleSpace()
	if _, err := ts.TsPut("task:image:001", []byte("payload")); err != nil {
		t.Fatalf("put: %v", err)
	}

	first, err := ts.TsRead("task:image:001")
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	second, err := ts.TsRead("task:image:001")
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if string(first) != "payload" || string(second) != "payload" {
		t.Fatalf("read values = %q, %q", first, second)
	}

	got, err := ts.TsGet("task:image:001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("get value = %q", got)
	}
	if _, err := ts.TsRead("task:image:001"); !errors.Is(err, ErrTupleNotFound) {
		t.Fatalf("read after get error = %v, want ErrTupleNotFound", err)
	}
}

func TestNativeTupleSpaceReplaceCollapsesExactName(t *testing.T) {
	ts := NewNativeTupleSpace()
	if _, err := ts.TsPut("offer:peer", []byte("old-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.TsPut("offer:peer", []byte("old-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.TsPut("other", []byte("keep")); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.TsReplace("offer:peer", []byte("current")); err != nil {
		t.Fatal(err)
	}
	if ts.Len() != 2 {
		t.Fatalf("tuple count = %d, want 2", ts.Len())
	}
	got, err := ts.TsGet("offer:peer")
	if err != nil || string(got) != "current" {
		t.Fatalf("replacement = %q, %v", got, err)
	}
	if _, err := ts.TsRead("offer:peer"); !errors.Is(err, ErrTupleNotFound) {
		t.Fatalf("duplicate survived replacement: %v", err)
	}
	got, err = ts.TsRead("other")
	if err != nil || string(got) != "keep" {
		t.Fatalf("unrelated tuple = %q, %v", got, err)
	}
}

func TestNativeTupleSpaceIsMultiset(t *testing.T) {
	ts := NewNativeTupleSpace()
	_, _ = ts.TsPut("result:dataset-a", []byte("first"))
	_, _ = ts.TsPut("result:dataset-a", []byte("second"))

	first, err := ts.TsGet("result:dataset-a")
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	second, err := ts.TsGet("result:dataset-a")
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if string(first) != "first" || string(second) != "second" {
		t.Fatalf("FIFO values = %q, %q", first, second)
	}
}

func TestNativeTupleSpaceAssociativeMatching(t *testing.T) {
	ts := NewNativeTupleSpace()
	_, _ = ts.TsPut("task:image:001", []byte("image"))
	_, _ = ts.TsPut("task:text:001", []byte("text"))

	got, err := ts.TsRead("task:image:*")
	if err != nil {
		t.Fatalf("wildcard read: %v", err)
	}
	if string(got) != "image" {
		t.Fatalf("wildcard value = %q", got)
	}

	got, err = ts.TsRead(`task:(image|text):001`)
	if err != nil {
		t.Fatalf("regex read: %v", err)
	}
	if string(got) != "image" {
		t.Fatalf("regex value = %q", got)
	}
}

func TestNativeTupleSpaceConcurrentGetConsumesOnce(t *testing.T) {
	ts := NewNativeTupleSpace()
	_, _ = ts.TsPut("task:exclusive", []byte("only-once"))

	const contenders = 64
	var successes atomic.Int32
	var wg sync.WaitGroup
	wg.Add(contenders)
	for i := 0; i < contenders; i++ {
		go func() {
			defer wg.Done()
			value, err := ts.TsGet("task:exclusive")
			switch {
			case err == nil:
				if string(value) != "only-once" {
					t.Errorf("value = %q", value)
				}
				successes.Add(1)
			case errors.Is(err, ErrTupleNotFound):
			default:
				t.Errorf("get: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful consumers = %d, want 1", got)
	}
}

func TestNativeTupleSpacePatternErrors(t *testing.T) {
	ts := NewNativeTupleSpace()
	_, _ = ts.TsPut("x", []byte("value"))
	if _, err := ts.TsRead("["); !errors.Is(err, ErrInvalidTuplePattern) {
		t.Fatalf("error = %v, want ErrInvalidTuplePattern", err)
	}
	if _, err := ts.TsRead(""); !errors.Is(err, ErrInvalidTuplePattern) {
		t.Fatalf("empty error = %v, want ErrInvalidTuplePattern", err)
	}
}

func BenchmarkNativeTupleSpaceAssociativeRead(b *testing.B) {
	ts := NewNativeTupleSpace()
	for i := 0; i < 1000; i++ {
		_, _ = ts.TsPut(fmt.Sprintf("task:image:%04d", i), []byte("payload"))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ts.TsRead("task:image:09*"); err != nil {
			b.Fatal(err)
		}
	}
}
