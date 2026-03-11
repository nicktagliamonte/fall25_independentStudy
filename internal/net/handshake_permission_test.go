// Purpose: Tests for HandshakePermissionChecker (P2P tuple space + handshake auth integration).

package net

import (
	"errors"
	"testing"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

func TestHandshakePermissionChecker_OpenMode_Allows(t *testing.T) {
	policy := HandshakePolicy{RequireCredential: false}
	checker := NewHandshakePermissionChecker(policy)
	if err := checker.CheckPermission(tuplespace.OpTsPut); err != nil {
		t.Errorf("open mode should allow: %v", err)
	}
}

func TestHandshakePermissionChecker_RequireCredential_NoAuth_Denies(t *testing.T) {
	policy := HandshakePolicy{
		RequireCredential: true,
		AuthScheme:        "",
	}
	checker := NewHandshakePermissionChecker(policy)
	err := checker.CheckPermission(tuplespace.OpTsRead)
	if err == nil {
		t.Fatal("expected permission denied when RequireCredential but no AuthScheme")
	}
	if !errors.Is(err, tuplespace.ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}

func TestHandshakePermissionChecker_RequireCredential_WithToken_Allows(t *testing.T) {
	policy := HandshakePolicy{
		RequireCredential: true,
		AuthScheme:        "token-ed25519-v1",
		Token:             "signed-token-b64",
	}
	checker := NewHandshakePermissionChecker(policy)
	if err := checker.CheckPermission(tuplespace.OpTsGet); err != nil {
		t.Errorf("should allow when Token set: %v", err)
	}
}

func TestHandshakePermissionChecker_RequireCredential_WithCAPubKeys_Allows(t *testing.T) {
	policy := HandshakePolicy{
		RequireCredential: true,
		AuthScheme:        "token-ed25519-v1",
		CAPubKeys:         [][]byte{[]byte("pubkey")},
	}
	checker := NewHandshakePermissionChecker(policy)
	if err := checker.CheckPermission(tuplespace.OpTsPut); err != nil {
		t.Errorf("should allow when CAPubKeys set: %v", err)
	}
}

func TestHandshakePermissionChecker_RequireCredential_NoTokenOrKeys_Denies(t *testing.T) {
	policy := HandshakePolicy{
		RequireCredential: true,
		AuthScheme:        "token-ed25519-v1",
		Token:             "",
		CAPubKeys:         nil,
	}
	checker := NewHandshakePermissionChecker(policy)
	if err := checker.CheckPermission(tuplespace.OpTsRead); err == nil {
		t.Fatal("expected permission denied when no Token or CAPubKeys")
	}
}
