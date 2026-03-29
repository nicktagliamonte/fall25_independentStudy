// Purpose: Integration tests for namespace HTTP handlers against a local stack.

package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

func TestNamespaceHandlers_MkdirLinkLsResolveRename(t *testing.T) {
	ctx := context.Background()
	h, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	stack, err := mystore.NewStack(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()

	mux := http.NewServeMux()
	registerNamespaceHandlers(mux, stack, h, nil)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	post := func(path string, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	var keyOut struct {
		DirKey string `json:"dir_key"`
	}

	resp := post("/namespace/mkdir", "{}")
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mkdir root: %s: %s", resp.Status, body)
	}
	if err := json.Unmarshal(body, &keyOut); err != nil {
		t.Fatal(err)
	}
	rootKey := keyOut.DirKey

	resp = post("/namespace/mkdir", "{}")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mkdir sub: %s: %s", resp.Status, body)
	}
	if err := json.Unmarshal(body, &keyOut); err != nil {
		t.Fatal(err)
	}
	subKey := keyOut.DirKey

	resp = post("/namespace/link", `{"dir_key":"`+rootKey+`","name":"sub","child_key":"`+subKey+`"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link root->sub: %s: %s", resp.Status, body)
	}
	if err := json.Unmarshal(body, &keyOut); err != nil {
		t.Fatal(err)
	}
	rootWithSub := keyOut.DirKey

	leaf := []byte("leaf-payload")
	leafK, _, err := stack.PutBlock(ctx, leaf)
	if err != nil {
		t.Fatal(err)
	}

	resp = post("/namespace/link", `{"dir_key":"`+subKey+`","name":"file","child_key":"`+leafK.String()+`"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link sub->file: %s: %s", resp.Status, body)
	}
	if err := json.Unmarshal(body, &keyOut); err != nil {
		t.Fatal(err)
	}
	subWithFile := keyOut.DirKey

	resp = post("/namespace/link", `{"dir_key":"`+rootWithSub+`","name":"sub","child_key":"`+subWithFile+`"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("relink root to new sub: %s: %s", resp.Status, body)
	}
	if err := json.Unmarshal(body, &keyOut); err != nil {
		t.Fatal(err)
	}
	rootNested := keyOut.DirKey

	resp = post("/namespace/ls", `{"dir_key":"`+subWithFile+`"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ls: %s: %s", resp.Status, body)
	}
	var lsOut struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(body, &lsOut); err != nil {
		t.Fatal(err)
	}
	if len(lsOut.Names) != 1 || lsOut.Names[0] != "file" {
		t.Fatalf("ls names: %v", lsOut.Names)
	}

	resp = post("/namespace/resolve", `{"root_key":"`+rootNested+`","path":"sub/file"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: %s: %s", resp.Status, body)
	}
	var resOut struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &resOut); err != nil {
		t.Fatal(err)
	}
	if resOut.Key != leafK.String() {
		t.Fatalf("resolve key: got %s want %s", resOut.Key, leafK.String())
	}

	resp = post("/namespace/rename", `{"dir_key":"`+subWithFile+`","old_name":"file","new_name":"renamed"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename: %s: %s", resp.Status, body)
	}
	if err := json.Unmarshal(body, &keyOut); err != nil {
		t.Fatal(err)
	}
	subRenamed := keyOut.DirKey

	resp = post("/namespace/link", `{"dir_key":"`+rootNested+`","name":"sub","child_key":"`+subRenamed+`"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("relink after rename: %s: %s", resp.Status, body)
	}
	if err := json.Unmarshal(body, &keyOut); err != nil {
		t.Fatal(err)
	}
	rootAfterRename := keyOut.DirKey

	resp = post("/namespace/resolve", `{"root_key":"`+rootAfterRename+`","path":"sub/renamed"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve after rename: %s: %s", resp.Status, body)
	}
	if err := json.Unmarshal(body, &resOut); err != nil {
		t.Fatal(err)
	}
	if resOut.Key != leafK.String() {
		t.Fatalf("resolve after rename: %s", resOut.Key)
	}

	resp = post("/namespace/unlink", `{"dir_key":"`+subRenamed+`","name":"renamed"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unlink: %s: %s", resp.Status, body)
	}
	if err := json.Unmarshal(body, &keyOut); err != nil {
		t.Fatal(err)
	}
	subEmpty := keyOut.DirKey

	resp = post("/namespace/link", `{"dir_key":"`+rootAfterRename+`","name":"sub","child_key":"`+subEmpty+`"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("relink after unlink: %s: %s", resp.Status, body)
	}
	if err := json.Unmarshal(body, &keyOut); err != nil {
		t.Fatal(err)
	}
	rootAfterUnlink := keyOut.DirKey

	resp = post("/namespace/ls", `{"dir_key":"`+subEmpty+`"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ls after unlink: %s: %s", resp.Status, body)
	}
	if err := json.Unmarshal(body, &lsOut); err != nil {
		t.Fatal(err)
	}
	if len(lsOut.Names) != 0 {
		t.Fatalf("expected empty dir, got %v", lsOut.Names)
	}

	resp = post("/namespace/resolve", `{"root_key":"`+rootAfterUnlink+`","path":"sub/renamed"}`)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected resolve to fail after unlink")
	}
	if !bytes.Contains(body, []byte("not found")) {
		t.Fatalf("unexpected body: %s", body)
	}
}
