package repositorylayout

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
func content(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s: %q %v; want %q", path, got, err, want)
	}
}
func noJournal(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, journalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight wrote journal: %v", err)
	}
}

func TestUUIDShapedAddressesCanExchangePlacesWithoutAdoption(t *testing.T) {
	root := t.TempDir()
	a := Mapping{ID: idstest.N(1), Slug: idstest.N(2).String()}
	b := Mapping{ID: idstest.N(2), Slug: idstest.N(1).String()}
	oldA, _, newA := paths(root, a)
	oldB, _, newB := paths(root, b)
	put(t, filepath.Join(oldA, "dirty"), "project A")
	put(t, filepath.Join(oldB, "dirty"), "project B")
	m, err := Prepare(root, "instance", []Mapping{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	content(t, filepath.Join(newA, "dirty"), "project A")
	content(t, filepath.Join(newB, "dirty"), "project B")
	m, err = Prepare(root, "instance", []Mapping{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
}

func TestCollisionIsRefusedBeforeAnyJournalOrMove(t *testing.T) {
	root := t.TempDir()
	a := Mapping{ID: idstest.N(1), Slug: "owned"}
	source, _, target := paths(root, a)
	put(t, filepath.Join(source, "dirty"), "owned")
	put(t, filepath.Join(target, "dirty"), "unowned")
	if _, err := Prepare(root, "instance", []Mapping{a}); err == nil {
		t.Fatal("adopted occupied UUID destination")
	}
	noJournal(t, root)
	content(t, filepath.Join(source, "dirty"), "owned")
	content(t, filepath.Join(target, "dirty"), "unowned")
}

func TestInterruptedRenameResumesOnlyTheRecordedContent(t *testing.T) {
	for _, phase := range []string{"staging", "staged", "installing", "installed"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			a := Mapping{ID: idstest.N(1), Slug: "owned"}
			source, stage, target := paths(root, a)
			put(t, filepath.Join(source, "dirty"), "not committed")
			m, err := Prepare(root, "instance", []Mapping{a})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, stagingName), 0o700); err != nil {
				t.Fatal(err)
			}
			m.record.Moves[0].Phase = "staging"
			if err := m.save(); err != nil {
				t.Fatal(err)
			}
			if err := moveChecked(source, stage, m.record.Moves[0].Digest); err != nil {
				t.Fatal(err)
			}
			if phase != "staging" {
				m.record.Moves[0].Phase = phase
				if err := m.save(); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "installing" || phase == "installed" {
				if err := moveChecked(stage, target, m.record.Moves[0].Digest); err != nil {
					t.Fatal(err)
				}
			}
			resumed, err := Prepare(root, "instance", []Mapping{a})
			if err != nil {
				t.Fatal(err)
			}
			if err := resumed.Install(); err != nil {
				t.Fatal(err)
			}
			content(t, filepath.Join(target, "dirty"), "not committed")
		})
	}
}

func TestChangedSourceAndForeignResumeDestinationAreRefused(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "source changed", true: "foreign staged content"}[foreign], func(t *testing.T) {
			root := t.TempDir()
			a := Mapping{ID: idstest.N(1), Slug: "owned"}
			source, stage, _ := paths(root, a)
			put(t, filepath.Join(source, "dirty"), "owned")
			m, err := Prepare(root, "instance", []Mapping{a})
			if err != nil {
				t.Fatal(err)
			}
			if foreign {
				if err := os.Rename(source, source+"-preserved"); err != nil {
					t.Fatal(err)
				}
				put(t, filepath.Join(stage, "dirty"), "foreign")
			} else {
				put(t, filepath.Join(source, "dirty"), "new external write")
			}
			if err := m.Install(); err == nil {
				t.Fatal("moved content that was not inventoried")
			}
		})
	}
}

func TestMissingRepositoryNeedsExplicitLifecycleEvidence(t *testing.T) {
	root := t.TempDir()
	a := Mapping{ID: idstest.N(1), Slug: "owned"}
	if _, err := Prepare(root, "instance", []Mapping{a}); err == nil {
		t.Fatal("missing initialized repository accepted")
	}
	noJournal(t, root)
	a.Missing = true
	m, err := Prepare(root, "instance", []Mapping{a})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	_, _, target := paths(root, a)
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created empty replacement: %v", err)
	}
}

func TestJournalCannotBeResumedWithDifferentOwnersOrInstallation(t *testing.T) {
	root := t.TempDir()
	a := Mapping{ID: idstest.N(1), Slug: "owned", Missing: true}
	if _, err := Prepare(root, "instance", []Mapping{a}); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(root, "another-instance", []Mapping{a}); err == nil {
		t.Fatal("adopted another installation's journal")
	}
	a.Slug = "renamed"
	if _, err := Prepare(root, "instance", []Mapping{a}); err == nil {
		t.Fatal("changed pending mapping")
	}
}

func TestRootSymlinkRefusedAndWorkingFileSymlinkPreserved(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		root := t.TempDir()
		a := Mapping{ID: idstest.N(1), Slug: "owned"}
		source, _, _ := paths(root, a)
		if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), source); err != nil {
			t.Fatal(err)
		}
		if _, err := Prepare(root, "instance", []Mapping{a}); err == nil {
			t.Fatal("followed repository root symlink")
		}
		noJournal(t, root)
	})
	t.Run("file", func(t *testing.T) {
		root := t.TempDir()
		a := Mapping{ID: idstest.N(1), Slug: "owned"}
		source, _, target := paths(root, a)
		put(t, filepath.Join(source, "dirty"), "owned")
		if err := os.Symlink("dirty", filepath.Join(source, "link")); err != nil {
			t.Fatal(err)
		}
		m, err := Prepare(root, "instance", []Mapping{a})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Install(); err != nil {
			t.Fatal(err)
		}
		if link, err := os.Readlink(filepath.Join(target, "link")); err != nil || link != "dirty" {
			t.Fatalf("lost link %q %v", link, err)
		}
	})
}

func TestUUIDArchiveNeverReAdoptsAnAddressNamedDirectory(t *testing.T) {
	root := t.TempDir()
	owner := idstest.N(1)
	uuidPath := filepath.Join(root, "workspaces", "project-"+owner.String())
	addressPath := filepath.Join(root, "workspaces", "project-old-address")
	put(t, filepath.Join(uuidPath, "owned"), "correct UUID repository")
	put(t, filepath.Join(addressPath, "owned"), "unowned old address")
	mapping := Mapping{ID: owner, Slug: "old-address", SourceUUID: true}
	moves, err := Prepare(root, "instance", []Mapping{mapping})
	if err != nil {
		t.Fatal(err)
	}
	if err := moves.Install(); err != nil {
		t.Fatal(err)
	}
	content(t, filepath.Join(uuidPath, "owned"), "correct UUID repository")
	content(t, filepath.Join(addressPath, "owned"), "unowned old address")
	ref := mapping.Reference("/original", "/restored")
	if ref.Before != filepath.Join("/original", "workspaces", "project-"+owner.String()) || ref.After != filepath.Join("/restored", "workspaces", "project-"+owner.String()) {
		t.Fatalf("UUID archive reference fell back to address: %+v", ref)
	}
}

func TestReadOnlyMoveCheckLeavesSourceAndControlFilesUntouched(t *testing.T) {
	root := t.TempDir()
	mapping := Mapping{ID: idstest.N(1), Slug: "original"}
	source, stage, target := paths(root, mapping)
	put(t, filepath.Join(source, "dirty"), "preserve")
	if err := Check(root, "instance", []Mapping{mapping}); err != nil {
		t.Fatal(err)
	}
	noJournal(t, root)
	content(t, filepath.Join(source, "dirty"), "preserve")
	for _, path := range []string{stage, target} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only check created %s: %v", path, err)
		}
	}
}
