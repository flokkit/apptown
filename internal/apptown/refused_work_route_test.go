package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/poll"
)

// A refused worker result is kept under its step attempt's refused ref
// (workflowexec's TestARefusedResultThatCannotBeKeptHoldsTheWorkspace and
// its neighbours). These are the run's authorized route that serves it.

// inRepo runs git in dir, which must be named: an empty dir would run git
// wherever the test happens to be.
func inRepo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir == "" {
		t.Fatal("inRepo needs a directory")
	}
	return gittest.Run(t, dir, args...)
}

// writeRemote writes files into a task's workspace on the worker.
func writeRemote(t *testing.T, remote string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(remote, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// runEvents are the run's events of kind, through the run's detail.
func runEvents(t *testing.T, a *App, run *workflow.Run, kind string) []map[string]any {
	t.Helper()
	snap, err := a.Work.Snapshot(a.asAppTown(context.Background(), run.ProjectID, "review"), run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, e := range snap.Events {
		if e.Kind == kind {
			var payload map[string]any
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			out = append(out, payload)
		}
	}
	return out
}

// awaitReleased waits until the attempt's task is released on the worker.
func awaitReleased(t *testing.T, a *App, fake *executionfake.Tasks, key string) {
	t.Helper()
	var attempt tasks.Attempt
	var err error
	if !poll.Until(context.Background(), 20*time.Second, 10*time.Millisecond, func() bool {
		attempt, err = tasks.New(a.DB).Get(context.Background(), key)
		return err == nil && attempt.Released && !fake.Present(key)
	}) {
		t.Fatalf("the task was not released: %+v %v; present %v", attempt, err, fake.Present(key))
	}
}

// A result the publication refuses (a branch handed to another holder or
// moved while the task ran, a change to non-source files, a file over the
// size limit) does not move the branch. Its work is kept under the step
// attempt's refused ref, the notice names the ref, and after the worker's
// workspace is released the work can still be downloaded, through the
// run's authorized route, as a bundle holding the refused commit.
func TestARefusedResultIsKeptForItsDownload(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"handed over", "moved", "not source", "too large"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			a, fake, step, run, remote := startOnWorker(t)
			source := workflowSourceForTest(t, a, run)
			hub, ws := source.Repository(a.cfg.dataDir), source.Workspace(a.cfg.dataDir)
			given := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
			files := map[string]string{"made.txt": "refused work\n"}
			why := ""
			switch how {
			case "handed over":
				handOver(t, a, run, source.Branch)
				why = "no longer holds the branch"
			case "moved":
				inRepo(t, ws, "commit", "-q", "--allow-empty", "-m", "published while the task ran")
				if _, err := a.branches.Publish(context.Background(), scm.Publication{ProjectID: run.ProjectID, Repository: hub, Branch: source.Branch, From: ws,
					Grant: scm.Grant{Holder: scm.RunHolder(run.ID)}}); err != nil {
					t.Fatal(err)
				}
				why = "moved: expected " + given[:12]
			case "not source":
				files[".gitignore"], files[".apptown/runtime.json"] = "!.apptown/runtime.json\n", "{}\n"
				why = ".apptown/runtime.json"
			case "too large":
				files["big.bin"] = strings.Repeat("x", 11<<20)
				why = "big.bin"
			}
			writeRemote(t, remote, files)
			before := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
			fake.Exit(taskKeyOf(step), 0)
			settled := awaitStep(t, a, step.ID, workflow.StatusError)
			if !strings.Contains(string(settled.Evidence), why) {
				t.Fatalf("the step's evidence: %s", settled.Evidence)
			}
			if after := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); after != before {
				t.Fatalf("a refused result moved the branch from %s to %s", before, after)
			}
			refusedRef := repositoryedit.RefusedStepRef(step.ID, 1)
			kept := scm.Ref(t.Context(), hub, refusedRef)
			if kept == "" || inRepo(t, hub, "rev-parse", kept+"^") != given {
				t.Fatalf("the refused work is not kept on the commit it was given: %q", kept)
			}
			if c := scm.Ref(t.Context(), hub, repositoryedit.CandidateRef(step.ID, 1)); c != "" {
				t.Fatalf("the refused candidate is still a candidate: %s", c)
			}
			notices := runEvents(t, a, run, "commit.unpublished")
			if len(notices) != 1 || notices[0]["ref"] != refusedRef || notices[0]["sha"] != kept || !strings.Contains(fmt.Sprint(notices[0]["reason"]), why) {
				t.Fatalf("the refusal's notice: %+v", notices)
			}
			awaitReleased(t, a, fake, taskKeyOf(step))
			path := fmt.Sprintf("/api/v1/runs/%d/steps/%d/attempts/1/refused", run.ID, step.ID)
			got := authzCall(a.Handler(), "owner", "GET", path, "")
			if got.Code != 200 {
				t.Fatalf("downloading the refused work: %d %s", got.Code, got.Body.String())
			}
			bundle := filepath.Join(t.TempDir(), "refused.bundle")
			if err := os.WriteFile(bundle, got.Body.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			clone := filepath.Join(t.TempDir(), "repair")
			inRepo(t, t.TempDir(), "clone", "-q", hub, clone)
			inRepo(t, clone, "fetch", "-q", bundle, refusedRef+":refs/heads/repair")
			if tip := inRepo(t, clone, "rev-parse", "refs/heads/repair"); tip != kept {
				t.Fatalf("the download holds %s, want the refused %s", tip, kept)
			}
			if body := inRepo(t, clone, "show", "refs/heads/repair:made.txt"); body != "refused work" {
				t.Fatalf("the downloaded work: %q", body)
			}
			// Another attempt of the step kept nothing.
			if other := authzCall(a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/steps/%d/attempts/2/refused", run.ID, step.ID), ""); other.Code != 404 {
				t.Fatalf("an attempt with nothing kept: %d", other.Code)
			}
		})
	}
}

// handOver gives the run's branch to another holder, as a desktop push
// ticket taking the change would.
func handOver(t *testing.T, a *App, run *workflow.Run, branch string) {
	t.Helper()
	if _, err := database.Transact(context.Background(), a.DB, func(tx *database.Tx) (scm.Authority, error) {
		return scm.AdvanceAuthorityIn(context.Background(), tx, run.ProjectID, branch, scm.TicketHolder(idstest.N(1)))
	}); err != nil {
		t.Fatal(err)
	}
}

// The refused-work route is the run's: a step of another run of the same
// project, whose repository holds the refused work, is not found through it.
func TestARefusedResultIsOnlyItsRunsToServe(t *testing.T) {
	t.Parallel()
	a, fake, step, run, remote := startOnWorker(t)
	source := workflowSourceForTest(t, a, run)
	handOver(t, a, run, source.Branch)
	writeRemote(t, remote, map[string]string{"made.txt": "refused work\n"})
	fake.Exit(taskKeyOf(step), 0)
	awaitStep(t, a, step.ID, workflow.StatusError)
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "build", Steps: []schema.Step{{Name: "other", Uses: "approval"}}}}}
	tx, err := a.DB.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.Work.AdmitRunIn(a.asEngine(context.Background()), tx, workflow.StartRequest{ProjectID: run.ProjectID, WorkflowName: "other", InputsJSON: `{}`},
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "other", Version: 1}, Spec: spec, AdoptionID: run.AdoptionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/runs/%d/steps/%d/attempts/1/refused"
	if got := authzCall(a.Handler(), "owner", "GET", fmt.Sprintf(path, run.ID, step.ID), ""); got.Code != 200 {
		t.Fatalf("the run's own route: %d %s", got.Code, got.Body.String())
	}
	if got := authzCall(a.Handler(), "owner", "GET", fmt.Sprintf(path, other.ID, step.ID), ""); got.Code != 404 {
		t.Fatalf("another run's route served the refused work: %d", got.Code)
	}
}
