// Purpose: Tests for WithRemoteOnlyGet context wiring used by control /get?remote_only=1.

package storage

import (
	"context"
	"testing"
)

func TestWithRemoteOnlyGetContext(t *testing.T) {
	ctx := context.Background()
	if remoteOnlyGetFromContext(ctx) {
		t.Fatal("expected false before WithRemoteOnlyGet")
	}
	ctx = WithRemoteOnlyGet(ctx)
	if !remoteOnlyGetFromContext(ctx) {
		t.Fatal("expected true after WithRemoteOnlyGet")
	}
}
