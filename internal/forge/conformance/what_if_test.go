package conformance

import (
	"fmt"
	"testing"

	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// A what-if row says what taking the version would change for one check.
// Inability to judge either side is never an improvement, and an excepted
// check counts as failing on either side (whether its exception lapses is
// the project's answer). Every pair of outcomes has an answer.
func TestWhatIfResultSaysWhatTakingTheVersionChanges(t *testing.T) {
	t.Parallel()
	pass, fail, excepted, na, cannot, broken := Conforms, DoesNotConform, Excepted, NotApplicable, CannotAssess, EvaluationError
	for _, tc := range []struct {
		today, version *Outcome
		want           string
	}{
		{&pass, &fail, WhatIfWouldFail},
		{nil, &fail, WhatIfWouldFail},
		{&fail, &pass, WhatIfWouldPass},
		{&excepted, &pass, WhatIfWouldPass},
		{&excepted, &fail, WhatIfUnchanged},
		{&pass, &pass, WhatIfUnchanged},
		{nil, &pass, WhatIfUnchanged},
		{&fail, &cannot, WhatIfNotAssessed},
		{&fail, &broken, WhatIfNotAssessed},
		{&broken, &pass, WhatIfNotAssessed},
		{&na, &pass, WhatIfNotAssessed},
		{&fail, nil, WhatIfNoLongerNeeded},
		{&cannot, nil, WhatIfNoLongerNeeded},
	} {
		if got := WhatIfResult(tc.today, tc.version); got != tc.want {
			t.Errorf("today %v, under the version %v: %s, want %s", tc.today, tc.version, got, tc.want)
		}
	}
	words := map[string]bool{WhatIfWouldFail: true, WhatIfWouldPass: true, WhatIfUnchanged: true, WhatIfNotAssessed: true, WhatIfNoLongerNeeded: true}
	all := []Outcome{Conforms, DoesNotConform, Excepted, NotApplicable, CannotAssess, EvaluationError}
	sides := []*Outcome{nil}
	for i := range all {
		sides = append(sides, &all[i])
	}
	for _, today := range sides {
		for _, version := range sides {
			if !words[WhatIfResult(today, version)] {
				t.Errorf("no answer for %v, %v", today, version)
			}
		}
	}
}

// A what-if's answer per project names what taking the version changes,
// worst first: a new failure (an excepted check whose exception does not
// cover the version's check counts), then something the version changes that was not
// judged, then nothing new. A check not judged at the same block version on
// both sides is one evaluation, which the version cannot change, so it does
// not hold a project back. A selected project with no rows was not
// evaluated, and unknown is never "no new failures". Kills: a would-fail
// or lapse missed; a covered exception counted as a lapse; a not-assessed row the version changes ignored; the
// same-evaluation rule widened to a moved block; a project without rows
// shown as clear; the outcome order inverted; selection order lost.
func TestAWhatIfAnswersEachProjectWithWhatTheVersionChanges(t *testing.T) {
	t.Parallel()
	row := func(project ids.ID, block, check string, cur, pro int, current, proposed, result string, reason NotAssessed) WhatIfRow {
		return WhatIfRow{ProjectID: project, ProjectSlug: fmt.Sprintf("p%d", project[5]), SHA: "abc", AdoptionRevision: 3, CandidateDigest: fmt.Sprintf("d%d", project[5]), Block: block, Check: check,
			CurrentVersion: cur, ProposedVersion: pro, Current: current, Proposed: proposed, Result: result, Reason: reason}
	}
	pass, fail, exc, cannot := Conforms.String(), DoesNotConform.String(), Excepted.String(), CannotAssess.String()
	rows := []WhatIfRow{
		// 1: a new failure beside a check not judged: new failures.
		row(idstest.N(1), "docs", "readme", 0, 1, "", fail, WhatIfWouldFail, ""),
		row(idstest.N(1), "img", "scan", 1, 2, pass, cannot, WhatIfNotAssessed, NotAssessedNoImage),
		// 2: an exception that does not cover the version's check lapses; one
		// that does (an identifier exception on a moved block) does not.
		row(idstest.N(2), "deps", "audit", 1, 2, exc, fail, WhatIfUnchanged, ""),
		row(idstest.N(2), "sast", "rules", 1, 2, exc, exc, WhatIfUnchanged, ""),
		// 3: a moved check not judged holds it back.
		row(idstest.N(3), "img", "scan", 1, 2, cannot, cannot, WhatIfNotAssessed, NotAssessedNoImage),
		// 4: the same evaluation not judged both ways does not; a fix counts.
		row(idstest.N(4), "tone", "rubric", 2, 2, cannot, cannot, WhatIfNotAssessed, NotAssessedRubricsNotIncluded),
		row(idstest.N(4), "hygiene", "fixme", 1, 2, fail, pass, WhatIfWouldPass, ""),
		// 5: a whole-project row.
		{ProjectID: idstest.N(5), ProjectSlug: "p5", Result: WhatIfNotAssessed, Reason: NotAssessedNoMain},
	}
	got := WhatIfByProject([]ids.ID{idstest.N(6), idstest.N(5), idstest.N(4), idstest.N(3), idstest.N(2), idstest.N(1)}, rows, map[ids.ID]string{idstest.N(6): "six"}, true)
	want := []struct {
		slug, outcome     string
		failures, unknown int
	}{{"six", WhatIfNotEvaluated, 0, 0}, {"p5", WhatIfCouldNotAssess, 0, 1}, {"p4", WhatIfNoNewFailures, 0, 0},
		{"p3", WhatIfCouldNotAssess, 0, 1}, {"p2", WhatIfNewFailures, 1, 0}, {"p1", WhatIfNewFailures, 1, 1}}
	if len(got) != len(want) {
		t.Fatalf("one answer per selected project: %+v", got)
	}
	for i, w := range want {
		g := got[i]
		if g.ProjectSlug != w.slug || g.Outcome != w.outcome || len(g.NewFailures) != w.failures || len(g.NotAssessed) != w.unknown {
			t.Errorf("%s: %+v, want %+v", w.slug, g, w)
		}
	}
	if lapse := got[4].NewFailures; len(lapse) != 1 || lapse[0].Block != "deps" || !lapse[0].ExceptedToday {
		t.Errorf("the moved block's exception lapses: %+v", lapse)
	}
	if got[3].NotAssessed[0].Reason != NotAssessedNoImage || got[3].NotAssessed[0].Why != NotAssessedNoImage.Words() || got[1].NotAssessed[0].Reason != NotAssessedNoMain || got[1].NotAssessed[0].Block != "" {
		t.Errorf("a check not judged keeps its reason: %+v %+v", got[3], got[1])
	}
	if got[2].Fixed != 1 || got[2].SHA != "abc" || got[2].AdoptionRevision != 3 || got[2].CandidateDigest != "d4" {
		t.Errorf("a fix is counted and the commit, revision and candidate kept: %+v", got[2])
	}
	if got[1].CandidateDigest != "" || got[0].CandidateDigest != "" {
		t.Errorf("a project with no candidate carries none: %+v %+v", got[1], got[0])
	}
	if lone := WhatIfByProject(nil, rows[:1], nil, true); len(lone) != 1 || lone[0].Outcome != WhatIfNewFailures {
		t.Errorf("a what-if with no stored list answers the projects it has rows for: %+v", lone)
	}
	if gone := WhatIfByProject([]ids.ID{idstest.N(9)}, nil, nil, true); gone[0].ProjectSlug != "project #"+idstest.N(9).String() || gone[0].Outcome != WhatIfNotEvaluated {
		t.Errorf("a project gone since: %+v", gone)
	}
	// While the what-if is queued or running, a project it has not reached
	// is not yet judged: neither an answer nor "not evaluated", which says
	// the what-if ended without it.
	if waiting := WhatIfByProject([]ids.ID{idstest.N(6), idstest.N(1)}, rows[:1], nil, false); waiting[0].Outcome != WhatIfNotYetJudged || waiting[1].Outcome != WhatIfNewFailures {
		t.Errorf("a running what-if's unreached project: %+v", waiting)
	}
}
