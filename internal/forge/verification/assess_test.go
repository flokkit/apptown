package verification

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/conformance"
)

// An assessment answers each check or says why it did not, in the fixed
// words every surface uses: a runtime probe with no app serving the
// commit, a diff rubric with no change, a rubric nobody may pay for, a
// judge that failed or passed an item over, and a check no evaluator
// knows. A probe of the app serving the commit answers, and a failure
// carries the check's own finding so it can be compared.
//
// Kills: a not-run check reported with no reason (or as a pass); a judge
// failure read as a verdict; a probe answer kept after the app changed.
func TestAnAssessmentSaysWhyEachCheckWasNotJudged(t *testing.T) {
	t.Parallel()
	probe := conformance.DeclaredCheck{Block: "run", Version: 1, Check: conformance.CheckDeclaration{Name: "page", Evaluator: "runtime", Probe: "page", Severity: "high"}}
	tree := conformance.DeclaredCheck{Block: "style", Version: 1, Check: conformance.CheckDeclaration{Name: "tone", Rubric: "r", Evaluator: "llm", Scope: "tree"}}
	diff := conformance.DeclaredCheck{Block: "style", Version: 1, Check: conformance.CheckDeclaration{Name: "small", Rubric: "r", Evaluator: "llm", Scope: "diff"}}
	odd := conformance.DeclaredCheck{Block: "odd", Version: 1, Check: conformance.CheckDeclaration{Name: "mystery", Evaluator: "telepathy"}}
	reasons := func(as []treeAssessment) map[string]treeAssessment {
		out := map[string]treeAssessment{}
		for _, a := range as {
			out[a.Check.Check.Name] = a
		}
		return out
	}
	f := Fleet{runtime: &Runtime{}}

	got := reasons(f.assessChecks(t.Context(), []conformance.DeclaredCheck{probe, tree, diff, odd}, assessInput{NoApp: "main's deployment runs another commit", NoJudge: conformance.NotAssessedRubricsNotIncluded}))
	for name, want := range map[string]conformance.NotAssessed{"page": conformance.NotAssessedNoRunningApp, "tone": conformance.NotAssessedRubricsNotIncluded,
		"small": conformance.NotAssessedJudgesAChange, "mystery": conformance.NotAssessedNoEvaluator} {
		if a := got[name]; a.State != conformance.CannotAssess || a.Reason != want {
			t.Errorf("%s: %+v, want not assessed for %q", name, a, want)
		}
	}
	if got["page"].Detail != "main's deployment runs another commit" {
		t.Errorf("the reason keeps the detail: %+v", got["page"])
	}

	failing := func(err error) func([]check.Item, string) (map[int]check.Verdict, *llmMetadata, error) {
		return func([]check.Item, string) (map[int]check.Verdict, *llmMetadata, error) { return nil, nil, err }
	}
	got = reasons(f.assessChecks(t.Context(), []conformance.DeclaredCheck{tree}, assessInput{Judge: failing(errors.New("provider down"))}))
	if a := got["tone"]; a.State != conformance.CannotAssess || a.Reason != conformance.NotAssessedError || a.Detail != "provider down" {
		t.Errorf("a judge that failed: %+v", a)
	}
	got = reasons(f.assessChecks(t.Context(), []conformance.DeclaredCheck{tree}, assessInput{Judge: failing(nil)}))
	if a := got["tone"]; a.State != conformance.CannotAssess || a.Reason != conformance.NotAssessedError {
		t.Errorf("an item the judge passed over: %+v", a)
	}
	got = reasons(f.assessChecks(t.Context(), []conformance.DeclaredCheck{tree, diff}, assessInput{Diff: "+line", Judge: func(items []check.Item, _ string) (map[int]check.Verdict, *llmMetadata, error) {
		return map[int]check.Verdict{1: {Item: 1, Verdict: "fail", Note: "curt"}, 2: {Item: 2, Verdict: "pass"}}, nil, nil
	}}))
	if a := got["tone"]; a.State != conformance.DoesNotConform || a.Reason != "" || len(a.Findings) != 1 {
		t.Errorf("a rubric the judge failed: %+v", a)
	}
	if a := got["small"]; a.State != conformance.Conforms {
		t.Errorf("a diff rubric judged on the change given: %+v", a)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	t.Cleanup(srv.Close)
	app := &runningApp{Target: Target{FrontDoor: srv.URL}, Still: func() bool { return true }}
	got = reasons(f.assessChecks(t.Context(), []conformance.DeclaredCheck{probe}, assessInput{Running: app}))
	if a := got["page"]; a.State != conformance.DoesNotConform || a.Reason != "" || len(a.Findings) != 1 || a.Findings[0].Fingerprint != conformance.Fingerprint(probe, "") {
		t.Errorf("a probe of the app serving the commit: %+v", a)
	}
	app.Still = func() bool { return false }
	got = reasons(f.assessChecks(t.Context(), []conformance.DeclaredCheck{probe}, assessInput{Running: app}))
	if a := got["page"]; a.State != conformance.CannotAssess || a.Reason != conformance.NotAssessedNoRunningApp {
		t.Errorf("an app that changed while probed: %+v", a)
	}
}

// Every reason a check was not judged has its own words, so no surface
// shows a bare code.
func TestEveryNotAssessedReasonHasWords(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, u := range conformance.NotAssessedReasons {
		w := u.Words()
		if w == string(u) || w == "" || seen[w] {
			t.Errorf("%q has no words of its own: %q", u, w)
		}
		seen[w] = true
	}
}
