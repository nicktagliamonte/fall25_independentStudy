// tarsusctl is the client-side named-object tool. Chunking, encryption,
// signing, envelope creation, and reconstruction intentionally happen here;
// the node only receives immutable blocks and signed mutable records.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/names"
)

type stringList []string

func (values *stringList) String() string         { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error { *values = append(*values, value); return nil }

type client struct {
	base string
	http *http.Client
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tarsusctl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return usageError()
	}
	switch args[0] {
	case "object":
		switch args[1] {
		case "put":
			return runPut(args[2:], false)
		case "update":
			return runPut(args[2:], true)
		case "get":
			return runGet(args[2:])
		case "delete":
			return runDelete(args[2:])
		case "search":
			return runSearch(args[2:])
		}
	case "directory":
		switch args[1] {
		case "mkdir":
			return runDirectoryMkdir(args[2:])
		case "ls":
			return runDirectoryList(args[2:])
		case "link":
			return runDirectoryMutation(args[2:], true)
		case "unlink":
			return runDirectoryMutation(args[2:], false)
		}
	}
	return usageError()
}

func usageError() error {
	return errors.New("usage: tarsusctl object put|update|get|delete|search [flags] | directory mkdir|ls|link|unlink [flags]")
}

func commonClient(set *flag.FlagSet) (*string, *time.Duration) {
	api := set.String("api", "http://127.0.0.1:2892", "node control API base URL")
	timeout := set.Duration("timeout", 5*time.Minute, "overall operation timeout")
	return api, timeout
}

func runPut(args []string, update bool) error {
	set := flag.NewFlagSet("object put", flag.ContinueOnError)
	api, timeout := commonClient(set)
	file := set.String("file", "-", "input file, or - for stdin")
	namespaceText := set.String("namespace", "", "64-hex namespace ID (generated for create)")
	pathValue := set.String("path", "", "absolute object path")
	nameText := set.String("name-id", "", "existing NameID (required for update)")
	signingKeyText := set.String("signing-key", "", "Ed25519 private key or seed in hex")
	readerPrivateText := set.String("reader-private", "", "X25519 private key in hex; generated for private create")
	dataKeyText := set.String("data-key", "", "same-epoch data key in hex (update only)")
	encryption := set.String("encryption", "private", "private or public")
	keyEpoch := set.Uint64("key-epoch", 1, "encryption key epoch")
	replicas := set.Uint("replicas", 7, "total required copies")
	near := set.Uint("near", 3, "near RTT-class copies")
	middle := set.Uint("middle", 2, "middle RTT-class copies")
	far := set.Uint("far", 2, "far RTT-class copies")
	searchable := set.Bool("searchable", true, "include current name in secondary search")
	var readerPublicValues stringList
	set.Var(&readerPublicValues, "reader", "X25519 reader public key in hex (repeatable)")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *pathValue == "" || *signingKeyText == "" {
		return errors.New("--path and --signing-key are required")
	}
	privateKey, err := parseEd25519Private(*signingKeyText)
	if err != nil {
		return err
	}
	owner := privateKey.Public().(ed25519.PublicKey)
	normalized, err := names.NormalizePath(*pathValue)
	if err != nil {
		return err
	}
	c := &client{base: strings.TrimRight(*api, "/"), http: &http.Client{Timeout: *timeout}}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var namespace names.NamespaceID
	var previousRecord *names.NameRecord
	var previousManifest *names.ObjectManifest
	if update {
		if *nameText == "" {
			return errors.New("--name-id is required for update")
		}
		id, err := names.ParseNameID(*nameText)
		if err != nil {
			return err
		}
		previousRecord, _, err = c.getRecord(ctx, id)
		if err != nil {
			return err
		}
		copy(namespace[:], previousRecord.Namespace)
		if previousRecord.Path != normalized {
			return errors.New("--path does not match immutable existing path")
		}
		manifestRaw, err := c.getBlock(ctx, previousRecord.ManifestKey)
		if err != nil {
			return err
		}
		previousManifest, err = names.DecodeObjectManifest(manifestRaw)
		if err != nil {
			return err
		}
		if *encryption == "" {
			*encryption = previousRecord.Policy.Encryption
		}
	} else if *namespaceText == "" {
		namespace, err = names.NewNamespaceID()
		if err != nil {
			return err
		}
	} else {
		namespace, err = names.ParseNamespaceID(*namespaceText)
		if err != nil {
			return err
		}
	}

	readerPublicKeys := make([][]byte, 0, len(readerPublicValues)+1)
	for _, value := range readerPublicValues {
		decoded, err := decodeHex32("reader public key", value)
		if err != nil {
			return err
		}
		readerPublicKeys = append(readerPublicKeys, decoded)
	}
	var readerPrivate []byte
	if *encryption == "private" && *readerPrivateText != "" {
		readerPrivate, err = decodeHex32("reader private key", *readerPrivateText)
		if err != nil {
			return err
		}
		public, err := curve25519.X25519(readerPrivate, curve25519.Basepoint)
		if err != nil {
			return err
		}
		readerPublicKeys = appendUnique(readerPublicKeys, public)
	}
	if *encryption == "private" && len(readerPublicKeys) == 0 && !update {
		readerPrivate = make([]byte, 32)
		if _, err := rand.Read(readerPrivate); err != nil {
			return err
		}
		public, _ := curve25519.X25519(readerPrivate, curve25519.Basepoint)
		readerPublicKeys = append(readerPublicKeys, public)
	}
	if *encryption == "private" && len(readerPublicKeys) == 0 {
		return errors.New("private update requires --reader or --reader-private")
	}
	var dataKey []byte
	if *dataKeyText != "" {
		dataKey, err = decodeHex32("data key", *dataKeyText)
		if err != nil {
			return err
		}
	}

	input, closeInput, err := openInput(*file)
	if err != nil {
		return err
	}
	defer closeInput()
	sink := func(ctx context.Context, data []byte) (names.ContentKey, error) { return c.putBlock(ctx, data) }
	built, err := names.BuildObject(ctx, input, sink, names.BuildObjectOptions{Encryption: *encryption, KeyEpoch: *keyEpoch, DataKey: dataKey, ReaderPublicKeys: readerPublicKeys, Previous: previousManifest, Signer: privateKey})
	if err != nil {
		return err
	}
	policy := names.ObjectPolicy{Replicas: uint16(*replicas), Placement: names.PlacementPolicy{Near: uint16(*near), Middle: uint16(*middle), Far: uint16(*far)}, StrictPublish: true, Encryption: *encryption, KeyEpoch: *keyEpoch, RetainVersions: 3, CollectionGrace: int64(24 * time.Hour), Searchable: *searchable}
	if err := policy.Validate(); err != nil {
		return err
	}
	if update {
		policy = previousRecord.Policy
		policy.Encryption = *encryption
		policy.KeyEpoch = *keyEpoch
		policy.Searchable = *searchable
	}
	id := names.DeriveNameID(namespace, normalized)
	generation := uint64(0)
	var previousHash []byte
	if previousRecord != nil {
		generation = previousRecord.Generation + 1
		hash, err := previousRecord.Hash()
		if err != nil {
			return err
		}
		previousHash = hash[:]
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	record := &names.NameRecord{Version: names.FormatVersion, Namespace: namespace[:], NameID: id[:], Path: normalized, Kind: "file", Generation: generation, PreviousHash: previousHash, ManifestKey: built.ManifestKey[:], Owner: owner, Policy: policy, Timestamp: time.Now().UnixNano(), Nonce: nonce}
	if err := record.Sign(privateKey); err != nil {
		return err
	}
	recordRaw, _ := record.Marshal()
	if err := c.waitForPublication(ctx, recordRaw); err != nil {
		return err
	}
	body := map[string]any{"expected_generation": uint64(0), "record_cbor": recordRaw}
	method, endpoint := http.MethodPost, "/v1/names"
	if update {
		body["expected_generation"] = previousRecord.Generation
		method, endpoint = http.MethodPut, "/v1/names/"+id.String()
	}
	var response any
	if err := c.json(ctx, method, endpoint, body, &response); err != nil {
		return err
	}
	result := map[string]any{"namespace": namespace.String(), "name_id": id.String(), "path": normalized, "generation": generation, "manifest_key": built.ManifestKey.String(), "new_blocks": built.NewBlocks, "reused_blocks": built.ReusedBlocks, "response": response}
	if len(built.DataKey) != 0 {
		result["data_key"] = hex.EncodeToString(built.DataKey)
	}
	if len(readerPrivate) != 0 {
		result["reader_private"] = hex.EncodeToString(readerPrivate)
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func runGet(args []string) error {
	set := flag.NewFlagSet("object get", flag.ContinueOnError)
	api, timeout := commonClient(set)
	nameText := set.String("name-id", "", "NameID")
	output := set.String("output", "-", "output file, or - for stdout")
	readerPrivateText := set.String("reader-private", "", "X25519 reader private key in hex")
	if err := set.Parse(args); err != nil {
		return err
	}
	id, err := names.ParseNameID(*nameText)
	if err != nil {
		return err
	}
	c := &client{base: strings.TrimRight(*api, "/"), http: &http.Client{Timeout: *timeout}}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	record, _, err := c.getRecord(ctx, id)
	if err != nil {
		return err
	}
	if record.Tombstone {
		return errors.New("name is deleted")
	}
	manifestRaw, err := c.getBlock(ctx, record.ManifestKey)
	if err != nil {
		return err
	}
	manifest, err := names.DecodeObjectManifest(manifestRaw)
	if err != nil {
		return err
	}
	var dataKey []byte
	if manifest.Encryption == "private" {
		readerPrivate, err := decodeHex32("reader private key", *readerPrivateText)
		if err != nil {
			return err
		}
		public, _ := curve25519.X25519(readerPrivate, curve25519.Basepoint)
		found := false
		for _, envelope := range manifest.Envelopes {
			if bytes.Equal(envelope.ReaderPublic, public) {
				dataKey, err = names.OpenDataKey(envelope, readerPrivate, manifest.KeyEpoch)
				found = true
				break
			}
		}
		if !found {
			return errors.New("no envelope for reader")
		}
		if err != nil {
			return err
		}
	}
	destination, closeOutput, err := openOutput(*output)
	if err != nil {
		return err
	}
	defer closeOutput()
	source := func(ctx context.Context, key names.ContentKey) ([]byte, error) { return c.getBlock(ctx, key[:]) }
	return names.ReconstructObject(ctx, manifest, source, dataKey, destination)
}

func runDelete(args []string) error {
	set := flag.NewFlagSet("object delete", flag.ContinueOnError)
	api, timeout := commonClient(set)
	nameText := set.String("name-id", "", "NameID")
	signingKeyText := set.String("signing-key", "", "Ed25519 owner private key in hex")
	if err := set.Parse(args); err != nil {
		return err
	}
	id, err := names.ParseNameID(*nameText)
	if err != nil {
		return err
	}
	privateKey, err := parseEd25519Private(*signingKeyText)
	if err != nil {
		return err
	}
	c := &client{base: strings.TrimRight(*api, "/"), http: &http.Client{Timeout: *timeout}}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	current, _, err := c.getRecord(ctx, id)
	if err != nil {
		return err
	}
	hash, err := current.Hash()
	if err != nil {
		return err
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	tombstone := *current
	tombstone.Generation++
	tombstone.PreviousHash = hash[:]
	tombstone.ManifestKey = nil
	tombstone.DirectoryChildren = nil
	tombstone.Tombstone = true
	tombstone.Timestamp = time.Now().UnixNano()
	tombstone.Nonce = nonce
	tombstone.Signature = nil
	if err := tombstone.Sign(privateKey); err != nil {
		return err
	}
	raw, _ := tombstone.Marshal()
	var response any
	if err := c.json(ctx, http.MethodDelete, "/v1/names/"+id.String(), map[string]any{"expected_generation": current.Generation, "record_cbor": raw}, &response); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(response)
}

func runSearch(args []string) error {
	set := flag.NewFlagSet("object search", flag.ContinueOnError)
	api, timeout := commonClient(set)
	prefix := set.String("prefix", "", "path prefix")
	suffix := set.String("suffix", "", "path suffix")
	if err := set.Parse(args); err != nil {
		return err
	}
	c := &client{base: strings.TrimRight(*api, "/"), http: &http.Client{Timeout: *timeout}}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var response any
	endpoint := "/v1/names/search?prefix=" + url.QueryEscape(*prefix) + "&suffix=" + url.QueryEscape(*suffix)
	if err := c.json(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(response)
}

func runDirectoryMkdir(args []string) error {
	set := flag.NewFlagSet("directory mkdir", flag.ContinueOnError)
	api, timeout := commonClient(set)
	namespaceText := set.String("namespace", "", "64-hex namespace ID (generated when omitted)")
	pathValue := set.String("path", "", "absolute directory path")
	signingKeyText := set.String("signing-key", "", "Ed25519 owner private key in hex")
	searchable := set.Bool("searchable", true, "include the directory in secondary name search")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *pathValue == "" || *signingKeyText == "" {
		return errors.New("--path and --signing-key are required")
	}
	privateKey, err := parseEd25519Private(*signingKeyText)
	if err != nil {
		return err
	}
	normalized, err := names.NormalizePath(*pathValue)
	if err != nil {
		return err
	}
	var namespace names.NamespaceID
	if *namespaceText == "" {
		namespace, err = names.NewNamespaceID()
	} else {
		namespace, err = names.ParseNamespaceID(*namespaceText)
	}
	if err != nil {
		return err
	}
	policy := names.DefaultPolicy()
	policy.Encryption = "public"
	policy.KeyEpoch = 0
	policy.Searchable = *searchable
	id := names.DeriveNameID(namespace, normalized)
	record := &names.NameRecord{
		Version: names.FormatVersion, Namespace: namespace[:], NameID: id[:],
		Path: normalized, Kind: "directory", Owner: privateKey.Public().(ed25519.PublicKey),
		Policy: policy, Timestamp: time.Now().UnixNano(), Nonce: randomNonce(),
	}
	if err := record.Sign(privateKey); err != nil {
		return err
	}
	raw, err := record.Marshal()
	if err != nil {
		return err
	}
	c := &client{base: strings.TrimRight(*api, "/"), http: &http.Client{Timeout: *timeout}}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var response any
	if err := c.json(ctx, http.MethodPost, "/v1/names", map[string]any{"expected_generation": 0, "record_cbor": raw}, &response); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"namespace": namespace.String(), "name_id": id.String(), "path": normalized, "response": response})
}

func runDirectoryList(args []string) error {
	set := flag.NewFlagSet("directory ls", flag.ContinueOnError)
	api, timeout := commonClient(set)
	nameText := set.String("name-id", "", "directory NameID")
	if err := set.Parse(args); err != nil {
		return err
	}
	id, err := names.ParseNameID(*nameText)
	if err != nil {
		return err
	}
	c := &client{base: strings.TrimRight(*api, "/"), http: &http.Client{Timeout: *timeout}}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var response any
	if err := c.json(ctx, http.MethodGet, "/v1/directories/"+id.String(), nil, &response); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(response)
}

func runDirectoryMutation(args []string, link bool) error {
	operation := "unlink"
	if link {
		operation = "link"
	}
	set := flag.NewFlagSet("directory "+operation, flag.ContinueOnError)
	api, timeout := commonClient(set)
	directoryText := set.String("name-id", "", "directory NameID")
	childText := set.String("child-name-id", "", "direct child NameID")
	signingKeyText := set.String("signing-key", "", "Ed25519 directory-owner private key in hex")
	if err := set.Parse(args); err != nil {
		return err
	}
	directoryID, err := names.ParseNameID(*directoryText)
	if err != nil {
		return err
	}
	childID, err := names.ParseNameID(*childText)
	if err != nil {
		return err
	}
	privateKey, err := parseEd25519Private(*signingKeyText)
	if err != nil {
		return err
	}
	c := &client{base: strings.TrimRight(*api, "/"), http: &http.Client{Timeout: *timeout}}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	current, _, err := c.getRecord(ctx, directoryID)
	if err != nil {
		return err
	}
	if current.Tombstone || current.Kind != "directory" {
		return errors.New("--name-id is not a live directory")
	}
	if !bytes.Equal(current.Owner, privateKey.Public().(ed25519.PublicKey)) {
		return errors.New("signing key is not the directory namespace owner")
	}
	if link {
		child, _, childErr := c.getRecord(ctx, childID)
		if childErr != nil {
			return childErr
		}
		if child.Tombstone || !bytes.Equal(child.Namespace, current.Namespace) || pathpkg.Dir(child.Path) != current.Path {
			return errors.New("child must be a live direct path child in the same namespace")
		}
	}
	next := *current
	next.DirectoryChildren = cloneByteSlices(current.DirectoryChildren)
	found := -1
	for index, existing := range next.DirectoryChildren {
		if bytes.Equal(existing, childID[:]) {
			found = index
			break
		}
	}
	if link {
		if found >= 0 {
			return errors.New("child is already linked")
		}
		next.DirectoryChildren = append(next.DirectoryChildren, append([]byte(nil), childID[:]...))
		sort.Slice(next.DirectoryChildren, func(i, j int) bool { return bytes.Compare(next.DirectoryChildren[i], next.DirectoryChildren[j]) < 0 })
	} else {
		if found < 0 {
			return errors.New("child is not linked")
		}
		next.DirectoryChildren = append(next.DirectoryChildren[:found], next.DirectoryChildren[found+1:]...)
	}
	previousHash, err := current.Hash()
	if err != nil {
		return err
	}
	next.Generation = current.Generation + 1
	next.PreviousHash = previousHash[:]
	next.Timestamp = time.Now().UnixNano()
	next.Nonce = randomNonce()
	next.Signature = nil
	next.Capability = nil
	if err := next.Sign(privateKey); err != nil {
		return err
	}
	raw, err := next.Marshal()
	if err != nil {
		return err
	}
	var response any
	if err := c.json(ctx, http.MethodPut, "/v1/names/"+directoryID.String(), map[string]any{"expected_generation": current.Generation, "record_cbor": raw}, &response); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(response)
}

func randomNonce() []byte {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	return nonce
}

func cloneByteSlices(values [][]byte) [][]byte {
	cloned := make([][]byte, len(values))
	for index := range values {
		cloned[index] = append([]byte(nil), values[index]...)
	}
	return cloned
}

func (c *client) putBlock(ctx context.Context, data []byte) (names.ContentKey, error) {
	var out names.ContentKey
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/put", bytes.NewReader(data))
	if err != nil {
		return out, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := c.http.Do(request)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return out, responseError(response)
	}
	var decoded struct {
		Key string `json:"multihash_hex"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return out, err
	}
	raw, err := decodeHex32("returned block key", decoded.Key)
	if err != nil {
		return out, err
	}
	copy(out[:], raw)
	return out, nil
}

func (c *client) getBlock(ctx context.Context, key []byte) ([]byte, error) {
	body, _ := json.Marshal(map[string]string{"key": hex.EncodeToString(key), "timeout": "2m"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/get?format=raw", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/octet-stream")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return nil, responseError(response)
	}
	return io.ReadAll(response.Body)
}

func (c *client) getRecord(ctx context.Context, id names.NameID) (*names.NameRecord, []byte, error) {
	var response struct {
		Record     *names.NameRecord `json:"record"`
		RecordCBOR []byte            `json:"record_cbor"`
	}
	if err := c.json(ctx, http.MethodGet, "/v1/names/"+id.String(), nil, &response); err != nil {
		return nil, nil, err
	}
	record, err := names.DecodeNameRecord(response.RecordCBOR)
	if err != nil {
		return nil, nil, err
	}
	return record, response.RecordCBOR, nil
}

func (c *client) waitForPublication(ctx context.Context, recordRaw []byte) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	lastDetail := "signed provider claims have not converged"
	for {
		var status struct {
			Ready  bool   `json:"ready"`
			Detail string `json:"detail"`
		}
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, 25*time.Second)
		err := c.json(attemptCtx, http.MethodPost, "/v1/names/preflight", map[string]any{"record_cbor": recordRaw}, &status)
		cancelAttempt()
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("strict publication preflight did not become ready (%s): %w", lastDetail, ctx.Err())
			}
			lastDetail = err.Error()
		} else if status.Ready {
			return nil
		}
		if status.Detail != "" {
			lastDetail = status.Detail
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("strict publication preflight did not become ready (%s): %w", lastDetail, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (c *client) json(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+endpoint, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return responseError(response)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func responseError(response *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
	return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
}

func parseEd25519Private(value string) (ed25519.PrivateKey, error) {
	raw, err := hex.DecodeString(value)
	if err != nil {
		return nil, err
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	default:
		return nil, errors.New("Ed25519 private key must be a 32-byte seed or 64-byte key")
	}
}
func decodeHex32(label, value string) ([]byte, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("%s must be 64 hexadecimal characters", label)
	}
	return raw, nil
}
func appendUnique(values [][]byte, value []byte) [][]byte {
	for _, existing := range values {
		if bytes.Equal(existing, value) {
			return values
		}
	}
	return append(values, value)
}
func openInput(path string) (io.Reader, func(), error) {
	if path == "-" {
		return os.Stdin, func() {}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}
func openOutput(path string) (io.Writer, func(), error) {
	if path == "-" {
		return os.Stdout, func() {}, nil
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}
