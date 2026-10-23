package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestChangeStatesUsesOneScopedRead(t *testing.T) {
	t.Parallel()
	// Three changes: a read per change already differs at three.
	for _, size := range []int{1, 3} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			refs := []ChangeRef{{ID: run.ChangeID, ProjectID: run.ProjectID}}
			for i := 1; i < size; i++ {
				id := insertChangeForTest(t, q.db, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(), ProjectID: run.ProjectID, Type: "feature", Title: "bulk", State: ChangeShipped.String(), AcceptanceJson: []byte(`{}`), TitleOwnership: "human"})
				refs = append(refs, ChangeRef{ID: id, ProjectID: run.ProjectID})
			}
			refs = append(refs, refs[0], ChangeRef{ID: run.ChangeID, ProjectID: idstest.N(999999)}, ChangeRef{ID: idstest.N(100000), ProjectID: run.ProjectID})
			counted := &countedQueries{Handle: q.db.SQL()}
			out, err := ChangeStatesIn(context.Background(), counted, refs)
			if err != nil || len(out) != size || counted.reads != 1 {
				t.Fatalf("states %+v reads %d: %v", out, counted.reads, err)
			}
			if out[refs[0]] != ChangeInProgress {
				t.Fatal(out)
			}
			for _, ref := range refs[1:size] {
				if out[ref] != ChangeShipped {
					t.Fatal(out)
				}
			}
		})
	}
}

func TestChangeStateObservationsDistinguishAbsenceFailureAndTransaction(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ref := ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}
	q.db.Pool().SetMaxOpenConns(1)
	if err := q.db.InTx(context.Background(), func(tx *database.Tx) error {
		current, err := workflowsql.New(tx.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: ref.ID})
		if err != nil {
			return err
		}
		if err := requireRunWrite(workflowsql.New(tx.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), ID: ref.ID, ProjectID: ref.ProjectID, OldState: current.State, OldRunID: current.RunID, State: ChangeShipped.String(), RunID: current.RunID, Branch: current.Branch})); err != nil {
			return err
		}
		out, err := ChangeStatesIn(context.Background(), tx, []ChangeRef{ref})
		if err == nil && out[ref] != ChangeShipped {
			t.Fatal(out)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, q.db, "JOIN changes c")
	if out, err := ChangeStatesIn(context.Background(), q.db, []ChangeRef{ref}); err == nil || out != nil {
		t.Fatalf("read failure %+v: %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := ChangeStatesIn(ctx, q.db, nil); !errors.Is(err, context.Canceled) || out != nil {
		t.Fatalf("cancelled %+v: %v", out, err)
	}
	{
		current, err := workflowsql.New(q.db.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: ref.ID})
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(q.db.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), ID: ref.ID, ProjectID: current.ProjectID, OldState: current.State, OldRunID: current.RunID, State: "invalid", RunID: current.RunID, Branch: current.Branch}); err != nil || rows != 1 {
			t.Fatalf("stored change transition: rows=%d err=%v", rows, err)
		}
	}
	if out, err := ChangeStatesIn(context.Background(), q.db, []ChangeRef{ref}); err == nil || out != nil {
		t.Fatalf("invalid state %+v: %v", out, err)
	}
}

func TestSupersessionEligibilityKeepsProjectAndChangeLocks(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	{
		current, err := workflowsql.New(q.db.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: run.ChangeID})
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(q.db.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, ProjectID: current.ProjectID, OldState: current.State, OldRunID: current.RunID, State: "in_progress", RunID: current.RunID, Branch: current.Branch}); err != nil || rows != 1 {
			t.Fatalf("stored change transition: rows=%d err=%v", rows, err)
		}
	}
	tx, err := q.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if eligible, err := SupersessionEligibleIn(context.Background(), tx, ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}); err != nil || !eligible {
		t.Fatalf("eligibility %v: %v", eligible, err)
	}
	for _, probe := range []struct {
		query string
		id    ids.ID
	}{
		// sql-guarantee: This independent no-op UPDATE must block on the project's retained lock; an owner read is not the contention assertion.
		{`UPDATE projects SET name=name WHERE id=?`, run.ProjectID},
		// sql-guarantee: This independent no-op UPDATE must block on the Change's retained lock after eligibility returns.
		{`UPDATE changes SET title=title WHERE id=?`, run.ChangeID},
	} {
		err := q.db.InTx(context.Background(), func(other *database.Tx) error {
			if _, err := other.Exec(`SET LOCAL lock_timeout='100ms'`); err != nil {
				return err
			}
			_, err := other.Exec(probe.query, probe.id)
			return err
		})
		var locked *pgconn.PgError
		if !errors.As(err, &locked) || locked.Code != "55P03" {
			t.Fatalf("expected retained lock for %s: %v", probe.query, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSupersessionEligibilityChecksLifecycleAndScope(t *testing.T) {
	t.Parallel()
	for _, state := range []ChangeState{ChangeInProgress, ChangeLanding, ChangeShipped, ChangeAbandoned} {
		t.Run(state.String(), func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			{
				current, err := workflowsql.New(q.db.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: run.ChangeID})
				if err != nil {
					t.Fatal(err)
				}
				if rows, err := workflowsql.New(q.db.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, ProjectID: current.ProjectID, OldState: current.State, OldRunID: current.RunID, State: state.String(), RunID: current.RunID, Branch: current.Branch}); err != nil || rows != 1 {
					t.Fatalf("stored change transition: rows=%d err=%v", rows, err)
				}
			}
			q.db.Pool().SetMaxOpenConns(1)
			if err := q.db.InTx(context.Background(), func(tx *database.Tx) error {
				eligible, err := SupersessionEligibleIn(context.Background(), tx, ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID})
				if err == nil && eligible != (state == ChangeInProgress) {
					t.Fatalf("eligible %v for %s", eligible, state)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := q.db.InTx(context.Background(), func(tx *database.Tx) error {
				_, err := SupersessionEligibleIn(context.Background(), tx, ChangeRef{ID: run.ChangeID, ProjectID: idstest.N(999999)})
				return err
			}); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("foreign Change: %v", err)
			}
		})
	}
}

func TestProjectWorkOccupancyPreservesReplacementRule(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"waiting replacement", "other run", "landing", "live step", "orphan live step", "foreign step", "unattached run"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			q, run, step := activityFixture(t)
			want := true
			switch test {
			case "waiting replacement":
				want = false
			case "other run":
				_ = insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: "other", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "current", DefinitionJson: []byte(`{}`)}).ID
			case "landing":
				{
					current, err := workflowsql.New(q.db.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: run.ChangeID})
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, ProjectID: current.ProjectID, OldState: current.State, OldRunID: current.RunID, State: "landing", RunID: current.RunID, Branch: current.Branch}); err != nil || rows != 1 {
						t.Fatalf("stored change transition: rows=%d err=%v", rows, err)
					}
				}
			case "live step":
				{
					current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), step)
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "running", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
						t.Fatalf("stored step status: rows=%d err=%v", rows, err)
					}
				}
			case "orphan live step":
				{
					current, err := workflowsql.New(q.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "done"}); err != nil || rows != 1 {
						t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
					}
				}
				{
					current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), step)
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "running", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
						t.Fatalf("stored step status: rows=%d err=%v", rows, err)
					}
				}
			case "foreign step":
				other := workflowTestProject(t, q.db, "other", "Other", "")
				foreign := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: other, ChangeID: run.ChangeID, WorkflowName: "foreign", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(run.DefinitionJSON)})
				step = insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: foreign.ID, Stage: "finish", Name: "accept", Type: "approval", Status: StatusPending.String()})
				{
					current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), step)
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "running", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
						t.Fatalf("stored step status: rows=%d err=%v", rows, err)
					}
				}
				want = false
			case "unattached run":
				insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: "unattached", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(run.DefinitionJSON)})
			}
			out, err := database.Transact(context.Background(), q.db, func(tx *database.Tx) (bool, error) {
				return ProjectBusyIn(context.Background(), tx, OccupancyScope{ProjectID: run.ProjectID, ReplacingChangeID: run.ChangeID})
			})
			if err != nil || out != want {
				t.Fatalf("busy %v want %v: %v", out, want, err)
			}
		})
	}
}
