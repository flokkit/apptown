package conformance

import (
	"encoding/json"
	"strings"
	"testing"

	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestProjectStandingRules(t *testing.T) {
	t.Parallel()
	adj := map[ids.ID]Adjudication{}
	mk := func(state Outcome, ev, sev string) Verdict {
		return Verdict{State: state, Evaluator: ev, Severity: sev, Block: "b", Version: 1}
	}
	cases := []struct {
		name string
		vs   []Verdict
		want Standing
	}{
		{"findings win", []Verdict{mk(DoesNotConform, "sh", "high"), mk(Conforms, "sh", ""), mk(DoesNotConform, "llm", "")}, StandingDoesNot},
		{"broken next", []Verdict{mk(EvaluationError, "sh", ""), mk(DoesNotConform, "llm", "")}, StandingBroken},
		{"AI flags need a look", []Verdict{mk(DoesNotConform, "llm", "medium"), mk(Conforms, "sh", "")}, StandingNeedsLook},
		{"unassessed is so far", []Verdict{mk(CannotAssess, "tool", ""), mk(Conforms, "sh", "")}, StandingSoFar},
		{"excepted never folded", []Verdict{mk(Excepted, "sh", ""), mk(Conforms, "sh", "")}, StandingExcepted},
		{"all clear", []Verdict{mk(Conforms, "sh", ""), mk(Conforms, "tool", "")}, StandingConforms},
		{"nothing adopted", nil, StandingNothing},
		{"n/a alone is nothing to judge", []Verdict{mk(NotApplicable, "llm", "")}, StandingNothing},
	}
	for _, c := range cases {
		ps := SummarizeProject(idstest.N(1), "p", RecordedObservations(c.vs), adj)
		if ps.Standing != c.want {
			t.Errorf("%s: standing %q (%s), want %q", c.name, ps.Standing, ps.Sentence, c.want)
		}
	}
	// n/a is excluded from M; a confirmed AI flag counts as a finding.
	vs := []Verdict{{ID: idstest.N(7), State: DoesNotConform, Evaluator: "llm", Severity: "high"}, mk(NotApplicable, "llm", ""), mk(Conforms, "sh", "")}
	ps := SummarizeProject(idstest.N(1), "p", RecordedObservations(vs), adj)
	if ps.Total != 2 || ps.Lanes.NA != 1 || ps.Lanes.Flagged != 1 || ps.Standing != StandingNeedsLook {
		t.Fatalf("n/a must not count toward M and an unconfirmed flag is flagged: %+v", ps)
	}
	ps = SummarizeProject(idstest.N(1), "p", RecordedObservations(vs), map[ids.ID]Adjudication{idstest.N(7): {VerdictID: idstest.N(7), Kind: "confirmed"}})
	if ps.Lanes.Findings != 1 || ps.High != 1 || ps.Standing != StandingDoesNot || !strings.Contains(ps.Sentence, "1 high") {
		t.Fatalf("a confirmed flag is a finding: %+v", ps)
	}
}

func TestReviewComparisonUsesProjectAndCheckIdentity(t *testing.T) {
	t.Parallel()
	previous := []Verdict{{ProjectID: idstest.N(7), ProjectSlug: "old-name", Block: "standard", Version: 1, Check: "clear", State: DoesNotConform}}
	current := []Verdict{{ProjectID: idstest.N(7), ProjectSlug: "new-name", Block: "standard", Version: 1, Check: "clear", State: Conforms},
		{ProjectID: idstest.N(8), ProjectSlug: "old-name", Block: "standard", Version: 1, Check: "clear", State: DoesNotConform}}
	d := CompareReviews(idstest.N(3), current, previous)
	if d.ComparedProjects != 1 || d.Fixed != 1 || d.New != 0 || len(d.FixedItems) != 1 || d.FixedItems[0] != "new-name · clear" {
		t.Fatalf("rename/reused slug changed comparison identity: %+v", d)
	}
	current[0].Version = 2
	if got := CompareReviews(idstest.N(3), current, previous); got.Fixed != 0 {
		t.Fatalf("different check version counted as a fix: %+v", got)
	}
}

func TestPartialConformanceDoesNotCountExceptionsAsPassed(t *testing.T) {
	t.Parallel()
	_, sentence := standingFor(Lanes{Conforms: 12, Excepted: 2, Unassessed: 1}, 0, 15)
	if sentence != "12 checks passed · 2 accepted exceptions · 1 not yet assessed" {
		t.Fatal(sentence)
	}
}

func TestLanesSeparateEvidenceFromAction(t *testing.T) {
	t.Parallel()
	record := Verdict{ID: idstest.N(3), ProjectID: idstest.N(7), SHA: "then", Block: "standard", Version: 1, Check: "clean", State: DoesNotConform, Evaluator: "llm"}
	confirmed := map[ids.ID]Adjudication{idstest.N(3): {VerdictID: idstest.N(3), Kind: "confirmed"}}
	for _, tc := range []struct {
		name          string
		observation   Observation
		confirmations map[ids.ID]Adjudication
		want          Lane
	}{
		{"AI flag", Recorded(record), nil, LaneFlagged},
		{"confirmed AI", Recorded(record), confirmed, LaneFindings},
		{"other confirmation", Recorded(record), map[ids.ID]Adjudication{idstest.N(4): {VerdictID: idstest.N(4)}}, LaneFlagged},
		{"stale confirmed AI", Observe(record, CurrentContext{Check: record.Reference(), HeadSHA: "now", ExceptionActive: true}), confirmed, LaneUnassessed},
		{"never scanned", Unassessed(record.Reference()), confirmed, LaneUnassessed},
		{"no evidence", Observation{State: DoesNotConform}, nil, LaneUnassessed},
	} {
		if got := LaneOf(tc.observation, tc.confirmations); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	record.Evaluator = "sh"
	for _, tc := range []struct {
		outcome Outcome
		want    Lane
	}{{Conforms, LaneConforms}, {DoesNotConform, LaneFindings}, {Excepted, LaneExcepted}, {NotApplicable, LaneNotApplicable}, {CannotAssess, LaneUnassessed}, {EvaluationError, LaneBroken}} {
		record.State = tc.outcome
		if got := LaneOf(Recorded(record), nil); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.outcome, got, tc.want)
		}
	}
}

func TestRepairAndConfirmationUseExactSavedEvidence(t *testing.T) {
	t.Parallel()
	before := Verdict{ID: idstest.N(3), ProjectID: idstest.N(7), Block: "standard", Version: 1, Check: "clean", State: DoesNotConform, Evaluator: "llm"}
	after := before
	after.ID = idstest.N(4)
	after.State = Conforms
	if !before.Confirmable() || after.Confirmable() || !before.FixedBy(after) {
		t.Fatal("valid confirmation/repair rejected")
	}
	for _, alter := range []func(*Verdict){
		func(v *Verdict) { v.ProjectID = idstest.N(999999) }, func(v *Verdict) { v.Block = "other" }, func(v *Verdict) { v.Version++ }, func(v *Verdict) { v.Check = "other" }, func(v *Verdict) { v.State = Excepted },
	} {
		changed := after
		alter(&changed)
		if before.FixedBy(changed) {
			t.Fatalf("different subject/outcome is not a verified fix: %+v", changed)
		}
	}
	before.ID = ids.Nil
	if before.FixedBy(after) {
		t.Fatal("absence cannot be a previous failure")
	}
	before.ID = idstest.N(3)
	before.State = Excepted
	if before.Confirmable() || !before.FixedBy(after) {
		t.Fatal("excepted evidence must be repairable, not confirmable")
	}
	before.State = DoesNotConform
	before.Evaluator = "sh"
	if before.Confirmable() {
		t.Fatal("deterministic finding does not need AI confirmation")
	}
}

func TestProjectionJSONUsesOwnedVocabulary(t *testing.T) {
	t.Parallel()
	summary := SummarizeFleet([]ProjectStanding{{Standing: StandingDoesNot, Lanes: Lanes{Findings: 2}, High: 1}}, 3)
	b, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Projects, Findings, High, Checks int
		ByStanding                       map[string]int
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Projects != 1 || wire.Findings != 2 || wire.High != 1 || wire.Checks != 3 || wire.ByStanding["does-not-conform"] != 1 {
		t.Fatalf("summary vocabulary: %s", b)
	}
	b, err = json.Marshal(Fix{CheckKey: CheckKey("quality", 2, "launch"), BeforeState: DoesNotConform})
	if err != nil {
		t.Fatal(err)
	}
	var fix map[string]json.RawMessage
	if err := json.Unmarshal(b, &fix); err != nil {
		t.Fatal(err)
	}
	if string(fix["checkKey"]) != `"quality@2·launch"` || string(fix["beforeState"]) != `"does-not-conform"` {
		t.Fatalf("fix vocabulary: %s", b)
	}
}
