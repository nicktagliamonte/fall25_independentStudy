// Purpose: Tests for permission enforcement in P2PTupleSpace.

package tuplespace

import (
	"errors"
	"testing"
)

type denyChecker struct{}

func (denyChecker) CheckPermission(operation string) error {
	return ErrPermissionDenied
}

type allowChecker struct{}

func (allowChecker) CheckPermission(operation string) error {
	return nil
}

func TestP2PTupleSpace_TsPut_PermissionDenied(t *testing.T) {
	p := NewP2PTupleSpace("127.0.0.1:9999", 0, "test")
	p.SetPermissionChecker(denyChecker{})
	_, err := p.TsPut("x", []byte("v"))
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}

func TestP2PTupleSpace_TsGet_PermissionDenied(t *testing.T) {
	p := NewP2PTupleSpace("127.0.0.1:9999", 0, "test")
	p.SetPermissionChecker(denyChecker{})
	_, err := p.TsGet("x")
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}

func TestP2PTupleSpace_TsRead_PermissionDenied(t *testing.T) {
	p := NewP2PTupleSpace("127.0.0.1:9999", 0, "test")
	p.SetPermissionChecker(denyChecker{})
	_, err := p.TsRead("x")
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}

func TestP2PTupleSpace_NilChecker_NoCheck(t *testing.T) {
	p := NewP2PTupleSpace("127.0.0.1:9999", 0, "test")
	if p.PermissionChecker != nil {
		t.Fatal("new P2PTupleSpace should have nil PermissionChecker")
	}
}

type recordChecker struct {
	ops []string
}

func (r *recordChecker) CheckPermission(operation string) error {
	r.ops = append(r.ops, operation)
	return nil
}

func TestP2PTupleSpace_CheckPermission_ReceivesCorrectOp(t *testing.T) {
	rec := &recordChecker{}
	p := NewP2PTupleSpace("127.0.0.1:9999", 0, "test")
	p.SetPermissionChecker(rec)

	_, _ = p.TsPut("x", []byte("v"))
	if len(rec.ops) != 1 || rec.ops[0] != OpTsPut {
		t.Errorf("TsPut: CheckPermission got %v, want [%q]", rec.ops, OpTsPut)
	}

	rec.ops = nil
	_, _ = p.TsGet("x")
	if len(rec.ops) != 1 || rec.ops[0] != OpTsGet {
		t.Errorf("TsGet: CheckPermission got %v, want [%q]", rec.ops, OpTsGet)
	}

	rec.ops = nil
	_, _ = p.TsRead("x")
	if len(rec.ops) != 1 || rec.ops[0] != OpTsRead {
		t.Errorf("TsRead: CheckPermission got %v, want [%q]", rec.ops, OpTsRead)
	}
}

func TestP2PTupleSpace_AllowChecker_ProceedsPastPermission(t *testing.T) {
	p := NewP2PTupleSpace("127.0.0.1:9999", 0, "test")
	p.SetPermissionChecker(allowChecker{})

	_, err := p.TsPut("x", []byte("v"))
	if err == nil {
		t.Fatal("expected error (TSH connection refused)")
	}
	if errors.Is(err, ErrPermissionDenied) {
		t.Error("AllowChecker should not return ErrPermissionDenied; error should be from TSH connection")
	}

	_, err = p.TsGet("x")
	if err == nil {
		t.Fatal("expected error (TSH connection refused)")
	}
	if errors.Is(err, ErrPermissionDenied) {
		t.Error("AllowChecker should not return ErrPermissionDenied for TsGet")
	}

	_, err = p.TsRead("x")
	if err == nil {
		t.Fatal("expected error (TSH connection refused)")
	}
	if errors.Is(err, ErrPermissionDenied) {
		t.Error("AllowChecker should not return ErrPermissionDenied for TsRead")
	}
}
