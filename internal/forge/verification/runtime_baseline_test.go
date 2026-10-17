package verification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/workflow"
)

// A running-app finding is compared with main only when main's deployment
// serves exactly the change's base commit: failing there too, it is
// preexisting; passing there, the change introduced it; main at another
// commit, or no main deployment at all, leaves it undecided — shown, not
// blocking — with the reason. The probe of main is the real page probe
// against a server that answers as main would.
//
// Kills: probing a main deployment at another commit as if it were the base;
// a runtime finding labelled introduced (blocking) when main could not be
// compared; an undecided finding that does not say why; a pre-existing
// runtime problem left blocking.
func TestARunningAppFindingIsComparedWithMainOnlyAtTheBaseCommit(t *testing.T) {
	t.Parallel()
	const base = "1111111111111111111111111111111111111111"
	const other = "2222222222222222222222222222222222222222"
	sc := conformance.DeclaredCheck{Block: "deploy/health-declared", Version: 1, Check: conformance.CheckDeclaration{
		Name: "front-page-answers", Evaluator: "runtime", Probe: "page", Severity: "high"}}
	checks := []conformance.DeclaredCheck{sc}
	mainAnswering := func(status int) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	for _, tc := range []struct {
		name   string
		at     func() (Target, string, func() bool, string)
		want   string
		reason string
	}{
		{"main at the base, failing there too", func() (Target, string, func() bool, string) {
			return Target{FrontDoor: mainAnswering(http.StatusForbidden)}, base, func() bool { return true }, ""
		}, "preexisting", ""},
		{"main at the base, passing there", func() (Target, string, func() bool, string) {
			return Target{FrontDoor: mainAnswering(http.StatusOK)}, base, func() bool { return true }, ""
		}, "introduced", ""},
		{"main at another commit", func() (Target, string, func() bool, string) {
			return Target{FrontDoor: mainAnswering(http.StatusForbidden)}, other, func() bool { return true }, ""
		}, "undecided", "main's deployment runs 222222222222, not this change's base 111111111111"},
		{"no main deployment", func() (Target, string, func() bool, string) {
			return Target{}, "", nil, "no standing main deployment — deploy the app to be judged at runtime"
		}, "undecided", "no standing main deployment"},
		{"main replaced while it was probed", func() (Target, string, func() bool, string) {
			return Target{FrontDoor: mainAnswering(http.StatusForbidden)}, base, func() bool { return false }, ""
		}, "undecided", "changed while it was being probed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := []conformance.Finding{{Finding: check.Finding{Fingerprint: conformance.Fingerprint(sc, ""), Severity: "high", Detail: "the root answered 403"}}}
			results := Results{{Block: sc.Block, Version: 1, Check: sc.Check.Name, Evaluator: "runtime", Verdict: workflow.VerdictFail}}
			judged := probeMainAtBase(context.Background(), failingChecks(checks, findings, check.IsRuntime), base, tc.at, (Runtime{}).Probe)
			// The deterministic baseline was taken (it found nothing); the
			// runtime comparison is what decides.
			b := &baselineSet{failing: map[string]bool{}, items: map[string][]string{}, unknown: map[string]bool{}, sha: base}
			own, pre, und := classifyAgainstBase(results, findings, checks, b, workflow.AcceptanceScope{}, judged...)
			if findings[0].Regression != tc.want {
				t.Fatalf("read %q, want %q (own %d pre %d und %d): %+v", findings[0].Regression, tc.want, len(own), len(pre), len(und), judged)
			}
			if blocks := len(own) == 1; blocks != (tc.want == "introduced") {
				t.Fatalf("only an introduced finding is the change's to fix: own=%v", own)
			}
			if tc.want == "undecided" {
				if !results[0].Undecided || !strings.Contains(results[0].Note, tc.reason) || !strings.Contains(und[0].Detail, tc.reason) {
					t.Fatalf("an undecided finding is shown with why: note %q detail %q", results[0].Note, und[0].Detail)
				}
			}
			if tc.want == "preexisting" && !results[0].Preexisting {
				t.Fatalf("the check row is marked preexisting: %+v", results[0])
			}
		})
	}

	// A remediation opened to close this check is judged absolutely: main
	// failing too, or main unknown, never excuses its own target.
	findings := []conformance.Finding{{Finding: check.Finding{Fingerprint: conformance.Fingerprint(sc, "")}}}
	judged := probeMainAtBase(context.Background(), checks, base, func() (Target, string, func() bool, string) { return Target{}, "", nil, "no standing main deployment" }, (Runtime{}).Probe)
	target := workflow.AcceptanceScopeOf(&workflow.Change{AcceptanceJSON: `{"checks":[{"block":"deploy/health-declared","check":"front-page-answers"}]}`})
	own, _, _ := classifyAgainstBase(Results{}, findings, checks, &baselineSet{failing: map[string]bool{}}, target, judged...)
	if len(own) != 1 {
		t.Fatalf("a remediation's own runtime target is the change's: %+v", findings)
	}
}

// A failing model rubric or runtime probe that main fails too is
// preexisting; a shell check without occurrences failing on both stays
// undecided, as before.
//
// Kills: whole-property checks judged by occurrences they never report.
func TestWholePropertyChecksFailingOnMainArePreexisting(t *testing.T) {
	t.Parallel()
	checks := []conformance.DeclaredCheck{
		{Block: "b", Version: 1, Check: conformance.CheckDeclaration{Name: "rubric", Evaluator: "llm", Scope: "tree", Rubric: "r"}},
		{Block: "b", Version: 1, Check: conformance.CheckDeclaration{Name: "probe", Evaluator: "runtime", Probe: "page"}},
		{Block: "b", Version: 1, Check: conformance.CheckDeclaration{Name: "script", Evaluator: "sh", Sh: "exit 1"}},
	}
	var findings []conformance.Finding
	var judged []check.Evaluation
	for _, sc := range checks {
		fp := conformance.Fingerprint(sc, "")
		findings = append(findings, conformance.Finding{Finding: check.Finding{Fingerprint: fp}})
		if sc.Check.Evaluator != "sh" {
			judged = append(judged, check.Evaluation{Declared: sc.Declared(), Outcome: check.DoesNotConform, Findings: []check.Finding{{Fingerprint: fp}}})
		}
	}
	b := &baselineSet{failing: map[string]bool{conformance.Fingerprint(checks[2], ""): true}, items: map[string][]string{}}
	classifyAgainstBase(Results{}, findings, checks, b, workflow.AcceptanceScope{}, judged...)
	if findings[0].Regression != "preexisting" || findings[1].Regression != "preexisting" || findings[2].Regression != "undecided" {
		t.Fatalf("rubric %q probe %q script %q", findings[0].Regression, findings[1].Regression, findings[2].Regression)
	}
}
