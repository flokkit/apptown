package attention

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type checkEvidence struct {
	standing []conformance.ProjectStanding
	calls    int
	selected []ids.ID
}

func (s *checkEvidence) CurrentView(_ context.Context, projects []ids.ID) ([]conformance.ProjectStanding, error) {
	s.calls++
	s.selected = projects
	return s.standing, nil
}
func TestAppChecksRenderOwnerSummaryFromOneBatch(t *testing.T) {
	t.Parallel()
	owner := &checkEvidence{standing: []conformance.ProjectStanding{
		{ProjectID: idstest.N(1), Name: "Calendar", Slug: "calendar", Lanes: conformance.Lanes{Findings: 2, Broken: 1, Flagged: 4, Unassessed: 1}},
		{ProjectID: idstest.N(2), Name: "Unrun", Slug: "unrun", Lanes: conformance.Lanes{Unassessed: 8}},
	}}
	got, err := (Checks{Conformance: owner}).Snapshot(t.Context(), []ids.ID{idstest.N(1), idstest.N(2)})
	if err != nil || !got.Known || len(got.Items) != 1 || got.Items[0].Count != 2 || got.Items[0].Unknown || len(got.Coverage) != 0 || owner.calls != 1 || len(owner.selected) != 2 {
		t.Fatalf("snapshot: %+v, owner=%+v, %v", got, owner, err)
	}
}

// The source row says when the apps were judged, by its oldest judgement, and
// opens Conformance, where those judgements are listed.
func TestAppChecksSourceIsAsOldAsItsOldestJudgementAndOpensConformance(t *testing.T) {
	t.Parallel()
	older, newer := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	owner := &checkEvidence{standing: []conformance.ProjectStanding{
		{ProjectID: idstest.N(1), Name: "Calendar", Slug: "calendar", JudgedAt: newer.Format(time.RFC3339)},
		{ProjectID: idstest.N(2), Name: "Notes", Slug: "notes", JudgedAt: older.Format(time.RFC3339)},
		{ProjectID: idstest.N(3), Name: "Unrun", Slug: "unrun"},
	}}
	got, err := (Checks{Conformance: owner}).Snapshot(t.Context(), nil)
	if err != nil || !got.ObservedAt.Equal(older) || got.Href != "/conformance" || got.Name != "App checks" {
		t.Fatalf("snapshot: %+v %v", got, err)
	}
}

// A failed attempt is uncertainty, while a completed failing check is a problem.
func TestAppChecksSeparateUnknownAttemptsFromConfirmedProblems(t *testing.T) {
	t.Parallel()
	for _, state := range []conformance.Outcome{conformance.EvaluationError} {
		p := conformance.SummarizeProject(idstest.N(1), "calendar", conformance.RecordedObservations([]conformance.Verdict{
			{Check: "scanner", State: state, Detail: "No Docker executor can start the scanner", Evaluator: "tool"},
			{Check: "tests", State: conformance.DoesNotConform, Evaluator: "sh"},
			{Check: "scan", State: conformance.DoesNotConform, Evaluator: "tool"},
		}, nil))
		p.Name = "Calendar"
		got, err := (Checks{Conformance: &checkEvidence{standing: []conformance.ProjectStanding{p}}}).Snapshot(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var known, unknown int
		for _, item := range got.Items {
			if item.Unknown {
				unknown++
				if !strings.Contains(item.Detail, "No Docker executor") {
					t.Fatalf("missing reason: %+v", item)
				}
			} else {
				known++
				if item.Count != 2 {
					t.Fatalf("unknown attempt counted as confirmed: %+v", item)
				}
			}
		}
		if known != 1 || unknown != 1 {
			t.Fatalf("want one problem row and one not-known row: %+v", got)
		}
		onlyUnknown := conformance.SummarizeProject(idstest.N(1), "calendar", conformance.RecordedObservations([]conformance.Verdict{{Check: "scan", State: state, Detail: "scanner timed out"}}, nil))
		alone, err := (Checks{Conformance: &checkEvidence{standing: []conformance.ProjectStanding{onlyUnknown}}}).Snapshot(t.Context(), nil)
		if err != nil || len(alone.Items) != 1 || !alone.Items[0].Unknown || !strings.Contains(alone.Items[0].Detail, "scanner timed out") || strings.Contains(alone.Items[0].Title, "confirmed") {
			t.Fatalf("unknown-only project: %+v %v", alone, err)
		}
	}
}

// Routine review deferrals are unassessed; only a failed attempt needs attention.
func TestAppCheckDeferralsRaiseNoAttention(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, check, detail string
		state               conformance.Outcome
		unknown             int
	}{
		{"no standing deployment", "deploy/memory-within", "no standing deployment", conformance.CannotAssess, 0},
		{"uncached image", "scan", "exec-image tool runs at the land gate", conformance.CannotAssess, 0},
		{"no evaluator", "scan", "", conformance.CannotAssess, 0},
		{"launch failure", "scan", "No Docker executor can start the scanner", conformance.EvaluationError, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := conformance.SummarizeProject(idstest.N(1), "calendar", conformance.RecordedObservations([]conformance.Verdict{{Check: tc.check, State: tc.state, Detail: tc.detail}}, nil))
			if len(p.NotKnown) != tc.unknown || p.Judged != 0 {
				t.Fatalf("standing: %+v", p)
			}
			if tc.unknown == 0 && (!strings.Contains(p.Sentence, "1 not yet assessed") || strings.Contains(p.Sentence, "not known")) {
				t.Fatalf("deferral: %s", p.Sentence)
			}
			got, err := (Checks{Conformance: &checkEvidence{standing: []conformance.ProjectStanding{p}}}).Snapshot(t.Context(), nil)
			if err != nil || len(got.Items) != tc.unknown {
				t.Fatalf("attention: %+v %v", got, err)
			}
			if tc.unknown == 1 && (!got.Items[0].Unknown || !strings.Contains(got.Items[0].Detail, tc.detail)) {
				t.Fatalf("launch failure: %+v", got)
			}
		})
	}
}

func TestUndecidedAIChecksStayOnConformanceWithoutRaisingAttention(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state  conformance.Outcome
		source bool
		want   int
	}{
		{conformance.EvaluationError, false, 0}, {conformance.CannotAssess, false, 0}, {conformance.EvaluationError, true, 1},
	} {
		v := conformance.Verdict{Evaluator: "llm", Check: "root-toolchain", State: tc.state, Detail: "could not decide", AgentEvidence: &check.AgentEvidence{SourceReadFailure: tc.source}}
		p := conformance.SummarizeProject(idstest.N(1), "fixture", conformance.RecordedObservations([]conformance.Verdict{v}, nil))
		if tc.state == conformance.EvaluationError && (len(p.NotKnown) != 1 || p.NotKnown[0].Evaluator != "llm") {
			t.Fatalf("standing lost structured AI reason: %+v", p)
		}
		got, err := (Checks{Conformance: &checkEvidence{standing: []conformance.ProjectStanding{p}}}).Snapshot(t.Context(), nil)
		if err != nil || len(got.Items) != tc.want {
			t.Fatalf("source=%t state=%v attention=%+v %v", tc.source, tc.state, got, err)
		}
	}
}

func TestAppTownAICheckFailuresRemainActionable(t *testing.T) {
	t.Parallel()
	for _, fault := range []bool{false, true} {
		v := conformance.Verdict{Evaluator: "llm", Check: "rubric", State: conformance.CannotAssess,
			Detail:        "no checks model is set up. An administrator must select a checks model",
			AgentEvidence: &check.AgentEvidence{AppTownFault: fault}}
		p := conformance.SummarizeProject(idstest.N(1), "app", conformance.RecordedObservations([]conformance.Verdict{v}, nil))
		got, err := (Checks{Conformance: &checkEvidence{standing: []conformance.ProjectStanding{p}}}).Snapshot(t.Context(), nil)
		want := 0
		if fault {
			want = 1
		}
		if err != nil || len(got.Items) != want {
			t.Fatalf("fault=%t: %+v %v", fault, got, err)
		}
		if fault && (!got.Items[0].Unknown || !strings.Contains(got.Items[0].Detail, "administrator must select")) {
			t.Fatalf("missing next action: %+v", got)
		}
	}
}
