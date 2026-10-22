package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

// Quorum is a Work model rule; authorization policy is covered by AppTown's authority tests.
func voteForTest(db *database.DB, id ids.ID, who string, decision ApprovalDecision, comment string) (*Step, error) {
	s := newService(db, authorityFunc(func(context.Context, operationPermission) (string, error) { return who, nil }), Runtime{})
	return s.Approve(context.Background(), Approval{StepID: id, Decision: decision, Comment: comment})
}
func mustApprovalEvidence(t *testing.T, body json.RawMessage) ApprovalEvidence {
	t.Helper()
	e, err := ReadApprovalEvidence(body)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestApprovalQuorumUnderRace(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	runID := run.ID
	// Quorum of two, three approvers at once: every vote is kept, exactly
	// one decides, and the losers see "already decided" rather than a
	// lost vote.
	stepID := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: runID, Stage: "finish", Name: "accept", Type: "approval", ConfigJson: []byte(`{"quorum":2}`), Status: StatusPending.String()})
	users := []string{"ann", "bob", "cy"}
	errs := race(3, func(i int) int {
		if _, err := voteForTest(db, stepID, users[i], VoteApprove, "ok"); err != nil {
			if errors.Is(err, errStepDecisionConflict) {
				return 1
			}
			return -1
		}
		return 0
	})
	if count(errs, -1) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	st, _ := readStep(context.Background(), db, stepID)
	ev := mustApprovalEvidence(t, st.Evidence)
	if st.Status != StatusPass || ev.Approvals < 2 || len(ev.Decisions) < 2 {
		t.Fatalf("quorum must be met with every counted vote kept: status=%s approvals=%d votes=%d", st.Status, ev.Approvals, len(ev.Decisions))
	}
	events := countEvents(storedRunEvents(t, db, runID), ofKind("approval"))
	if events != len(ev.Decisions) {
		t.Fatalf("one event per recorded vote: %d events, %d votes", events, len(ev.Decisions))
	}
	// Quorum of one, three approvers: exactly one deciding transition.
	single := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: runID, Stage: "finish", Name: "gate", Type: "approval", ConfigJson: []byte(`{}`), Status: StatusPending.String()})
	res := race(3, func(i int) int {
		if _, err := voteForTest(db, single, users[i], VoteApprove, ""); err != nil {
			return 1
		}
		return 0
	})
	if count(res, 0) != 1 {
		t.Fatalf("exactly one approver decides a quorum-of-one step: %v", res)
	}
}

func TestApprovalRollsBackWhole(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	runID := run.ID
	stepID := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: runID, Stage: "finish", Name: "accept", Type: "approval", ConfigJson: []byte(`{"quorum":2}`), Status: StatusPending.String()})
	dbtest.FailNextStatement(t, db, "INSERT INTO run_events")
	if _, err := voteForTest(db, stepID, "ann", VoteApprove, "first"); err == nil {
		t.Fatal("the injected failure must surface")
	}
	st, _ := readStep(context.Background(), db, stepID)
	if st.Status != StatusPending || strings.Contains(string(st.Evidence), "ann") {
		t.Fatalf("a failed vote must leave no trace: %s %s", st.Status, st.Evidence)
	}
	if _, err := voteForTest(db, stepID, "ann", VoteApprove, "again"); err != nil {
		t.Fatalf("the vote can be retried: %v", err)
	}
}
