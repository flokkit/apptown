package workflow

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"flokkit.io/forge/internal/ids/idstest"
)

func TestApprovalModelQuorumAndReopenedRounds(t *testing.T) {
	t.Parallel()
	cfg := ApprovalConfig{Quorum: 2, Means: "reviewed this revision"}
	initial := ApprovalEvidence{Approvals: 999}
	first, status, pending, err := initial.cast(StatusPending, cfg, ApprovalVote{Approver: "alice", Decision: VoteApprove, At: "first"})
	if err != nil || !pending || status != StatusPending || first.Approvals != 1 || first.Decision != 0 || first.Means != cfg.Means {
		t.Fatalf("first vote: %+v %s %v %v", first, status, pending, err)
	}
	if initial.HasVoted("alice") || !first.HasVoted("alice") {
		t.Fatal("vote mutated its input or was not saved")
	}
	rejected, status, pending, err := first.cast(status, cfg, ApprovalVote{Approver: "bob", Decision: VoteReject, Comment: "fix the bug", At: "second"})
	if err != nil || pending || status != StatusFail || rejected.Approvals != 1 || rejected.Decision != VoteReject {
		t.Fatalf("rejection: %+v %s %v %v", rejected, status, pending, err)
	}
	reopened, err := rejected.reopen(status)
	if err != nil || len(reopened.Decisions) != 0 || len(reopened.PreviousDecisions) != 2 || reopened.LastRejection.Comment != "fix the bug" || reopened.Approvals != 0 {
		t.Fatalf("reopen: %+v %v", reopened, err)
	}
	next, status, pending, err := reopened.cast(StatusPending, cfg, ApprovalVote{Approver: "alice", Decision: VoteApprove, At: "third"})
	if err != nil || !pending || next.Approvals != 1 {
		t.Fatalf("fresh round: %+v %s %v %v", next, status, pending, err)
	}
	done, status, pending, err := next.cast(status, cfg, ApprovalVote{Approver: "bob", Decision: VoteApprove, At: "fourth"})
	if err != nil || pending || status != StatusPass || done.Approvals != 2 || done.Approver != "bob" || done.Decision != VoteApprove || len(done.PreviousDecisions) != 2 {
		t.Fatalf("quorum: %+v %s %v %v", done, status, pending, err)
	}
	if _, _, _, err := done.cast(status, cfg, ApprovalVote{Approver: "third", Decision: VoteReject}); err == nil {
		t.Fatal("a closed round accepted another vote")
	}
	if _, err := done.reopen(status); err == nil {
		t.Fatal("a passed round reopened as a rejection")
	}
	done.PreviousDecisions[0].Comment = "changed"
	done.LastRejection.Comment = "changed"
	if reopened.PreviousDecisions[0].Comment == "changed" || reopened.LastRejection.Comment == "changed" {
		t.Fatal("round snapshots share mutable history")
	}
	// A step that names no quorum is decided by one approval, whether its
	// configuration is read from the step or left unset.
	named, err := ReadApprovalConfig([]byte(`{"approvers":["bls"]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []ApprovalConfig{named, {}} {
		one, status, pending, err := ApprovalEvidence{}.cast(StatusPending, cfg, ApprovalVote{Approver: "bls", Decision: VoteApprove, At: "only"})
		if err != nil || pending || status != StatusPass || one.Quorum != 1 || one.Approvals != 1 || one.Approver != "bls" {
			t.Fatalf("default quorum %+v: %+v %s %v %v", cfg, one, status, pending, err)
		}
	}
}

func TestApprovalModelRejectsInvalidVotesAndContradictoryRounds(t *testing.T) {
	t.Parallel()
	valid := ApprovalVote{Approver: "new", Decision: VoteApprove}
	for _, tc := range []struct {
		name     string
		state    StepState
		evidence ApprovalEvidence
		vote     ApprovalVote
	}{
		{"missing decision", StatusPending, ApprovalEvidence{}, ApprovalVote{Approver: "alice"}},
		{"invalid decision", StatusPending, ApprovalEvidence{}, ApprovalVote{Approver: "alice", Decision: 255}},
		{"missing actor", StatusPending, ApprovalEvidence{}, ApprovalVote{Decision: VoteApprove}},
		{"duplicate voter", StatusPending, ApprovalEvidence{Decisions: []ApprovalVote{valid}}, valid},
		{"duplicate saved votes", StatusPending, ApprovalEvidence{Decisions: []ApprovalVote{{Approver: "old", Decision: VoteApprove}, {Approver: "old", Decision: VoteApprove}}}, valid},
		{"rejected current round", StatusPending, ApprovalEvidence{Decisions: []ApprovalVote{{Approver: "old", Decision: VoteReject}}}, valid},
		{"quorum already met", StatusPending, ApprovalEvidence{Decisions: []ApprovalVote{{Approver: "old", Decision: VoteApprove}, {Approver: "other", Decision: VoteApprove}}}, valid},
		{"running", StatusRunning, ApprovalEvidence{}, valid},
		{"error", StatusError, ApprovalEvidence{}, valid},
		{"skipped", StatusSkipped, ApprovalEvidence{}, valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := tc.evidence.cast(tc.state, ApprovalConfig{Quorum: 2}, tc.vote)
			if err == nil {
				t.Fatal("invalid vote admitted")
			}
		})
	}
}

func TestApprovalDecisionAndConfigurationDecodeStrictly(t *testing.T) {
	t.Parallel()
	for _, d := range []ApprovalDecision{VoteApprove, VoteReject} {
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var back ApprovalDecision
		if err := json.Unmarshal(raw, &back); err != nil || back != d {
			t.Fatalf("decision roundtrip: %s %v", back, err)
		}
	}
	for _, raw := range []string{`""`, `"accept"`, `"APPROVE"`, `null`, `1`} {
		old := VoteApprove
		if err := json.Unmarshal([]byte(raw), &old); err == nil || old != VoteApprove {
			t.Fatalf("invalid %s changed vote: %s %v", raw, old, err)
		}
	}
	if _, err := json.Marshal(ApprovalDecision(0)); !errors.Is(err, errApprovalDecision) {
		t.Fatalf("invalid decision serialized: %v", err)
	}
	for _, raw := range []string{`{"quorum":"2"}`, `{"quorum":-1}`, `{"quorum":1.5}`, `{"approvers":"alice"}`, `{"means":false}`, `null`, `[]`} {
		if _, err := ReadApprovalConfig([]byte(raw)); err == nil {
			t.Fatalf("invalid configuration lowered the bar: %s", raw)
		}
	}
	cfg, err := ReadApprovalConfig([]byte(`{"extra":9007199254740993}`))
	if err != nil || cfg.Quorum != 1 {
		t.Fatalf("default quorum: %+v %v", cfg, err)
	}
	for _, raw := range []string{`{"decisions":[{"approver":"a","decision":"unknown"}]}`, `{"decisions":[null]}`, `{"previousDecisions":[{}]}`, `{"lastRejection":{"decision":"approve"}}`, `[]`, `null`} {
		if _, err := ReadApprovalEvidence([]byte(raw)); err == nil {
			t.Fatalf("invalid evidence admitted: %s", raw)
		}
	}
}

func TestApprovalCodecPreservesOpaqueMetadataAcrossRounds(t *testing.T) {
	t.Parallel()
	const number = "9007199254740993"
	document, err := readApprovalDocument([]byte(`{"identifier":` + number + `,"decisions":[{"approver":"alice","decision":"reject","trace":` + number + `}],"approver":"alice","decision":"reject","comment":"fix"}`))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := document.evidence.reopen(StatusFail)
	if err != nil {
		t.Fatal(err)
	}
	body, err := document.encode(reopened)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"identifier":`+number) || !strings.Contains(string(body), `"trace":`+number) {
		t.Fatalf("reopening lost exact metadata: %s", body)
	}
	next, _, _, err := reopened.cast(StatusPending, ApprovalConfig{Quorum: 2}, ApprovalVote{Approver: "alice", Decision: VoteApprove})
	if err != nil {
		t.Fatal(err)
	}
	body, err = document.encode(next)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"trace":`+number) || len(document.evidence.Decisions) != 1 {
		t.Fatalf("new round lost history: %s", body)
	}
	// Unknown feedback fields remain attached to that rejection during voting.
	document, err = readApprovalDocument([]byte(`{"lastRejection":{"approver":"old","comment":"fix","trace":` + number + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	next, _, _, err = document.evidence.cast(StatusPending, ApprovalConfig{Quorum: 2}, ApprovalVote{Approver: "alice", Decision: VoteApprove})
	if err != nil {
		t.Fatal(err)
	}
	body, err = document.encode(next)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"trace":`+number) {
		t.Fatalf("feedback lost metadata: %s", body)
	}
	next.LastRejection.original[0] = '['
	if document.evidence.LastRejection.original[0] != '{' {
		t.Fatal("opaque metadata shares mutable backing storage")
	}
}

// The typed shapes write exactly the keys the readers look for.
func TestApprovalEvidenceWireShape(t *testing.T) {
	t.Parallel()
	ev := ApprovalEvidence{Decisions: []ApprovalVote{{Approver: "bls", Decision: VoteApprove, Comment: "ok", At: "t1"}}, Quorum: 1, Approvals: 1,
		Means: "I looked", Approver: "bls", Decision: VoteApprove, Comment: "ok", At: "t1"}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"decisions"`, `"quorum"`, `"approvals"`, `"means"`, `"approver"`, `"decision"`, `"comment"`, `"at"`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("approval evidence missing %s", k)
		}
	}
	back, err := ReadApprovalEvidence(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.Approver != "bls" || len(back.Decisions) != 1 || back.LastRejection != nil {
		t.Fatalf("round trip: %+v", back)
	}
	// A reopened approval keeps its last rejection where feedback reads it.
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["lastRejection"] = map[string]any{"approver": "bls", "comment": "no", "at": "t2"}
	raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ReadApprovalEvidence(raw)
	if err != nil {
		t.Fatal(err)
	}
	if again.LastRejection == nil || again.LastRejection.At != "t2" {
		t.Fatalf("lastRejection must decode: %+v", again)
	}
}

// Approvals that form a quorum are for one commit; a rejection closes the
// round whatever commit the earlier approvals named.
func TestApprovalQuorumIsForOneCommitButAnyRejectionCloses(t *testing.T) {
	t.Parallel()
	cfg := ApprovalConfig{Quorum: 2, Means: "reviewed this revision"}
	first, status, _, err := ApprovalEvidence{}.cast(StatusPending, cfg, ApprovalVote{Approver: "alice", Decision: VoteApprove, Candidate: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := first.cast(status, cfg, ApprovalVote{Approver: "bob", Decision: VoteApprove, Candidate: "y"}); err == nil {
		t.Fatal("a quorum formed from approvals of two commits")
	}
	rejected, status, pending, err := first.cast(status, cfg, ApprovalVote{Approver: "bob", Decision: VoteReject})
	if err != nil || pending || status != StatusFail {
		t.Fatalf("a rejection naming no commit: %s %v %v", status, pending, err)
	}
	if rejected.Decision != VoteReject {
		t.Fatalf("the round was not closed by the rejection: %+v", rejected)
	}
}

// A reopened round that approves another commit records that commit, not
// the one the earlier, rejected round named: the stored candidate is a
// typed field, never carried over as opaque metadata.
func TestAReopenedRoundRecordsTheCommitItApproved(t *testing.T) {
	t.Parallel()
	document, err := readApprovalDocument([]byte(`{"decisions":[{"approver":"alice","decision":"reject","candidate":"old"}],"approver":"alice","decision":"reject","candidate":"old","quorum":1}`))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := document.evidence.reopen(StatusFail)
	if err != nil {
		t.Fatal(err)
	}
	next, status, _, err := reopened.cast(StatusPending, ApprovalConfig{Quorum: 1}, ApprovalVote{Approver: "bob", Decision: VoteApprove, Candidate: "new"})
	if err != nil || status != StatusPass {
		t.Fatalf("approve: %s %v", status, err)
	}
	body, err := document.encode(next)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := ReadApprovalEvidence(body)
	if err != nil || stored.Candidate != "new" {
		t.Fatalf("the approved round names %q, want new: %s %v", stored.Candidate, body, err)
	}
}

// A quorum counts people, not names: one person voting under two names
// (renamed between votes) is refused, and a vote AppTown could not
// resolve to a person (an account removed before principals) is not
// counted as another one.
func TestAQuorumCountsPeopleNotNames(t *testing.T) {
	t.Parallel()
	cfg := ApprovalConfig{Quorum: 2}
	alice := idstest.N(1)
	first, status, _, err := ApprovalEvidence{}.cast(StatusPending, cfg, ApprovalVote{Approver: "alice", Principal: alice, Via: "session", Decision: VoteApprove})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := first.cast(status, cfg, ApprovalVote{Approver: "alicia", Principal: alice, Via: "session", Decision: VoteApprove}); !errors.Is(err, errStepDecisionConflict) {
		t.Fatalf("one person voted twice under two names: %v", err)
	}
	unresolved := ApprovalEvidence{Decisions: []ApprovalVote{{Approver: "ghost", Decision: VoteApprove}}}
	after, status, pending, err := unresolved.cast(StatusPending, cfg, ApprovalVote{Approver: "bob", Principal: idstest.N(2), Via: "session", Decision: VoteApprove})
	if err != nil || !pending || status != StatusPending || after.Approvals != 1 {
		t.Fatalf("an unresolved vote counted as a second person: %+v %s %v %v", after, status, pending, err)
	}
	duplicate := ApprovalEvidence{Decisions: []ApprovalVote{{Approver: "alice", Principal: alice, Decision: VoteApprove}, {Approver: "alicia", Principal: alice, Decision: VoteApprove}}}
	if err := duplicate.validate(); err == nil {
		t.Fatal("saved evidence with one principal's two votes validated")
	}
	// Two people who voted under one name, either side of its reuse, are
	// two votes.
	twoAlices := ApprovalEvidence{Decisions: []ApprovalVote{{Approver: "alice", Principal: alice, Decision: VoteApprove}, {Approver: "alice", Principal: idstest.N(3), Decision: VoteApprove}}}
	if err := twoAlices.validate(); err != nil || twoAlices.people() != 2 {
		t.Fatalf("two people under one name: %d %v", twoAlices.people(), err)
	}
}

// Assent is judged by the way a vote was given: a person's yes through a
// chat is not their assent, the same person's through a session is.
func TestAssentIsJudgedByTheWayAVoteWasGiven(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		vote ApprovalVote
		want bool
	}{
		{ApprovalVote{Approver: "alice", Principal: idstest.N(1), Via: "session"}, true},
		{ApprovalVote{Approver: "alice", Principal: idstest.N(1), Via: "chat:x"}, false},
		{ApprovalVote{Approver: "alice", Via: "session"}, false},
		{ApprovalVote{Approver: "alice", Principal: idstest.N(1)}, true},
		{ApprovalVote{Approver: "alice"}, false},
		{ApprovalVote{Approver: "alice via console/c", Principal: idstest.N(1)}, false},
	} {
		if got := votedInPerson(tc.vote); got != tc.want {
			t.Errorf("%+v: assent %v, want %v", tc.vote, got, tc.want)
		}
	}
}
