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
	hint := retryHint(idstest.N(308), idstest.N(444), idstest.N(3060), "catch-up-with-main", false)
	for _, want := range []string{"change 00000000-0134-7000-8000-000000000000", "catch-up-with-main (00000000-0bf4-7000-8000-000000000000)", "run 00000000-01bc-7000-8000-000000000000", `apptown sendback 00000000-0134-7000-8000-000000000000 "what to change"`} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint lacks %q:\n%s", want, hint)
		}
	}
	if !strings.Contains(retryHint(idstest.N(9), idstest.N(12), ids.Nil, "", false), "apptown sendback 00000000-0009-7000-8000-000000000000") {
		t.Fatal("hint without a blocker still names the door")
	}
}

// A blocked landing's next step from the command line is apptown recheck
// <change>, which works outside a chat: retry and the landing's own blocked
// line name it, and still name the send-back.
//
// Kills: "run apptown recheck there" (in a chat) and a retry hint naming
// only the send-back, which left run-step one step at a time as the only
// way on (one-server test, 22 Oct).
func TestABlockedLandingNamesTheRecheckThatWorks(t *testing.T) {
	change := idstest.N(308)
	for _, hint := range []string{
		retryHint(change, idstest.N(444), idstest.N(3060), "scan-source:osv", true),
		retryHint(change, idstest.N(444), ids.Nil, "", true),
	} {
		for _, want := range []string{"apptown recheck " + change.String(), "apptown sendback " + change.String()} {
			if !strings.Contains(hint, want) {
				t.Errorf("hint lacks %q:\n%s", want, hint)
			}
		}
	}
	if hint := retryHint(change, idstest.N(444), idstest.N(3060), "tests", false); strings.Contains(hint, "recheck") {
		t.Errorf("a run that is not a landing was offered a landing recheck:\n%s", hint)
	}
	line := landingStopped("hello-game#1", "blocked")
	if !strings.Contains(line, "apptown recheck hello-game#1") || strings.Contains(line, "in its chat") {
		t.Errorf("the blocked landing's line: %s", line)
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

// A change no agent works on, sent back from a blocked landing, is back in
// progress for its person: the CLI says to push a fix and land again, and
// follows nothing (no rework run started).
func TestSendingBackAPersonsChangeSaysPushAndLandAgain(t *testing.T) {
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case method == "POST" && path == "/changes/00000000-0007-7000-8000-000000000000/sendback":
			return 202, `{"runId":null,"stepId":null}`
		case method == "GET" && path == "/changes/00000000-0007-7000-8000-000000000000":
			return 200, `{"projectSlug":"hello-game","change":{"number":2}}`
		}
		return 404, `{"error":"unexpected"}`
	})
	out, err := run(t, "sendback", "00000000-0007-7000-8000-000000000000", "take the token out of the history")
	if err != nil || !strings.Contains(out, "hello-game#2 is back in progress; push your fix to its branch, then land it again: apptown land hello-game#2") {
		t.Fatalf("sendback of a person's change: %v\n%s", err, out)
	}
	if len(*seen) != 2 {
		t.Fatalf("followed a run that does not exist: %+v", *seen)
	}
}

// After apptown new, the next step offered is one this AppTown can do: with
// no model, the person's own change, not apptown quick.
func TestTheFirstChangeHintNeedsNoModelWhenThereIsNone(t *testing.T) {
	if hint := firstChangeHint("hello-game", true); !strings.Contains(hint, "apptown start -p hello-game") || strings.Contains(strings.SplitN(hint, "\n", 2)[0], "apptown quick") {
		t.Fatalf("with no model: %s", hint)
	}
	if hint := firstChangeHint("hello-game", false); !strings.Contains(hint, `apptown quick -p hello-game "<what to change>"`) {
		t.Fatalf("with a model: %s", hint)
	}
}
