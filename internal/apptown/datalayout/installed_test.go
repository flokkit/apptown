package datalayout

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	root := t.TempDir()
	hub := filepath.Join(root, "workspaces", "project-original")
	if err := os.MkdirAll(hub, 0o700); err != nil {
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

func TestUpgradeKeepsDatabaseEvidenceMainAndUnownedDirectories(t *testing.T) {
	db, root, owner, head := installedProject(t)
	foreign := filepath.Join(root, "workspaces", "project-unowned")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "unowned"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := dbtest.Snapshot(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Upgrade(t.Context(), db, root, root, "instance", Observers{}); err != nil {
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
	if _, err := os.Stat(filepath.Join(root, "workspaces", "project-original")); !os.IsNotExist(err) {
		t.Fatalf("old slug location remains: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(foreign, "unowned"))
	if err != nil || string(raw) != "preserve" {
		t.Fatalf("unowned data changed: %q %v", raw, err)
	}
	if version, err := ReadVersion(root); err != nil || version != Version {
		t.Fatalf("completion marker: %d %v", version, err)
	}
	if err := Upgrade(t.Context(), nil, root, root, "instance", Observers{}); err != nil {
		t.Fatalf("completed layout not idempotent: %v", err)
	}
}

type installedConsoleObserver struct {
	console.Agent
	status console.Status
}

func (o installedConsoleObserver) Status(context.Context, string) (console.Status, error) {
	return o.status, nil
}

func TestSurvivingConsoleSidecarRefusesBeforeLayoutIntent(t *testing.T) {
	db, root, owner, _ := installedProject(t)
	checkout := filepath.Join(root, "console", "checkout-owned")
	recordDir := filepath.Join(root, "console", "chat")
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
	record := filepath.Join(recordDir, console.MetaFile)
	if err := os.WriteFile(record, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	err = Upgrade(t.Context(), db, root, root, "instance", Observers{Local: installedConsoleObserver{status: console.Status{SidecarRunning: true}}})
	if err == nil {
		t.Fatal("absent primary hid surviving sidecar")
	}
	for _, name := range []string{journalName, "repository-layout.json", "layout"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("refusal wrote %s: %v", name, err)
		}
	}
	after, err := os.ReadFile(record)
	if err != nil || string(after) != string(raw) {
		t.Fatal("refusal changed console record")
	}
	if _, err := os.Stat(filepath.Join(root, "workspaces", "project-original")); err != nil {
		t.Fatal("refusal moved source", err)
	}
	if err := Upgrade(t.Context(), db, root, root, "instance", Observers{Local: installedConsoleObserver{}}); err != nil {
		t.Fatal(err)
	}
}

func TestInitializedMissingRepositoryRefusesWithoutCreatingAnEmptyReplacement(t *testing.T) {
	db, root, owner, _ := installedProject(t)
	if err := os.RemoveAll(filepath.Join(root, "workspaces", "project-original")); err != nil {
		t.Fatal(err)
	}
	if err := Upgrade(t.Context(), db, root, root, "instance", Observers{}); err == nil {
		t.Fatal("initialized missing repository accepted")
	}
	for _, name := range []string{journalName, "layout", filepath.Join("workspaces", "project-"+owner.ID.String())} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("refusal created %s: %v", name, err)
		}
	}
}

func TestPublicationRelocationResumesAfterDatabaseFailureWithoutReplayingMovedSources(t *testing.T) {
	db, root, owner, head := installedProject(t)
	hub := filepath.Join(root, "workspaces", "project-original")
	if _, err := gitexec.HubRun(t.Context(), hub, "branch", "work"); err != nil {
		t.Fatal(err)
	}
	publisher := scm.NewBranches(db, &scm.RepositoryLocks{})
	authority, err := scm.ReadAuthorityIn(t.Context(), db, owner.ID, "work")
	if err != nil {
		t.Fatal(err)
	}
	restore := db.FailNextStatement("-- name: SettlePublication")
	_, err = publisher.Publish(t.Context(), scm.Publication{ProjectID: owner.ID, Repository: hub, Branch: "work", Commit: head, Grant: scm.Grant{Generation: authority.Generation}})
	restore()
	if !errors.Is(err, scm.ErrPublicationApplied) {
		t.Fatalf("fixture did not retain an applied publication: %v", err)
	}
	before, err := scm.PublicationLocationsIn(t.Context(), db)
	if err != nil || len(before) != 1 {
		t.Fatalf("pending publication: %+v %v", before, err)
	}
	restore = db.FailNextStatement("-- name: RelocatePublication")
	err = Upgrade(t.Context(), db, root, root, "instance", Observers{})
	restore()
	if err == nil {
		t.Fatal("database relocation failure hidden")
	}
	if _, err := os.Stat(hub); !os.IsNotExist(err) {
		t.Fatalf("fixture did not reach post-move failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "layout")); !os.IsNotExist(err) {
		t.Fatalf("failed transition stamped completion: %v", err)
	}
	if err := Upgrade(t.Context(), db, root, root, "instance", Observers{}); err != nil {
		t.Fatalf("resume replayed stale source fingerprints: %v", err)
	}
	after, err := scm.PublicationLocationsIn(t.Context(), db)
	if err != nil || len(after) != 1 {
		t.Fatalf("publication after migration: %+v %v", after, err)
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
	// AfterRestore has settled and its intent was removed. The original-root
	// input is now unavailable; only the completed journal can finish cleanup.
	j, _, err := readJournal(root, "instance")
	if err != nil {
		t.Fatal(err)
	}
	j.OriginalRoot = "/archived-installation"
	for index := range j.Inventory.References {
		relative, err := filepath.Rel(root, j.Inventory.References[index].Before)
		if err != nil {
			t.Fatal(err)
		}
		j.Inventory.References[index].Before = filepath.Join(j.OriginalRoot, relative)
	}
	for index := range j.Inventory.Publications {
		j.Inventory.Publications[index].Before.Repository = filepath.Join(j.OriginalRoot, "workspaces", "project-original")
	}
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	if err := Upgrade(t.Context(), nil, root, root, "instance", Observers{}); err != nil {
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
