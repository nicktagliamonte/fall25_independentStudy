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
const Kind = "vnipfs-directory-v1"

// Directory is a Merkle-style folder: content-addressed blob listing name → child object key (64 hex).
// Updates are copy-on-write: mutating produces a new block and thus a new key (UnixFS-like).
type Directory struct {
	Kind    string            `json:"kind"`
	Entries map[string]string `json:"entries"`
}

// New returns an empty directory block.
func New() *Directory {
	return &Directory{
		Kind:    Kind,
		Entries: make(map[string]string),
	}
}

// Decode parses directory JSON from a stored block. Returns error if kind is wrong or payload invalid.
func Decode(data []byte) (*Directory, error) {
	var d Directory
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("directory decode: %w", err)
	}
	if d.Kind != Kind {
		return nil, fmt.Errorf("directory decode: want kind %q, got %q", Kind, d.Kind)
	}
	if d.Entries == nil {
		d.Entries = make(map[string]string)
	}
	return &d, nil
}

// Encode serializes the directory for PutBlock.
func (d *Directory) Encode() ([]byte, error) {
	if d.Kind == "" {
		d.Kind = Kind
	}
	if d.Entries == nil {
		d.Entries = make(map[string]string)
	}
	return json.Marshal(d)
}

// Clone returns a deep copy for copy-on-write edits.
func (d *Directory) Clone() *Directory {
	out := New()
	for k, v := range d.Entries {
		out.Entries[k] = v
	}
	return out
}

// ValidateName rejects path separators and reserved segments.
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
func (d *Directory) RemoveLink(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	delete(d.Entries, name)
	return nil
}

// List returns a sorted copy of names for listing.
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
