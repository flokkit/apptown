package conformance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
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
		{"findings win", []Verdict{mk(DoesNotConform, "sh", "high"), mk(Conforms, "sh", ""), mk(DoesNotConform, "llm", "")}, standingDoesNot},
		{"broken next", []Verdict{mk(EvaluationError, "sh", ""), mk(DoesNotConform, "llm", "")}, standingBroken},
		{"AI flags need a look", []Verdict{mk(DoesNotConform, "llm", "medium"), mk(Conforms, "sh", "")}, StandingNeedsLook},
		{"unassessed is so far", []Verdict{mk(CannotAssess, "tool", ""), mk(Conforms, "sh", "")}, standingSoFar},
		{"excepted never folded", []Verdict{mk(Excepted, "sh", ""), mk(Conforms, "sh", "")}, standingExcepted},
		{"all clear", []Verdict{mk(Conforms, "sh", ""), mk(Conforms, "tool", "")}, StandingConforms},
		{"nothing adopted", nil, standingNothing},
		{"n/a alone is nothing to judge", []Verdict{mk(NotApplicable, "llm", "")}, standingNothing},
	}
	for _, c := range cases {
		ps := SummarizeProject(idstest.N(1), "p", RecordedObservations(c.vs, adj))
		if ps.Standing != c.want {
			t.Errorf("%s: standing %q (%s), want %q", c.name, ps.Standing, ps.Sentence, c.want)
		}
	}
	// n/a is excluded from M; a confirmed AI flag counts as a finding.
	vs := []Verdict{{ID: idstest.N(7), State: DoesNotConform, Evaluator: "llm", Severity: "high"}, mk(NotApplicable, "llm", ""), mk(Conforms, "sh", "")}
	ps := SummarizeProject(idstest.N(1), "p", RecordedObservations(vs, adj))
	if ps.Total != 2 || ps.Lanes.NA != 1 || ps.Lanes.Flagged != 1 || ps.Standing != StandingNeedsLook {
		t.Fatalf("n/a must not count toward M and an unconfirmed flag is flagged: %+v", ps)
	}
	ps = SummarizeProject(idstest.N(1), "p", RecordedObservations(vs, map[ids.ID]Adjudication{idstest.N(7): {VerdictID: idstest.N(7), Kind: "confirmed"}}))
	if ps.Lanes.Findings != 1 || ps.High != 1 || ps.Standing != standingDoesNot || !strings.Contains(ps.Sentence, "1 high") {
		t.Fatalf("a confirmed flag is a finding: %+v", ps)
	}
}

func TestReviewComparisonUsesProjectAndCheckIdentity(t *testing.T) {
	t.Parallel()
	previous := []Verdict{{ProjectID: idstest.N(7), ProjectSlug: "old-name", Block: "standard", Version: 1, Check: "clear", State: DoesNotConform}}
	current := []Verdict{{ProjectID: idstest.N(7), ProjectSlug: "new-name", Block: "standard", Version: 1, Check: "clear", State: Conforms},
		{ProjectID: idstest.N(8), ProjectSlug: "old-name", Block: "standard", Version: 1, Check: "clear", State: DoesNotConform}}
	d := compareReviews(idstest.N(3), current, previous)
	if d.ComparedProjects != 1 || d.Fixed != 1 || d.New != 0 || len(d.FixedItems) != 1 || d.FixedItems[0] != "new-name · clear" {
		t.Fatalf("rename/reused slug changed comparison identity: %+v", d)
	}
	current[0].Version = 2
	if got := compareReviews(idstest.N(3), current, previous); got.Fixed != 0 {
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
		{"AI flag", Recorded(record, nil), nil, laneFlagged},
		{"confirmed AI", Recorded(record, confirmed), confirmed, LaneFindings},
		{"other confirmation", Recorded(record, nil), map[ids.ID]Adjudication{idstest.N(4): {VerdictID: idstest.N(4)}}, laneFlagged},
		{"stale confirmed AI", observe(record, currentContext{Check: record.reference(), HeadSHA: "now", ExceptionActive: true}, confirmed), confirmed, laneUnassessed},
		{"never scanned", Unassessed(record.reference()), confirmed, laneUnassessed},
		{"no evidence", Observation{State: DoesNotConform, lane: laneUnassessed}, nil, laneUnassessed},
	} {
		b, err := json.Marshal(tc.observation)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Lane string `json:"lane"`
		}
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Lane != tc.want.String() {
			t.Fatalf("owner classification missing from response: %s", b)
		}
		if got := LaneOf(tc.observation, tc.confirmations); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	record.Evaluator = "sh"
	for _, tc := range []struct {
		outcome Outcome
		want    Lane
	}{{Conforms, laneConforms}, {DoesNotConform, LaneFindings}, {Excepted, laneExcepted}, {NotApplicable, laneNotApplicable}, {CannotAssess, laneUnassessed}, {EvaluationError, laneBroken}} {
		record.State = tc.outcome
		if got := LaneOf(Recorded(record, nil), nil); got != tc.want {
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
	if !before.confirmable() || after.confirmable() || !before.fixedBy(after) {
		t.Fatal("valid confirmation/repair rejected")
	}
	for _, alter := range []func(*Verdict){
		func(v *Verdict) { v.ProjectID = idstest.N(999999) }, func(v *Verdict) { v.Block = "other" }, func(v *Verdict) { v.Version++ }, func(v *Verdict) { v.Check = "other" }, func(v *Verdict) { v.State = Excepted },
	} {
		changed := after
		alter(&changed)
		if before.fixedBy(changed) {
			t.Fatalf("different subject/outcome is not a verified fix: %+v", changed)
		}
	}
	before.ID = ids.Nil
	if before.fixedBy(after) {
		t.Fatal("absence cannot be a previous failure")
	}
	before.ID = idstest.N(3)
	before.State = Excepted
	if before.confirmable() || !before.fixedBy(after) {
		t.Fatal("excepted evidence must be repairable, not confirmable")
	}
	before.State = DoesNotConform
	before.Evaluator = "sh"
	if before.confirmable() {
		t.Fatal("deterministic finding does not need AI confirmation")
	}
}

func TestProjectionJSONUsesOwnedVocabulary(t *testing.T) {
	t.Parallel()
	summary := summarizeFleet([]ProjectStanding{{Standing: standingDoesNot, Lanes: Lanes{Findings: 2}, High: 1}}, 3)
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

// A check that could not run leaves the review incomplete: the sentence says
// so in words, and says the check, not the app, is what failed.
func TestABrokenCheckReadsAsAnIncompleteReview(t *testing.T) {
	t.Parallel()
	standing, sentence := standingFor(Lanes{Conforms: 5, Broken: 1}, 0, 6)
	if standing != standingBroken || sentence != "Review incomplete · 1 not known" {
		t.Fatal(standing, sentence)
	}
	if _, sentence := standingFor(Lanes{Broken: 2}, 0, 2); sentence != "Review incomplete · 2 not known" {
		t.Fatal(sentence)
	}
}

func TestFleetIncompleteCountIncludesChecksHiddenByHigherPriorityStanding(t *testing.T) {
	t.Parallel()
	summary := summarizeFleet([]ProjectStanding{
		{Standing: standingBroken, Lanes: Lanes{Broken: 1}},
		{Standing: standingDoesNot, Lanes: Lanes{Findings: 1, Unassessed: 2}},
		{Standing: StandingNeedsLook, Lanes: Lanes{Flagged: 1, Unassessed: 1}},
		{Standing: StandingConforms, Lanes: Lanes{Conforms: 1}},
	}, 6)
	body, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		IncompleteProjects int `json:"incompleteProjects"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.IncompleteProjects != 3 {
		t.Fatalf("incomplete projects must count lanes regardless of standing: %s", body)
	}
}

func TestFindingSentenceKeepsChecksThatCouldNotRun(t *testing.T) {
	t.Parallel()
	standing, sentence := standingFor(Lanes{Findings: 2, Broken: 1}, 0, 3)
	if standing != standingDoesNot || !strings.Contains(sentence, "2 findings · 1 not known") {
		t.Fatalf("mixed result hides failed execution: %s: %s", standing, sentence)
	}
}

func TestUnknownAttemptIsNotJudgedOrPassed(t *testing.T) {
	t.Parallel()
	for _, state := range []Outcome{EvaluationError} {
		p := SummarizeProject(idstest.N(1), "calendar", RecordedObservations([]Verdict{{Check: "scan", State: state, Detail: "scanner timed out"}}, nil))
		if p.Judged != 0 || p.Lanes.Findings != 0 || p.StatusLabel != checkUnknown || !strings.Contains(p.Sentence, "not known") || !strings.Contains(p.Sentence, "scanner timed out") {
			t.Fatalf("attempt without an answer must be not known with its reason: %+v", p)
		}
	}
}

func TestStandingCountsAnUnansweredAttemptOnce(t *testing.T) {
	t.Parallel()
	p := SummarizeProject(idstest.N(1), "calendar", RecordedObservations([]Verdict{{Check: "scan", State: EvaluationError, Detail: "scanner timed out"}}, nil))
	if p.Sentence != "Review incomplete · 1 not known: scan: scanner timed out" {
		t.Fatal(p.Sentence)
	}
}
