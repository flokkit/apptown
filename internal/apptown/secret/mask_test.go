package secret

import (
	"os"
	"path/filepath"
	"testing"
)

// Mask replaces every stored value eight characters or longer, longest
// first, and leaves shorter values (a port, "true") alone.
func TestMaskReplacesStoredValuesLongestFirst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("TOKEN=abcd1234efgh\nLONGER=abcd1234efgh-and-more\nPORT=5432\n"), 0o600); err != nil { // gitleaks:allow (test fixture, not a credential)
		t.Fatal(err)
	}
	s := NewService(nil, Config{DataDir: dir})
	got := s.Masker(t.Context())("a=abcd1234efgh b=abcd1234efgh-and-more port 5432")
	if got != "a=[secret] b=[secret] port 5432" {
		t.Fatalf("masked: %q", got)
	}
}

// A value typed into the built-in store is masked like any other: the
// store's current values are what masking reads, not only the secrets
// file (review CT-C03). An older version, replaced, is not masked.
func TestMaskCoversTheBuiltInStore(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := NewService(db, Config{DataDir: t.TempDir()})
	if _, err := s.Vault.Put(t.Context(), "deployment:demo/main", "API_KEY", "old-value-123456", "bls"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Vault.Put(t.Context(), "deployment:demo/main", "API_KEY", "typed-in-value-98765", "bls"); err != nil {
		t.Fatal(err)
	}
	if got := s.Masker(t.Context())("echo typed-in-value-98765; was old-value-123456"); got != "echo [secret]; was old-value-123456" {
		t.Fatalf("masked: %q", got)
	}
}

// One masker is one read of the stores: masking many strings with it reads
// them once, so a value stored afterwards is not seen until the next
// operation's masker (review CT-C06).
func TestAMaskerReadsTheStoresOnce(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := NewService(db, Config{DataDir: t.TempDir()})
	if _, err := s.Vault.Put(t.Context(), "deployment:demo/main", "A", "first-value-123456", "bls"); err != nil {
		t.Fatal(err)
	}
	mask := s.Masker(t.Context())
	if _, err := s.Vault.Put(t.Context(), "deployment:demo/main", "B", "later-value-654321", "bls"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if got := mask("first-value-123456 later-value-654321"); got != "[secret] later-value-654321" {
			t.Fatalf("masked with the snapshot: %q", got)
		}
	}
	if got := s.Masker(t.Context())("later-value-654321"); got != "[secret]" {
		t.Fatalf("a new masker reads the stores again: %q", got)
	}
}
