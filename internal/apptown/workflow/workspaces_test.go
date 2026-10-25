package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/poll"
)

type processFunc func(context.Context, string, execution.Request) execution.Outcome

func (f processFunc) Run(ctx context.Context, backend string, in execution.Request) execution.Outcome {
	return f(ctx, backend, in)
}
func workspaceFixture(t *testing.T) (workspaces, ChangeRef, string) {
	t.Helper()
	i, in, base := importFixture(t)
	created, err := i.creator.records.create(context.Background(), ChangeDraft{ProjectID: in.ProjectID, Title: "Work"}, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	return newWorkspaces(i.creator.records.db, i.git, i.locks), ChangeRef{ID: created.Change.ID, ProjectID: in.ProjectID}, base
}
func changeWorkspacePath(w workspaces, ref ChangeRef) string {
	return filepath.Join(w.git.Paths().DataDir, "workspaces", fmt.Sprintf("change-%d", ref.ID))
}
func workspaceGit(t *testing.T, g Git, path string, args ...string) string {
	t.Helper()
	out, err := g.(GitWorkspace).git(context.Background(), path, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func preparedRunFixture(t *testing.T) (workspaces, RunRef, ChangeRef, string) {
	t.Helper()
	w, ch, base := workspaceFixture(t)
	id := insertStoredRun(t, w.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: ch.ProjectID, ChangeID: ch.ID, WorkflowName: "work", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "build", DefinitionJson: json.RawMessage(`{"workspace":"run","role":"working"}`)}).ID
	transitionScopeChange(t, w.db, workflowsql.TransitionChangeParams{At: time.Now(), ID: ch.ID, RunID: id})
	return w, RunRef{ID: id, ProjectID: ch.ProjectID}, ch, base
}
func TestWorkspacePreservesDefaultBaseStandaloneCloneAndHumanWork(t *testing.T) {
	t.Parallel()
	w, ref, base := workspaceFixture(t)
	path := changeWorkspacePath(w, ref)
	repo := filepath.Join(w.git.Paths().DataDir, "workspaces", "project-"+ref.ProjectID.String())
	// The hub's current checkout need not be its declared default branch.
	workspaceGit(t, w.git, repo, "checkout", "feature-early")
	attachments := filepath.Join(w.git.Paths().DataDir, "changes", fmt.Sprintf("%d", ref.ID), "attachments")
	if err := os.MkdirAll(attachments, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attachments, "reference.txt"), []byte("original reference"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := w.PrepareChange(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	wantEqual(t, "change base", storedChange(t, w.db, ref.ID).BaseSHA, base)
	if _, err := os.Stat(filepath.Join(path, "game.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("clone used the hub checkout instead of the declared default")
	}
	branch := fmt.Sprintf("apptown/change-%d", ref.ID)
	if got := workspaceGit(t, w.git, path, "symbolic-ref", "--short", "HEAD"); got != branch {
		t.Fatal(got)
	}
	if got := workspaceGit(t, w.git, path, "remote"); got != "" {
		t.Fatalf("workspace retained a host remote: %s", got)
	}
	if got := workspaceGit(t, w.git, path, "config", "user.name"); got != "apptown helper" {
		t.Fatal(got)
	}
	if got := workspaceGit(t, w.git, path, "config", "user.email"); got != "helper@apptown.local" {
		t.Fatal(got)
	}
	object := filepath.Join(".git", "objects", base[:2], base[2:])
	hubInfo, e1 := os.Stat(filepath.Join(repo, object))
	cloneInfo, e2 := os.Stat(filepath.Join(path, object))
	if e1 != nil || e2 != nil || os.SameFile(hubInfo, cloneInfo) {
		t.Fatalf("clone shares mutable Git objects: %v %v", e1, e2)
	}
	if data, err := os.ReadFile(filepath.Join(path, ".apptown", "attachments", "reference.txt")); err != nil || string(data) != "original reference" {
		t.Fatalf("attachments: %q %v", data, err)
	}
	if err := os.WriteFile(filepath.Join(path, "human.txt"), []byte("unsaved human work"), 0644); err != nil {
		t.Fatal(err)
	}
	// Preparing the folder again, for its next holder, keeps the human work:
	// it is committed as the previous worker's, never discarded or changed.
	if err := w.PrepareChange(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(path, "human.txt")); err != nil || string(data) != "unsaved human work" {
		t.Fatalf("repeat changed human work: %q %v", data, err)
	}
	if got := workspaceGit(t, w.git, path, "show", "HEAD:human.txt"); got != "unsaved human work" {
		t.Fatalf("the human work was not committed as the previous worker's: %q", got)
	}
	if got := workspaceGit(t, w.git, path, "log", "-1", "--format=%s"); got != leftoverMessage {
		t.Fatalf("leftover commit: %q", got)
	}
	wantEqual(t, "change base", storedChange(t, w.db, ref.ID).BaseSHA, base)
}
func TestWorkspaceGitFailuresLeaveNoHalfConfiguredClone(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"show-ref", "clone", "checkout", "remote", "prepared base"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			w, ref, _ := workspaceFixture(t)
			path := changeWorkspacePath(w, ref)
			failure := errors.New("injected Git failure")
			hit := false
			w.git = GitWorkspace{DataDir: w.git.Paths().DataDir, Runner: processFunc(func(ctx context.Context, backend string, in execution.Request) execution.Outcome {
				name := in.Argv[1]
				if name == "config" {
					name += " " + in.Argv[2]
				}
				if name == "rev-parse" && strings.Contains(in.Workspace, ".forge-preparing-") {
					name = "prepared base"
				}
				if name == command {
					hit = true
					return execution.Outcome{RunErr: failure, ExitCode: 17, Termination: execution.Exited}
				}
				return (execution.Runner{}).Run(ctx, backend, in)
			})}
			if err := w.PrepareChange(context.Background(), ref); !errors.Is(err, failure) || !hit {
				t.Fatalf("Git failure escaped: %v, hit %v", err, hit)
			}
			wantEqual(t, "change base", storedChange(t, w.db, ref.ID).BaseSHA, "")
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial published clone: %v", err)
			}
			if leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".forge-preparing-*")); err != nil || len(leftovers) != 0 {
				t.Fatalf("staging left: %v %v", leftovers, err)
			}
			w.git = GitWorkspace{DataDir: w.git.Paths().DataDir}
			if err := w.PrepareChange(context.Background(), ref); err != nil {
				t.Fatalf("explicit retry: %v", err)
			}
		})
	}
}
func TestWorkspaceRefusesPointerMetadataAndDifferentCheckout(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"pointer", "wrong branch", "broken Git", "missing historical branch"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			w, ref, _ := workspaceFixture(t)
			path := changeWorkspacePath(w, ref)
			switch mode {
			case "pointer":
				if err := os.MkdirAll(path, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: /elsewhere\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "broken Git":
				if err := os.MkdirAll(filepath.Join(path, ".git"), 0755); err != nil {
					t.Fatal(err)
				}
			case "wrong branch":
				if err := w.PrepareChange(context.Background(), ref); err != nil {
					t.Fatal(err)
				}
				workspaceGit(t, w.git, path, "checkout", "-b", "human-experiment")
				if err := os.WriteFile(filepath.Join(path, "human.txt"), []byte("keep this"), 0644); err != nil {
					t.Fatal(err)
				}
			case "missing historical branch":
				scopeChangeBase(t, w.db, ref.ID, "witnessed")
			}
			if err := w.PrepareChange(context.Background(), ref); err == nil {
				t.Fatal("invalid workspace reported ready")
			}
			if mode == "wrong branch" {
				if got := workspaceGit(t, w.git, path, "symbolic-ref", "--short", "HEAD"); got != "human-experiment" {
					t.Fatal("replaced human branch")
				}
				data, err := os.ReadFile(filepath.Join(path, "human.txt"))
				if err != nil || string(data) != "keep this" {
					t.Fatalf("lost work: %q %v", data, err)
				}
			}
			if mode == "pointer" {
				data, err := os.ReadFile(filepath.Join(path, ".git"))
				if err != nil || string(data) != "gitdir: /elsewhere\n" {
					t.Fatal("converted metadata")
				}
			}
		})
	}
}
func TestWorkspaceRechecksChangeAndProjectAfterGit(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"default branch", "branch", "base", "selected source", "state", "attached run", "deleted"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			w, ref, _ := workspaceFixture(t)
			once := false
			w.git = GitWorkspace{DataDir: w.git.Paths().DataDir, Runner: processFunc(func(ctx context.Context, backend string, in execution.Request) execution.Outcome {
				out := (execution.Runner{}).Run(ctx, backend, in)
				if !once && in.Argv[1] == "rev-parse" && strings.Contains(in.Workspace, ".forge-preparing-") && out.RunErr == nil {
					once = true
					switch mutation {
					case "default branch":
						if err := project.New(w.db, nil).SetDefaultBranch(ctx, ref.ProjectID, "new-main"); err != nil {
							t.Fatal(err)
						}
					case "branch":
						if n, err := workflowsql.New(w.db.SQL()).SetChangeBranch(context.Background(), workflowsql.SetChangeBranchParams{ID: ref.ID, ProjectID: ref.ProjectID, Branch: "new-branch"}); err != nil || n != 1 {
							t.Fatalf("SetChangeBranch: %d %v", n, err)
						}
					case "base":
						scopeChangeBase(t, w.db, ref.ID, "other-base")
					case "selected source":
						if n, err := workflowsql.New(w.db.SQL()).SetChangeLandedSHA(context.Background(), workflowsql.SetChangeLandedSHAParams{ChangeID: ref.ID, ProjectID: ref.ProjectID, LandedSha: "selected-later"}); err != nil || n != 1 {
							t.Fatalf("SetChangeLandedSHA: %d %v", n, err)
						}
					case "state":
						transitionScopeChange(t, w.db, workflowsql.TransitionChangeParams{At: time.Now(), ID: ref.ID, State: "abandoned"})
					case "attached run":
						transitionScopeChange(t, w.db, workflowsql.TransitionChangeParams{At: time.Now(), ID: ref.ID, RunID: idstest.N(999)})
					case "deleted":
						if err := workflowsql.New(w.db.SQL()).DeleteProjectChanges(context.Background(), ref.ProjectID); err != nil {
							t.Fatal(err)
						}
					}
				}
				return out
			})}
			err := w.PrepareChange(context.Background(), ref)
			if err == nil || !once {
				t.Fatalf("stale source accepted: %v, injected %v", err, once)
			}
			if mutation != "deleted" {
				want := ""
				if mutation == "base" {
					want = "other-base"
				}
				wantEqual(t, "change base", storedChange(t, w.db, ref.ID).BaseSHA, want)
			}
		})
	}
}
func TestWorkspaceChangePublicationFailureKeepsIdentityForExplicitRetry(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"read", "write", "suppressed", "deferred"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			w, ref, _ := workspaceFixture(t)
			switch fault {
			case "read":
				dbtest.FailNextStatement(t, w.db, "SELECT p.slug,p.default_branch,c.branch")
			case "write":
				dbtest.FailNextStatement(t, w.db, "UPDATE changes SET base_sha")
			default:
				action := "RETURN NULL;"
				trigger := "CREATE TRIGGER veto BEFORE UPDATE ON changes"
				if fault == "deferred" {
					action = "RAISE EXCEPTION 'deferred base failure';"
					trigger = "CREATE CONSTRAINT TRIGGER veto AFTER UPDATE ON changes DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, w.db, `CREATE FUNCTION veto() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+action+` END $$`)
				completionExec(t, w.db, trigger+` FOR EACH ROW EXECUTE FUNCTION veto()`)
			}
			if err := w.PrepareChange(context.Background(), ref); err == nil {
				t.Fatal("unrecorded base reported prepared")
			}
			wantEqual(t, "change base", storedChange(t, w.db, ref.ID).BaseSHA, "")
			wantReceiptCount(t, w.db, 1, receiptWith("change.created", "", "changeId", fmt.Sprint(ref.ID)))
			if fault == "suppressed" || fault == "deferred" {
				completionExec(t, w.db, `DROP TRIGGER veto ON changes`)
			}
			if err := w.PrepareChange(context.Background(), ref); err != nil {
				t.Fatalf("explicit preparation retry: %v", err)
			}
			if storedChange(t, w.db, ref.ID).BaseSHA == "" {
				t.Fatal("change base not recorded")
			}
		})
	}
}
func TestRunWorkspacePublishesBothBasesAndReceiptAtomically(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"none", "run write", "change write", "receipt", "suppressed run", "suppressed change", "suppressed receipt", "deferred receipt"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			w, run, ch, base := preparedRunFixture(t)
			if n, err := workflowsql.New(w.db.SQL()).SetChangeBranch(context.Background(), workflowsql.SetChangeBranchParams{ID: ch.ID, ProjectID: ch.ProjectID, Branch: ""}); err != nil || n != 1 {
				t.Fatalf("SetChangeBranch: %d %v", n, err)
			}
			switch fault {
			case "run write":
				dbtest.FailNextStatement(t, w.db, "UPDATE runs SET base_sha")
			case "change write":
				dbtest.FailNextStatement(t, w.db, "UPDATE changes SET base_sha")
			case "receipt":
				dbtest.FailNextStatement(t, w.db, "INSERT INTO run_events")
			case "none":
			default:
				table, event := "run_events", "INSERT"
				if fault == "suppressed run" {
					table, event = "runs", "UPDATE"
				}
				if fault == "suppressed change" {
					table, event = "changes", "UPDATE"
				}
				action := "RETURN NULL;"
				trigger := "CREATE TRIGGER veto BEFORE " + event + " ON " + table
				if fault == "deferred receipt" {
					action = "RAISE EXCEPTION 'deferred workspace receipt';"
					trigger = "CREATE CONSTRAINT TRIGGER veto AFTER INSERT ON run_events DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, w.db, `CREATE FUNCTION veto() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+action+` END $$`)
				completionExec(t, w.db, trigger+` FOR EACH ROW EXECUTE FUNCTION veto()`)
			}
			err := w.PrepareRun(context.Background(), run)
			if fault != "none" {
				if err == nil {
					t.Fatal("partial run preparation succeeded")
				}
				wantEqual(t, "run base", storedRunRow(t, w.db, run.ID).BaseSha, "")
				wantEqual(t, "change source", changeSource(storedChange(t, w.db, ch.ID)), [2]string{"", ""})
				wantEventCount(t, w.db, 0, anyEvent)
				if strings.HasPrefix(fault, "suppressed") || strings.HasPrefix(fault, "deferred") {
					completionExec(t, w.db, `DROP TRIGGER veto ON `+map[string]string{"suppressed run": "runs", "suppressed change": "changes", "suppressed receipt": "run_events", "deferred receipt": "run_events"}[fault])
				}
				err = w.PrepareRun(context.Background(), run)
			}
			if err != nil {
				t.Fatalf("prepare/retry: %v", err)
			}
			wantEqual(t, "run base", storedRunRow(t, w.db, run.ID).BaseSha, base)
			wantEqual(t, "change source", changeSource(storedChange(t, w.db, ch.ID)), [2]string{base, fmt.Sprintf("apptown/change-%d", ch.ID)})
			wantRunEventCount(t, w.db, run.ID, 1, kindWith("workspace.prepared", "tip", base))
		})
	}
}
func TestRunWorkspaceRejectsLateStateScopeAndRevision(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"done", "stage", "revision", "source", "change run"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			w, run, ch, _ := preparedRunFixture(t)
			once := false
			w.git = GitWorkspace{DataDir: w.git.Paths().DataDir, Runner: processFunc(func(ctx context.Context, backend string, in execution.Request) execution.Outcome {
				out := (execution.Runner{}).Run(ctx, backend, in)
				if !once && in.Argv[1] == "rev-parse" && strings.Contains(in.Workspace, ".forge-preparing-") && out.RunErr == nil {
					once = true
					switch mutation {
					case "done":
						transitionScopeRun(t, w.db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStatus: "done"})
					case "stage":
						transitionScopeRun(t, w.db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStage: "later"})
					case "revision":
						invalidateScopeRun(t, w.db, run.ID)
					case "source":
						if n, err := workflowsql.New(w.db.SQL()).SetChangeLandedSHA(context.Background(), workflowsql.SetChangeLandedSHAParams{ChangeID: ch.ID, ProjectID: ch.ProjectID, LandedSha: "new-selection"}); err != nil || n != 1 {
							t.Fatalf("SetChangeLandedSHA: %d %v", n, err)
						}
					case "change run":
						transitionScopeChange(t, w.db, workflowsql.TransitionChangeParams{At: time.Now(), ID: ch.ID, RunID: idstest.N(999)})
					}
				}
				return out
			})}
			if err := w.PrepareRun(context.Background(), run); !errors.Is(err, errWorkspaceChanged) || !once {
				t.Fatalf("stale run: %v injected %v", err, once)
			}
			wantEqual(t, "run base", storedRunRow(t, w.db, run.ID).BaseSha, "")
			wantEqual(t, "change base", storedChange(t, w.db, ch.ID).BaseSHA, "")
			wantEventCount(t, w.db, 0, anyEvent)
		})
	}
}
func TestWorkspaceCancellationReachesLocksAndGit(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"repository", "project", "Git"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			w, ref, _ := workspaceFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var gate *database.Tx
			var release func()
			reached := make(chan struct{})
			switch mode {
			case "repository":
				var err error
				release, err = w.locks.Acquire(context.Background(), ref.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "project":
				var err error
				gate, err = w.db.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = gate.Rollback() }()
				holdProjectLock(t, gate, ref.ProjectID)
			case "Git":
				w.git = GitWorkspace{DataDir: w.git.Paths().DataDir, Runner: processFunc(func(ctx context.Context, backend string, in execution.Request) execution.Outcome {
					if in.Argv[1] == "clone" {
						close(reached)
						<-ctx.Done()
						return execution.Outcome{RunErr: ctx.Err(), ExitCode: -1, Termination: execution.Canceled}
					}
					return (execution.Runner{}).Run(ctx, backend, in)
				})}
			}
			done := make(chan error, 1)
			go func() { done <- w.PrepareChange(ctx, ref) }()
			switch mode {
			case "project":
				dbtest.WaitForLock(t, w.db, "-- name: ProjectForUpdate")
			case "Git":
				select {
				case <-reached:
				case <-time.After(3 * time.Second):
					t.Fatal("Git never started")
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("preparation did not cancel")
			}
			wantEqual(t, "change base", storedChange(t, w.db, ref.ID).BaseSHA, "")
		})
	}
}
func TestRunWorkspaceReleasesSnapshotBeforeGitWithOneConnection(t *testing.T) {
	t.Parallel()
	w, run, ch, base := preparedRunFixture(t)
	w.db.Pool().SetMaxOpenConns(1)
	observed := false
	w.git = GitWorkspace{DataDir: w.git.Paths().DataDir, Runner: processFunc(func(ctx context.Context, backend string, in execution.Request) execution.Outcome {
		if in.Argv[1] == "clone" {
			if _, err := readChange(ctx, w.db, ch.ID); err != nil {
				return execution.Outcome{RunErr: err, ExitCode: -1}
			}
			observed = true
		}
		return (execution.Runner{}).Run(ctx, backend, in)
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.PrepareRun(ctx, run); err != nil || !observed {
		t.Fatalf("snapshot held during Git: %v, observed %v", err, observed)
	}
	wantEqual(t, "run base", storedRunRow(t, w.db, run.ID).BaseSha, base)
}
func TestWorkspaceImportedBaseAndAttachmentErrorsRemainExplicit(t *testing.T) {
	t.Parallel()
	i, in, base := importFixture(t)
	w := newWorkspaces(i.creator.records.db, i.git, i.locks)
	i.creator.prepare = func(ctx context.Context, created CreatedChange) error {
		return w.PrepareChange(ctx, ChangeRef{ID: created.Change.ID, ProjectID: in.ProjectID})
	}
	ch, err := i.Import(context.Background(), in, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if ch.BaseSHA != base {
		t.Fatalf("import base became branch tip: %+v", ch)
	}
	data, err := os.ReadFile(filepath.Join(changeWorkspacePath(w, ChangeRef{ID: ch.ID}), "game.txt"))
	if err != nil || string(data) != "original playable code\n" {
		t.Fatalf("historical contents changed: %q %v", data, err)
	}
	dir := filepath.Join(w.git.Paths().DataDir, "changes", fmt.Sprintf("%d", ch.ID))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attachments"), []byte("broken canonical directory"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := w.PrepareChange(context.Background(), ChangeRef{ID: ch.ID, ProjectID: ch.ProjectID}); err == nil {
		t.Fatal("attachment read failure was hidden")
	}
	wantEqual(t, "change base", storedChange(t, w.db, ch.ID).BaseSHA, base)
}
func TestWorkspaceGitCancellationStopsARealProcessGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (GitWorkspace{}).git(ctx, dir, "-c", `alias.forge-wait=!f() { touch "$1"; exec sleep 60; }; f`, "forge-wait", marker)
		done <- err
	}()
	if !poll.Until(context.Background(), 3*time.Second, 10*time.Millisecond, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}) {
		t.Fatal("real Git child did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("real Git process group did not cancel")
	}
}
