package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

type changeTransitionAuthority struct{}

func (a changeTransitionAuthority) Require(ctx context.Context, _ access.Permission) (access.Caller, error) {
	caller, _ := access.CallerFrom(ctx)
	return caller, nil
}

func changeTransitionContext(ctx context.Context, who string) context.Context {
	return access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "user", Name: who, Via: "open"}, Verified: true, Receipt: who})
}

func changeTransitionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gittest.Command(args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// beginChangeAs starts a Change through the Work service. A named branch is
// prepared as a workspace, so the project's repository must live in dataDir.
func beginChangeAs(ctx context.Context, db *database.DB, dataDir string, who string, id ids.ID) (*Change, error) {
	return New(context.Background(), db, Dependencies{Authority: changeTransitionAuthority{}, Git: GitWorkspace{DataDir: dataDir}, Repositories: &scm.RepositoryLocks{}}).Begin(changeTransitionContext(ctx, who), id)
}

func projectRepository(t *testing.T, slug string) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "workspaces", "project-"+slug)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	changeTransitionGit(t, repo, "init", "-q", "-b", "main")
	changeTransitionGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "base")
	return dir
}

func TestChangeStartRequiresItsCompleteRecord(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"project read", "change read", "write", "receipt", "readback", "suppressed write", "suppressed receipt"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			w, run, _ := completionFixture(t, "working")
			testAccounts(t, w, "alice")
			dataDir := projectRepository(t, "completion")
			if err := project.New(w, nil).SetDefaultBranch(context.Background(), run.ProjectID, "main"); err != nil {
				t.Fatal(err)
			}
			transitionScopeChange(t, w, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, State: "proposed"})
			if n, err := workflowsql.New(w.SQL()).SetChangeBranch(context.Background(), workflowsql.SetChangeBranchParams{ID: run.ChangeID, ProjectID: run.ProjectID, Branch: ""}); err != nil || n != 1 {
				t.Fatalf("SetChangeBranch: %d %v", n, err)
			}
			switch failure {
			case "project read":
				dbtest.FailNextStatement(t, w, "-- name: ProjectForUpdate")
			case "change read":
				dbtest.FailNextStatement(t, w, "-- name: ChangeForUpdate")
			case "write":
				dbtest.FailNextStatement(t, w, "UPDATE changes SET state=")
			case "receipt":
				dbtest.FailNextStatement(t, w, "INSERT INTO audit_log")
			case "readback":
				dbtest.FailNextStatement(t, w, "-- name: ChangeByID")
			default:
				table, act := "changes", "UPDATE"
				if failure == "suppressed receipt" {
					table, act = "audit_log", "INSERT"
				}
				completionExec(t, w, `CREATE FUNCTION veto_change_start() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
				completionExec(t, w, `CREATE TRIGGER veto_change_start BEFORE `+act+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_change_start()`)
			}
			if got, err := beginChangeAs(context.Background(), w, dataDir, "alice", run.ChangeID); got != nil || err == nil {
				t.Fatalf("partial start: %+v %v", got, err)
			}
			ch := seenChange(t, w, run.ChangeID)
			expect(t, ch.State == "proposed" && ch.Branch == "" && !ch.ClosedAt.Valid, "partial start changed the record: %+v", ch)
			expect(t, auditCount(t, w, "", nil) == 0, "partial start left a receipt")
			if failure == "suppressed write" {
				completionExec(t, w, `DROP TRIGGER veto_change_start ON changes`)
			}
			if failure == "suppressed receipt" {
				completionExec(t, w, `DROP TRIGGER veto_change_start ON audit_log`)
			}
			for i := 1; i <= 2; i++ {
				got, err := beginChangeAs(context.Background(), w, dataDir, "alice", run.ChangeID)
				if err != nil || got.State != ChangeInProgress || got.Branch != fmt.Sprintf("apptown/change-%d", run.ChangeID) || got.ClosedAt.Valid {
					t.Fatalf("explicit start/repeat: %+v %v", got, err)
				}
				started := auditCount(t, w, "change.started", func(e audit.Entry) bool { return e.Actor == "alice" && e.ProjectID == run.ProjectID })
				expect(t, started == i, "start receipts %d want %d", started, i)
			}
		})
	}
}

func TestChangeStartUsesFactsAfterProjectLock(t *testing.T) {
	t.Parallel()
	for _, changed := range []string{"closed", "repository", "cancel"} {
		t.Run(changed, func(t *testing.T) {
			t.Parallel()
			w, run, _ := completionFixture(t, "working")
			testAccounts(t, w, "alice")
			if err := project.New(w, nil).SetDefaultBranch(context.Background(), run.ProjectID, "main"); err != nil {
				t.Fatal(err)
			}
			transitionScopeChange(t, w, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, State: "proposed"})
			gate, err := w.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Rollback() }()
			holdProjectLock(t, gate, run.ProjectID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			results := make(chan error, 1)
			go func() { _, err := beginChangeAs(ctx, w, t.TempDir(), "alice", run.ChangeID); results <- err }()
			dbtest.WaitForLock(t, w, "-- name: ProjectForUpdate")
			switch changed {
			case "closed":
				transitionScopeChange(t, gate, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, State: "abandoned", Closed: true})
			case "repository":
				// sql-guarantee: Change default_branch in the transaction holding the project lock while Start waits; Projects.SetDefaultBranch only accepts a DB and cannot join gate, and the project generated query is internal to Project.
				if _, err := gate.Exec(`UPDATE projects SET default_branch='' WHERE id=?`, run.ProjectID); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			}
			if err := gate.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-results:
				switch changed {
				case "closed":
					if !errors.Is(err, ErrChangeTransition) {
						t.Fatalf("closed start: %v", err)
					}
				case "cancel":
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancelled start: %v", err)
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("start did not release its transaction")
			}
			if changed == "repository" {
				ch := seenChange(t, w, run.ChangeID)
				expect(t, ch.State == "in_progress" && ch.Branch == "", "start without repository: %+v", ch)
				expect(t, auditCount(t, w, "change.started", nil) == 1, "start receipts")
			} else {
				expect(t, auditCount(t, w, "", nil) == 0, "refused start left a receipt")
			}
		})
	}
}

func TestChangeStartRacesClosureWithoutReopening(t *testing.T) {
	t.Parallel()
	w, run, _ := completionFixture(t, "working")
	testAccounts(t, w, "starter", "late")
	transitionScopeChange(t, w, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, State: "proposed"})
	results := race(9, func(i int) int {
		if i == 0 {
			_, err := NewAbandonments(w, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{}).Abandon(context.Background(), Abandonment{ChangeID: run.ChangeID, Who: "closer"})
			if err != nil {
				t.Errorf("closure: %v", err)
			}
			return 2
		}
		_, err := beginChangeAs(context.Background(), w, t.TempDir(), "starter", run.ChangeID)
		if errors.Is(err, ErrChangeTransition) {
			return 1
		}
		if err != nil {
			t.Errorf("start: %v", err)
			return 3
		}
		return 0
	})
	ch := seenChange(t, w, run.ChangeID)
	expect(t, ch.State == "abandoned" && ch.ClosedAt.Valid, "closure lost: %+v", ch)
	expect(t, auditCount(t, w, "change.started", nil) == count(results, 0), "start receipts do not match successful starts")
	expect(t, auditCount(t, w, "change.abandoned", nil) == 1, "abandonment receipts")
	if _, err := beginChangeAs(context.Background(), w, t.TempDir(), "late", run.ChangeID); !errors.Is(err, ErrChangeTransition) {
		t.Fatalf("closed record reopened: %v", err)
	}
}

func TestChangeParticipantRequiresProjectAndOwnedRoot(t *testing.T) {
	t.Parallel()
	w, run, _ := completionFixture(t, "working")
	other := workflowTestProject(t, w, "other", "Other", "")
	for _, tc := range []struct {
		name    string
		project ids.ID
		move    ChangeMove
		missing bool
	}{
		{"foreign project", other, ChangeMove{Action: StartChange}, true},
		{"missing root", run.ProjectID, ChangeMove{Action: AttachChangeRun, RunID: idstest.N(99999)}, false},
		{"absent run", run.ProjectID, ChangeMove{Action: LandChange}, false},
		{"unexpected run", run.ProjectID, ChangeMove{Action: StartChange, RunID: run.ID}, false},
		{"unexpected branch", run.ProjectID, ChangeMove{Action: ShipChange, NameBranch: true}, false},
	} {
		err := w.InTx(context.Background(), func(tx *database.Tx) error {
			if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(context.Background(), tc.project); err != nil {
				return err
			}
			_, err := TransitionChangeIn(context.Background(), tx, tc.project, run.ChangeID, tc.move, time.Now())
			return err
		})
		if err == nil || tc.missing && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("%s accepted: %v", tc.name, err)
		}
	}
	for _, mutation := range []string{"project_id", "change_id", "parent_run_id"} {
		// sql-guarantee: Independently corrupt project_id, change_id or parent_run_id on the same candidate root to test attachment scope; existing run mutations preserve these admission identities.
		completionExec(t, w, `UPDATE runs SET `+mutation+`=? WHERE id=?`, other, run.ID)
		err := w.InTx(context.Background(), func(tx *database.Tx) error {
			if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(context.Background(), run.ProjectID); err != nil {
				return err
			}
			_, err := TransitionChangeIn(context.Background(), tx, run.ProjectID, run.ChangeID, ChangeMove{Action: AttachChangeRun, RunID: run.ID}, time.Now())
			return err
		})
		if !errors.Is(err, ErrChangeTransition) {
			t.Fatalf("foreign %s attached: %v", mutation, err)
		}
		// sql-guarantee: Restore the original candidate's three admission identities between independent attachment-fence cases; no generated run mutation can reparent or repair these fields.
		completionExec(t, w, `UPDATE runs SET project_id=?,number=number+100000,change_id=?,parent_run_id=NULL WHERE id=?`, run.ProjectID, run.ChangeID, run.ID)
	}
	ch := seenChange(t, w, run.ChangeID)
	expect(t, ch.RunID.IsZero() && ch.State == "in_progress", "refused attachment moved the change: %+v", ch)
}

func TestChangeReadsRejectInvalidStoredState(t *testing.T) {
	t.Parallel()
	store, run, _ := completionFixture(t, "working")
	testAccounts(t, store, "alice")
	transitionScopeChange(t, store, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, State: "unexpected"})
	if got, err := ReadChangeIn(context.Background(), store, run.ChangeID); got != nil || !errors.Is(err, errChangeState) {
		t.Fatalf("invalid record escaped: %+v %v", got, err)
	}
	if got, err := New(context.Background(), store, Dependencies{Authority: changeTransitionAuthority{}}).Changes(changeTransitionContext(context.Background(), "alice"), run.ProjectID, ChangeList{}); got != nil || !errors.Is(err, errChangeState) {
		t.Fatalf("invalid list escaped: %+v %v", got, err)
	}
	if got, err := beginChangeAs(context.Background(), store, t.TempDir(), "alice", run.ChangeID); got != nil || !errors.Is(err, errChangeState) {
		t.Fatalf("invalid state started: %+v %v", got, err)
	}
	expect(t, auditCount(t, store, "", nil) == 0, "invalid state left a receipt")
}

// This command-only double deliberately cannot authorize aggregate reads.
func (changeTransitionAuthority) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return nil, access.ErrAuthorityUnavailable
}
