package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestAgentSessionKeepsReceiptsAndLegacyDispatchNumbers(t *testing.T) {
	t.Parallel()
	q, run, step := activityFixture(t)
	ctx := context.Background()
	ref := RunRef{ID: run.ID, ProjectID: run.ProjectID}
	for range 3 {
		insertRunEventForTest(t, q.db, run.ID, "step.started", json.RawMessage(`{"step":"accept"}`))
	}
	insertRunEventForTest(t, q.db, run.ID, "step.started", json.RawMessage(`{"step":"other","sessionId":"wrong"}`))
	// Five attempts: the counter differs from both dispatch counts (3, 4).
	for attempt := int64(0); attempt < 5; attempt++ {
		if err := requireAttemptWrite(workflowsql.New(q.db.SQL()).StartParentCall(ctx, workflowsql.StartParentCallParams{ID: step, Attempt: attempt, OldStatus: StatusPending.String(), Status: StatusPending.String(), EvidenceJson: []byte(`{}`)})); err != nil {
			t.Fatal(err)
		}
	}
	counted := &countedQueries{Handle: q.db.SQL()}
	got, err := AgentSessionIn(ctx, counted, ref, step, RetryContinue)
	if err != nil || got.Dispatches != 3 || got.ID != fmt.Sprintf("step-%d-a2", step) || counted.reads != 1 {
		t.Fatalf("legacy %+v reads %d: %v", got, counted.reads, err)
	}
	insertRunEventForTest(t, q.db, run.ID, "step.started", json.RawMessage(`{"step":"accept","sessionId":"original-history"}`))
	for _, mode := range []RetryMode{RetryDefault, RetryContinue, RetryRestart} {
		got, err = AgentSessionIn(ctx, q.db, ref, step, mode)
		if err != nil || got.ID != "original-history" || got.Dispatches != 4 {
			t.Fatalf("receipt %s %+v: %v", mode, got, err)
		}
	}
	if _, err := AgentSessionIn(ctx, q.db, RunRef{ID: run.ID, ProjectID: idstest.N(999999)}, step, RetryContinue); err == nil {
		t.Fatal("foreign session admitted")
	}
	if _, err := AgentSessionIn(ctx, q.db, ref, step, RetryMode(255)); err == nil {
		t.Fatal("invalid retry mode admitted")
	}
	dbtest.FailNextStatement(t, q.db, "SELECT COUNT(e.id)")
	_, err = AgentSessionIn(ctx, q.db, ref, step, RetryContinue)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "42883" || !strings.Contains(pgerr.Message, "forge_injected_failure") {
		t.Fatalf("invented session or changed injected failure: %v", err)
	}
}

func TestAgentPrerequisitesAndReopenedFeedbackAreScoped(t *testing.T) {
	t.Parallel()
	q, run, step := activityFixture(t)
	ctx := context.Background()
	other := workflowTestProject(t, q.db, "private", "Private", "")
	foreign := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: other, WorkflowName: "private", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: "work", DefinitionJson: []byte(`{}`), Status: RunRunning.String()}).ID
	insertTextArtifactForTest(t, q.db, run.ID, "spec", "accepted", "", "present", time.Time{})
	insertTextArtifactForTest(t, q.db, foreign, "secret", "accepted", "", "foreign", time.Time{})
	counted := &countedQueries{Handle: q.db.SQL()}
	missing, err := MissingArtifactKindsIn(ctx, counted, run.ProjectID, []string{"secret", "spec", "other", "secret"})
	if err != nil || len(missing) != 3 || missing[0] != "secret" || missing[1] != "other" || missing[2] != "secret" || counted.reads != 1 {
		t.Fatalf("prerequisites %v reads %d: %v", missing, counted.reads, err)
	}
	changed, err := workflowsql.New(q.db.SQL()).RecordApproval(ctx, workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPending.String(), EvidenceJson: []byte(`{"lastRejection":{"at":"2026-09-12T00:00:00Z","comment":"try again","approver":"owner"}}`), ID: step, Attempt: 0, ExpectedStatus: StatusPending.String()})
	if err != nil || changed != 1 {
		t.Fatalf("store reopened feedback: changed=%d %v", changed, err)
	}
	feedback, err := latestFeedbackIn(ctx, q.db, RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil || feedback == nil || feedback.ID != step || feedback.Status != StatusPending || feedback.OwnedAttempt() != 0 {
		t.Fatalf("feedback %+v: %v", feedback, err)
	}
	feedback, err = latestFeedbackIn(ctx, q.db, RunRef{ID: run.ID, ProjectID: other})
	if err != nil || feedback != nil {
		t.Fatalf("foreign feedback %+v: %v", feedback, err)
	}
	dbtest.FailNextStatement(t, q.db, "-- name: MissingArtifactKinds")
	if _, err := MissingArtifactKindsIn(ctx, q.db, run.ProjectID, []string{"spec"}); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("hidden prerequisite read: %v", err)
	}
}
