package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execfile"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executionfake"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
)

// The run's folder after a worker task's result. A result is recorded for
// the folder before it is published; the folder is then brought forward,
// confined. A folder the
// AppTown will not run Git in stays where it is and is behind, and every
// reader of the run's work reads the published commit instead.

// folderRun is a run whose first step, a script on a worker, has made
// made.txt and the native contract and published them (B) on the commit
// the run's folder was at (A). Its other steps (tests, a preview and a
// scan) are left for the test to run.
type folderRun struct {
	a                           *App
	fake                        *executionfake.Tasks
	run                         *workflow.Run
	source                      workflow.Source
	hub, ws                     string
	given, published            string
	scanned, state              string
	make, tests, boot, scanStep ids.ID
}

// publishedFolderRun makes a folderRun for a run on a change or without one.
// With refuse set, the folder is given one inert config key AppTown's
// clone does not write, before the script runs.
func publishedFolderRun(t *testing.T, onChange, refuse bool) folderRun {
	t.Helper()
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><script src="game.js"></script></head></html>`))
	})
	server := httptest.NewServer(page)
	t.Cleanup(server.Close)
	daemon := newFakeDocker()
	daemon.port = server.Listener.Addr().(*net.TCPAddr).Port
	a := testApp(t, Config{DockerClient: daemon, DockerProbe: &docker.Probe{Settle: time.Millisecond}})
	fake := &executionfake.Tasks{}
	a.tasks.Driver, a.tasks.Poll = fake, 20*time.Millisecond
	state := onWorker(t, a, fake)
	// The scan's Docker names the tree it was given.
	dir := t.TempDir()
	scanned := filepath.Join(dir, "scanned")
	scanner := filepath.Join(dir, "docker")
	if err := execfile.Write(scanner, []byte(`#!/bin/sh
case "$*" in inspect*OOMKilled*) echo false; exit 0;; "rm -f "*) exit 0;; esac
prev=""
for arg in "$@"; do
  case "$arg" in *:/work) [ "$prev" = "-v" ] && ls "${arg%:/work}" >> "`+scanned+`";; esac
  prev="$arg"
done
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}
	attachScanner(t, a, "secrets", map[string]any{"image": "gitleaks:test", "command": []string{"detect", "--source", "/work", "--no-git"}, "kind": "secret", "target": "source"})
	a.processRunner = execution.Runner{DockerBinary: scanner}
	a.scanSteps = scanStepsFor(a)
	rebuildWork(t, a, func(*workflow.Dependencies) {})
	projectID := plainTestProject(t, a.DB, "folder-runs", "Folder Runs")
	p := a.projectByID(context.Background(), projectID)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "build", Steps: []schema.Step{
		{Name: "make", Uses: "script", With: map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_OUTPUT": "ran", "FAKE_EXIT": "0", "FAKE_BLOCK": "1"}}},
		{Name: "tests", Uses: "tests"},
		{Name: "boot", Uses: "preview"},
		{Name: "scan", Uses: "scan", With: map[string]any{"scanner": "secrets"}},
	}}}}
	prepared := workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "work", Version: 1}, Spec: spec}
	ctx := a.asForge(context.Background(), projectID, "fixture")
	var run *workflow.Run
	if onChange {
		defineWorkflows(t, a, prepared)
		a.lifecycle.SelectLane(ctx, projectID, "work", true, "fixture")
		ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: projectID, Type: "feature", Title: "folder runs"})
		if err == nil {
			ch, err = a.Work.Begin(ctx, ch.ID)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
			t.Fatal(err)
		}
		a.Work.Stop()
		if run, err = a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "work"}); !errors.Is(err, workflow.ErrDispatchStopped) {
			t.Fatal(err)
		}
	} else {
		a.Work.Stop()
		tx, err := a.DB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if run, err = a.Work.AdmitRunIn(a.asEngine(context.Background()), tx, workflow.StartRequest{ProjectID: projectID, WorkflowName: "work", InputsJSON: `{}`}, prepared); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	w := folderRun{a: a, fake: fake, run: run, source: workflowSourceForTest(t, a, run), scanned: scanned, state: state}
	w.hub, w.ws = w.source.Repository(a.cfg.dataDir), w.source.Workspace(a.cfg.dataDir)
	if !onChange {
		// The run's folder, made by AppTown's own clone as a run's
		// preparation makes it.
		if _, err := (workflow.GitWorkspace{DataDir: a.cfg.dataDir}).EnsureClone(context.Background(), workflow.CloneRequest{Repository: w.hub, Workspace: w.ws, Branch: w.source.Branch, DefaultBranch: "main"}); err != nil {
			t.Fatal(err)
		}
	}
	w.given = gittest.Run(t, w.ws, "rev-parse", "HEAD")
	if refuse {
		gittest.Run(t, w.ws, "config", "review.note", "example")
	}
	snap, err := a.Work.Snapshot(a.asForge(context.Background(), projectID, "review"), run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Steps {
		switch s.Name {
		case "make":
			w.make = s.ID
		case "tests":
			w.tests = s.ID
		case "boot":
			w.boot = s.ID
		case "scan":
			w.scanStep = s.ID
		}
	}
	return w
}

// madeFiles are what the script makes on the worker: a file, and the
// native contract and a page so the run's preview boots.
var madeFiles = map[string]string{"made.txt": "made on the worker\n", ".apptown/app.yaml": "app:\n  web: {static: ., public: true}\n", "index.html": "<html></html>\n"}

// runMake runs the script step's attempt on the worker, where it writes
// files, and waits for its step to settle as want.
func (w folderRun) runMake(t *testing.T, attempt int, files map[string]string, want workflow.StepState) *workflow.Step {
	t.Helper()
	key := fmt.Sprintf("step-%d-attempt-%d", w.make, attempt)
	executeUntilStarted(t, w.a, w.fake, w.make, key)
	remote := filepath.Join(w.state, "workspaces", key)
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(remote, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remote, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.fake.Exit(key, 0)
	return awaitStep(t, w.a, w.make, want)
}

func (w folderRun) execute(t *testing.T, id ids.ID) *workflow.Step {
	t.Helper()
	if err := w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), id); err != nil {
		t.Fatal(err)
	}
	step, err := workflow.ReadStepIn(context.Background(), w.a.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	return step
}

// readsThePublishedCommit runs the run's readers of its work and checks
// each read the published commit: the commit its gates select, its tests,
// its preview and its scan.
func (w folderRun) readsThePublishedCommit(t *testing.T) {
	t.Helper()
	sel, err := verification.SelectCommit(context.Background(), w.a.DB, w.a.cfg.dataDir, w.run, w.source)
	if err != nil || sel.Commit != w.published {
		t.Fatalf("the run's gates select %+v, want the published %s: %v", sel, w.published, err)
	}
	if tests := w.execute(t, w.tests); tests.Status != workflow.StatusPass || stepEvidence(t, tests)["commit"] != w.published {
		t.Fatalf("the tests step read %s, want the published %s", tests.Evidence, w.published)
	}
	if boot := w.execute(t, w.boot); boot.Status != workflow.StatusPass || !strings.HasPrefix(fmt.Sprint(stepEvidence(t, boot)["incarnation"]), w.published+"+") {
		t.Fatalf("the preview booted %s, want the published %s", boot.Evidence, w.published)
	}
	if scan := w.execute(t, w.scanStep); scan.Status != workflow.StatusPass || stepEvidence(t, scan)["commit"] != w.published {
		t.Fatalf("the scan read %s, want the published %s", scan.Evidence, w.published)
	}
	if listed, err := os.ReadFile(w.scanned); err != nil || !strings.Contains(string(listed), "made.txt") {
		t.Fatalf("the scanned tree lacks the published work: %q %v", listed, err)
	}
}

// A folder AppTown's own clone made is brought forward to the result and
// is not behind: its readers read it, at the published commit.
func TestAFolderTheForgeClonedIsBroughtForward(t *testing.T) {
	t.Parallel()
	for _, onChange := range []bool{true, false} {
		t.Run(fmt.Sprintf("change=%t", onChange), func(t *testing.T) {
			t.Parallel()
			w := publishedFolderRun(t, onChange, false)
			w.runMake(t, 1, madeFiles, workflow.StatusPass)
			w.published = scm.Ref(t.Context(), w.hub, "refs/heads/"+w.source.Branch)
			if w.published == "" || w.published == w.given {
				t.Fatalf("nothing was published: %s", w.published)
			}
			if head := gittest.Run(t, w.ws, "rev-parse", "HEAD"); head != w.published {
				t.Fatalf("the folder is at %s, want the published %s", head, w.published)
			}
			if behind, err := scm.Behind(context.Background(), w.ws, w.hub); err != nil || behind {
				t.Fatalf("a folder brought forward is behind: %v %v", behind, err)
			}
			w.readsThePublishedCommit(t)
		})
	}
}

// A folder with one inert config key AppTown's clone does not write is
// refused: it stays at A, it is behind (the record written by the result
// itself, none by the test), its files are not shown, the refusal is noted
// on the run, and every reader reads the published B.
func TestAFolderTheForgeWillNotRunGitInIsBehindAndReadersReadTheBranch(t *testing.T) {
	t.Parallel()
	for _, onChange := range []bool{true, false} {
		t.Run(fmt.Sprintf("change=%t", onChange), func(t *testing.T) {
			t.Parallel()
			w := publishedFolderRun(t, onChange, true)
			w.runMake(t, 1, madeFiles, workflow.StatusPass)
			w.published = scm.Ref(t.Context(), w.hub, "refs/heads/"+w.source.Branch)
			if w.published == "" || w.published == w.given {
				t.Fatalf("nothing was published: %s", w.published)
			}
			if head := gittest.Run(t, w.ws, "rev-parse", "HEAD"); head != w.given {
				t.Fatalf("a refused folder moved from %s to %s", w.given, head)
			}
			if behind, err := scm.Behind(context.Background(), w.ws, w.hub); err != nil || !behind {
				t.Fatalf("a refused folder is not behind: %v %v", behind, err)
			}
			files := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", w.run.ID), "")
			if files.Code != 409 {
				t.Fatalf("a folder behind its branch is shown: %d %s", files.Code, files.Body.String())
			}
			if !w.noted(t, "folder.behind") {
				t.Fatal("the refusal is not noted on the run")
			}
			w.readsThePublishedCommit(t)
		})
	}
}

// noted reports whether the run's events hold one of kind.
func (w folderRun) noted(t *testing.T, kind string) bool {
	t.Helper()
	snap, err := w.a.Work.Snapshot(w.a.asForge(context.Background(), w.run.ProjectID, "review"), w.run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snap.Events {
		if e.Kind == kind {
			var payload map[string]any
			_ = json.Unmarshal(e.Payload, &payload)
			return payload["refused"] == true
		}
	}
	return false
}

// A folder left behind its branch does not fail the next task's source:
// the next attempt is given the published tip, not the folder's older one.
func TestAFolderLeftBehindDoesNotFailTheNextScript(t *testing.T) {
	t.Parallel()
	w := publishedFolderRun(t, true, true)
	w.runMake(t, 1, madeFiles, workflow.StatusPass)
	published := scm.Ref(t.Context(), w.hub, "refs/heads/"+w.source.Branch)
	if behind, err := scm.Behind(context.Background(), w.ws, w.hub); err != nil || !behind {
		t.Fatalf("precondition: the folder is behind: %v %v", behind, err)
	}
	if _, err := w.a.Work.RetryStep(w.a.asForge(context.Background(), w.run.ProjectID, "fixture"), workflow.RetryCommand{StepID: w.make}); err != nil {
		t.Fatal(err)
	}
	w.runMake(t, 2, map[string]string{"again.txt": "made again\n"}, workflow.StatusPass)
	attempt, err := tasks.New(w.a.DB).Get(context.Background(), fmt.Sprintf("step-%d-attempt-2", w.make))
	if err != nil || attempt.Source != published {
		t.Fatalf("the next task was given %s, want the published %s: %v", attempt.Source, published, err)
	}
	tip := scm.Ref(t.Context(), w.hub, "refs/heads/"+w.source.Branch)
	if parent := gittest.Run(t, w.hub, "rev-parse", tip+"^"); parent != published {
		t.Fatalf("the next result builds on %s, want %s", parent, published)
	}
}

// The state a crash between a result's record and its publication leaves:
// the record names a commit the branch never got (here the publication
// refuses it), so the folder, still at the branch, is not behind and is
// shown, and its readers read it.
func TestAResultNeverPublishedLeavesTheFolderShown(t *testing.T) {
	t.Parallel()
	w := publishedFolderRun(t, true, false)
	w.runMake(t, 1, map[string]string{"made.txt": "made\n", ".gitignore": "!.apptown/runtime.json\n", ".apptown/runtime.json": "{}\n"}, workflow.StatusError)
	if tip := scm.Ref(t.Context(), w.hub, "refs/heads/"+w.source.Branch); tip == "" || tip != w.given {
		t.Fatalf("a refused result moved the branch to %s", tip)
	}
	record, err := os.ReadFile(filepath.Join(filepath.Dir(w.ws), ".saved", filepath.Base(w.ws)))
	if err != nil || strings.HasPrefix(string(record), w.given) {
		t.Fatalf("the result was not recorded before its publication: %q %v", record, err)
	}
	if behind, err := scm.Behind(context.Background(), w.ws, w.hub); err != nil || behind {
		t.Fatalf("a folder at its branch is behind after a refused result: %v %v", behind, err)
	}
	files := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", w.run.ID), "")
	if files.Code != 200 {
		t.Fatalf("a folder at its branch is not shown: %d %s", files.Code, files.Body.String())
	}
	if sel, err := verification.SelectCommit(context.Background(), w.a.DB, w.a.cfg.dataDir, w.run, w.source); err != nil || sel.Commit != w.given || sel.Repo != w.ws {
		t.Fatalf("the run's gates select %+v, want the folder's %s: %v", sel, w.given, err)
	}
}

// The state a crash between a result's publication and the folder's
// fast-forward leaves (here a lock another Git left in the folder stops the
// fast-forward): the folder, which AppTown would run Git in, is behind,
// and its readers read the branch.
func TestAResultPublishedButNotTakenIsReadFromTheBranch(t *testing.T) {
	t.Parallel()
	w := publishedFolderRun(t, true, false)
	if err := os.WriteFile(filepath.Join(w.ws, ".git", "index.lock"), []byte("interrupted git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.runMake(t, 1, madeFiles, workflow.StatusPass)
	w.published = scm.Ref(t.Context(), w.hub, "refs/heads/"+w.source.Branch)
	if head := gittest.Run(t, w.ws, "rev-parse", "HEAD"); head != w.given || w.published == w.given {
		t.Fatalf("precondition: published %s, the folder at %s", w.published, head)
	}
	if behind, err := scm.Behind(context.Background(), w.ws, w.hub); err != nil || !behind {
		t.Fatalf("a folder that did not take the result is not behind: %v %v", behind, err)
	}
	files := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", w.run.ID), "")
	if files.Code != 409 {
		t.Fatalf("a folder behind its branch is shown: %d %s", files.Code, files.Body.String())
	}
	if err := os.Remove(filepath.Join(w.ws, ".git", "index.lock")); err != nil {
		t.Fatal(err)
	}
	w.readsThePublishedCommit(t)
}
