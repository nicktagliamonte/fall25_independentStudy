// Purpose: First-class directory blocks (JSON, content-addressed) for namespace trees.

package directory

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// Kind is the JSON discriminator for directory blocks stored via normal PutBlock (key = SHA256(bytes)).
const Kind = "tarsus-directory-v1"

// legacyKind remains readable so content-addressed directory blocks produced
// before the Tarsus rename do not become inaccessible. New encodings use Kind.
const legacyKind = "vnipfs-directory-v1"

// Directory is a Merkle-style folder: content-addressed blob listing name → child object key (64 hex).
// Updates are copy-on-write: mutating produces a new block and thus a new key (UnixFS-like).
// A Directory value is itself serialized (via Encode) and stored as an ordinary
// block; its own content key is SHA256 of that serialized form, not a field
// on the struct.
type Directory struct {
	// Kind is the JSON discriminator, always equal to the Kind constant
	// ("tarsus-directory-v1") for a new directory block. Decode also accepts
	// the pre-rename discriminator for read compatibility.
	Kind string `json:"kind"`
	// Entries maps single-segment child names (no "/", not "." or "..") to
	// the 64-hex-char content key of the child block (which may itself be
	// another directory or an arbitrary blob).
	Entries map[string]string `json:"entries"`
}

// New returns an empty directory block.
//
// Returns:
//   - (*Directory): a new Directory with Kind set and an empty, non-nil Entries map.
func New() *Directory {
	return &Directory{
		Kind:    Kind,
		Entries: make(map[string]string),
	}
}

// Decode parses directory JSON from a stored block. Returns error if kind is wrong or payload invalid.
//
// Parameters:
//   - data ([]byte): the raw block bytes previously produced by Encode.
//
// Returns:
//   - (*Directory): the decoded directory (Entries is guaranteed non-nil), or nil on error.
//   - (error): non-nil if data is not valid JSON or the "kind" discriminator does not match Kind.
func Decode(data []byte) (*Directory, error) {
	var d Directory
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("directory decode: %w", err)
	}
	if d.Kind != Kind && d.Kind != legacyKind {
		return nil, fmt.Errorf("directory decode: want kind %q, got %q", Kind, d.Kind)
	}
	if d.Entries == nil {
		d.Entries = make(map[string]string)
	}
	return &d, nil
}

// Encode serializes the directory for PutBlock. If Kind or Entries are unset
// (e.g. on a zero-value Directory{}) they are defaulted before marshaling, so
// the resulting bytes always round-trip through Decode.
//
// Returns:
//   - ([]byte): the JSON-encoded directory block, suitable for storage.PutBlock.
//   - (error): non-nil if JSON marshaling fails.
func (d *Directory) Encode() ([]byte, error) {
	if d.Kind == "" {
		d.Kind = Kind
	}
	if d.Entries == nil {
		d.Entries = make(map[string]string)
	}
	return json.Marshal(d)
}

// Clone returns a deep copy for copy-on-write edits. Callers should mutate the
// clone (via AddLink/RemoveLink) and Encode/PutBlock it as a new block,
// leaving the original Directory (and its underlying stored block) untouched.
//
// Returns:
//   - (*Directory): a new Directory with an independent copy of Entries.
func (d *Directory) Clone() *Directory {
	out := New()
	for k, v := range d.Entries {
		out.Entries[k] = v
	}
	return out
}

// ValidateName rejects path separators and reserved segments.
//
// Parameters:
//   - name (string): a candidate single-segment directory entry name.
//
// Returns:
//   - (error): non-nil if name is empty, ".", "..", or contains "/" or a NUL byte.
func ValidateName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid name %q", name)
	}
	if strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("invalid name %q", name)
	}
	return nil
}

// AddLink adds or replaces a child name → target key (64 hex).
//
// Parameters:
//   - name (string): the single-segment entry name to add or overwrite; validated via ValidateName.
//   - keyHex (string): the 64-hex-char content key of the child block; validated via storage.ParseKey.
//
// Returns:
//   - (error): non-nil if name or keyHex fail validation; d.Entries is unchanged in that case.
func (d *Directory) AddLink(name, keyHex string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := storage.ParseKey(keyHex); err != nil {
		return fmt.Errorf("target key: %w", err)
	}
	d.Entries[name] = keyHex
	return nil
}

// RemoveLink deletes a name. Idempotent if missing.
//
// Parameters:
//   - name (string): the single-segment entry name to remove; validated via ValidateName.
//
// Returns:
//   - (error): non-nil only if name itself fails validation; a missing entry is not an error.
func (d *Directory) RemoveLink(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	delete(d.Entries, name)
	return nil
}

// List returns a sorted copy of names for listing.
//
// Returns:
//   - ([]string): all entry names in d.Entries, sorted lexicographically.
func (d *Directory) List() []string {
	names := make([]string, 0, len(d.Entries))
	for n := range d.Entries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ResolvePath walks from rootKey following slash-separated relative path. get loads block bytes for a key.
// Empty path returns rootKey. Final segment must exist as an entry name; its value (key) is returned.
// Each intermediate key visited (including rootKey, for a non-empty path) is
// fetched via get and decoded as a Directory, so every segment except the
// last must currently resolve to a directory block.
//
// Parameters:
//   - rootKey (storage.Key): the key to start resolution from.
//   - path (string): a "/"-separated relative path; leading/trailing slashes and "." segments are ignored; ".." is rejected.
//   - get (func(storage.Key) ([]byte, error)): loader for raw block bytes given a key, typically stack.GetBlock.
//
// Returns:
//   - (storage.Key): rootKey if path is empty, otherwise the key stored under the final path segment.
//   - (error): non-nil if any intermediate fetch/decode fails, a segment is missing, or ".." is used.
func ResolvePath(rootKey storage.Key, path string, get func(storage.Key) ([]byte, error)) (storage.Key, error) {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, "/")
	if path == "" {
		return rootKey, nil
	}
	segs := strings.Split(path, "/")
	var cur storage.Key = rootKey
	for _, seg := range segs {
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." {
			return storage.Key{}, fmt.Errorf("resolve: '..' not supported")
		}
		data, err := get(cur)
		if err != nil {
			return storage.Key{}, fmt.Errorf("resolve fetch %s: %w", cur.String(), err)
		}
		dir, err := Decode(data)
		if err != nil {
			return storage.Key{}, fmt.Errorf("resolve at %s: %w", cur.String(), err)
		}
		nextHex, ok := dir.Entries[seg]
		if !ok {
			return storage.Key{}, fmt.Errorf("resolve: %q not found under %s", seg, cur.String())
		}
		next, err := storage.ParseKey(nextHex)
		if err != nil {
			return storage.Key{}, err
		}
		cur = next
	}
	return cur, nil
}
