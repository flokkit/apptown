package datalayout

import (
	"errors"
	"github.com/flokkit/apptown/internal/apptown/project"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayoutReadNeverStampsIncompleteUpgrade(t *testing.T) {
	root := t.TempDir()
	if version, err := ReadVersion(root); err != nil || version != 1 {
		t.Fatalf("missing marker: %d %v", version, err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("layout read wrote files: %v %v", entries, err)
	}
	for _, tc := range []struct {
		body    string
		version int
		refuse  bool
	}{{"1\n", 1, false}, {"2\n", 2, false}, {"0\n", 0, true}, {"-1\n", 0, true}, {"future", 0, true}, {"3\n", 0, true}} {
		if err := os.WriteFile(filepath.Join(root, "layout"), []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		version, err := ReadVersion(root)
		if (err != nil) != tc.refuse || (!tc.refuse && version != tc.version) {
			t.Fatalf("%q: %d %v", tc.body, version, err)
		}
		if tc.body == "3\n" && !errors.Is(err, ErrNewerLayout) {
			t.Fatalf("newer marker did not refuse downgrade: %v", err)
		}
	}
}
func TestLayoutJournalBindsInstallationAndPhase(t *testing.T) {
	root := t.TempDir()
	j := journal{Nonce: strings.Repeat("A", 26), Format: 1, Instance: "instance", Root: root, OriginalRoot: root, FromVersion: 1, Phase: "planned"}
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	got, found, err := readJournal(root, "instance")
	if err != nil || !found || got.Phase != "planned" {
		t.Fatalf("journal: %+v %v %v", got, found, err)
	}
	if _, _, err := readJournal(root, "another"); err == nil {
		t.Fatal("resumed another installation's journal")
	}
	j.Phase = "unknown"
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readJournal(root, "instance"); err == nil {
		t.Fatal("accepted unknown transition")
	}
	if _, err := os.Lstat(filepath.Join(root, "layout")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal stamped completed layout: %v", err)
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
