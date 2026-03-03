// Purpose: Tests for IBLT.

package sync

import (
	"testing"
)

func TestIBLTInsert(t *testing.T) {
	tbl := NewIBLT(64)
	tbl.Insert([]byte("a"))
	tbl.Insert([]byte("b"))
	var nonzero int
	for i := range tbl.Cells {
		if tbl.Cells[i].Count != 0 {
			nonzero++
		}
	}
	if nonzero < 2 {
		t.Errorf("Insert: expect cells to be updated, got %d nonzero cells", nonzero)
	}
}

func TestIBLTDelete(t *testing.T) {
	tbl := NewIBLT(64)
	key := []byte("delete-me")
	tbl.Insert(key)
	tbl.Delete(key)
	for i := range tbl.Cells {
		c := &tbl.Cells[i]
		if c.Count != 0 || c.KeySum != 0 || c.HashSum != 0 {
			t.Errorf("cell %d: Insert then Delete should restore zero state, got Count=%d KeySum=%x HashSum=%x", i, c.Count, c.KeySum, c.HashSum)
		}
	}
}

func TestIBLTSubtract(t *testing.T) {
	a := NewIBLT(64)
	b := NewIBLT(64)
	a.Insert([]byte("x"))
	b.Insert([]byte("x"))
	diff := a.Subtract(b)
	if diff == nil {
		t.Fatal("Subtract: got nil")
	}
	allZero := true
	for i := range diff.Cells {
		c := &diff.Cells[i]
		if c.Count != 0 || c.KeySum != 0 || c.HashSum != 0 {
			allZero = false
			break
		}
	}
	if !allZero {
		t.Error("Subtract: same key in both should yield all-zero difference")
	}
}

func TestIBLTSubtractSymmetricDifference(t *testing.T) {
	a := NewIBLT(128)
	b := NewIBLT(128)
	a.Insert([]byte("only-in-a"))
	a.Insert([]byte("in-both"))
	b.Insert([]byte("in-both"))
	b.Insert([]byte("only-in-b"))
	diff := a.Subtract(b)
	if diff == nil {
		t.Fatal("Subtract: got nil")
	}
	nonzero := 0
	for i := range diff.Cells {
		c := &diff.Cells[i]
		if c.Count != 0 || c.KeySum != 0 || c.HashSum != 0 {
			nonzero++
		}
	}
	if nonzero == 0 {
		t.Error("Subtract: symmetric difference should have nonzero cells")
	}
}

func TestIBLTSubtractIncompatible(t *testing.T) {
	a := NewIBLT(64)
	b := NewIBLT(128)
	diff := a.Subtract(b)
	if diff != nil {
		t.Error("Subtract: incompatible cell counts should return nil")
	}
}

func TestIBLTPeel(t *testing.T) {
	a := NewIBLT(1024)
	b := NewIBLT(1024)
	keyA := []byte("only-in-a")
	keyB := []byte("only-in-b")
	a.Insert(keyA)
	b.Insert(keyB)
	diff := a.Subtract(b)
	if diff == nil {
		t.Fatal("Subtract: got nil")
	}
	res := diff.Peel()
	khA := a.keyHash(keyA)
	khB := a.keyHash(keyB)
	if len(res.Positive) != 1 || res.Positive[0] != khA {
		t.Errorf("Peel: want Positive=[hash(only-in-a)], got %v", res.Positive)
	}
	if len(res.Negative) != 1 || res.Negative[0] != khB {
		t.Errorf("Peel: want Negative=[hash(only-in-b)], got %v", res.Negative)
	}
}

func TestIBLTHasUnpeeled(t *testing.T) {
	empty := NewIBLT(16)
	if empty.HasUnpeeled() {
		t.Error("empty IBLT should not have unpeeled")
	}
	tbl := NewIBLT(16)
	tbl.Insert([]byte("x"))
	if !tbl.HasUnpeeled() {
		t.Error("IBLT with item should have unpeeled")
	}
	tbl.Delete([]byte("x"))
	if tbl.HasUnpeeled() {
		t.Error("IBLT after Insert+Delete should have no unpeeled")
	}
}

func TestIBLTInsertXORAccumulation(t *testing.T) {
	tbl := NewIBLT(256)
	key := []byte("test-key")
	tbl.Insert(key)
	tbl.Insert(key)
	var nonzero int
	for i := range tbl.Cells {
		c := &tbl.Cells[i]
		if c.Count != 0 {
			nonzero++
			if c.Count != 2 {
				t.Errorf("cell %d: Insert same key twice should give Count=2, got %d", i, c.Count)
			}
			if c.KeySum != 0 || c.HashSum != 0 {
				t.Errorf("cell %d: XOR of same value twice should cancel (0), got KeySum=%x HashSum=%x", i, c.KeySum, c.HashSum)
			}
		}
	}
	if nonzero == 0 {
		t.Error("Insert: expect at least one cell updated")
	}
}
