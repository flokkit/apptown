package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
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

// partialMask is a Mask of the given secrets file.
func partialMask(t *testing.T, env string) Mask {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewService(nil, Config{DataDir: dir}).Mask(t.Context())
}

// Stores for the Partial tests: values use only upper-case letters (one
// of them multibyte), digits and spaces, so any of their characters in
// what is sent is a leak. One store has a value with spaces in it, which a word does not
// bound.
const (
	wordValues   = "TOKEN=QX7ZK9WV2M\nWIDE=ÉTÉ42XQ7ZZ\nLONGER=QX7ZK9WV2M88AB\nPORT=5432\n"                       // gitleaks:allow (test fixture, not a credential)
	spacedValues = "TOKEN=QX7ZK9WV2M\nWIDE=ÉTÉ42XQ7ZZ\nLONGER=QX7ZK9WV2M88AB\nPHRASE=CORRECT HORSE 42 BATTERY\n" // gitleaks:allow (test fixture, not a credential)
)

// What a message still being written withholds does not depend on the
// values: an ending that is the start of a value and an ordinary ending of
// the same length are sent alike, so a reader cannot test guesses.
//
// Kills: hiding an ending only when it starts a value.
func TestPartialSendsTheSameWhetherOrNotTheEndingStartsAValue(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ env, value, other string }{
		"word":   {wordValues, "QX7ZK9WV2", "abcdefghi"},
		"spaced": {spacedValues, "CORRECT HORSE 42 BATTER", "abcdefg hijkl mn opqrst"},
	} {
		m := partialMask(t, c.env)
		for n := 1; n <= len(c.value); n++ {
			secretEnd, ok1 := m.Partial("the key is " + c.value[:n])
			plainEnd, ok2 := m.Partial("the key is " + c.other[:n])
			if !ok1 || !ok2 || secretEnd != plainEnd {
				t.Errorf("%s, %d characters: %q against %q", name, n, secretEnd, plainEnd)
			}
		}
	}
}

// No character of a value is sent at any point while it is being written,
// whichever store: a value with spaces is withheld across its spaces, and
// a whole value reads "[secret]".
//
// Kills: showing the start of a value; cutting at the end of the text, not
// its last word; bounding a value with spaces by a word.
func TestPartialNeverSendsAnyPartOfAValue(t *testing.T) {
	t.Parallel()
	words := "first QX7ZK9WV2M then QX7ZK9WV2M88AB, café\u00a0and ünïcode ÉTÉ42XQ7ZZ then\u00a0QX7ZK9WV2M."
	for name, c := range map[string]struct{ env, passage string }{
		"word":   {wordValues, words},
		"spaced": {spacedValues, words + " a phrase CORRECT HORSE 42 BATTERY and CORRECT HORSE 42 BATTERY again"},
	} {
		m, passage := partialMask(t, c.env), c.passage
		for n := 0; n <= len(passage); n++ {
			got, ok := m.Partial(passage[:n])
			if !ok {
				t.Fatalf("%s: undecided", name)
			}
			if !utf8.ValidString(got) {
				t.Errorf("%s, %q: sent a broken character: %q", name, passage[:n], got)
			}
			if i := strings.IndexFunc(strings.ReplaceAll(got, "[secret]", ""), func(r rune) bool { return unicode.IsUpper(r) || unicode.IsDigit(r) }); i >= 0 {
				t.Errorf("%s, %q: sent %q", name, passage[:n], got)
			}
		}
	}
	// The finished words are all sent, values masked.
	if got, _ := partialMask(t, wordValues).Partial("a QX7ZK9WV2M88AB and QX7ZK9WV2M then "); got != "a [secret] and [secret] then " {
		t.Errorf("finished words: %q", got)
	}
}

// What to hide in a message still being written cannot be decided when a
// store could not be read, so it is not shown at all.
func TestPartialIsUndecidedWhenAStoreCannotBeRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "secrets.env"), 0o700); err != nil { // a folder: the file cannot be read
		t.Fatal(err)
	}
	if got, ok := NewService(nil, Config{DataDir: dir}).Mask(t.Context()).Partial("abcd"); ok || got != "" {
		t.Fatalf("an unreadable store decided %q %v", got, ok)
	}
}
