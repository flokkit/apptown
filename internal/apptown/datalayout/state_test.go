package datalayout

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
)

// Only layout 2 is read. A directory from before the marker, or marked 1, is
// told which releases upgrade it; a newer one refuses a downgrade. Reading
// never writes.
func TestLayoutReadAcceptsOnlyTheCurrentLayout(t *testing.T) {
	root := t.TempDir()
	if _, err := ReadVersion(root); !errors.Is(err, ErrOldLayout) {
		t.Fatalf("missing marker: %v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("layout read wrote files: %v %v", entries, err)
	}
	for _, tc := range []struct {
		body string
		want error
	}{{"2\n", nil}, {"1\n", ErrOldLayout}, {"3\n", ErrNewerLayout}, {"0\n", errAny}, {"-1\n", errAny}, {"future", errAny}} {
		if err := os.WriteFile(filepath.Join(root, "layout"), []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		version, err := ReadVersion(root)
		switch {
		case tc.want == nil && (err != nil || version != Version):
			t.Fatalf("%q: %d %v", tc.body, version, err)
		case tc.want == errAny && err == nil:
			t.Fatalf("%q accepted", tc.body)
		case tc.want != nil && tc.want != errAny && !errors.Is(err, tc.want):
			t.Fatalf("%q: %v, want %v", tc.body, err, tc.want)
		}
	}
}

var errAny = errors.New("any refusal")

// A directory without a marker is marked layout 2 only when it holds no
// project repository, the one thing layouts place differently; one that
// does is refused and left exactly as it was.
func TestInitializeMarksOnlyADirectoryWithoutProjectRepositories(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "new")
	if err := Initialize(missing); err != nil {
		t.Fatal(err)
	}
	if version, err := ReadVersion(missing); err != nil || version != Version {
		t.Fatalf("new directory: %d %v", version, err)
	}
	seeded := t.TempDir()
	if err := os.WriteFile(filepath.Join(seeded, "secrets.env"), []byte("A=b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(seeded); err != nil {
		t.Fatalf("a directory seeded before its first start: %v", err)
	}
	if version, err := ReadVersion(seeded); err != nil || version != Version {
		t.Fatalf("seeded directory: %d %v", version, err)
	}
	if err := Initialize(seeded); err != nil {
		t.Fatalf("an initialized directory again: %v", err)
	}
	old := t.TempDir()
	if err := os.MkdirAll(filepath.Join(old, "workspaces", "project-original"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(old); !errors.Is(err, ErrOldLayout) {
		t.Fatalf("a layout 1 directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(old, "layout")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused directory was marked: %v", err)
	}
	newer := t.TempDir()
	if err := os.WriteFile(filepath.Join(newer, "layout"), []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(newer); !errors.Is(err, ErrNewerLayout) {
		t.Fatalf("a newer directory: %v", err)
	}
}
func TestLayoutJournalBindsInstallationAndPhase(t *testing.T) {
	root := t.TempDir()
	j := journal{Nonce: strings.Repeat("A", 26), Format: 2, Instance: "instance", Root: root, OriginalRoot: root, Phase: "references"}
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	got, found, err := readJournal(root, "instance")
	if err != nil || !found || got.Phase != "references" {
		t.Fatalf("journal: %+v %v %v", got, found, err)
	}
	if _, _, err := readJournal(root, "another"); err == nil {
		t.Fatal("resumed another installation's journal")
	}
	j.Format = 1
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readJournal(root, "instance"); err == nil {
		t.Fatal("resumed a journal an earlier build left mid-upgrade")
	}
	j.Format = 2
	j.Phase = "unknown"
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readJournal(root, "instance"); err == nil {
		t.Fatal("accepted unknown transition")
	}
}

func TestMissingHubRequiresUninitializedOrIrreversibleDeletionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name           string
		owner          project.RepositoryOwner
		recorded, want bool
	}{
		{"active initialized", project.RepositoryOwner{Lifecycle: project.Active, DefaultBranch: "main"}, true, false},
		{"active receipt without branch field", project.RepositoryOwner{Lifecycle: project.Active}, true, false},
		{"uninitialized", project.RepositoryOwner{Lifecycle: project.Active}, false, true},
		{"interrupted purge", project.RepositoryOwner{Lifecycle: project.Deleted, DefaultBranch: "main"}, true, true},
		{"retained deletion", project.RepositoryOwner{Retained: true}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := missingRepositoryAllowed(tc.owner, tc.recorded); got != tc.want {
				t.Fatalf("missing repository allowed=%v want %v", got, tc.want)
			}
		})
	}
}
