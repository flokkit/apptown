package main

import (
	"encoding/json"
	"strings"
	"testing"

	"flokkit.io/forge/internal/ids/idstest"
)

// apptown data says each item's copy state with its newest copy, the
// provider's backups or why they are unknown (never none), and whether a
// copy came back; where it started from; what it covers and what it does
// not; a running take-down says writes are stopped.
func TestDataSaysHowEachItemIsProtected(t *testing.T) {
	var p protection
	if err := json.Unmarshal([]byte(`{"deployment":"prod","dataClass":"production","offHost":true,"startFrom":{"backup":"00000000-0025-7000-8000-000000000000","filled":"2026-10-08T04:53:44Z"},
		"items":[{"need":"postgres","where":"database app on forge-ledger-prod","copy":"stale","copyNote":"the last copy failed: could not connect",
		  "lastCopy":{"backup":"00000000-0003-7000-8000-000000000000","takenAt":"2026-10-07T06:10:00Z","bytes":2048},
		  "native":{"mechanism":"do-database-backups","count":0,"unknown":"DigitalOcean answered 500"},
		  "drill":{"at":"2026-10-07T07:00:00Z","backup":"00000000-0003-7000-8000-000000000000","target":"do-syd","place":"a new database on the deployment's cluster","user":"app","passed":true}},
		  {"need":"s3","where":"bucket forge-ledger-prod-b0b0","copy":"fresh",
		  "drill":{"at":"2026-10-07T07:00:01Z","backup":"00000000-0003-7000-8000-000000000000","target":"do-syd","place":"a new s3 store on demo-docker","passed":true}}],
		"running":{"id":"00000000-0009-7000-8000-000000000000","kind":"deployment.take-down","state":"running","askedBy":"alice","createdAt":"2026-10-08T00:00:00Z","startedAt":"2026-10-08T00:00:01Z","result":{"writesStopped":true},
		  "steps":[{"name":"deleting the app, so nothing writes","started":"x","ended":"y"},{"name":"copying postgres","started":"y"}]},
		"coverage":{"assessed":["postgres · database app on forge-ledger-prod"],"notAssessed":["data the app keeps outside this deployment"]}}`), &p); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	printProtection(&out, p)
	for _, want := range []string{
		"started from set 00000000-0025-7000-8000-000000000000, filled 2026-10-08T04:53:44Z",
		"copies stale: newest set 00000000-0003-7000-8000-000000000000, 2026-10-07T06:10:00Z, 2048 bytes (the last copy failed: could not connect)",
		"do-database-backups: unknown — DigitalOcean answered 500",
		"came back on 2026-10-07T07:00:00Z from set 00000000-0003-7000-8000-000000000000 into a new database on the deployment's cluster as app\n",
		"came back on 2026-10-07T07:00:01Z from set 00000000-0003-7000-8000-000000000000 into a new s3 store on demo-docker\n",
		"running: deployment.take-down 00000000-0009-7000-8000-000000000000, asked by alice at 2026-10-08T00:00:00Z — copying postgres (writes stopped)",
		"covers: postgres · database app on forge-ledger-prod\n  not assessed: data the app keeps outside this deployment\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

// apptown data says why copies, checks and restore tests cannot start, in the
// forge's own sentence, and says nothing of it when they can.
func TestDataSaysWhyItsOperationsCannotStart(t *testing.T) {
	const why = "Backups and restore tests need a copy runner: a Docker host that runs the copy commands. An administrator can choose one in the target's settings."
	for name, tc := range map[string]struct {
		body string
		says bool
	}{
		"unavailable": {`{"deployment":"prod","dataClass":"production","items":[],"operations":{"copy":{"available":false,"reason":"no-copy-runner","why":"` + why + `"}}}`, true},
		"available":   {`{"deployment":"prod","dataClass":"production","items":[],"operations":{"copy":{"available":true}}}`, false},
	} {
		var p protection
		if err := json.Unmarshal([]byte(tc.body), &p); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		printProtection(&out, p)
		if strings.Contains(out.String(), why) != tc.says {
			t.Errorf("%s: says the reason %v, want %v:\n%s", name, !tc.says, tc.says, out.String())
		}
	}
}

// apptown data says the last restore rehearsal, what it held back and that it
// blocked no outbound network; with none, why there was none.
func TestDataSaysTheLastRehearsal(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"deployment":"prod","dataClass":"production","rehearsal":{"at":"2026-10-08T01:00:00Z","outcome":"passed","passed":true,"backup":"00000000-0004-7000-8000-000000000000","release":7,"host":"rehearse-1",
			"facts":{"withheld":["SMTP_PASSWORD"],"notStarted":["worker"],"egress":"outbound network not blocked"}}}`,
			"last restore rehearsal: passed 2026-10-08T01:00:00Z from set 00000000-0004-7000-8000-000000000000, release 7, on rehearse-1\n    outbound network not blocked; withheld: SMTP_PASSWORD; not started: worker"},
		{`{"deployment":"prod","dataClass":"production","rehearsal":{"at":"2026-10-08T01:00:00Z","outcome":"failed","backup":"00000000-0004-7000-8000-000000000000","release":7,"host":"rehearse-1","failedStep":"asking the app","facts":{"egress":"outbound network not blocked"}}}`,
			"last restore rehearsal: failed 2026-10-08T01:00:00Z from set 00000000-0004-7000-8000-000000000000, release 7, on rehearse-1, while asking the app"},
		{`{"deployment":"prod","dataClass":"production","rehearsal":{"at":"2026-10-08T01:00:00Z","outcome":"unknown: AppTown stopped while it ran","operation":"00000000-000c-7000-8000-000000000000","facts":{"egress":"outbound network not blocked"}}}`,
			"last restore rehearsal 2026-10-08T01:00:00Z: unknown: AppTown stopped while it ran (operation 00000000-000c-7000-8000-000000000000)"},
		{`{"deployment":"prod","dataClass":"production","rehearsalNote":"never rehearsed: no rehearsal place"}`, "last restore rehearsal: never rehearsed: no rehearsal place"},
	} {
		var p protection
		if err := json.Unmarshal([]byte(tc.body), &p); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		printProtection(&out, p)
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("missing %q in:\n%s", tc.want, out.String())
		}
	}
}

// apptown data says the last restore test apart from the rehearsal, with the
// forge's own check of its gate as the web card says it, and each
// standing restored copy: until when, who opened it, that it is opened from
// the forge in a browser, and what it can still do outside.
func TestDataSaysTheLastRestoreTestAndStandingCopies(t *testing.T) {
	var p protection
	if err := json.Unmarshal([]byte(`{"deployment":"prod","dataClass":"production","rehearsalNote":"never rehearsed",
		"restoreTest":{"id":"00000000-0003-7000-8000-000000000000","backup":"00000000-0029-7000-8000-000000000000","release":7,"target":"do-syd","test":true,"outcome":"passed","checkedAt":"2026-10-08T05:00:00Z","endedAt":"2026-10-08T05:02:00Z",
		  "facts":{"gate":[{"name":"refuses a request with no token","path":"/healthz","status":404,"passed":true},{"name":"lets AppTown's own ticket in","path":"/_apptown/enter","status":403,"passed":false}]}},
		"restoredCopies":[{"id":"00000000-0004-7000-8000-000000000000","number":4,"backup":"00000000-0029-7000-8000-000000000000","target":"do-syd","open":true,"checkedAt":"2026-10-08T06:00:00Z","expiresAt":"2026-10-08T10:00:00Z",
		  "warning":"Outbound calls are not blocked.","entries":[{"person":"bls"},{"person":"ana"},{"person":"bls"}]},
		  {"id":"00000000-0003-7000-8000-000000000000","backup":"00000000-0029-7000-8000-000000000000","test":true,"endedAt":"2026-10-08T05:02:00Z"}]}`), &p); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	printProtection(&out, p)
	for _, want := range []string{
		"last restore rehearsal: never rehearsed",
		"last restore test: passed 2026-10-08T05:00:00Z from set 00000000-0029-7000-8000-000000000000, release 7, on do-syd\n    gate: 1 of 2 checks passed; failed: lets AppTown's own ticket in (/_apptown/enter 403)\n",
		"restored copy 4 of prod from set 00000000-0029-7000-8000-000000000000 · open until 2026-10-08T10:00:00Z · opened by bls, ana (open it from AppTown in a browser)",
		"Outbound calls are not blocked.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "restored copy 3") {
		t.Errorf("an ended test is shown as standing:\n%s", out.String())
	}
	p = protection{Deployment: "prod", RestoreTestNote: "never tested on its platform"}
	out.Reset()
	printProtection(&out, p)
	if !strings.Contains(out.String(), "last restore test: never tested on its platform") {
		t.Errorf("no test:\n%s", out.String())
	}
	// A test whose gate was not checked says nothing about a gate.
	p = protection{Deployment: "prod", RestoreTest: &restoredCopy{Outcome: "passed", Backup: idstest.N(41)}}
	out.Reset()
	printProtection(&out, p)
	if strings.Contains(out.String(), "gate:") {
		t.Errorf("a gate line without gate checks:\n%s", out.String())
	}
}

// apptown data take-down says the forge's refusal when no final copy can be
// taken, and sends the choice to go without one only when the person
// passes --without-final-copy, naming the deployment.
func TestDataTakeDownSendsTheChoiceOnlyWhenAsked(t *testing.T) {
	const refused = "prod holds data AppTown cannot take a final copy of now."
	seen := fakeForge(t, func(method, path string) (int, string) {
		if method == "DELETE" && path == "/projects/shop/deployments/prod" {
			return 409, `{"error":"` + refused + `"}`
		}
		return 404, `{"error":"no"}`
	})
	if _, err := run(t, "data", "-p", "shop", "take-down", "prod"); err == nil || !strings.Contains(err.Error(), refused) {
		t.Fatalf("a refused take-down: %v", err)
	}
	if _, err := run(t, "data", "-p", "shop", "take-down", "prod", "--without-final-copy"); err == nil {
		t.Fatal("the fake refuses every take-down")
	}
	var choices []any
	for _, r := range *seen {
		if r.Method == "DELETE" {
			choices = append(choices, r.Body["withoutFinalCopy"])
		}
	}
	if len(choices) != 2 || choices[0] != nil || choices[1] != "prod" {
		t.Fatalf("the choices sent: %v", choices)
	}
}
