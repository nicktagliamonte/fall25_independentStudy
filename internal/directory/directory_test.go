// Purpose: Unit tests for directory blocks and path resolution.

package directory

import (
	"fmt"
	"sync"
	"testing"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	d := New()
	if err := d.AddLink("a", storage.KeyFromData([]byte("child-a-bytes")).String()); err != nil {
		t.Fatal(err)
	}
	raw, err := d.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != Kind {
		t.Fatalf("kind: %q", got.Kind)
	}
	if got.Entries["a"] != d.Entries["a"] {
		t.Fatalf("entries mismatch")
	}
}

func TestDecodeAcceptsPreRenameDirectoryKind(t *testing.T) {
	got, err := Decode([]byte(`{"kind":"vnipfs-directory-v1","entries":{}}`))
	if err != nil {
		t.Fatalf("Decode legacy directory: %v", err)
	}
	if got.Kind != legacyKind {
		t.Fatalf("kind: got %q, want %q", got.Kind, legacyKind)
	}
}

func TestAddLinkValidate(t *testing.T) {
	d := New()
	if err := d.AddLink("bad/name", storage.KeyFromData([]byte("x")).String()); err == nil {
		t.Fatal("expected error for slash in name")
	}
}

func TestListSorted(t *testing.T) {
	d := New()
	_ = d.AddLink("z", storage.KeyFromData([]byte("1")).String())
	_ = d.AddLink("a", storage.KeyFromData([]byte("2")).String())
	_ = d.AddLink("m", storage.KeyFromData([]byte("3")).String())
	names := d.List()
	if len(names) != 3 || names[0] != "a" || names[1] != "m" || names[2] != "z" {
		t.Fatalf("got %v", names)
	}
}

func TestCopyOnWriteUnlink(t *testing.T) {
	kChild := storage.KeyFromData([]byte("blob")).String()
	d := New()
	_ = d.AddLink("x", kChild)
	d2 := d.Clone()
	_ = d2.RemoveLink("x")
	if _, ok := d.Entries["x"]; !ok {
		t.Fatal("original mutated")
	}
	if _, ok := d2.Entries["x"]; ok {
		t.Fatal("clone should drop x")
	}
}

func TestResolvePathNested(t *testing.T) {
	leaf := storage.KeyFromData([]byte("leaf-data")).String()
	mid := New()
	_ = mid.AddLink("b", leaf)
	midRaw, err := mid.Encode()
	if err != nil {
		t.Fatal(err)
	}
	midKey := storage.KeyFromData(midRaw)

	root := New()
	_ = root.AddLink("a", midKey.String())
	rootRaw, err := root.Encode()
	if err != nil {
		t.Fatal(err)
	}
	rootKey := storage.KeyFromData(rootRaw)

	store := map[storage.Key][]byte{
		rootKey: rootRaw,
		midKey:  midRaw,
	}
	get := func(k storage.Key) ([]byte, error) {
		b, ok := store[k]
		if !ok {
			return nil, fmt.Errorf("missing %s", k.String())
		}
		return b, nil
	}

	got, err := ResolvePath(rootKey, "a/b", get)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != leaf {
		t.Fatalf("got %s want %s", got.String(), leaf)
	}
}

func TestResolvePathEmptyReturnsRoot(t *testing.T) {
	root := storage.KeyFromData([]byte("root-bytes"))
	get := func(k storage.Key) ([]byte, error) { return nil, fmt.Errorf("no") }
	got, err := ResolvePath(root, "", get)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(root) {
		t.Fatalf("want root")
	}
}

func TestResolvePathRejectsDotDot(t *testing.T) {
	root := storage.KeyFromData([]byte("r"))
	_, err := ResolvePath(root, "..", func(storage.Key) ([]byte, error) { return nil, nil })
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestConcurrentReadersList(t *testing.T) {
	d := New()
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("n%02d", i)
		_ = d.AddLink(name, storage.KeyFromData([]byte(name)).String())
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_ = d.List()
			}
		}()
	}
	wg.Wait()
}
