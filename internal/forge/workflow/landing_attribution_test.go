package workflow

import (
	"encoding/json"
	"testing"

	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/ids/idstest"
)

// A step as its records hold it; attributeStep reads values, not storage.
func attributedStep(t *testing.T, step Step, evidence any) *Step {
	t.Helper()
	if evidence != nil {
		raw, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		step.Evidence = raw
	}
	return &step
}

// An approval is attributed with the approving votes of its deciding round
// only: a vote from an earlier, rejected round and the rejection itself are
// not approvals of what lands. The declared approvers and the round's
// recorded sentence come with each vote.
func TestOnlyTheDecidingRoundsApprovalsAreAttributed(t *testing.T) {
	t.Parallel()
	step := attributedStep(t, Step{ID: idstest.N(3), Name: "review", Type: "approval", Status: StatusPass, Config: json.RawMessage(`{"approvers":["role:project-approver"],"means":"configured"}`)},
		ApprovalEvidence{
			PreviousDecisions: []ApprovalVote{{Approver: "early", Decision: VoteApprove, At: "2026-10-01T00:00:00Z"}, {Approver: "rejector", Decision: VoteReject, At: "2026-10-01T01:00:00Z"}},
			Decisions:         []ApprovalVote{{Approver: "alice", Decision: VoteApprove, At: "2026-10-09T03:12:00Z", Candidate: "abc"}},
			Quorum:            1, Approvals: 1, Means: "as recorded", Approver: "alice", Decision: VoteApprove, At: "2026-10-09T03:12:00Z", Candidate: "abc",
		})
	var a scm.Attribution
	if err := attributeStep(&a, step, true); err != nil {
		t.Fatal(err)
	}
	if len(a.Approvals) != 1 {
		t.Fatalf("approvals: %+v", a.Approvals)
	}
	got := a.Approvals[0]
	if got.By != "alice" || got.Means != "as recorded" || len(got.Declared) != 1 || got.Declared[0] != "role:project-approver" || got.Candidate != "abc" {
		t.Fatalf("approval: %+v", got)
	}
}

// A check's recorded result is said as it is: skipped lets a gate through
// but is not a pass, and an overridden step is an override, listed with
// its reason apart from any approval.
func TestChecksAreAttributedWithTheResultTheyRecorded(t *testing.T) {
	t.Parallel()
	var a scm.Attribution
	for _, step := range []*Step{
		{ID: idstest.N(1), Name: "tests", Type: "tests", Status: StatusPass, Attempt: 2},
		{ID: idstest.N(2), Name: "scan-image", Type: "scan", Status: StatusSkipped, Attempt: 1},
		{ID: idstest.N(3), Name: "lint", Type: "script", Status: StatusPass, Attempt: 1, OverrideBy: "bls", OverrideReason: "flaky mirror"},
		// One vote of a quorum of two, then overridden: the override decided it.
		attributedStep(t, Step{ID: idstest.N(4), Name: "approve", Type: "approval", Status: StatusPass, OverrideBy: "bls", OverrideReason: "on call"},
			ApprovalEvidence{Decisions: []ApprovalVote{{Approver: "alice", Decision: VoteApprove, At: "2026-10-09T03:12:00Z"}}, Quorum: 2, Approvals: 1}),
	} {
		if err := attributeStep(&a, step, true); err != nil {
			t.Fatal(err)
		}
	}
	want := []scm.AttributedCheck{{Step: "tests", StepID: idstest.N(1), Attempt: 2, Result: "passed"}, {Step: "scan-image", StepID: idstest.N(2), Attempt: 1, Result: "skipped"}, {Step: "lint", StepID: idstest.N(3), Attempt: 1, Result: "overridden"}}
	if len(a.Checks) != len(want) {
		t.Fatalf("checks: %+v", a.Checks)
	}
	for i := range want {
		if a.Checks[i] != want[i] {
			t.Errorf("check %d: %+v, want %+v", i, a.Checks[i], want[i])
		}
	}
	if len(a.Approvals) != 0 || len(a.Overrides) != 2 || a.Overrides[1] != (scm.AttributedOverride{Step: "approve", By: "bls", Reason: "on call"}) {
		t.Fatalf("an override was attributed as an approval: approvals %+v overrides %+v", a.Approvals, a.Overrides)
	}
}

// Approval evidence that cannot be read refuses the attribution; it never
// becomes an empty list of approvals.
func TestUnreadableApprovalEvidenceIsAnError(t *testing.T) {
	t.Parallel()
	var a scm.Attribution
	step := &Step{ID: idstest.N(9), Name: "review", Type: "approval", Status: StatusPass, Evidence: json.RawMessage(`[]`)}
	if err := attributeStep(&a, step, true); err == nil {
		t.Fatalf("unreadable evidence attributed as %+v", a)
	}
}
