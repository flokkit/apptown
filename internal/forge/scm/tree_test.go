package scm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The forge's own writes and reads inside a tree never pass through a link
// the tree holds, at the end or on the way, and never leave the tree.
func TestTreeWritesAndReadsNeverFollowALink(t *testing.T) {
	t.Parallel()
	root, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "host-file")
	if err := os.WriteFile(secret, []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"dangling": filepath.Join(outside, "made"), "existing": secret, "dir": outside} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	var unsafe *UnsafePath
	if created, err := CreateInTree(root, "dangling", []byte("x"), 0o644); err != nil || created {
		t.Fatalf("create over a dangling link = %v %v", created, err)
	}
	if _, err := CreateInTree(root, "dir/new", []byte("x"), 0o644); !errors.As(err, &unsafe) {
		t.Fatalf("create under a linked directory = %v", err)
	}
	if _, err := CreateInTree(root, "../escape", []byte("x"), 0o644); !errors.As(err, &unsafe) {
		t.Fatalf("create outside the tree = %v", err)
	}
	if _, err := UpdateInTree(root, "existing", func([]byte) []byte { return []byte("changed") }); !errors.As(err, &unsafe) {
		t.Fatalf("update through a link = %v", err)
	}
	if err := WriteInTree(root, "existing", []byte("changed"), 0o644); !errors.As(err, &unsafe) {
		t.Fatalf("write through a link = %v", err)
	}
	if _, err := ReadInTree(root, "existing"); !errors.As(err, &unsafe) {
		t.Fatalf("read through a link = %v", err)
	}
	if _, err := ReadInTree(root, "dir/host-file"); !errors.As(err, &unsafe) {
		t.Fatalf("read under a linked directory = %v", err)
	}
	if b, _ := os.ReadFile(secret); string(b) != "host" {
		t.Fatalf("the host file changed: %q", b)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Fatalf("something was made outside: %v", entries)
	}
	// Ordinary files still work.
	if err := WriteInTree(root, "a/b.txt", []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteInTree(root, "a/b.txt", []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadInTree(root, "a/b.txt"); err != nil || string(b) != "two" {
		t.Fatalf("read = %q %v", b, err)
	}
	if _, err := ReadInTree(root, "a/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing file = %v", err)
	}
}
