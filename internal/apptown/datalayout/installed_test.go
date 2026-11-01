package datalayout

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
)

var installedDatabases = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) { os.Exit(installedDatabases.Run(m)) }

func installedProject(t *testing.T) (*database.DB, string, project.Project, string) {
	t.Helper()
	db := installedDatabases.New(t)
	id, err := database.Transact(t.Context(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "original"})
	})
	if err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, nil)
	if err := projects.SetDefaultBranch(t.Context(), id, "main"); err != nil {
		t.Fatal(err)
	}
	owner, err := projects.ByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "original")
	hub := filepath.Join(root, "workspaces", "project-"+id.String())
	if err := os.MkdirAll(hub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "layout"), []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := gitexec.HubRun(t.Context(), hub, args...)
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return out
	}
	git("init", "--quiet", "--template=", "-b", "main")
	git("config", "user.name", gitexec.FolderUserName)
	git("config", "user.email", gitexec.FolderUserEmail)
	if err := os.WriteFile(filepath.Join(hub, "file"), []byte("installed content"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "file")
	git("commit", "--quiet", "-m", "installed main")
	if err := scm.RecordMain(t.Context(), db, hub, id, "main", scm.ViaInit, time.Now()); err != nil {
		t.Fatal(err)
	}
	return db, root, owner, git("rev-parse", "HEAD")
}

// restoredInto moves an installation's data directory to another path, as a
// whole-AppTown restore into a different destination leaves it.
func restoredInto(t *testing.T, original string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "restored")
	if err := os.Rename(original, root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRelocationKeepsDatabaseEvidenceAndMain(t *testing.T) {
	db, original, owner, head := installedProject(t)
	root := restoredInto(t, original)
	before, err := dbtest.Snapshot(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Relocate(t.Context(), db, root, original, "instance", Observers{}); err != nil {
		t.Fatal(err)
	}
	after, err := dbtest.Snapshot(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("filesystem relocation rewrote durable database evidence")
	}
	target := filepath.Join(root, "workspaces", "project-"+owner.ID.String())
	got, err := gitexec.HubRun(t.Context(), target, "rev-parse", "HEAD")
	if err != nil || got != head {
		t.Fatalf("main changed: %s %v", got, err)
	}
	if version, err := readVersion(root); err != nil || version != Version {
		t.Fatalf("layout marker: %d %v", version, err)
	}
	if err := Relocate(t.Context(), nil, root, original, "instance", Observers{}); err != nil {
		t.Fatalf("completed relocation not idempotent: %v", err)
	}
	if err := Finalize(root, "instance"); err != nil {
		t.Fatal(err)
	}
	if err := Relocate(t.Context(), nil, root, root, "instance", Observers{}); err != nil {
		t.Fatalf("an ordinary start did work: %v", err)
	}
}

type installedConsoleObserver struct {
	console.Agent
	status console.Status
}

func (o installedConsoleObserver) Status(context.Context, string) (console.Status, error) {
	return o.status, nil
}

func TestSurvivingConsoleSidecarRefusesBeforeRelocationIntent(t *testing.T) {
	db, original, owner, _ := installedProject(t)
	checkout := filepath.Join(original, "console", "checkout-owned")
	recordDir := filepath.Join(original, "console", "chat")
	for _, path := range []string{checkout, recordDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	meta := console.Meta{ID: "chat", Scope: conversation.Key(owner.ID, ids.Nil), Labels: conversation.LabelsFor(&owner, nil), Checkout: checkout, OwnsCheckout: true, LastUsed: time.Now()}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recordDir, console.MetaFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	root := restoredInto(t, original)
	record := filepath.Join(root, "console", "chat", console.MetaFile)
	err = Relocate(t.Context(), db, root, original, "instance", Observers{Local: installedConsoleObserver{status: console.Status{SidecarRunning: true}}})
	if err == nil {
		t.Fatal("absent primary hid surviving sidecar")
	}
	if _, err := os.Lstat(filepath.Join(root, journalName)); !os.IsNotExist(err) {
		t.Fatalf("refusal wrote its journal: %v", err)
	}
	after, err := os.ReadFile(record)
	if err != nil || string(after) != string(raw) {
		t.Fatal("refusal changed console record")
	}
	if err := Relocate(t.Context(), db, root, original, "instance", Observers{Local: installedConsoleObserver{}}); err != nil {
		t.Fatal(err)
	}
}

func TestInitializedMissingRepositoryRefusesWithoutCreatingAnEmptyReplacement(t *testing.T) {
	db, original, owner, _ := installedProject(t)
	root := restoredInto(t, original)
	if err := os.RemoveAll(filepath.Join(root, "workspaces", "project-"+owner.ID.String())); err != nil {
		t.Fatal(err)
	}
	if err := Relocate(t.Context(), db, root, original, "instance", Observers{}); err == nil {
		t.Fatal("initialized missing repository accepted")
	}
	for _, name := range []string{journalName, filepath.Join("workspaces", "project-"+owner.ID.String())} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("refusal created %s: %v", name, err)
		}
	}
}

func TestPublicationRelocationResumesAfterDatabaseFailure(t *testing.T) {
	db, original, owner, head := installedProject(t)
	hub := filepath.Join(original, "workspaces", "project-"+owner.ID.String())
	if _, err := gitexec.HubRun(t.Context(), hub, "branch", "work"); err != nil {
		t.Fatal(err)
	}
	publisher := scm.NewBranches(db, &scm.RepositoryLocks{})
	authority, err := scm.ReadAuthorityIn(t.Context(), db, owner.ID, "work")
	if err != nil {
		t.Fatal(err)
	}
	restore := dbtest.NextStatementFailure(db, "-- name: SettlePublication")
	_, err = publisher.Publish(t.Context(), scm.Publication{ProjectID: owner.ID, Repository: hub, Branch: "work", Commit: head, Grant: scm.Grant{Generation: authority.Generation}})
	restore()
	if !errors.Is(err, scm.ErrPublicationApplied) {
		t.Fatalf("fixture did not retain an applied publication: %v", err)
	}
	before, err := scm.PublicationLocationsIn(t.Context(), db)
	if err != nil || len(before) != 1 {
		t.Fatalf("pending publication: %+v %v", before, err)
	}
	root := restoredInto(t, original)
	restore = dbtest.NextStatementFailure(db, "-- name: RelocatePublication")
	err = Relocate(t.Context(), db, root, original, "instance", Observers{})
	restore()
	if err == nil {
		t.Fatal("database relocation failure hidden")
	}
	if err := Relocate(t.Context(), db, root, original, "instance", Observers{}); err != nil {
		t.Fatalf("relocation did not resume: %v", err)
	}
	after, err := scm.PublicationLocationsIn(t.Context(), db)
	if err != nil || len(after) != 1 {
		t.Fatalf("publication after relocation: %+v %v", after, err)
	}
	expected := before[0]
	expected.Repository = filepath.Join(root, "workspaces", "project-"+owner.ID.String())
	if after[0] != expected {
		t.Fatalf("publication tuple changed beyond path: %+v want %+v", after[0], expected)
	}
	result, err := publisher.Recover(t.Context())
	if err != nil || result.Settled != 1 || result.Unknown != 0 {
		t.Fatalf("recovery did not use relocated hub: %+v %v", result, err)
	}
	if err := RequireCurrent(root); err == nil {
		t.Fatal("unlocked command accepted pending completion journal")
	}
	// AfterRestore has settled and its intent was removed: a restart passes
	// this root as the original one; only the completed journal remains.
	if err := Relocate(t.Context(), nil, root, root, "instance", Observers{}); err != nil {
		t.Fatalf("complete journal could not finish after restore intent removal: %v", err)
	}
	if err := Finalize(root, "instance"); err != nil {
		t.Fatal(err)
	}
	if err := RequireCurrent(root); err != nil {
		t.Fatal(err)
	}
	if _, found, err := readJournal(root, "instance"); err != nil || found {
		t.Fatalf("completion journal retained: %v %v", found, err)
	}
}

// AppTown makes standalone clones; linked or borrowed Git metadata predates
// layout 2 and is refused before any relocation intent, not converted.
func TestLinkedOrBorrowedWorkspaceMetadataRefusesBeforeRelocationIntent(t *testing.T) {
	for _, tc := range []struct{ name, file string }{
		{"linked", ".git"},
		{"borrowed", filepath.Join(".git", "objects", "info", "alternates")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, original, owner, _ := installedProject(t)
			checkout := filepath.Join(original, "console", "checkout-owned")
			metadata := filepath.Join(checkout, tc.file)
			if err := os.MkdirAll(filepath.Dir(metadata), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(metadata, []byte("elsewhere\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			recordDir := filepath.Join(original, "console", "chat")
			if err := os.MkdirAll(recordDir, 0o700); err != nil {
				t.Fatal(err)
			}
			meta := console.Meta{ID: "chat", Scope: conversation.Key(owner.ID, ids.Nil), Labels: conversation.LabelsFor(&owner, nil), Checkout: checkout, OwnsCheckout: true, LastUsed: time.Now()}
			raw, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(recordDir, console.MetaFile), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			root := restoredInto(t, original)
			err = Relocate(t.Context(), db, root, original, "instance", Observers{Local: installedConsoleObserver{}})
			if err == nil || !strings.Contains(err.Error(), "does not relocate") {
				t.Fatalf("pre-layout-2 metadata accepted: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(root, journalName)); !os.IsNotExist(err) {
				t.Fatalf("refusal wrote its journal: %v", err)
			}
		})
	}
}
