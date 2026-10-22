package workflow

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestApprovalWithdrawalPreservesVotesAndReopensAsANewRound(t *testing.T) {
	t.Parallel()
	const raw = `{"identifier":9007199254740993,"decisions":[{"approver":"alice","principal":"00000000-0015-7000-8000-000000000000","via":"session","decision":"approve","trace":9007199254740993}],"approvals":99}`
	document, err := readApprovalDocument([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	withdrawal := ApprovalWithdrawal{"operator", "no longer needed", "2026-09-11T09:00:00Z"}
	closed, status, err := document.evidence.withdraw(StatusNeedsHuman, ApprovalConfig{Quorum: 2}, withdrawal)
	if err != nil || status != StatusFail || closed.Withdrawal == nil || *closed.Withdrawal != withdrawal || len(closed.Decisions) != 1 || closed.Approvals != 1 || closed.Decision != 0 || closed.Approver != "" {
		t.Fatalf("withdrawal: %+v %s %v", closed, status, err)
	}
	if document.evidence.Withdrawal != nil || document.evidence.Approvals != 99 {
		t.Fatal("input evidence was changed")
	}
	if _, _, _, err := closed.cast(StatusPending, ApprovalConfig{Quorum: 2}, ApprovalVote{Approver: "bob", Principal: idstest.N(22), Via: "session", Decision: VoteApprove}); err == nil {
		t.Fatal("withdrawn round accepted a vote")
	}
	reopened, err := closed.reopen(status)
	if err != nil || reopened.Withdrawal != nil || len(reopened.PreviousWithdrawals) != 1 || reopened.PreviousWithdrawals[0] != withdrawal || len(reopened.Decisions) != 0 || len(reopened.PreviousDecisions) != 1 || reopened.LastRejection != nil {
		t.Fatalf("reopen: %+v %v", reopened, err)
	}
	fresh, status, pending, err := reopened.cast(StatusPending, ApprovalConfig{Quorum: 2}, ApprovalVote{Approver: "alice", Principal: idstest.N(21), Via: "session", Decision: VoteApprove})
	if err != nil || !pending || status != StatusPending || fresh.Approvals != 1 {
		t.Fatalf("new round: %+v %s %v", fresh, status, err)
	}
	body, err := document.encode(fresh)
	if err != nil || !strings.Contains(string(body), `"identifier":9007199254740993`) || !strings.Contains(string(body), `"trace":9007199254740993`) || !strings.Contains(string(body), `"previousWithdrawals"`) {
		t.Fatalf("history: %s %v", body, err)
	}
	fresh.PreviousWithdrawals[0].Reason = "changed"
	closed.Withdrawal.Reason = "changed"
	if reopened.PreviousWithdrawals[0].Reason != withdrawal.Reason {
		t.Fatal("history shares mutable state")
	}
}

func TestApprovalWithdrawalRefusesClosedOrInvalidRounds(t *testing.T) {
	t.Parallel()
	valid := ApprovalWithdrawal{"operator", "not needed", "now"}
	for _, state := range []StepState{0, StatusRunning, StatusPass, StatusFail, StatusError, StatusSkipped} {
		if _, _, err := (ApprovalEvidence{}).withdraw(state, ApprovalConfig{}, valid); err == nil {
			t.Fatalf("withdrew %s", state)
		}
	}
	for _, evidence := range []ApprovalEvidence{
		{Withdrawal: &valid},
		{Decisions: []ApprovalVote{{Approver: "alice", Principal: idstest.N(21), Via: "session", Decision: VoteReject}}},
		{Decisions: []ApprovalVote{{Approver: "alice", Principal: idstest.N(21), Via: "session", Decision: VoteApprove}}},
		{Decisions: []ApprovalVote{{Approver: "alice", Principal: idstest.N(21), Via: "session", Decision: VoteApprove}, {Approver: "alice", Principal: idstest.N(21), Via: "session", Decision: VoteApprove}}},
	} {
		if _, _, err := evidence.withdraw(StatusPending, ApprovalConfig{}, valid); err == nil {
			t.Fatalf("closed/corrupt round admitted: %+v", evidence)
		}
	}
	for _, withdrawal := range []ApprovalWithdrawal{{Reason: "reason", At: "now"}, {By: "actor", At: "now"}, {By: "actor", Reason: "reason"}} {
		if _, _, err := (ApprovalEvidence{}).withdraw(StatusPending, ApprovalConfig{}, withdrawal); err == nil {
			t.Fatal("missing withdrawal facts accepted")
		}
	}
	for _, raw := range []string{`{"withdrawal":{}}`, `{"previousWithdrawals":[{}]}`, `{"withdrawal":{"by":"a","reason":"why","at":"now"},"decision":"approve"}`} {
		if _, err := ReadApprovalEvidence([]byte(raw)); err == nil {
			t.Fatalf("invalid stored closure accepted: %s", raw)
		}
	}
}

func TestReopenedApprovalCanBeWithdrawnWithoutReusingItsOldVote(t *testing.T) {
	t.Parallel()
	old := ApprovalEvidence{Approver: "alice", Decision: VoteReject, Comment: "fix it", At: "old", Decisions: []ApprovalVote{{Approver: "alice", Principal: idstest.N(21), Via: "session", Decision: VoteReject, Comment: "fix it", At: "old"}}}
	reopened, err := old.reopen(StatusFail)
	if err != nil || reopened.Decision != 0 || reopened.Approver != "" || reopened.Comment != "" || reopened.LastRejection.Comment != "fix it" {
		t.Fatalf("stale closing decision: %+v %v", reopened, err)
	}
	withdrawn, status, err := reopened.withdraw(StatusPending, ApprovalConfig{}, ApprovalWithdrawal{"operator", "no longer needed", "new"})
	if err != nil || status != StatusFail || withdrawn.Withdrawal == nil || len(withdrawn.PreviousDecisions) != 1 || withdrawn.LastRejection.Comment != "fix it" {
		t.Fatalf("withdrawal: %+v %s %v", withdrawn, status, err)
	}
}
