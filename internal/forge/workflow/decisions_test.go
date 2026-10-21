package workflow

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestDecisionServiceRequiresAuthorityBeforeStorage(t *testing.T) {
	t.Parallel()
	s := newService(nil, nil, Runtime{})
	if _, err := s.Approve(context.Background(), Approval{StepID: idstest.N(1), Decision: VoteApprove}); !errors.Is(err, ErrCallerRequired) {
		t.Fatal(err)
	}
	if _, err := s.Override(context.Background(), Override{StepID: idstest.N(1), Reason: "reviewed"}); !errors.Is(err, ErrCallerRequired) {
		t.Fatal(err)
	}
}
func TestDecisionServiceOwnsEligibilityAndExactRoundReceipts(t *testing.T) {
	t.Parallel()
	store, run, _, _ := retryFixture(t)
	id := insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "approval", Status: StatusPending.String(), ConfigJson: []byte(`{"quorum":2,"approvers":["reviewers"]}`)})
	if rows, err := workflowsql.New(store.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, ExpectedStatus: StatusPending.String(), Status: StatusPending.String(), EvidenceJson: []byte(`{"n":9007199254740993}`)}); err != nil || rows != 1 {
		t.Fatalf("stored evidence: rows=%d err=%v", rows, err)
	}
	var identity string
	s := newService(store.db, authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		if p.Action != actionIdentify {
			want := operationPermission{Action: actionApproveStep, StepID: id, ProjectID: run.ProjectID, ChangeID: run.ChangeID, RunID: run.ID, Approvers: []string{"reviewers"}}
			if !reflect.DeepEqual(p, want) {
				t.Fatalf("scope/eligibility %+v", p)
			}
			p.Approvers[0] = "mutated port input"
		}
		return identity, nil
	}), Runtime{})
	identity = "first"
	result, err := s.Approve(context.Background(), Approval{StepID: id, Decision: VoteApprove, Comment: "read it"})
	if err != nil || result.Status != StatusPending {
		t.Fatalf("first vote %+v %v", result, err)
	}
	if _, err := s.Approve(context.Background(), Approval{StepID: id, Decision: VoteApprove}); !errors.Is(err, errStepDecisionConflict) {
		t.Fatalf("duplicate vote %v", err)
	}
	identity = "second"
	result, err = s.Approve(context.Background(), Approval{StepID: id, Decision: VoteApprove})
	if err != nil || result.Status != StatusPass || result.OwnedAttempt() != 0 {
		t.Fatalf("quorum %+v %v", result, err)
	}
	step := storedStep(t, store.db, id)
	wantJSONField(t, "evidence", step.Evidence, "n", "9007199254740993", "approvals", "2")
	wantJSONField(t, "config", step.Config, "approvers.0", "reviewers")
	wantEventCount(t, store.db, 2, ofKind("approval"))
}
