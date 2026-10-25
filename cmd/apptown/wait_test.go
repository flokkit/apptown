package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// wait judges from AppTown's activity, not from step statuses: a failed
// verdict AppTown is about to check again after a rework is working, not
// wedged, and only AppTown's "blocked" and "waiting on you" stop the wait.
func TestWaitJudgesFromAppTownsActivity(t *testing.T) {
	steps := `[{"id":"00000000-0001-7000-8000-000000000000","stage":"build","name":"work","type":"agent/pi","status":"pass"},
		{"id":"00000000-0002-7000-8000-000000000000","stage":"build","name":"review","type":"agent/review","status":"fail"},
		{"id":"00000000-0003-7000-8000-000000000000","stage":"build","name":"accept","type":"approval","status":"pending"},
		{"id":"00000000-0009-7000-8000-000000000000","stage":"old","name":"gone","type":"tests","status":"fail"}]`
	for _, c := range []struct {
		state, detail string
		kind          string
		id            ids.ID
		name          string
	}{
		{"working", "checking review again after the rework", "working", ids.Nil, ""},
		{"blocked", "review (fail)", "wedged", idstest.N(2), "review"},
		{"waiting on you", "accept", "decision", idstest.N(3), "accept"},
		{"agent working", "work", "reworking", idstest.N(2), "review"},
		{"queued", "work — waiting for a slot", "reworking", idstest.N(2), "review"},
		{"ready to advance", "", "working", ids.Nil, ""},
	} {
		var d runDetail
		if err := json.Unmarshal([]byte(`{"run":{"status":"running","currentStage":"build"},"state":"`+c.state+`","stateDetail":"`+c.detail+`","steps":`+steps+`}`), &d); err != nil {
			t.Fatal(err)
		}
		kind, id, name := waitVerdict(d)
		if kind != c.kind || id != c.id || name != c.name {
			t.Errorf("%s: got %s %d %q, want %s %d %q", c.state, kind, id, name, c.kind, c.id, c.name)
		}
	}
	// An approval in a called workflow: AppTown's words name it.
	var d runDetail
	_ = json.Unmarshal([]byte(`{"run":{"status":"running","currentStage":"build"},"state":"waiting on you","stateDetail":"accept (in land)","steps":[]}`), &d)
	if kind, id, name := waitVerdict(d); kind != "decision" || !id.IsZero() || name != "accept (in land)" {
		t.Errorf("called approval: %s %d %q", kind, id, name)
	}
}

// A state described by two lines (a run in its stage and the rework in it)
// prints once, not alternately on every poll; a new state prints.
func TestWaitPrintsAStateOnce(t *testing.T) {
	var out strings.Builder
	lines := pollLines{out: &out}
	for _, poll := range [][]string{{"… in build", "↻ reworking"}, {"… in build", "↻ reworking"}, {"… in build", "↻ reworking"}, {"… in review"}} {
		lines.next()
		for _, l := range poll {
			lines.note(l)
		}
	}
	if got := out.String(); got != "… in build\n↻ reworking\n… in review\n" {
		t.Fatalf("printed:\n%s", got)
	}
}

// A run id that names no run is AppTown's answer, not a failure to ride
// out: wait asks once, and when the id is a change's, waits on the change
// (it once only said to rerun with -c).
func TestWaitForAMissingRunWaitsOnTheChangeItNames(t *testing.T) {
	seen := fakeAppTown(t, func(_, path string) (int, string) {
		if path == "/changes/00000000-0514-7000-8000-000000000000" {
			return 200, `{"change":{"id":"00000000-0514-7000-8000-000000000000","state":"shipped"}}`
		}
		return 404, `{"error":"no run 1300"}`
	})
	out, err := run(t, "wait", "00000000-0514-7000-8000-000000000000", "-i", "0")
	if err != nil || !strings.Contains(out, "00000000-0514-7000-8000-000000000000 is a change") || !strings.Contains(out, "change 00000000-0514-7000-8000-000000000000 shipped") {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	runs := 0
	for _, r := range *seen {
		if r.Path == "/runs/00000000-0514-7000-8000-000000000000" {
			runs++
		}
	}
	if runs != 1 {
		t.Fatalf("asked for the missing run %d times", runs)
	}
	// An id that names neither is AppTown's refusal.
	fakeAppTown(t, func(_, path string) (int, string) { return 404, `{"error":"no such thing"}` })
	if _, err := run(t, "wait", "00000000-0515-7000-8000-000000000000", "-i", "0"); err == nil || !strings.Contains(err.Error(), "no such thing") {
		t.Fatalf("neither a run nor a change: %v", err)
	}
}

// A change's name (shop#12) is what a script passes to wait: wait takes
// that form, and a bare number in a project, as the change, never as the
// run with the same number.
func TestWaitTakesAChangesNameAsTheChange(t *testing.T) {
	const change = "00000000-0c12-7000-8000-000000000000"
	for _, args := range [][]string{{"wait", "shop#12", "-i", "0"}, {"wait", "12", "-p", "shop", "-i", "0"}} {
		seen := fakeAppTown(t, func(_, path string) (int, string) {
			switch path {
			case "/changes/shop#12", "/changes/" + change:
				return 200, `{"change":{"id":"` + change + `","state":"shipped"}}`
			}
			return 404, `{"error":"no such thing"}`
		})
		out, err := run(t, args...)
		if err != nil || !strings.Contains(out, "change "+change+" shipped") {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		for _, r := range *seen {
			if strings.HasPrefix(r.Path, "/runs/") {
				t.Fatalf("%v asked for a run: %s", args, r.Path)
			}
		}
	}
}

// retry --no-follow dispatches the rework AppTown offers, with the mode
// and note asked for, and returns, as the other loop commands do, saying how
// to come back.
func TestRetryNoFollowDispatchesAndReturns(t *testing.T) {
	seen := fakeAppTown(t, func(method, path string) (int, string) {
		switch {
		case path == "/changes/00000000-0007-7000-8000-000000000000":
			// AppTown's act names the step; the steps alone would point at
			// another agent step.
			return 200, `{"runs":[{"id":"00000000-0046-7000-8000-000000000000","status":"running"}],"detail":{"run":{"id":"00000000-0046-7000-8000-000000000000","status":"running","currentStage":"build"},
				"next":[{"act":"rework","stepId":"00000000-02bd-7000-8000-000000000000","step":"work"}],
				"steps":[{"id":"00000000-02bd-7000-8000-000000000000","stage":"build","name":"work","type":"agent/pi","status":"pass"},{"id":"00000000-02be-7000-8000-000000000000","stage":"build","name":"polish","type":"agent/pi","status":"pass"}]}}`
		case method == "POST" && path == "/steps/00000000-02bd-7000-8000-000000000000/run":
			return 200, `{}`
		}
		return 404, `{"error":"unexpected"}`
	})
	out, err := run(t, "retry", "00000000-0007-7000-8000-000000000000", "-c", "-m", "use crypto/rand", "--no-follow")
	if err != nil || !strings.Contains(out, "apptown follow 00000000-0007-7000-8000-000000000000") {
		t.Fatalf("retry --no-follow: %v\n%s", err, out)
	}
	dispatched := false
	for _, r := range *seen {
		dispatched = dispatched || r.Method == "POST" && r.Path == "/steps/00000000-02bd-7000-8000-000000000000/run" && r.Body["mode"] == "continue" && r.Body["feedback"] == "use crypto/rand"
	}
	if last := (*seen)[len(*seen)-1]; !dispatched || last.Path != "/steps/00000000-02bd-7000-8000-000000000000/run" {
		t.Fatalf("the rework was not dispatched, or the change was followed after it: %+v", *seen)
	}
}

// Under -y an approval decided while the CLI looked (an acceptance that
// carries over to the landing) is not an error, on the step's own recorded
// pass. A step still pending, or rejected, is not decided however the run
// moved: that AppTown no longer offers it is no evidence of a decision.
func TestAnApprovalDecidedMeanwhileReadsAsDecided(t *testing.T) {
	for _, c := range []struct {
		run, stage, status string
		decided            bool
	}{
		{"running", "ship", "pending", false},
		{"running", "ship", "fail", false},
		{"running", "ship", "pass", true},
		{"running", "deploy", "pending", false},
		{"running", "deploy", "pass", true},
		{"completed", "ship", "pending", false},
	} {
		var d loopRunDetail
		if err := json.Unmarshal([]byte(`{"run":{"status":"`+c.run+`","currentStage":"`+c.stage+`"},"next":[],"steps":[{"id":"00000000-0007-7000-8000-000000000000","stage":"ship","type":"approval","status":"`+c.status+`"}]}`), &d); err != nil {
			t.Fatal(err)
		}
		if decided, found := stepNoLongerAsking(d, idstest.N(7)); !found || decided != c.decided {
			t.Errorf("%+v: decided %v found %v", c, decided, found)
		}
	}
	var d loopRunDetail
	if _, found := stepNoLongerAsking(d, idstest.N(7)); found {
		t.Error("an unknown step was found")
	}
}

// A change whose last run ended without finishing (its landing found main
// had moved) is waited on to AppTown's reason, which carries the
// catch-up, not only "a person's move" (change 61's chat went on saying
// its landing awaited approval). A run waited on directly says the same.
func TestWaitNamesWhyTheLastRunEnded(t *testing.T) {
	const ending = `apptown/change-61 is behind main; catch-up-with-main (fail): behind main: main has moved ahead and apptown/change-61 does not contain it. Catch up in the change's chat (git fetch "$APPTOWN_MAIN_REMOTE" main && git merge --no-edit FETCH_HEAD), then land again.`
	var d runDetail
	body, _ := json.Marshal(map[string]any{"run": map[string]any{"id": "00000000-032c-7000-8000-000000000000", "status": "abandoned", "workflowName": "land"}, "state": "abandoned", "ending": ending})
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if got := noRunGoing("61", "in_progress", &d); !strings.Contains(got, "run 00000000-032c-7000-8000-000000000000 (land) ended: "+ending) || strings.Contains(got, "a person's move") {
		t.Fatalf("wait -c on an ended landing: %s", got)
	}
	if got := runEnded("812", d); got != "✗ run 812 abandoned: "+ending {
		t.Fatalf("wait on the ended landing: %s", got)
	}
	// With no ending (no run yet, or one that finished), the next move is a person's.
	if got := noRunGoing("61", "in_progress", nil); !strings.HasSuffix(got, "a person's move") {
		t.Fatalf("no run: %s", got)
	}
	if got := noRunGoing("61", "in_progress", &runDetail{}); !strings.HasSuffix(got, "a person's move") {
		t.Fatalf("a run with no ending: %s", got)
	}
}

// A release is waited on by its project and number: wait follows its log,
// each line once, until it settles. Released answers success; failed is an
// error (exit 1); a release that does not exist is answered at once.
func TestWaitForAReleaseFollowsItsLogUntilItSettles(t *testing.T) {
	for _, c := range []struct {
		settled string
		wantErr bool
	}{{"released", false}, {"failed", true}} {
		polls := 0
		fakeAppTown(t, func(_, path string) (int, string) {
			if path != "/projects/demo-smoke/releases/4" {
				return 404, `{"error":"no release"}`
			}
			polls++
			if polls < 3 {
				return 200, `{"release":{"state":"releasing"},"events":[{"at":"2026-10-03T05:56:03Z","text":"copying"}]}`
			}
			return 200, `{"release":{"state":"` + c.settled + `"},"events":[{"at":"2026-10-03T05:56:03Z","text":"copying"},{"at":"2026-10-03T05:56:46Z","text":"the end"}]}`
		})
		out, err := run(t, "wait", "-p", "demo-smoke", "-r", "r4", "-i", "0")
		if (err != nil) != c.wantErr || polls != 3 || strings.Count(out, "copying") != 1 || !strings.Contains(out, "05:56:46  the end") {
			t.Fatalf("%s: polls %d, err %v\n%s", c.settled, polls, err, out)
		}
		if !c.wantErr && !strings.Contains(out, "release r4 of demo-smoke released") {
			t.Fatalf("released: %s", out)
		}
	}
	seen := fakeAppTown(t, func(_, path string) (int, string) { return 404, `{"error":"no release #9"}` })
	if _, err := run(t, "wait", "-p", "demo-smoke", "-r", "9", "-i", "0"); err == nil || len(*seen) != 1 {
		t.Fatalf("a missing release was waited on: %v after %d asks", err, len(*seen))
	}
	if _, err := run(t, "wait", "-r", "4"); err == nil || !strings.Contains(err.Error(), "-p <slug>") {
		t.Fatalf("a release without its project: %v", err)
	}
}

// watch and the bare status screen say what a running run is doing in the
// AppTown's words, as the change page and wait do, not by its stage name.
func TestWatchAndHomeSayWhatARunIsDoing(t *testing.T) {
	fakeAppTown(t, func(_, path string) (int, string) {
		switch path {
		case "/runs/00000000-0046-7000-8000-000000000000":
			return 200, `{"run":{"id":"00000000-0046-7000-8000-000000000000","status":"running","currentStage":"stage-2b"},"state":"waiting on you","stateDetail":"accept",
				"steps":[{"id":"00000000-02bd-7000-8000-000000000000","stage":"stage-2b","name":"accept","type":"approval","status":"pending"}]}`
		case "/dashboard":
			return 200, `{"pendingApprovals":[],"activeRuns":[{"runId":"00000000-0047-7000-8000-000000000000","changeId":"00000000-0007-7000-8000-000000000000","itemTitle":"add stats","workflow":"quick-change","stage":"stage-2b","state":"agent working","stateDetail":"implement","projectSlug":"shop"},
				{"runId":"00000000-0048-7000-8000-000000000000","changeId":"00000000-0008-7000-8000-000000000000","itemTitle":"old AppTown","workflow":"quick-change","stage":"stage-2b","projectSlug":"shop"}]}`
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "watch", "00000000-0046-7000-8000-000000000000")
	if err != nil || !strings.Contains(out, "now: waiting on you: accept") || !strings.Contains(out, "run 00000000-0046-7000-8000-000000000000: waiting on you: accept — apptown status 00000000-0046-7000-8000-000000000000") || strings.Contains(out, "stage \"stage-2b\"") {
		t.Fatalf("watch: %v\n%s", err, out)
	}
	out = capture(t, func() { err = cmdHome() })
	if err != nil || !strings.Contains(out, "quick-change · agent working: implement") || !strings.Contains(out, "quick-change · unknown") || strings.Contains(out, "stage-2b") {
		t.Fatalf("home: %v\n%s", err, out)
	}
}

// wait -c says main AppTown could not merge into the change, read from
// the change's payload as the server sends it, with the files and the
// catch-up for the change's chat.
func TestWaitSaysMainThatConflictsWithTheChange(t *testing.T) {
	fakeAppTown(t, func(_, path string) (int, string) {
		return 200, `{"change":{"id":"00000000-0004-7000-8000-000000000000","state":"shipped"},"mainConflict":{"mainBranch":"main","main":"abc","step":"rework","files":["README.md","app.go"],` +
			`"catchUp":"git fetch \"$APPTOWN_MAIN_REMOTE\" main && git merge --no-edit -m \"Merge main into change-00000000-0004-7000-8000-000000000000\" FETCH_HEAD"}}`
	})
	out, err := run(t, "wait", "-c", "00000000-0004-7000-8000-000000000000", "-i", "0")
	want := `! change 00000000-0004-7000-8000-000000000000: main has moved and AppTown could not merge it in before step rework: README.md, app.go conflict. Catch up in the change's chat: git fetch "$APPTOWN_MAIN_REMOTE" main && git merge --no-edit -m "Merge main into change-00000000-0004-7000-8000-000000000000" FETCH_HEAD`
	if err != nil || !strings.Contains(out, want) {
		t.Fatalf("wait -c said (%v):\n%s\nwant\n%s", err, out, want)
	}
}

// apptown wait operation follows an operation to its end and answers how it
// ended in its exit: succeeded is 0 whatever its verification, failed and
// cancelled are 1, and unknown is the unknown-outcome exit (4), never a
// success or a plain failure. A queued operation is waited on.
func TestWaitOperationExitsByHowItEnded(t *testing.T) {
	for _, c := range []struct {
		state   string
		unknown bool
		failed  bool
	}{
		{"succeeded", false, false},
		{"failed", false, true},
		{"cancelled", false, true},
		{"unknown", true, true},
	} {
		t.Run(c.state, func(t *testing.T) {
			polls := 0
			seen := fakeAppTown(t, func(_, path string) (int, string) {
				if path != "/operations/00000000-000c-7000-8000-000000000000" {
					return 404, `{"error":"no such thing"}`
				}
				polls++
				if polls == 1 {
					return 200, `{"id":"00000000-000c-7000-8000-000000000000","kind":"deployment.copy","state":"queued","steps":[]}`
				}
				return 200, `{"id":"00000000-000c-7000-8000-000000000000","kind":"deployment.copy","state":"` + c.state + `","verification":"not-verified","detail":"d","steps":[{"name":"copying","started":"x","ended":"y"}]}`
			})
			out, err := run(t, "wait", "operation", "00000000-000c-7000-8000-000000000000", "-i", "0")
			var unknown *operationUnknown
			if errors.As(err, &unknown) != c.unknown || (err != nil) != c.failed {
				t.Fatalf("%s: %v\n%s", c.state, err, out)
			}
			if len(*seen) != 2 || !strings.Contains(out, "✓ copying") {
				t.Fatalf("%s: polls %v, printed:\n%s", c.state, *seen, out)
			}
		})
	}
	if _, err := run(t, "wait", "-o", "x"); err == nil {
		t.Fatal("a non-numeric operation id was waited on")
	}
}

// apptown release names the deployment it releases to: named, the release
// goes through that deployment's deploy route; unnamed, to the intranet as
// before. Either way the CLI follows that release's log until it settles.
func TestReleaseGoesToTheNamedDeploymentOrTheIntranet(t *testing.T) {
	for _, c := range []struct {
		args []string
		post string
		want string
	}{
		{[]string{"release", "shop", "staging"}, "/projects/shop/deployments/staging/deploy", "released to staging"},
		{[]string{"release", "shop"}, "/projects/shop/release", "live: https://shop.intranet.example"},
	} {
		var posted []string
		fakeAppTown(t, func(method, path string) (int, string) {
			if method == "POST" {
				posted = append(posted, path)
				return 202, `{"ok":true,"number":5,"url":"https://shop.intranet.example"}`
			}
			if path == "/projects/shop/releases/5" {
				return 200, `{"release":{"state":"released"},"events":[]}`
			}
			return 404, `{"error":"no"}`
		})
		out, err := run(t, c.args...)
		if err != nil || len(posted) != 1 || posted[0] != c.post || !strings.Contains(out, c.want) {
			t.Fatalf("%v: posted %v, err %v\n%s", c.args, posted, err, out)
		}
	}
	// -domain is the intranet's hostname; the deploy route has none, so with
	// a deployment named it is refused before anything is sent.
	seen := fakeAppTown(t, func(_, _ string) (int, string) { return 202, `{"ok":true,"number":5}` })
	if _, err := run(t, "release", "shop", "staging", "-domain", "x.example"); err == nil || !strings.Contains(err.Error(), "intranet's") || len(*seen) != 0 {
		t.Fatalf("-domain with a named deployment: %v after %d requests", err, len(*seen))
	}
}

func TestReleaseHostnameCarriesTheObservedAddressRevision(t *testing.T) {
	for _, c := range []struct {
		plan     string
		revision float64
	}{{`{"address":{"revision":7}}`, 7}, {`{}`, 0}} {
		seen := jsonAppTown(t, func(method, path string) (int, string) {
			if method == "GET" && path == "/projects/shop/release/plan" {
				return 200, c.plan
			}
			return 202, `{"number":5,"state":"releasing"}`
		})
		if _, err := run(t, "release", "shop", "-domain", "customer.example", "--json"); err != nil {
			t.Fatal(err)
		}
		if len(*seen) != 2 || (*seen)[0].Path != "/projects/shop/release/plan" || (*seen)[1].Path != "/projects/shop/release" {
			t.Fatalf("revision was not read before release: %+v", *seen)
		}
		body := (*seen)[1].Body
		if body["addressRevision"] != c.revision || body["domain"] != "customer.example" {
			t.Fatalf("release omitted current address revision: %+v", body)
		}
	}
	seen := jsonAppTown(t, func(_, _ string) (int, string) { return 503, `{"error":"address read unavailable"}` })
	if _, err := run(t, "release", "shop", "-domain", "customer.example", "--json"); err == nil || len(*seen) == 0 {
		t.Fatalf("failed address read still released: %v %+v", err, *seen)
	}
	for _, request := range *seen {
		if request.Method != "GET" {
			t.Fatalf("failed address read sent %s", request.Method)
		}
	}
}
