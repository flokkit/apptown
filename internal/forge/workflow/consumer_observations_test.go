package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func storedAgentAttempt(t *testing.T, db *database.DB, runID ids.ID, name string, attempt int64, status StepState) ids.ID {
	t.Helper()
	id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: runID, Stage: "finish", Name: name, Type: "agent/pi", Status: StatusPending.String(), ConfigJson: json.RawMessage(`{}`)})
	q := workflowsql.New(db.SQL())
	for current := int64(0); current < attempt; current++ {
		if n, err := q.StartAttempt(context.Background(), workflowsql.StartAttemptParams{At: time.Now(), ID: id, Attempt: current}); err != nil || n != 1 {
			t.Fatalf("start stored agent attempt %d: %d %v", current+1, n, err)
		}
		if current+1 < attempt {
			if n, err := q.FinishAttempt(context.Background(), workflowsql.FinishAttemptParams{At: time.Now(), Status: StatusFail.String(), EvidenceJson: json.RawMessage(`{}`), ID: id, Attempt: current + 1}); err != nil || n != 1 {
				t.Fatalf("finish stored agent attempt %d: %d %v", current+1, n, err)
			}
			if n, err := q.RetryStep(context.Background(), workflowsql.RetryStepParams{ConfigJson: json.RawMessage(`{}`), ID: id, Attempt: current + 1}); err != nil || n != 1 {
				t.Fatalf("retry stored agent attempt %d: %d %v", current+1, n, err)
			}
		}
	}
	if n, err := q.FinishAttempt(context.Background(), workflowsql.FinishAttemptParams{At: time.Now(), Status: status.String(), EvidenceJson: json.RawMessage(`{}`), ID: id, Attempt: attempt}); err != nil || n != 1 {
		t.Fatalf("finish stored agent: %d %v", n, err)
	}
	return id
}

func TestConformanceWorkObservationsOwnScopeAndState(t *testing.T) {
	t.Parallel()
	q, run, step := scopeActivityRecord(t, "land", ids.Nil)
	ctx := context.Background()
	transitionScopeChange(t, q.db, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, State: "shipped"})
	if n, err := workflowsql.New(q.db.SQL()).SetChangeBranch(context.Background(), workflowsql.SetChangeBranchParams{ID: run.ChangeID, ProjectID: run.ProjectID, Branch: "apptown/change-1"}); err != nil || n != 1 {
		t.Fatalf("SetChangeBranch: %d %v", n, err)
	}
	other := workflowTestProject(t, q.db, "foreign", "Foreign", "")
	foreign := insertScopeChange(t, q.db, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(), ProjectID: other, Type: "feature", Title: "private", State: "shipped", Branch: "private-branch"})
	insertScopeChange(t, q.db, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(), ProjectID: run.ProjectID, Type: "feature", Title: "unfinished", State: "in_progress", Branch: "unfinished-branch"})
	tips, err := ShippedBranchesIn(ctx, q.db, run.ProjectID)
	if err != nil || len(tips) != 1 || tips[0] != (ChangeBranch{ID: run.ChangeID, Branch: "apptown/change-1"}) {
		t.Fatalf("tips %+v: %v", tips, err)
	}
	recordScopeStep(t, q.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Status: "pass", EvidenceJson: json.RawMessage(`{"approver":"named person","decision":"approve","decisions":[{"approver":"named person","principal":"` + idstest.N(5).String() + `","via":"session","decision":"approve"}]}`)})
	// The approver is the person their vote recorded, principal and way.
	for _, tc := range []struct {
		ref  ChangeRef
		want access.Actor
	}{
		{ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}, access.Actor{Principal: idstest.N(5), Name: "named person", Via: "session"}},
		{ChangeRef{ID: run.ChangeID, ProjectID: other}, access.Actor{}},
		{ChangeRef{ID: foreign, ProjectID: run.ProjectID}, access.Actor{}},
	} {
		got, err := LandingApproverIn(ctx, q.db, tc.ref)
		if err != nil || got != tc.want {
			t.Fatalf("scope %+v: %q %v", tc.ref, got, err)
		}
	}
	if n, err := workflowsql.New(q.db.SQL()).RecordApproval(ctx, workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusFail.String(), EvidenceJson: json.RawMessage(`{"approver":"named person"}`), ID: step, Attempt: 0, ExpectedStatus: StatusPass.String()}); err != nil || n != 1 {
		t.Fatalf("invalidate landing approval: %d %v", n, err)
	}
	if who, err := LandingApproverIn(ctx, q.db, ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}); err != nil || who != (access.Actor{}) {
		t.Fatalf("unaccepted evidence: %q %v", who, err)
	}
	dbtest.FailNextStatement(t, q.db, "SELECT s.evidence_json")
	if _, err := LandingApproverIn(ctx, q.db, ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("hidden read failure: %v", err)
	}
}

func TestChatContinuationIsCanonicalAndProjectScoped(t *testing.T) {
	t.Parallel()
	q, run, step := activityFixture(t)
	ctx := context.Background()
	first := storedAgentAttempt(t, q.db, run.ID, "first", 4, StatusFail)
	last := storedAgentAttempt(t, q.db, run.ID, "last", 7, StatusPass)
	insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "future", Name: "other-stage", Type: "agent/pi", Status: StatusPending.String(), ConfigJson: json.RawMessage(`{}`)})
	service := newService(q.db, authorityFunc(func(context.Context, operationPermission) (string, error) { return "reader", nil }), Runtime{})
	got, err := service.ChatContinuation(ctx, run.ChangeID, step)
	if err != nil || got.Run.ID != run.ID || got.Step.ID != step || got.Producer == nil || got.Producer.ID != last || got.Producer.Attempt != 7 || got.Source.ProjectID != run.ProjectID {
		t.Fatalf("continuation %+v: %v", got, err)
	}
	// A different stage has no producing agent, so the caller can describe a
	// fresh conversation without inventing a session identity or source path.
	step = insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "no-agent", Name: "accept", Type: "approval", Status: "pending"})
	got, err = service.ChatContinuation(ctx, run.ChangeID, step)
	if err != nil || got.Producer != nil || !got.Source.RunID.IsZero() {
		t.Fatalf("missing producer %+v: %v", got, err)
	}
	other := workflowTestProject(t, q.db, "foreign", "Foreign", "")
	foreign := createScopeChange(t, q.db, ChangeDraft{ProjectID: other, Type: "feature", Title: "foreign"})
	if _, err := service.ChatContinuation(ctx, foreign, first); err == nil {
		t.Fatal("foreign Step associated with requested Change")
	}
	// sql-guarantee: Move the existing run's change association after a successful continuation read to test source revalidation; admission fixes change_id and no generated run mutation reparents it.
	completionExec(t, q.db, `UPDATE runs SET change_id=? WHERE id=?`, foreign, run.ID)
	if _, err := service.ChatContinuation(ctx, run.ChangeID, first); err == nil {
		t.Fatal("changed Run association leaked")
	}
	// sql-guarantee: Restore the original run.change_id before the independent Step-read failure case; existing mutations cannot repair a run's immutable change association.
	completionExec(t, q.db, `UPDATE runs SET change_id=? WHERE id=?`, run.ChangeID, run.ID)
	dbtest.FailNextStatement(t, q.db, "-- name: RunSteps")
	if _, err := service.ChatContinuation(ctx, run.ChangeID, first); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("hidden Step read failure: %v", err)
	}
}

func TestChangeRecordBatchExplicitProjectNarrowsBroadAuthority(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	other := workflowTestProject(t, q.db, "other", "Other", "")
	foreign := createScopeChange(t, q.db, ChangeDraft{ProjectID: other, Type: "feature", Title: "private"})
	s := newService(q.db, aggregateAuthority{operationAuthority: authorityFunc(func(context.Context, operationPermission) (string, error) { return "reader", nil }), read: func(context.Context, commandAction) ([]ids.ID, error) { return []ids.ID{run.ProjectID, other}, nil }}, Runtime{})
	got, err := s.ChangeRecords(context.Background(), run.ProjectID, []ids.ID{run.ChangeID, foreign})
	if err != nil || len(got) != 1 || got[0].ID != run.ChangeID {
		t.Fatalf("project selection %+v: %v", got, err)
	}
	got, err = s.ChangeRecords(context.Background(), ids.Nil, []ids.ID{run.ChangeID, foreign})
	if err != nil || len(got) != 2 {
		t.Fatalf("explicit instance selection %+v: %v", got, err)
	}
}

func insertScopeChange(t *testing.T, db *database.DB, in workflowsql.InsertChangeParams) ids.ID {
	t.Helper()
	in.AcceptanceJson = json.RawMessage(`{}`)
	in.TitleOwnership = "human"
	return insertChangeForTest(t, db, in)
}
func scopeActivityRecord(t *testing.T, name string, adoption ids.ID) (queries, *Run, ids.ID) {
	t.Helper()
	db := testDB(t)
	project := workflowTestProject(t, db, "completion", "Completion", "")
	change := admissionTestChange(t, db, project, "A change")
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: project, ChangeID: change, WorkflowName: name, WorkflowVersion: 1, Kind: "pipeline", Status: "running", CurrentStage: "finish", AdoptionID: adoption, DefinitionJson: json.RawMessage(`{"kind":"pipeline","role":"working","stages":[{"name":"finish"}]}`)})
	step := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "approval", Status: "pending"})
	return queries{db: db}, run, step
}
