package durable

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesAnExistingFileWithTheModeAsked(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	existed, err := Write(dir, "f", []byte("new"), 0o644, false)
	if err != nil || existed {
		t.Fatalf("Write = %v, %v; want false, nil", existed, err)
	}
	assertOnly(t, dir, "f", "new", 0o644)
}

func TestWriteNoReplaceKeepsAnExistingFileAndReportsIt(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, "f", []byte("first"), 0o600, true); err != nil {
		t.Fatal(err)
	}
	existed, err := Write(dir, "f", []byte("second"), 0o600, true)
	if err != nil || !existed {
		t.Fatalf("second Write = %v, %v; want true, nil", existed, err)
	}
	assertOnly(t, dir, "f", "first", 0o600)
}

func TestWriteSurfacesAFailedDirectorySync(t *testing.T) {
	want := errors.New("sync failed")
	previous := syncDir
	syncDir = func(string) error { return want }
	t.Cleanup(func() { syncDir = previous })
	dir := t.TempDir()
	if _, err := Write(dir, "f", []byte("x"), 0o600, false); !errors.Is(err, want) {
		t.Fatalf("Write error = %v, want %v", err, want)
	}
}

func TestWriteIntoAMissingDirectoryFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if _, err := Write(dir, "f", []byte("x"), 0o600, false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Write error = %v, want not-exist", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Write created %s", dir)
	}
}

// assertOnly checks dir holds just name, with body and perm: no temporary
// file is left behind.
func assertOnly(t *testing.T, dir, name, body string, perm fs.FileMode) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		t.Fatalf("dir holds %v, want only %s", entries, name)
	}
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(got) != body {
		t.Fatalf("%s = %q, %v; want %q", name, got, err, body)
	}
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != perm {
		t.Fatalf("%s mode = %v, want %v", name, info.Mode().Perm(), perm)
	}
}
