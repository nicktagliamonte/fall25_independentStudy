package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/names"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

func TestNamedObjectHTTPCreateResolveUpdateAndSearch(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := names.NewNamespaceID()
	if err != nil {
		t.Fatal(err)
	}
	nameID := names.DeriveNameID(namespace, "/api/a.dat")
	manifest := sha256.Sum256([]byte("manifest"))
	newRecord := func(generation uint64, previous *names.NameRecord) *names.NameRecord {
		nonce := make([]byte, 16)
		_, _ = rand.Read(nonce)
		policy := names.DefaultPolicy()
		policy.StrictPublish = false
		record := &names.NameRecord{Version: names.FormatVersion, Namespace: namespace[:], NameID: nameID[:], Path: "/api/a.dat", Kind: "file", Generation: generation, ManifestKey: manifest[:], Owner: public, Policy: policy, Timestamp: time.Now().UnixNano(), Nonce: nonce}
		if previous != nil {
			hash, _ := previous.Hash()
			record.PreviousHash = hash[:]
		}
		if err := record.Sign(private); err != nil {
			t.Fatal(err)
		}
		return record
	}
	stack := &mystore.Stack{Datastore: dssync.MutexWrap(ds.NewMapDatastore())}
	mux := http.NewServeMux()
	registerNamedObjectHandlers(mux, stack, nil, nil, nil)
	server := httptest.NewServer(mux)
	defer server.Close()
	postRecord := func(method, endpoint string, expected uint64, record *names.NameRecord) *http.Response {
		raw, _ := record.Marshal()
		body, _ := json.Marshal(map[string]any{"expected_generation": expected, "record_cbor": raw})
		request, _ := http.NewRequest(method, server.URL+endpoint, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	initial := newRecord(0, nil)
	response := postRecord(http.MethodPost, "/v1/names/preflight", 0, initial)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("preflight status=%d", response.StatusCode)
	}
	var preflight struct {
		Ready bool `json:"ready"`
	}
	if err := json.NewDecoder(response.Body).Decode(&preflight); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !preflight.Ready {
		t.Fatal("non-strict preflight was not ready")
	}
	response = postRecord(http.MethodPost, "/v1/names", 0, initial)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	response.Body.Close()
	update := newRecord(1, initial)
	response = postRecord(http.MethodPut, "/v1/names/"+nameID.String(), 0, update)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d", response.StatusCode)
	}
	response.Body.Close()
	response, err = http.Get(server.URL + "/v1/names/" + nameID.String())
	if err != nil {
		t.Fatal(err)
	}
	var resolved struct {
		Record names.NameRecord `json:"record"`
	}
	if err := json.NewDecoder(response.Body).Decode(&resolved); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if resolved.Record.Generation != 1 {
		t.Fatalf("generation=%d", resolved.Record.Generation)
	}
	response, err = http.Get(server.URL + "/v1/names/search?prefix=%2Fapi%2F&suffix=.dat&fanout_attempted=3&fanout_completed=2")
	if err != nil {
		t.Fatal(err)
	}
	var search names.SearchResult
	if err := json.NewDecoder(response.Body).Decode(&search); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(search.Records) != 1 || search.Complete {
		t.Fatalf("search=%+v", search)
	}
	directoryID := names.DeriveNameID(namespace, "/api")
	directoryPolicy := names.DefaultPolicy()
	directoryPolicy.StrictPublish = false
	directoryPolicy.Encryption = "public"
	directory := &names.NameRecord{
		Version: names.FormatVersion, Namespace: namespace[:], NameID: directoryID[:],
		Path: "/api", Kind: "directory", DirectoryChildren: [][]byte{append([]byte(nil), nameID[:]...)},
		Owner: public, Policy: directoryPolicy, Timestamp: time.Now().UnixNano(), Nonce: bytes.Repeat([]byte{9}, 16),
	}
	if err := directory.Sign(private); err != nil {
		t.Fatal(err)
	}
	response = postRecord(http.MethodPost, "/v1/names", 0, directory)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("directory create status=%d", response.StatusCode)
	}
	response.Body.Close()
	response, err = http.Get(server.URL + "/v1/directories/" + directoryID.String())
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Directory names.NameRecord   `json:"directory"`
		Children  []names.NameRecord `json:"children"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if listing.Directory.Kind != "directory" || len(listing.Children) != 1 || listing.Children[0].Generation != 1 {
		t.Fatalf("directory listing=%+v", listing)
	}
}
