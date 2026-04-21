// Purpose: Unit tests for /get query flags (remote_only).

package control

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetRemoteOnlyQuery(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"/get", false},
		{"/get?remote_only=1", true},
		{"/get?remote_only=true", true},
		{"/get?remote_only=TRUE", true},
		{"/get?remote_only=yes", true},
		{"/get?format=raw&remote_only=1", true},
		{"/get?remote_only=0", false},
		{"/get?remote_only=false", false},
		{"/get?remote_only=", false},
	} {
		r := httptest.NewRequest(http.MethodPost, tc.raw, nil)
		if got := getRemoteOnlyQuery(r); got != tc.want {
			t.Errorf("%q: got %v want %v", tc.raw, got, tc.want)
		}
	}
}
