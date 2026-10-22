package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/stock"
	"gopkg.in/yaml.v3"
)

// The loop's next move is AppTown's reading of the run, not the client's:
// a failed verdict AppTown is about to check again after a rework is
// still working, and only AppTown's "blocked" and "waiting on you" stop
// it (sweep 7, B1).
func TestTheLoopMovesOnAppTownsState(t *testing.T) {
	steps := `[{"id":"00000000-0001-7000-8000-000000000000","stage":"build","name":"work","type":"agent/pi","status":"pass"},
		{"id":"00000000-0002-7000-8000-000000000000","stage":"build","name":"review","type":"agent/review","status":"fail"},
		{"id":"00000000-0003-7000-8000-000000000000","stage":"build","name":"accept","type":"approval","status":"pending"},
		{"id":"00000000-0009-7000-8000-000000000000","stage":"old","name":"gone","type":"approval","status":"pending"}]`
	// The approval to decide is the one AppTown offers, not one the
	// client finds: without the act, a pending approval is not offered.
	offer := `[{"act":"approve","stepId":"00000000-0003-7000-8000-000000000000","step":"accept"}]`
	for _, c := range []struct {
		state, detail string
		next          string
		move          string
		id            ids.ID
		name          string
	}{
		{"working", "checking review again after the rework", "[]", "wait", ids.Nil, ""},
		{"agent working", "work", "[]", "wait", ids.Nil, ""},
		{"blocked", "review (fail)", "[]", "blocked", ids.Nil, "review (fail)"},
		{"waiting on you", "accept", offer, "approve", idstest.N(3), "accept"},
		{"waiting on you", "accept", "[]", "waiting", ids.Nil, "accept"},
		{"ready to advance", "", "[]", "wait", ids.Nil, ""},
	} {
		d := detailFrom(t, `{"run":{"status":"running","currentStage":"build"},"state":"`+c.state+`","stateDetail":"`+c.detail+`","next":`+c.next+`,"steps":`+steps+`}`)
		move, id, name := loopMove(d)
		if move != c.move || id != c.id || name != c.name {
			t.Errorf("%s: got %s %d %q, want %s %d %q", c.state, move, id, name, c.move, c.id, c.name)
		}
	}
	// An approval in a called workflow is a person's move the loop names.
	d := detailFrom(t, `{"run":{"status":"running","currentStage":"build"},"state":"waiting on you","stateDetail":"accept (in land)","steps":[]}`)
	if move, id, name := loopMove(d); move != "waiting" || !id.IsZero() || name != "accept (in land)" {
		t.Errorf("called approval: %s %d %q", move, id, name)
	}
}

// A run whose failed verdict AppTown is re-checking after a rework is
// followed to its end, however many polls that takes; it is not called
// blocked by the client (sweep 7, B1: blocked after ~6 s while reworking).
func TestTheLoopWaitsOutAReworkAppTownIsDoing(t *testing.T) {
	pollEvery = 0
	polls := 0
	fakeAppTown(t, func(_, path string) (int, string) {
		if path != "/runs/00000000-0028-7000-8000-000000000000" {
			return 404, `{"error":"unexpected"}`
		}
		polls++
		if polls < 8 {
			return 200, `{"run":{"id":"00000000-0028-7000-8000-000000000000","status":"running","currentStage":"build"},"state":"working","stateDetail":"checking review again after the rework",
				"steps":[{"id":"00000000-0001-7000-8000-000000000000","stage":"build","name":"work","type":"agent/pi","status":"pass"},{"id":"00000000-0002-7000-8000-000000000000","stage":"build","name":"review","type":"agent/review","status":"fail"}]}`
		}
		return 200, `{"run":{"id":"00000000-0028-7000-8000-000000000000","status":"done","currentStage":"build"},"steps":[]}`
	})
	var status string
	var err error
	out := capture(t, func() { status, err = streamRunFor(idstest.N(40), ids.Nil, true) })
	if err != nil || status != "done" || strings.Contains(out, "blocked") {
		t.Fatalf("status %q err %v after %d polls:\n%s", status, err, polls, out)
	}
}

// AppTown's "blocked" stops the loop at once, with AppTown's words.
func TestTheLoopStopsWhenAppTownSaysBlocked(t *testing.T) {
	pollEvery = 0
	fakeAppTown(t, func(_, path string) (int, string) {
		return 200, `{"run":{"id":"00000000-0029-7000-8000-000000000000","status":"running","currentStage":"gates"},"state":"blocked","stateDetail":"tests (fail)","steps":[{"id":"00000000-0001-7000-8000-000000000000","stage":"gates","name":"tests","type":"tests","status":"fail"}]}`
	})
	var status string
	out := capture(t, func() { status, _ = streamRunFor(idstest.N(41), ids.Nil, true) })
	if status != "blocked" || !strings.Contains(out, "blocked at gates by: tests (fail)") {
		t.Fatalf("status %q:\n%s", status, out)
	}
}

// Every loop verb opens its change on a stock workflow that is current:
// `apptown build` once opened changes on the deprecated build workflow
// (sweep 7, A1).
func TestEveryLoopVerbUsesACurrentStockWorkflow(t *testing.T) {
	for name, spec := range verbs {
		raw, err := stock.FS.ReadFile(spec.workflow + ".yaml")
		if err != nil {
			t.Fatalf("forge %s: no stock workflow %q: %v", name, spec.workflow, err)
		}
		var wf struct {
			Metadata struct{ Deprecated bool }
		}
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatal(err)
		}
		if wf.Metadata.Deprecated {
			t.Errorf("forge %s opens changes on the deprecated %s workflow", name, spec.workflow)
		}
	}
}

// The shipped line names the deployment the landing released to with its
// address whatever was observed there, and says what was observed: the
// address once appeared only when the app was seen running (sweep 7, A2).
func TestTheShippedLineNamesTheFedDeploymentAndWhatWasObserved(t *testing.T) {
	deps := []shippedDeployment{
		{Name: "main", URL: "https://shop.example", Observation: "unknown", ObservedAt: "2026-10-03T07:51:12Z", ReleaseRun: idstest.N(70)},
		{Name: "lan", URL: "http://shop.lan", Observation: "running", ObservedAt: "2026-10-03T07:51:12Z", ReleaseRun: idstest.N(12)},
	}
	line := deploymentsLine(deps, idstest.N(70))
	if !strings.Contains(line, "main https://shop.example (its state is not known)") || strings.Contains(line, "lan") {
		t.Fatalf("fed by run 70: %q", line)
	}
	// No deployment names the landing: every deployment, each with its state.
	deps[1].ObservationStale = true
	line = deploymentsLine(deps, idstest.N(99))
	if !strings.Contains(line, "main https://shop.example (its state is not known)") || !strings.Contains(line, "lan http://shop.lan (running when last observed)") {
		t.Fatalf("no deployment names the landing: %q", line)
	}
	deps[0].Observation = "stopped"
	if line := deploymentsLine(deps[:1], idstest.N(70)); !strings.Contains(line, "(stopped)") {
		t.Fatalf("stopped: %q", line)
	}
	// Right after a landing nothing has been observed yet: not an unknown
	// state, and the words say so.
	fresh := []shippedDeployment{{Name: "main", URL: "https://shop.example", Observation: "unknown", ObservationStale: true, ReleaseRun: idstest.N(70)}}
	if line := deploymentsLine(fresh, idstest.N(70)); !strings.Contains(line, "main https://shop.example (not observed yet)") {
		t.Fatalf("before the first observation: %q", line)
	}
}

// Without a terminal, a waiting approval names the change and step, the
// exact commands that decide it, and the page — not "approve in the UI".
func TestAnApprovalWithoutATerminalSaysExactlyHowToDecideIt(t *testing.T) {
	fakeAppTown(t, func(_, path string) (int, string) {
		if path == "/steps/00000000-004d-7000-8000-000000000000/question" {
			return 200, `{"step":"accept","status":"pending","means":"I read it"}`
		}
		return 404, `{"error":"no"}`
	})
	var err error
	capture(t, func() {
		err = handleApproval(&loopRunDetail{}, idstest.N(9), idstest.N(77), "accept", false, map[ids.ID]bool{})
	})
	if err == nil {
		t.Fatal("an approval was decided without a terminal or -y")
	}
	for _, want := range []string{`"accept" (step 00000000-004d-7000-8000-000000000000)`, "apptown approve 00000000-004d-7000-8000-000000000000 -y", "apptown follow 00000000-0009-7000-8000-000000000000 -y", api.base + "/changes/00000000-0009-7000-8000-000000000000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("advice lacks %q:\n%s", want, err)
		}
	}
}

// Under -y an approval says in one line what it agreed to, the landing's
// sentence beside it, and sends that sentence back; the briefing's
// paragraphs are for a person deciding (sweep 7: three paragraphs on every
// approval under -y).
func TestAnApprovalUnderYesIsOneLine(t *testing.T) {
	seen := fakeAppTown(t, func(method, path string) (int, string) {
		switch path {
		case "/steps/00000000-004d-7000-8000-000000000000/question":
			return 200, `{"step":"accept","status":"pending","means":"I read it","landingMeans":["it ships on my say-so"],"instructions":"Read the report."}`
		case "/steps/00000000-004d-7000-8000-000000000000/brief":
			return 200, `{"brief":"WHAT THIS IS\n\nthree paragraphs"}`
		case "/steps/00000000-004d-7000-8000-000000000000/approve":
			return 200, `{"name":"accept","status":"pass"}`
		}
		return 404, `{"error":"no"}`
	})
	out := capture(t, func() {
		if err := handleApproval(&loopRunDetail{}, idstest.N(9), idstest.N(77), "accept", true, map[ids.ID]bool{}); err != nil {
			t.Error(err)
		}
	})
	if strings.Count(strings.TrimSpace(out), "\n") != 0 || !strings.Contains(out, "approving accept (-y): “I read it”") || !strings.Contains(out, "the landing: “it ships on my say-so”") {
		t.Fatalf("-y printed:\n%s", out)
	}
	last := (*seen)[len(*seen)-1]
	if also, _ := last.Body["alsoApproves"].([]any); last.Path != "/steps/00000000-004d-7000-8000-000000000000/approve" || len(also) != 1 {
		t.Fatalf("the approval sent: %+v", last)
	}
}

// At an approval the documents its stage requires (the plan, the spec, the
// report its instructions point at) are one key away, newest of each kind.
func TestAnApprovalOffersTheDocumentsItsStageRequires(t *testing.T) {
	d := detailFrom(t, `{"run":{"id":"00000000-0005-7000-8000-000000000000","status":"running","currentStage":"plan"},
		"steps":[{"stage":"plan","name":"draft","type":"agent/pi","config":{"output":{"kind":"plan"}}},
			{"stage":"plan","name":"plan-exists","type":"artifact/require","config":{"kind":"plan"}},
			{"stage":"execute","name":"report-exists","type":"artifact/require","config":{"kind":"exec-report"}},
			{"stage":"plan","name":"accept","type":"approval"}],
		"artifacts":[{"id":"00000000-0003-7000-8000-000000000000","kind":"plan"},{"id":"00000000-0008-7000-8000-000000000000","kind":"plan"},{"id":"00000000-0009-7000-8000-000000000000","kind":"trivy-report"}]}`)
	docs := stageDocuments(d)
	if strings.Join(docs, ",") != "plan" {
		t.Fatalf("documents: %v", docs)
	}
	fakeAppTown(t, func(_, path string) (int, string) {
		if path == "/artifacts/00000000-0008-7000-8000-000000000000" {
			return 200, `{"body":"## Needs a human\n- nothing"}`
		}
		return 200, `{"body":"an older plan"}`
	})
	out := capture(t, func() { printDocuments(d, docs) })
	if !strings.Contains(out, "Needs a human") || strings.Contains(out, "older") {
		t.Fatalf("printed:\n%s", out)
	}
}

// A step's time is AppTown's record of it, whenever the loop attached.
func TestAStepsTimeIsAppTownsRecord(t *testing.T) {
	pollEvery = 0
	fakeAppTown(t, func(_, path string) (int, string) {
		return 200, `{"run":{"id":"00000000-002a-7000-8000-000000000000","status":"done","currentStage":"build"},"steps":[
			{"id":"00000000-0001-7000-8000-000000000000","stage":"build","name":"apply-change","type":"agent/pi","status":"pass","startedAt":"2026-10-02T10:00:00Z","finishedAt":"2026-10-02T10:07:30Z"},
			{"id":"00000000-0002-7000-8000-000000000000","stage":"build","name":"report","type":"artifact/require","status":"pass","startedAt":"","finishedAt":"2026-10-02T10:07:31Z"}]}`
	})
	out := capture(t, func() { _, _ = streamRunFor(idstest.N(42), ids.Nil, true) })
	if !strings.Contains(out, "apply-change 7m30s") || strings.Contains(out, "report 0s") {
		t.Fatalf("step times:\n%s", out)
	}
	if stepTook("2026-10-02T10:07:30Z", "2026-10-02T10:00:00Z") != 0 {
		t.Fatal("a step that finished before it started has a time")
	}
}

// A run silent for a while says what it is still doing, by AppTown's
// clock and in AppTown's words where they say more.
func TestALongSilentStepGetsAHeartbeat(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-10-02T10:04:10Z")
	d := detailFrom(t, `{"run":{"status":"running","currentStage":"ship"},"state":"working","stateDetail":"deploy",
		"steps":[{"name":"deploy","type":"deploy","status":"running","startedAt":"2026-10-02T10:00:00Z"},{"name":"done","type":"tests","status":"pass"}]}`)
	if got := heartbeatLine(d, now); !strings.Contains(got, "deploy still running · 4m10s") || strings.Contains(got, "working") {
		t.Fatalf("heartbeat: %q", got)
	}
	q := detailFrom(t, `{"run":{"status":"running"},"state":"queued","stateDetail":"work — waiting for a slot on worker-1, 2 ahead","steps":[]}`)
	if got := heartbeatLine(q, now); !strings.Contains(got, "queued: work — waiting for a slot on worker-1, 2 ahead") {
		t.Fatalf("queued heartbeat: %q", got)
	}
	pollEvery, heartbeatEvery = 0, 0
	t.Cleanup(func() { heartbeatEvery = 30 * time.Second })
	polls := 0
	fakeAppTown(t, func(_, path string) (int, string) {
		polls++
		if polls < 3 {
			return 200, `{"run":{"id":"00000000-002b-7000-8000-000000000000","status":"running","currentStage":"ship"},"state":"working","stateDetail":"deploy","steps":[{"id":"00000000-0001-7000-8000-000000000000","stage":"ship","name":"deploy","type":"deploy","status":"running","startedAt":"2026-10-02T10:00:00Z"}]}`
		}
		return 200, `{"run":{"id":"00000000-002b-7000-8000-000000000000","status":"done","currentStage":"ship"},"steps":[]}`
	})
	out := capture(t, func() { _, _ = streamRunFor(idstest.N(43), ids.Nil, true) })
	if !strings.Contains(out, "deploy still running") {
		t.Fatalf("no heartbeat while deploying:\n%s", out)
	}
}

// A blocked run's moves include sending it back with a note.
func TestABlockedRunsMovesIncludeSendBack(t *testing.T) {
	pollEvery = 0
	fakeAppTown(t, func(_, path string) (int, string) {
		switch path {
		case "/runs/00000000-0046-7000-8000-000000000000":
			return 200, `{"run":{"id":"00000000-0046-7000-8000-000000000000","status":"running","currentStage":"build"},"state":"blocked","stateDetail":"review (fail)","steps":[]}`
		case "/changes/00000000-0007-7000-8000-000000000000":
			return 200, `{"change":{"id":"00000000-0007-7000-8000-000000000000","state":"in_progress"},"projectSlug":"shop","runs":[{"id":"00000000-0046-7000-8000-000000000000","status":"running","role":"working"}]}`
		}
		return 404, `{"error":"no"}`
	})
	out := capture(t, func() { _ = followChange(idstest.N(7), idstest.N(70), "shop", true) })
	if !strings.Contains(out, `apptown sendback 00000000-0007-7000-8000-000000000000 "<note>"`) || !strings.Contains(out, "apptown retry 00000000-0007-7000-8000-000000000000") {
		t.Fatalf("moves:\n%s", out)
	}
}

// A request that is one word naming an AppTown command is asked about, not
// opened as a change (`apptown quick -p shop follow` opened "follow").
func TestAOneWordRequestThatIsACommandIsNotOpenedUnasked(t *testing.T) {
	seen := fakeAppTown(t, func(string, string) (int, string) {
		return 200, `{"change":{"id":"00000000-0005-7000-8000-000000000000"},"run":{"id":"00000000-0009-7000-8000-000000000000"}}`
	})
	_, err := run(t, "quick", "-p", "shop", "follow")
	if err == nil || !strings.Contains(err.Error(), "apptown follow") {
		t.Fatalf("one-word command request: %v", err)
	}
	if len(*seen) != 0 {
		t.Fatalf("a change was opened: %+v", *seen)
	}
	for _, body := range []string{"follow the redirect", "fixup", "lint"} {
		if err := confirmCommandWord("quick", body, true); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
}

// apptown land knows its change at an approval, so the advice without a
// terminal names the change as well as the step.
func TestLandAtAnApprovalWithoutATerminalNamesTheChange(t *testing.T) {
	pollEvery = 0
	fakeAppTown(t, func(method, path string) (int, string) {
		switch path {
		case "/changes/00000000-0007-7000-8000-000000000000/land":
			return 200, `{"run":{"id":"00000000-0050-7000-8000-000000000000"}}`
		case "/changes/00000000-0007-7000-8000-000000000000":
			return 200, `{"change":{"id":"00000000-0007-7000-8000-000000000000","state":"landing"},"projectSlug":"shop"}`
		case "/runs/00000000-0050-7000-8000-000000000000":
			return 200, `{"run":{"id":"00000000-0050-7000-8000-000000000000","status":"running","currentStage":"ship"},"state":"waiting on you","stateDetail":"approve-landing",
				"next":[{"act":"approve","stepId":"00000000-0051-7000-8000-000000000000","step":"approve-landing"}],
				"steps":[{"id":"00000000-0051-7000-8000-000000000000","stage":"ship","name":"approve-landing","type":"approval","status":"pending"}]}`
		case "/steps/00000000-0051-7000-8000-000000000000/question":
			return 200, `{"step":"approve-landing","means":"it ships on my say-so"}`
		}
		return 404, `{"error":"no"}`
	})
	_, err := run(t, "land", "00000000-0007-7000-8000-000000000000")
	if err == nil || !strings.Contains(err.Error(), "apptown follow 00000000-0007-7000-8000-000000000000 -y") || !strings.Contains(err.Error(), "apptown approve 00000000-0051-7000-8000-000000000000 -y") {
		t.Fatalf("land without a terminal: %v", err)
	}
}

// Under -y the briefing is skipped but what the commit adds is not: it is
// what the approval accepts, and its digest goes back with the approval.
func TestAnApprovalUnderYesStillShowsWhatTheChangeAdds(t *testing.T) {
	seen := fakeAppTown(t, func(method, path string) (int, string) {
		switch path {
		case "/steps/00000000-004d-7000-8000-000000000000/question":
			return 200, questionWithAdds
		case "/steps/00000000-004d-7000-8000-000000000000/approve":
			return 200, `{"name":"accept","status":"pass"}`
		}
		return 404, `{"error":"no"}`
	})
	out := capture(t, func() {
		if err := handleApproval(&loopRunDetail{}, idstest.N(9), idstest.N(77), "accept", true, map[ids.ID]bool{}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "This change adds") || !strings.Contains(out, "Secrets: STRIPE_KEY") {
		t.Fatalf("-y printed:\n%s", out)
	}
	if last := (*seen)[len(*seen)-1]; last.Path != "/steps/00000000-004d-7000-8000-000000000000/approve" || last.Body["adds"] != "d1" {
		t.Fatalf("the approval sent: %+v", last)
	}
}

// Under -y a vote AppTown refuses while the approval still asks (a rework
// started meanwhile) is AppTown's answer: the approval no longer being
// offered is no evidence it was decided.
func TestARefusedVoteOnAStillPendingApprovalIsNotADecision(t *testing.T) {
	fakeAppTown(t, func(method, path string) (int, string) {
		switch path {
		case "/steps/00000000-004d-7000-8000-000000000000/question":
			return 200, questionWithAdds
		case "/steps/00000000-004d-7000-8000-000000000000/approve":
			return 409, `{"error":"the agent is still revising this work; decide after it finishes"}`
		case "/runs/00000000-0028-7000-8000-000000000000":
			return 200, `{"run":{"id":"00000000-0028-7000-8000-000000000000","status":"running","currentStage":"build"},"state":"agent working","next":[],
				"steps":[{"id":"00000000-004d-7000-8000-000000000000","stage":"build","name":"accept","type":"approval","status":"pending"}]}`
		}
		return 404, `{"error":"no"}`
	})
	d := &loopRunDetail{}
	d.Run.ID = idstest.N(40)
	var err error
	out := capture(t, func() { err = handleApproval(d, idstest.N(9), idstest.N(77), "accept", true, map[ids.ID]bool{}) })
	if err == nil || !strings.Contains(err.Error(), "still revising") || strings.Contains(out, "already decided") {
		t.Fatalf("refused vote: %v\n%s", err, out)
	}
}

// apptown help's first section is exactly the commands the compatibility page
// promises through 1.x, and only it says so: a command moved in or out of the
// promise without the page (or the page without help) is caught here.
func TestHelpsPromisedSectionIsTheCompatibilityPagesCommands(t *testing.T) {
	page, err := stock.FS.ReadFile("docs/guides/compatibility.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	start := strings.Index(text, "**The command line's core loop.**")
	end := strings.Index(text[start:], "and their exit codes")
	if start < 0 || end < 0 {
		t.Fatal("the compatibility page no longer lists the core loop's commands")
	}
	var promised []string
	for _, m := range regexp.MustCompile("`([a-z]+)`").FindAllStringSubmatch(text[start:start+end], -1) {
		promised = append(promised, m[1])
	}
	var first []string
	for _, c := range sections[0].commands {
		first = append(first, c.names[0])
	}
	slices.Sort(promised)
	slices.Sort(first)
	if !slices.Equal(promised, first) {
		t.Fatalf("help promises %v; the compatibility page promises %v", first, promised)
	}
	if !strings.HasPrefix(sections[0].heading, "promised through 1.x") {
		t.Fatalf("the first section does not say it is promised: %q", sections[0].heading)
	}
	for _, s := range sections[1:] {
		if !strings.HasPrefix(s.heading, "not promised in 1.x") {
			t.Fatalf("section %q does not say it is not promised", s.heading)
		}
	}
}
