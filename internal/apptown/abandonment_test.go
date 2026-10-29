package apptown

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func abandonmentScenario(t *testing.T, role string, steps []schema.Step, configs ...Config) (*App, *workflow.Run) {
	t.Helper()
	calls := []schema.Step{{Name: "child", Uses: "workflow/call"}, {Name: "sibling", Uses: "workflow/call"}}
	if steps == nil {
		steps = calls
	}
	a, ctx, change := changeScenarioIn(t, testApp(t, configs...), schema.Spec{Kind: "pipeline", Role: role, Stages: []schema.Stage{{Name: "finish", Steps: steps}}},
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "child", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "finish", Steps: calls}}}},
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "done", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "finish"}}}})
	a.Work.Stop()
	root, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "test"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	return a, root
}

func abandonChild(t *testing.T, a *App, parent ids.ID, completed bool) ids.ID {
	t.Helper()
	ctx := asPerson("alice")
	snapshot, err := a.Work.Snapshot(ctx, parent, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Type != "workflow/call" || step.Status != workflow.StatusPending {
			continue
		}
		name := "child"
		if completed {
			name = "done"
		}
		child, err := a.Work.Call(ctx, workflow.WorkflowCall{ParentStepID: step.ID, Workflow: name})
		if !errors.Is(err, workflow.ErrDispatchStopped) {
			t.Fatal(err)
		}
		if completed {
			if out, err := a.Work.Advance(ctx, child.ID); err != nil || out.RunStatus != workflow.RunDone {
				t.Fatalf("finish child: %+v %v", out, err)
			}
		}
		return child.ID
	}
	t.Fatal("no unclaimed child call")
	return ids.Nil
}

func TestAbandonmentRollsBackRequiredRecords(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"run", "change"} {
		// Workflow owner tests cover suppressed run transitions, run receipts, and
		// abandonment audit rollback. Root keeps the cross-owner participants.
		failures := []string{"INSERT INTO jobs"}
		if target == "change" {
			failures = append(failures, "UPDATE changes SET state=", "UPDATE object_capabilities")
		} else {
			failures = append(failures, "UPDATE changes SET state=")
		}
		for _, failure := range failures {
			t.Run(target+"/"+failure, func(t *testing.T) {
				a, root := abandonmentScenario(t, "landing", nil)
				db := a.DB
				child := abandonChild(t, a, root.ID, false)
				token, ticket, err := a.Access.IssueTicket(context.Background(), "alice", ids.Nil, root.ChangeID, "refs/heads/test", "")
				if err != nil {
					t.Fatal(err)
				}
				req := workflow.Abandonment{Who: "alice", Reason: "stop"}
				if target == "run" {
					req.RunID = root.ID
				} else {
					req.ChangeID = root.ChangeID
				}
				before := map[string]int{}
				for _, table := range []string{"run_events", "audit_log", "river_job"} {
					var n int
					// sql-guarantee: the failed abandonment must leave no hidden row in any of these tables; only a whole-table count can prove nothing was written.
					if err := db.SQL().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
						t.Fatal(err)
					}
					before[table] = n
				}
				if failure == "INSERT INTO jobs" {
					failNextJobInsert(t, db)
				} else {
					dbtest.FailNextStatement(t, a.DB, failure)
				}
				s := abandonmentStore{db: db}
				if _, err := s.abandon(req); err == nil {
					t.Fatal("failure was hidden")
				}
				requireRunStatus(t, a, workflow.RunRunning, root.ID, child)
				if change := requireChangeState(t, a, root.ChangeID, workflow.ChangeLanding); change.ClosedAt.Valid {
					t.Fatalf("change closed by a failed abandonment: %+v", change)
				}
				if live, err := a.Access.TicketByToken(context.Background(), token); err != nil || live == nil || live.ID != ticket.ID {
					t.Fatalf("ticket no longer opens the change: %+v %v", live, err)
				}
				for _, table := range []string{"run_events", "audit_log", "river_job"} {
					// sql-guarantee: rollback proves no hidden row was written; only a whole-table count shows that.
					completionCount(t, db, before[table], `SELECT COUNT(*) FROM `+table)
				}
				if _, err := s.abandon(req); err != nil {
					t.Fatal(err)
				}
				requireRunStatus(t, a, workflow.RunAbandoned, root.ID, child)
				for _, id := range []ids.ID{root.ID, child} {
					if n := runEventCount(t, a, id, "run.abandoned"); n != 1 {
						t.Fatalf("run %d: %d run.abandoned events", id, n)
					}
				}
				// sql-guarantee: exactly one ledger row of any kind, against the whole-table count taken before.
				completionCount(t, db, before["audit_log"]+1, `SELECT COUNT(*) FROM audit_log`)
				if n := len(operationJobs(t, a, workflowAbandonmentJob)); n != 1 {
					t.Fatalf("%d abandonment cleanup jobs", n)
				}
			})
		}
	}
}

// AppTown's abandonment participant revokes a change's Git tickets when the
// change is abandoned, and only that change's: stopping a run revokes
// nothing. Workflow's TestAbandonmentSettlesTheChangeByItsTargetAndRole
// covers which runs stop and what becomes of the change.
func TestAbandoningAChangeRevokesOnlyItsTickets(t *testing.T) {
	t.Parallel()
	a, root := abandonmentScenario(t, "landing", nil)
	ctx := a.asAppTown(context.Background(), root.ProjectID, "fixture")
	other, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: root.ProjectID, Type: "feature", Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[ids.ID]string{}
	for _, change := range []ids.ID{root.ChangeID, other.ID} {
		token, _, err := a.Access.IssueTicket(ctx, "alice", ids.Nil, change, "refs/heads/test", "")
		if err != nil {
			t.Fatal(err)
		}
		tokens[change] = token
	}
	live := func(change ids.ID) bool {
		t.Helper()
		ticket, err := a.Access.TicketByToken(ctx, tokens[change])
		if err != nil {
			t.Fatal(err)
		}
		return ticket != nil
	}
	if _, err := (abandonmentStore{a.DB}).abandon(workflow.Abandonment{RunID: root.ID, Who: "alice"}); err != nil {
		t.Fatal(err)
	}
	if !live(root.ChangeID) || !live(other.ID) {
		t.Fatal("stopping a run revoked a ticket")
	}
	if _, err := (abandonmentStore{a.DB}).abandon(workflow.Abandonment{ChangeID: root.ChangeID, Who: "alice"}); err != nil {
		t.Fatal(err)
	}
	if live(root.ChangeID) || !live(other.ID) {
		t.Fatalf("after abandoning the change: its ticket live %v, the other's %v", live(root.ChangeID), live(other.ID))
	}
	// sql-guarantee: the ticket read answers nil alike for a revoked and a deleted ticket, and no owner read lists revoked capabilities; only the stored revocation shows the ticket was revoked, not removed, and that nothing else was.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM object_capabilities WHERE revoked_at IS NOT NULL`)
}

func TestAbandonmentConcurrentWinner(t *testing.T) {
	t.Parallel()
	a, root := abandonmentScenario(t, "landing", nil)
	w := a.DB
	child := abandonChild(t, a, root.ID, false)
	var beforeAudit int
	// sql-guarantee: the race must add exactly one ledger row of any kind; only a whole-table count bounds every kind.
	if err := w.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM audit_log`).Scan(&beforeAudit); err != nil {
		t.Fatal(err)
	}
	result := race(8, func(int) int {
		_, err := (abandonmentStore{db: w}).abandon(workflow.Abandonment{RunID: root.ID})
		if err == nil {
			return 0
		}
		var conflict *workflow.AbandonConflict
		if !errors.As(err, &conflict) {
			t.Error(err)
		}
		return 1
	})
	if count(result, 0) != 1 {
		t.Fatalf("winners: %v", result)
	}
	for _, id := range []ids.ID{root.ID, child} {
		if n := runEventCount(t, a, id, "run.abandoned"); n != 1 {
			t.Fatalf("run %d: %d run.abandoned events", id, n)
		}
	}
	// sql-guarantee: compared with the whole-table count above.
	completionCount(t, w, beforeAudit+1, `SELECT COUNT(*) FROM audit_log`)
	if n := len(ledgerEntries(t, a, ids.Nil, "run.abandoned")); n != 1 {
		t.Fatalf("%d run.abandoned receipts", n)
	}
	if n := len(operationJobs(t, a, workflowAbandonmentJob)); n != 1 {
		t.Fatalf("%d abandonment cleanup jobs", n)
	}
}

func TestAbandonmentRacesCompletion(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"run", "change"} {
		t.Run(target, func(t *testing.T) {
			a, root := abandonmentScenario(t, "landing", []schema.Step{{Name: "deploy", Uses: "deploy", With: map[string]any{"deployment": "main"}}})
			w := a.DB
			snapshot, err := a.Work.Snapshot(asPerson("alice"), root.ID, workflow.DetailView)
			if err != nil || len(snapshot.Steps) != 1 {
				t.Fatalf("deploy step: %+v %v", snapshot, err)
			}
			if err := a.Work.ExecuteStep(a.asEngine(context.Background()), snapshot.Steps[0].ID); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Deployment.Bundles.Cut(t.Context(), root.ProjectID, "tree", map[string]string{"web": "image"}, nil, []byte("contract"), deployment.BundleOrigin{RunID: root.ID, ChangeID: root.ChangeID}); err != nil {
				t.Fatal(err)
			}
			req := workflow.Abandonment{RunID: root.ID}
			if target == "change" {
				req.RunID = ids.Nil
				req.ChangeID = root.ChangeID
			}
			result := race(2, func(i int) int {
				if i == 0 {
					err := completeWorkflowForTest(a.Work, root)
					if err == nil {
						return 0
					}
					if !errors.Is(err, workflow.ErrRunMoved) {
						t.Error(err)
					}
					return 1
				}
				_, err := (abandonmentStore{db: w}).abandon(req)
				if err == nil {
					return 0
				}
				var conflict *workflow.AbandonConflict
				if !errors.As(err, &conflict) {
					t.Error(err)
				}
				return 1
			})
			if count(result, 0) != 1 {
				t.Fatalf("winners: %v", result)
			}
			run, err := workflow.ReadRunIn(context.Background(), w, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			state := workflow.ChangeShipped
			releases := 1
			event := "run.done"
			if run.Status == workflow.RunAbandoned {
				state = workflow.ChangeInProgress
				if target == "change" {
					state = workflow.ChangeAbandoned
				}
				releases = 0
				event = "run.abandoned"
			}
			requireChangeState(t, a, root.ChangeID, state)
			if got, err := a.Deployment.Releases.ForProject(context.Background(), root.ProjectID, 100); err != nil || len(got) != releases {
				t.Fatalf("releases: %d %v, want %d", len(got), err, releases)
			}
			// The scenario's only run: exactly one ending, the winner's.
			if done, abandoned := runEventCount(t, a, root.ID, "run.done"), runEventCount(t, a, root.ID, "run.abandoned"); done+abandoned != 1 || runEventCount(t, a, root.ID, event) != 1 {
				t.Fatalf("endings: done %d abandoned %d, want one %s", done, abandoned, event)
			}
		})
	}
}

func TestAbandonmentCleanupSurvivesRestartAndRetries(t *testing.T) {
	t.Parallel()
	original, root := abandonmentScenario(t, "working", nil)
	w := original.DB
	_, err := (abandonmentStore{db: w}).abandon(workflow.Abandonment{ChangeID: root.ChangeID})
	if err != nil {
		t.Fatal(err)
	}
	// Process ended after committing, before attempting cleanup.
	a := newApp(t, w, Config{DockerClient: newFakeDocker()})
	workspaces := filepath.Join(a.cfg.dataDir, "workspaces")
	if err := os.MkdirAll(a.cfg.dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaces, []byte("blocks workspace access"), 0600); err != nil {
		t.Fatal(err)
	}
	j := runQueuedJobForTest(t, a, workflowAbandonmentJob, fmt.Sprintf("change/%d/cleanup", root.ChangeID))
	if state := jobState(t, a, j.ID); (state != "available" && state != "retryable") || j.Attempt != 1 || !j.ScheduledAt.After(time.Now()) {
		t.Fatalf("cleanup failure not retryable: %s", state)
	}
	requireChangeState(t, a, root.ChangeID, workflow.ChangeAbandoned)
	if err := os.Remove(workspaces); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(workspaces, fmt.Sprintf("change-%d", root.ChangeID))
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}

	j = runQueuedJobForTest(t, a, workflowAbandonmentJob, fmt.Sprintf("change/%d/cleanup", root.ChangeID))
	if state := jobState(t, a, j.ID); state != "completed" {
		t.Fatalf("cleanup failed: %s", state)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace survived: %v", err)
	}
	if n := len(ledgerEntries(t, a, ids.Nil, "change.abandoned")); n != 1 {
		t.Fatalf("%d change.abandoned receipts", n)
	}
}

func TestAbandonmentFailureStopsSendBack(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"send back"} {
		t.Run(action, func(t *testing.T) {
			working := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "agent", Uses: "agent/pi"}}}}}
			landing := workflow.PreparedRun{
				Workflow: workflow.DefinitionRef{Name: "land", Version: 1},
				Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{
					Name: "finish", Steps: []schema.Step{{Name: "gate", Uses: "approval", With: map[string]any{
						"approvers": []string{"alice"}, "means": "the result is ready",
					}}},
				}}},
			}
			a, ctx, ch := changeScenario(t, working, landing)
			rebuildWork(t, a, func(deps *workflow.Dependencies) {
				deps.Automatic = []string{"agent/pi"}
				deps.Report = func(err error) { t.Error(err) }
				deps.Runners = map[string]workflow.Runner{"agent/pi": func(task *workflow.Execution) {
					if _, err := task.Claim(); err != nil {
						t.Error(err)
						return
					}
					if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
						t.Error(err)
					}
				}}
			})
			a.Work.Stop()
			work, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "test"})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			workSnapshot, err := a.Work.Snapshot(ctx, work.ID, workflow.DetailView)
			if err != nil || len(workSnapshot.Steps) != 1 {
				t.Fatalf("working run: %+v %v", workSnapshot, err)
			}
			agent := workSnapshot.Steps[0].ID
			if err := a.Work.ExecuteStep(a.asEngine(context.Background()), agent); err != nil {
				t.Fatal(err)
			}
			if advanced, err := a.Work.Advance(ctx, work.ID); err != nil || !advanced.Advanced || advanced.RunStatus != workflow.RunDone {
				t.Fatalf("complete working run: %+v %v", advanced, err)
			}
			root, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			landingSnapshot, err := a.Work.Snapshot(ctx, root.ID, workflow.DetailView)
			if err != nil || len(landingSnapshot.Steps) != 1 {
				t.Fatalf("landing run: %+v %v", landingSnapshot, err)
			}
			gate := landingSnapshot.Steps[0].ID
			if _, err := a.Work.Approve(ctx, workflow.Approval{StepID: gate, Decision: workflow.VoteReject, Comment: "try again"}); err != nil {
				t.Fatal(err)
			}
			dbtest.FailNextStatement(t, a.DB, "INSERT INTO audit_log")
			if _, err := a.Work.SendBack(asPerson("alice"), workflow.SendBack{ChangeID: ch.ID, Note: "try again"}); err == nil {
				t.Fatal("send back survived failure")
			}

			requireRunStatus(t, a, workflow.RunRunning, root.ID)
			if step := readStep(t, a, agent); step.Status != workflow.StatusPass {
				t.Fatalf("agent step: %s", step.Status)
			}
			if change := requireChangeState(t, a, ch.ID, workflow.ChangeLanding); change.RunID != root.ID {
				t.Fatalf("change run: %d, want %d", change.RunID, root.ID)
			}
		})
	}
}

func TestAbandonmentFailureKeepsWorkspace(t *testing.T) {
	t.Parallel()
	a, root := abandonmentScenario(t, "working", nil, Config{DockerClient: newFakeDocker()})
	w := a.DB
	workspace := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", root.ChangeID))
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(workspace, "work.txt")
	if err := os.WriteFile(file, []byte("work in progress"), 0600); err != nil {
		t.Fatal(err)
	}
	failNextJobInsert(t, a.DB)
	if _, err := (abandonmentStore{db: a.DB}).abandon(workflow.Abandonment{ChangeID: root.ChangeID}); err == nil {
		t.Fatal("failed abandonment was hidden")
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != "work in progress" {
		t.Fatalf("workspace changed: %q %v", content, err)
	}
	requireChangeState(t, a, root.ChangeID, workflow.ChangeInProgress)
	// sql-guarantee: rollback proves no job of any kind was queued; the jobs page lists only AppTown operations.
	completionCount(t, w, 0, `SELECT COUNT(*) FROM river_job`)
}
