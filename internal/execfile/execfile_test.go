package execfile

import (
	"os"
	"path/filepath"
	"testing"
)

// The lock is the point of Write, and it shows only as a rare ETXTBSY on
// Linux under load, which no fast test reproduces. These tests hold what a
// caller relies on besides: Write behaves as os.WriteFile does, and an error
// comes back rather than being lost.

func TestWriteCreatesTheFileWithItsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake")
	if err := Write(path, []byte("#!/bin/sh\necho ran\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v, want 0700", info.Mode().Perm())
	}
	if got, _ := os.ReadFile(path); string(got) != "#!/bin/sh\necho ran\n" {
		t.Fatalf("content %q", got)
	}
}

func TestWriteReplacesAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake")
	if err := Write(path, []byte("#!/bin/sh\necho first and longer\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("#!/bin/sh\necho second\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "#!/bin/sh\necho second\n" {
		t.Fatalf("content %q, want the second script only", got)
	}
}

func TestWriteReturnsTheErrorWhenTheFolderIsMissing(t *testing.T) {
	err := Write(filepath.Join(t.TempDir(), "missing", "fake"), []byte("#!/bin/sh\n"), 0o755)
	if !os.IsNotExist(err) {
		t.Fatalf("err %v, want not-exist", err)
	}
}
