package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func detailFrom(t *testing.T, raw string) loopRunDetail {
	t.Helper()
	var d loopRunDetail
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// A build log several steps report is shown once, as its first line and
// its tail, not in full for each step (apptown show printed one three times).
func TestTheDisagreementShowsEachOutputOnce(t *testing.T) {
	var log []string
	log = append(log, "building the app image: exit status 1")
	for i := 1; i <= 200; i++ {
		log = append(log, fmt.Sprintf("#%d step %d", i, i))
	}
	log = append(log, "ERROR: gem not found")
	reason, _ := json.Marshal(strings.Join(log, "\n"))
	d := detailFrom(t, `{"run":{"id":"00000000-0005-7000-8000-000000000000","status":"running","currentStage":"build"},"steps":[
		{"stage":"build","name":"boot","type":"preview","status":"fail","evidence":{"reason":`+string(reason)+`}},
		{"stage":"build","name":"tests","type":"tests","status":"fail","evidence":{"reason":`+string(reason)+`,"logTail":`+string(reason)+`}}]}`)
	out := disputeText(d)
	if strings.Count(out, "ERROR: gem not found") != 1 || strings.Count(out, "building the app image") != 1 {
		t.Fatalf("an output was repeated or lost:\n%s", out)
	}
	if strings.Contains(out, "#5 step 00000000-0005-7000-8000-000000000000\n") || !strings.Contains(out, "lines omitted") || strings.Count(out, "the same output as boot") != 2 {
		t.Fatalf("the log was not cut to its tail, or a repeat not named:\n%s", out)
	}
}

// A review the model passed and AppTown's file check failed reads as the
// forge's failure, with the reviewer's pass shown apart — never as
// "reviewer rejected" over a positive summary.
func TestTheDisagreementKeepsTheFileCheckApartFromTheReviewer(t *testing.T) {
	d := detailFrom(t, `{"run":{"id":"00000000-0005-7000-8000-000000000000","status":"running","currentStage":"build"},"steps":[
		{"stage":"build","name":"review","type":"agent/review","status":"fail","evidence":{"verdict":"fail","reviewerVerdict":"pass",
		 "reason":"AppTown's file check failed: 2 changed files the report doesn't mention","summary":"AppTown's file check failed: 2 changed files the report doesn't mention",
		 "reviewerSummary":"all claims verified","findings":["unclaimed change: Gemfile, bin/rails"]}}]}`)
	out := disputeText(d)
	if !strings.Contains(out, "review (AppTown's file check, fail)") || !strings.Contains(out, "the reviewer itself passed it: all claims verified") || strings.Contains(out, "(reviewer,") {
		t.Fatalf("the file check and the reviewer's pass are not told apart:\n%s", out)
	}
}

// show names a preview's address only while the preview owner says it is
// serving, never a step's recorded address.
func TestShowNamesOnlyALivePreview(t *testing.T) {
	const url = "https://change-00000000-0024-7000-8000-000000000000.preview.example.test"
	for state, want := range map[string]string{"live": url, "starting": "starting…", "cold": "", "broken": "", "none": "", "unavailable": ""} {
		if got := previewLine(previewStatus{State: state, URL: url}); got != want {
			t.Errorf("%s: %q, want %q", state, got, want)
		}
	}
	fakeForge(t, func(_, path string) (int, string) {
		switch path {
		case "/changes/00000000-0024-7000-8000-000000000000":
			return 200, `{"change":{"id":"00000000-0024-7000-8000-000000000000","title":"t","state":"in_progress"},"runs":[{"id":"00000000-0168-7000-8000-000000000000","status":"running"}]}`
		case "/runs/00000000-0168-7000-8000-000000000000":
			return 200, `{"run":{"id":"00000000-0168-7000-8000-000000000000","status":"running","currentStage":"build"},"steps":[{"name":"boot","type":"preview","status":"pass","evidence":{"url":"https://run-00000000-0168-7000-8000-000000000000.preview.example.test"}}]}`
		case "/changes/00000000-0024-7000-8000-000000000000/preview":
			return 200, `{"state":"cold"}`
		}
		return 404, `{"error":"unexpected"}`
	})
	out, err := run(t, "show", "00000000-0024-7000-8000-000000000000")
	if err != nil || strings.Contains(out, "Preview") || strings.Contains(out, "run-00000000-0168-7000-8000-000000000000") {
		t.Fatalf("a preview nobody serves was named: %v\n%s", err, out)
	}
}

// apptown changes says where each change is in AppTown's own words: one
// waiting on its reader says so, not the stage's name; --all adds the
// recently shipped and abandoned, which are otherwise left out.
func TestChangesSaysWhatEachChangeWaitsOn(t *testing.T) {
	seen := fakeForge(t, func(_, path string) (int, string) {
		switch path {
		case "/projects/shop/board":
			return 200, `{"columns":[
				{"state":"in_progress","total":2,"items":[{"id":"00000000-0005-7000-8000-000000000000","title":"add stats","stage":"apply","runStatus":"running","workflowName":"quick-change"},
					{"id":"00000000-0006-7000-8000-000000000000","title":"fix totals","stage":"fix","runStatus":"running","workflowName":"fix-bug"}]},
				{"state":"shipped","total":12,"items":[{"id":"00000000-0003-7000-8000-000000000000","title":"the cart","runStatus":"done"}]}]}`
		case "/runs":
			return 200, `[{"id":"00000000-0032-7000-8000-000000000000","changeId":"00000000-0005-7000-8000-000000000000","state":"waiting on you","stateDetail":"accept"},{"id":"00000000-003d-7000-8000-000000000000","changeId":"00000000-0006-7000-8000-000000000000","state":"agent working","stateDetail":"fix"},
				{"id":"00000000-003e-7000-8000-000000000000","changeId":"00000000-0006-7000-8000-000000000000","state":"blocked","stateDetail":"a called run's"}]`
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "changes", "-p", "shop")
	if err != nil || !strings.Contains(out, "waiting on you: accept") || !strings.Contains(out, "fix-bug · fix") || strings.Contains(out, "the cart") {
		t.Fatalf("changes: %v\n%s", err, out)
	}
	if q := (*seen)[0]; q.Path != "/runs" || q.Query != "status=running&project=shop" {
		t.Fatalf("activity read: %+v", q)
	}
	out, err = run(t, "changes", "-p", "shop", "--all")
	if err != nil || !strings.Contains(out, "the cart") || !strings.Contains(out, "11 more shipped") {
		t.Fatalf("changes --all: %v\n%s", err, out)
	}
}

// show says what a change is doing in AppTown's words, with the move that
// answers it, and names a closed change's story for what it became.
func TestShowSaysWhatTheChangeIsDoing(t *testing.T) {
	fakeForge(t, func(_, path string) (int, string) {
		switch path {
		case "/changes/00000000-0005-7000-8000-000000000000":
			return 200, `{"change":{"id":"00000000-0005-7000-8000-000000000000","title":"add stats","state":"in_progress","summary":"stats added"},"state":"waiting on you","stateDetail":"accept","runs":[]}`
		case "/changes/00000000-0006-7000-8000-000000000000":
			return 200, `{"change":{"id":"00000000-0006-7000-8000-000000000000","title":"the cart","state":"shipped","summary":"the cart works"},"runs":[]}`
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "show", "00000000-0005-7000-8000-000000000000")
	if err != nil || !strings.Contains(out, "Now waiting on you: accept — apptown follow 00000000-0005-7000-8000-000000000000") || !strings.Contains(out, "So far") {
		t.Fatalf("show 5: %v\n%s", err, out)
	}
	out, err = run(t, "show", "00000000-0006-7000-8000-000000000000")
	if err != nil || strings.Contains(out, "So far") || !strings.Contains(out, "What shipped") || strings.Contains(out, "Now") {
		t.Fatalf("show 6: %v\n%s", err, out)
	}
}

// A change AppTown closed because there was nothing to change says so in
// changes, show and their --json, from the closedAs AppTown recorded; an
// abandonment, whatever its reason's words, still says abandoned.
func TestAChangeClosedAsNothingToChangeSaysSo(t *testing.T) {
	const closed, abandoned = "00000000-0008-7000-8000-000000000000", "00000000-0009-7000-8000-000000000000"
	fakeForge(t, func(_, path string) (int, string) {
		switch path {
		case "/projects/speed/board":
			return 200, `{"columns":[{"state":"abandoned","total":2,"items":[
				{"id":"` + closed + `","number":8,"title":"update dependencies","closedAs":"nothing-to-change"},
				{"id":"` + abandoned + `","number":9,"title":"try a thing"}]}]}`
		case "/changes/" + closed:
			return 200, `{"projectSlug":"speed","change":{"id":"` + closed + `","number":8,"title":"update dependencies","state":"abandoned","closedAs":"nothing-to-change"},"state":"abandoned","stateDetail":"Nothing to update","runs":[]}`
		case "/changes/" + abandoned:
			return 200, `{"projectSlug":"speed","change":{"id":"` + abandoned + `","number":9,"title":"try a thing","state":"abandoned"},"state":"abandoned","stateDetail":"Nothing to update","runs":[]}`
		case "/runs":
			return 200, `[]`
		}
		return 404, `{"error":"no"}`
	})
	line := func(out, title string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, title) {
				return l
			}
		}
		return ""
	}
	out, err := run(t, "changes", "-p", "speed", "--all")
	if err != nil || !strings.Contains(line(out, "update dependencies"), "closed: nothing to change") || !strings.Contains(line(out, "try a thing"), "abandoned") {
		t.Fatalf("changes --all: %v\n%s", err, out)
	}
	out, err = run(t, "changes", "-p", "speed", "--all", "--json")
	var list ChangeList
	if err != nil || json.Unmarshal([]byte(out), &list) != nil || len(list.Changes) != 2 ||
		list.Changes[0].ClosedAs != "nothing-to-change" || list.Changes[0].State != "abandoned" || list.Changes[1].ClosedAs != "" {
		t.Fatalf("changes --json: %v\n%s", err, out)
	}
	out, err = run(t, "show", closed)
	if err != nil || !strings.Contains(line(out, "update dependencies"), "closed: nothing to change") {
		t.Fatalf("show %s: %v\n%s", closed, err, out)
	}
	out, err = run(t, "show", abandoned)
	if err != nil || !strings.Contains(line(out, "try a thing"), "abandoned") || strings.Contains(out, "nothing to change") {
		t.Fatalf("show %s: %v\n%s", abandoned, err, out)
	}
	out, err = run(t, "show", closed, "--json")
	var detail ChangeDetail
	if err != nil || json.Unmarshal([]byte(out), &detail) != nil || detail.ClosedAs != "nothing-to-change" || detail.State != "abandoned" {
		t.Fatalf("show --json: %v\n%s", err, out)
	}
}

// apptown preview <change> boots or reuses that change's preview from any
// terminal and prints its address; it once refused outside a chat.
func TestPreviewAChangeFromAnyTerminal(t *testing.T) {
	seen := fakeForge(t, func(method, path string) (int, string) {
		if method == "POST" && path == "/changes/00000000-0024-7000-8000-000000000000/preview" {
			return 200, `{"url":"https://change-00000000-0024-7000-8000-000000000000.preview.example.test","commit":"abc1234"}`
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "preview", "00000000-0024-7000-8000-000000000000")
	if err != nil || !strings.Contains(out, "is up at abc1234: https://change-00000000-0024-7000-8000-000000000000.preview.example.test") {
		t.Fatalf("preview: %v\n%s", err, out)
	}
	if len(*seen) != 1 || (*seen)[0].Method != "POST" {
		t.Fatalf("requests: %+v", *seen)
	}
	if _, err := run(t, "preview", "abc"); err == nil || !strings.Contains(err.Error(), `"abc"`) {
		t.Fatalf("bad id: %v", err)
	}
}

// apptown status and apptown show tell a tests step's three readings apart:
// counts with no skips is a plain pass and adds nothing; skips, and counts
// not known, are said (soak finding 23).
func TestTheTestsNoteTellsSkipsAndUnknownCountsApart(t *testing.T) {
	ev := func(raw string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	for raw, want := range map[string]string{
		`{"command":"go test","testCounts":{"passed":44,"failed":0,"skipped":0}}`:                "",
		`{"command":"go test","testCounts":{"passed":41,"failed":0,"skipped":3}}`:                "3 of 44 skipped",
		`{"command":"npm test","testReport":"the test command wrote no report"}`:                 "test counts not reported",
		`{"command":"go test","testReport":"…","testReportUnreadable":"line 3 is not an event"}`: "test report unreadable: line 3 is not an event",
		`{"command":"go test ./..."}`: "test counts not reported",
		`{"reason":"no tests found"}`: "no tests found",
	} {
		if got := testsNote("tests", "pass", ev(raw)); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
	if got := testsNote("tests", "error", ev(`{"command":"go test"}`)); got != "" {
		t.Errorf("an errored tests step has no counts to read: %q", got)
	}
	if got := testsNote("script", "pass", ev(`{"command":"make"}`)); got != "" {
		t.Errorf("a script step: %q", got)
	}
	var d runDetail
	if err := json.Unmarshal([]byte(`{"run":{"id":"00000000-0005-7000-8000-000000000000"},"steps":[
		{"name":"tests","type":"tests","status":"pass","evidence":{"command":"go test","testCounts":{"passed":1,"failed":0,"skipped":4},
		 "skippedTests":["ex/a TestA","ex/a TestB","ex/a TestC","ex/a TestD"]}},
		{"name":"clean","type":"tests","status":"pass","evidence":{"command":"go test","testCounts":{"passed":3,"failed":0,"skipped":0}}}]}`), &d); err != nil {
		t.Fatal(err)
	}
	lines := testsLines([]runDetail{d})
	if len(lines) != 1 || !strings.Contains(lines[0], "tests: 4 of 5 skipped — ex/a TestA, ex/a TestB, ex/a TestC, …") {
		t.Fatalf("show's tests lines: %q", lines)
	}
}
