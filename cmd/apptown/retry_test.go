package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// When the run in flight has no agent step, retry names the send-back
// door and the wedged step, rather than only saying no.
func TestRetryHintNamesSendBack(t *testing.T) {
	hint := retryHint(idstest.N(308), idstest.N(444), idstest.N(3060), "catch-up-with-main")
	for _, want := range []string{"change 00000000-0134-7000-8000-000000000000", "catch-up-with-main (00000000-0bf4-7000-8000-000000000000)", "run 00000000-01bc-7000-8000-000000000000", `apptown sendback 00000000-0134-7000-8000-000000000000 "what to change"`} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint lacks %q:\n%s", want, hint)
		}
	}
	if !strings.Contains(retryHint(idstest.N(9), idstest.N(12), ids.Nil, ""), "apptown sendback 00000000-0009-7000-8000-000000000000") {
		t.Fatal("hint without a blocker still names the door")
	}
}

// The implementer's claim in a dispute is the report the run's reviewer
// declares it audits, not the newest artifact whose kind ends in "report":
// a scan's report written later is not the implementer's claim.
func TestTheDisputesClaimIsTheReportTheReviewerAudits(t *testing.T) {
	var d loopRunDetail
	if err := json.Unmarshal([]byte(`{
		"steps": [{"type":"agent/pi","config":{"output":{"kind":"uplift-report"}}},{"type":"agent/review","config":{"of":"uplift-report"}}],
		"artifacts": [{"id":"00000000-0001-7000-8000-000000000000","kind":"uplift-report"},{"id":"00000000-0002-7000-8000-000000000000","kind":"verify-report"},{"id":"00000000-0003-7000-8000-000000000000","kind":"trivy-report"}]}`), &d); err != nil {
		t.Fatal(err)
	}
	if i := claimOf(d); i != 0 {
		t.Fatalf("the claim is artifact %d, want the uplift report", i)
	}
	d.Steps = d.Steps[:1]
	if i := claimOf(d); i != -1 {
		t.Fatalf("with no review declaring a report, the claim is artifact %d, want none", i)
	}
}

// override overrules the failed check AppTown names, with the reason; the
// first failed step in the list is not the client's to pick.
func TestOverrideOverrulesTheStepTheForgeNames(t *testing.T) {
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case path == "/changes/00000000-0007-7000-8000-000000000000":
			return 200, `{"detail":{"run":{"id":"00000000-0046-7000-8000-000000000000","status":"running","currentStage":"gates"},
				"next":[{"act":"override","stepId":"00000000-02c1-7000-8000-000000000000","step":"tests"}],
				"steps":[{"id":"00000000-02c0-7000-8000-000000000000","stage":"gates","name":"lint","type":"script","status":"fail"},{"id":"00000000-02c1-7000-8000-000000000000","stage":"gates","name":"tests","type":"tests","status":"fail"}]}}`
		case method == "POST" && (path == "/steps/00000000-02c1-7000-8000-000000000000/override" || path == "/runs/00000000-0046-7000-8000-000000000000/advance"):
			return 200, `{}`
		}
		return 404, `{"error":"unexpected"}`
	})
	if out, err := run(t, "override", "00000000-0007-7000-8000-000000000000", "flaky on CI", "--no-follow"); err != nil || !strings.Contains(out, "tests overridden") {
		t.Fatalf("override: %v\n%s", err, out)
	}
	overridden := false
	for _, r := range *seen {
		overridden = overridden || r.Method == "POST" && r.Path == "/steps/00000000-02c1-7000-8000-000000000000/override" && r.Body["reason"] == "flaky on CI"
	}
	if !overridden {
		t.Fatalf("the named step was not overridden: %+v", *seen)
	}
}

// Without an override act there is nothing to overrule, whatever the steps
// say: the run is refused by name and nothing is sent.
func TestOverrideWithoutAnActSendsNothing(t *testing.T) {
	seen := fakeForge(t, func(method, path string) (int, string) {
		if path == "/changes/00000000-0007-7000-8000-000000000000" {
			return 200, `{"detail":{"run":{"id":"00000000-0046-7000-8000-000000000000","status":"running","currentStage":"gates"},"next":[],
				"steps":[{"id":"00000000-02c1-7000-8000-000000000000","stage":"gates","name":"tests","type":"tests","status":"fail"}]}}`
		}
		return 404, `{"error":"unexpected"}`
	})
	if _, err := run(t, "override", "00000000-0007-7000-8000-000000000000", "why", "--no-follow"); err == nil || !strings.Contains(err.Error(), "run 00000000-0046-7000-8000-000000000000 has no failed check to override") {
		t.Fatalf("override without an act: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("sent more than the read: %+v", *seen)
	}
}

// A landing refused because another change is landing waits its turn, on the
// refusal's code; the same sentence without the code is AppTown's answer.
func TestLandingWaitsItsTurnOnTheCodeNotTheSentence(t *testing.T) {
	landingRetryEvery = 0
	busy := `{"error":"one landing at a time: another change is landing"}`
	tries := 0
	fakeForgeHeaders(t, func(method, path string) (int, http.Header, string) {
		tries++
		if tries == 1 {
			return 409, http.Header{"X-AppTown-Deny-Code": {"landing-occupied"}}, busy
		}
		return 201, nil, `{"run":{"id":"00000000-005a-7000-8000-000000000000"}}`
	})
	var err error
	out := capture(t, func() { _, err = landWhenClear(idstest.N(7), os.Stdout) })
	if err != nil || tries != 2 || !strings.Contains(out, "landing now") {
		t.Fatalf("contended landing: %v after %d tries\n%s", err, tries, out)
	}
	tries = 0
	fakeForge(t, func(method, path string) (int, string) { tries++; return 422, busy })
	if _, err := landWhenClear(idstest.N(7), os.Stdout); err == nil || tries != 1 {
		t.Fatalf("an uncoded refusal was waited on: %v after %d tries", err, tries)
	}
}
