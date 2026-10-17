package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/ids"
)

// oneDocument decodes stdout as exactly one JSON document into v, with no
// field v does not name, and nothing after it.
func oneDocument(t *testing.T, out string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("stdout is not one %T: %v\n%s", v, err, out)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("stdout holds more than one document:\n%s", out)
	}
}

const (
	changeUUID = "00000000-0061-7000-8000-000000000000"
	runUUID    = "00000000-0812-7000-8000-000000000000"
	stepUUID   = "00000000-0003-7000-8000-000000000000"
)

// A loop command with --json opens the change and returns: one request, no
// following, and a document naming the change and run by id and number.
func TestLoopJSONOpensAndReturnsWithoutFollowing(t *testing.T) {
	seen := jsonForge(t, func(method, path string) (int, string) {
		if method == "POST" && path == "/projects/shop/changes" {
			return 201, `{"change":{"id":"` + changeUUID + `","number":61},"run":{"id":"` + runUUID + `","number":812}}`
		}
		return 500, `{"error":"followed"}`
	})
	out, err := run(t, "quick", "-p", "shop", "--json", "add a footer")
	if err != nil {
		t.Fatal(err)
	}
	var got Opened
	oneDocument(t, out, &got)
	want := Opened{Change: ChangeRef{ID: mustID(t, changeUUID), Number: 61, Name: "shop#61", Project: "shop"},
		Run: RunRef{ID: mustID(t, runUUID), Number: 812}, Workflow: "quick-change"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(*seen) != 1 {
		t.Fatalf("--json followed the change: %v", *seen)
	}
}

// land --json returns the landing run once the forge accepts it. Waiting
// its turn behind another landing is said on stderr, so stdout is still
// the one document.
func TestLandJSONNamesTheLandingRun(t *testing.T) {
	prev := landingRetryEvery
	landingRetryEvery = 0
	t.Cleanup(func() { landingRetryEvery = prev })
	tries := 0
	seen := jsonForgeHeaders(t, func(method, path string) (int, http.Header, string) {
		switch {
		case method == "GET" && path == "/changes/"+changeUUID:
			return 200, nil, `{"projectSlug":"shop","change":{"id":"` + changeUUID + `","number":61}}`
		case method == "POST" && path == "/changes/"+changeUUID+"/land":
			if tries++; tries == 1 {
				return 409, http.Header{"X-Forge-Deny-Code": {"landing-occupied"}}, `{"error":"shop#60 is landing"}`
			}
			return 202, nil, `{"run":{"id":"` + runUUID + `","number":813}}`
		}
		return 500, nil, `{"error":"followed"}`
	})
	out, err := run(t, "land", changeUUID, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got LandingRequested
	oneDocument(t, out, &got)
	if got.Change.Name != "shop#61" || got.Run != (RunRef{ID: mustID(t, runUUID), Number: 813}) || len(*seen) != 3 {
		t.Fatalf("got %+v after %v", got, *seen)
	}
}

// show --json carries the change, its runs and commits, and the approval it
// waits on.
func TestShowJSONCarriesTheApprovalItWaitsOn(t *testing.T) {
	jsonForge(t, func(method, path string) (int, string) {
		return 200, `{"projectSlug":"shop","change":{"id":"` + changeUUID + `","number":61,"title":"footer","state":"in_progress","branch":"forge/change-61"},
			"state":"waiting on you","stateDetail":"accept",
			"runs":[{"id":"` + runUUID + `","number":812,"workflowName":"quick-change","role":"working","status":"running","currentStage":"build"}],
			"commits":[{"sha":"abc1234","subject":"Add a footer","when":"1 minute ago"}],
			"approval":{"stepId":"` + stepUUID + `"}}`
	})
	out, err := run(t, "show", changeUUID, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got ChangeDetail
	oneDocument(t, out, &got)
	if got.Change.Name != "shop#61" || got.State != "in_progress" || got.StateDetail != "accept" || got.Branch != "forge/change-61" ||
		len(got.Runs) != 1 || got.Runs[0] != (RunDetail{Run: RunRef{ID: mustID(t, runUUID), Number: 812}, Workflow: "quick-change", Role: "working", Status: "running", Stage: "build"}) ||
		len(got.Commits) != 1 || got.Commits[0] != (Commit{SHA: "abc1234", Subject: "Add a footer"}) ||
		got.WaitingOn == nil || got.WaitingOn.Step != mustID(t, stepUUID) {
		t.Fatalf("got %+v", got)
	}
}

// changes --json lists each change by id and number; a board read as empty
// is an empty list, and a board that cannot be read is an error, never a
// list that looks like nothing in flight.
func TestChangesJSONFailsOnAnUnreadableBoard(t *testing.T) {
	board := `{"columns":[{"state":"in_progress","total":1,"items":[{"id":"` + changeUUID + `","number":61,"title":"footer"}]},
		{"state":"shipped","total":1,"items":[{"id":"00000000-0060-7000-8000-000000000000","number":60,"title":"old"}]}]}`
	jsonForge(t, func(_, path string) (int, string) { return 200, board })
	out, err := run(t, "changes", "-p", "shop", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got ChangeList
	oneDocument(t, out, &got)
	if len(got.Changes) != 1 || got.Changes[0] != (ChangeRow{Change: ChangeRef{ID: mustID(t, changeUUID), Number: 61, Name: "shop#61", Project: "shop"}, Title: "footer", State: "in_progress"}) {
		t.Fatalf("got %+v", got)
	}

	jsonForge(t, func(_, path string) (int, string) { return 200, `{"columns":[]}` })
	if out, err := run(t, "changes", "-p", "shop", "--json"); err != nil || strings.TrimSpace(out) != `{
  "changes": []
}` {
		t.Fatalf("an empty board: %v\n%s", err, out)
	}

	jsonForge(t, func(_, path string) (int, string) { return 404, `{"error":"no project shpo"}` })
	out, err = run(t, "changes", "-p", "shpo", "--json")
	if err == nil || !strings.Contains(err.Error(), "no project shpo") || out != "" {
		t.Fatalf("an unreadable board: %v\n%s", err, out)
	}
}

// waitJSON runs wait --json and answers its document and exit code.
func waitJSON(t *testing.T, args ...string) (WaitOutcome, int) {
	t.Helper()
	code := 0
	prev := exitProcess
	exitProcess = func(c int) { code = c }
	t.Cleanup(func() { exitProcess = prev })
	out, err := run(t, append([]string{"wait", "--json", "-i", "0"}, args...)...)
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	var got WaitOutcome
	oneDocument(t, out, &got)
	return got, code
}

// wait --json says what a script acts on: the approval to pass to approve,
// the failed step, or the ending of a change with no run going, with the
// exit code wait always had. The approval it names is the one approve
// --json then decides.
func TestWaitJSONGivesWhatAScriptActsOn(t *testing.T) {
	steps := `[{"id":"00000000-0002-7000-8000-000000000000","stage":"build","name":"review","type":"agent/review","status":"fail"},
		{"id":"` + stepUUID + `","stage":"build","name":"accept","type":"approval","status":"pending"}]`
	running := func(state string) string {
		return `{"run":{"id":"` + runUUID + `","number":812,"status":"running","currentStage":"build","workflowName":"quick-change"},"state":"` + state + `","steps":` + steps + `}`
	}
	ref := &RunRef{ID: mustID(t, runUUID), Number: 812}

	jsonForge(t, func(_, path string) (int, string) { return 200, running("waiting on you") })
	got, code := waitJSON(t, runUUID)
	var err error
	if code != 2 || got.Outcome != "needs_decision" || got.Target != (Target{Kind: "run", ID: mustID(t, runUUID)}) ||
		!reflect.DeepEqual(got.Run, ref) || got.Step == nil || *got.Step != mustID(t, stepUUID) || got.Detail != "accept" {
		t.Fatalf("an approval: exit %d, %+v", code, got)
	}
	// The approval's question carries what the commit adds: it is shown, on
	// stderr, and stdout stays the one document.
	seen := jsonForge(t, func(method, path string) (int, string) {
		if method == "GET" {
			return 200, `{"step":"accept","means":"the footer is right","adds":{"digest":"abc","readFrom":"committed tree","nothingNew":"No new requirements."}}`
		}
		return 200, `{"name":"accept","status":"pass"}`
	})
	var out string
	shown := captureStderr(t, func() { out, err = run(t, "approve", got.Step.String(), "-y", "--json") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shown, "This change adds") || !strings.Contains(shown, "No new requirements.") || !strings.Contains(shown, "the footer is right") {
		t.Fatalf("what approving means was not shown on stderr:\n%s", shown)
	}
	if (*seen)[len(*seen)-1].Body["adds"] != "abc" {
		t.Fatalf("the approval did not send what it showed: %v", (*seen)[len(*seen)-1].Body)
	}
	var decided Decided
	oneDocument(t, out, &decided)
	last := (*seen)[len(*seen)-1]
	if decided != (Decided{Step: mustID(t, stepUUID), Decision: "approve", Status: "pass"}) || last.Method != "POST" || last.Path != "/steps/"+stepUUID+"/approve" {
		t.Fatalf("approve the step wait named: %+v after %v", decided, *seen)
	}

	jsonForge(t, func(_, path string) (int, string) { return 200, running("blocked") })
	got, code = waitJSON(t, runUUID)
	if code != 1 || got.Outcome != "blocked" || got.Step == nil || *got.Step != mustID(t, "00000000-0002-7000-8000-000000000000") || got.Detail != "review" || !reflect.DeepEqual(got.Run, ref) {
		t.Fatalf("a blocked run: exit %d, %+v", code, got)
	}

	const ending = "forge/change-61 is behind main; catch up, then land again."
	jsonForge(t, func(_, path string) (int, string) {
		return 200, `{"change":{"id":"` + changeUUID + `","state":"in_progress"},"state":"in progress",
			"detail":{"run":{"id":"` + runUUID + `","number":812,"status":"abandoned","workflowName":"land"},"ending":"` + ending + `"}}`
	})
	got, code = waitJSON(t, "-c", changeUUID)
	if code != 2 || got.Outcome != "ended" || got.Target.Kind != "change" || got.Step != nil || got.Detail != ending || !reflect.DeepEqual(got.Run, ref) {
		t.Fatalf("a change with no run going: exit %d, %+v", code, got)
	}

	jsonForge(t, func(_, path string) (int, string) {
		return 200, `{"run":{"id":"` + runUUID + `","number":812,"status":"done","workflowName":"quick-change"}}`
	})
	if got, code = waitJSON(t, runUUID); code != 0 || got.Outcome != "done" || got.Step != nil {
		t.Fatalf("a finished run: exit %d, %+v", code, got)
	}
}

// jsonForge is fakeForge for a --json command: when the test ends, every
// request it was sent must be a route the shipped API description
// describes for the CLI (x-forge-used-by: cli), so the description covers
// what the stable commands call.
func jsonForge(t *testing.T, reply func(method, path string) (int, string)) *[]recorded {
	t.Helper()
	return jsonForgeHeaders(t, func(method, path string) (int, http.Header, string) {
		code, body := reply(method, path)
		return code, nil, body
	})
}

func jsonForgeHeaders(t *testing.T, reply func(method, path string) (int, http.Header, string)) *[]recorded {
	t.Helper()
	seen := fakeForgeHeaders(t, reply)
	t.Cleanup(func() {
		for _, r := range *seen {
			if !describedForTheCLI(t, r.Method, "/api/v1"+r.Path) {
				t.Errorf("%s /api/v1%s is called by a --json command but not described for the CLI in stock/docs/api/openapi.json", r.Method, r.Path)
			}
		}
	})
	return seen
}

// describedForTheCLI reports whether method and path match an operation
// the shipped description marks x-forge-used-by: cli.
func describedForTheCLI(t *testing.T, method, path string) bool {
	t.Helper()
	raw, err := os.ReadFile("../../stock/docs/api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for template, item := range doc.Paths {
		var op struct {
			UsedBy string `json:"x-forge-used-by"`
		}
		if json.Unmarshal(item[strings.ToLower(method)], &op) != nil || op.UsedBy != "cli" {
			continue
		}
		pattern := "^" + regexp.MustCompile(`\\\{[A-Za-z0-9_]+\\\}`).ReplaceAllString(regexp.QuoteMeta(template), `[^/]+`) + "$"
		if regexp.MustCompile(pattern).MatchString(path) {
			return true
		}
	}
	return false
}

// captureStderr returns what f wrote on stderr.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	w.Close()
	os.Stderr = prev
	return <-done
}

// Inside a chat, a bare land answers in words; with --json it refuses
// before anything starts, rather than printing half a document.
func TestABareChatLandingRefusesJSON(t *testing.T) {
	t.Setenv("FORGE_SESSION_ID", "00000000-0007-7000-8000-000000000000")
	t.Setenv("FORGE_SESSION_TOKEN", "tok")
	seen := fakeForge(t, func(_, _ string) (int, string) {
		return 202, `{"runId":"` + runUUID + `","message":"landing started"}`
	})
	out, err := run(t, "land", "--json")
	if err == nil || out != "" || len(*seen) != 0 {
		t.Fatalf("bare land --json in a chat: %v, sent %v\n%s", err, *seen, out)
	}
	if _, err := run(t, "land"); err != nil || len(*seen) != 1 || (*seen)[0].Path != "/console/00000000-0007-7000-8000-000000000000/land" {
		t.Fatalf("bare land in a chat: %v, sent %v", err, *seen)
	}
}

// wait --json on an operation gives an outcome only for the record it saw
// end, with the operation's own detail; a read that fails is an error with
// no document, never a failed operation.
func TestWaitJSONOnAnOperationReportsOnlyWhatItSaw(t *testing.T) {
	const opUUID = "00000000-000c-7000-8000-000000000000"
	for _, c := range []struct {
		state, outcome string
		code           int
		unknown        bool
	}{
		{"succeeded", "done", 0, false},
		{"failed", "failed", 1, false},
		{"cancelled", "failed", 1, false},
		{"unknown", "unknown", exitUnknown, true},
	} {
		jsonForge(t, func(_, path string) (int, string) {
			return 200, `{"id":"` + opUUID + `","kind":"deployment.copy","state":"` + c.state + `","detail":"copied 3 volumes","steps":[]}`
		})
		out, err := run(t, "wait", "operation", opUUID, "--json", "-i", "0")
		var got WaitOutcome
		oneDocument(t, out, &got)
		var unknown *operationUnknown
		if got.Outcome != c.outcome || got.ExitCode != c.code || got.Detail != "copied 3 volumes" || got.Target != (Target{Kind: "operation", ID: mustID(t, opUUID)}) ||
			(err != nil) != (c.code != 0) || errors.As(err, &unknown) != c.unknown {
			t.Errorf("%s: %+v, %v", c.state, got, err)
		}
	}
	jsonForge(t, func(_, _ string) (int, string) { return 403, `{"error":"you may not read this operation"}` })
	out, err := run(t, "wait", "operation", opUUID, "--json", "-i", "0")
	if err == nil || !strings.Contains(err.Error(), "may not read") || out != "" {
		t.Fatalf("an unreadable operation: %v\n%s", err, out)
	}
}

// new --json with each of its options prints the project, says what it did
// on stderr, and calls only described routes (jsonForge).
func TestNewJSONWithEveryOption(t *testing.T) {
	const projectUUID = "00000000-0005-7000-8000-000000000000"
	reply := func(method, path string) (int, string) {
		switch path {
		case "/projects":
			return 201, `{"id":"` + projectUUID + `","slug":"shop","name":"Shop"}`
		case "/scm/namespaces":
			return 200, `{"namespaces":[{"id":4,"path":"team"}],"configured":true}`
		case "/projects/shop/repo/remote":
			return 200, `{"remote":"https://git.example/team/shop.git","pushed":"main"}`
		case "/projects/shop/toolchain":
			return 200, `{"ok":true,"toolchain":"go"}`
		case "/projects/shop/pack":
			return 200, `{"pack":"go-small","workflowsInstalled":["quick-change"],"policiesInstalled":[],"guidanceImports":2}`
		case "/projects/shop/secrets":
			return 200, `{"declared":false}`
		}
		return 200, `{"defaultBranch":"main"}`
	}
	seen := jsonForge(t, reply)
	var out string
	var err error
	said := captureStderr(t, func() {
		out, err = run(t, "new", "shop", "--name", "Shop", "--from", "https://git.example/shop.git", "--toolchain", "go", "--pack", "go-small", "--remote", "--json")
	})
	var made ProjectCreated
	if oneDocument(t, out, &made); err != nil || made.ID != mustID(t, projectUUID) || !strings.Contains(said, "pack go-small applied") {
		t.Fatalf("new with options: %v %+v\n%s", err, made, said)
	}
	var paths []string
	for _, r := range *seen {
		paths = append(paths, r.Path)
	}
	for _, want := range []string{"/projects/shop/repo/from-url", "/projects/shop/toolchain", "/scm/namespaces", "/projects/shop/repo/remote", "/projects/shop/pack", "/projects/shop/secrets"} {
		if !slices.Contains(paths, want) {
			t.Errorf("new did not call %s: %v", want, paths)
		}
	}

	bundle := filepath.Join(t.TempDir(), "shop.bundle")
	if err := os.WriteFile(bundle, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	seen = jsonForge(t, reply)
	if out, err = run(t, "new", "shop", "--from", bundle, "--json"); err != nil || !slices.ContainsFunc(*seen, func(r recorded) bool { return r.Path == "/projects/shop/repo/from-bundle" }) {
		t.Fatalf("new from a bundle: %v %v", err, *seen)
	}
	oneDocument(t, out, &made)
}

// approve --json never asks: without -y it refuses before sending anything.
func TestApproveJSONNeedsYes(t *testing.T) {
	seen := jsonForge(t, func(_, _ string) (int, string) { return 200, `{}` })
	if _, err := run(t, "approve", stepUUID, "--json"); err == nil || !strings.Contains(err.Error(), "-y") || len(*seen) != 0 {
		t.Fatalf("approve --json without -y: %v, sent %v", err, *seen)
	}
}

// release, complain, new and version print their documents.
func TestTheOtherCoreCommandsPrintTheirDocuments(t *testing.T) {
	jsonForge(t, func(_, _ string) (int, string) {
		return 202, `{"ok":true,"number":7,"url":"http://shop.lan","state":"releasing"}`
	})
	out, err := run(t, "release", "shop", "--json")
	var rel ReleaseRequested
	if oneDocument(t, out, &rel); err != nil || rel != (ReleaseRequested{Project: "shop", Number: 7, URL: "http://shop.lan", State: "releasing"}) {
		t.Fatalf("release: %v %+v", err, rel)
	}
	// The named-deployment form answers through the deployment's deploy
	// route, with the deployment named and no intranet address.
	seen := jsonForge(t, func(_, _ string) (int, string) {
		return 202, `{"ok":true,"number":8,"deployment":"cloud","state":"releasing"}`
	})
	out, err = run(t, "release", "shop", "cloud", "--json")
	if oneDocument(t, out, &rel); err != nil || rel != (ReleaseRequested{Project: "shop", Deployment: "cloud", Number: 8, State: "releasing"}) ||
		len(*seen) != 1 || (*seen)[0].Path != "/projects/shop/deployments/cloud/deploy" {
		t.Fatalf("release to a deployment: %v %+v %v", err, rel, *seen)
	}

	jsonForge(t, func(_, _ string) (int, string) { return 201, `{"id":"` + stepUUID + `"}` })
	out, err = run(t, "complain", "--json", "slow")
	var filed ComplaintFiled
	if oneDocument(t, out, &filed); err != nil || filed.ID != mustID(t, stepUUID) {
		t.Fatalf("complain: %v %+v", err, filed)
	}

	projectUUID := "00000000-0005-7000-8000-000000000000"
	jsonForge(t, func(_, path string) (int, string) {
		if path == "/projects" {
			return 201, `{"id":"` + projectUUID + `","slug":"shop","name":"Shop"}`
		}
		return 201, `{"defaultBranch":"main"}`
	})
	out, err = run(t, "new", "shop", "--name", "Shop", "--json")
	var made ProjectCreated
	if oneDocument(t, out, &made); err != nil || made != (ProjectCreated{ID: mustID(t, projectUUID), Slug: "shop", Name: "Shop"}) {
		t.Fatalf("new: %v %+v", err, made)
	}

	jsonForge(t, func(_, _ string) (int, string) { return 200, `{}` })
	out, err = run(t, "version", "--json")
	var v Versions
	if oneDocument(t, out, &v); err != nil || v.CLI.Version == "" || v.CLI.Protocol == 0 || !v.Forge.Reached || v.Forge.URL == "" {
		t.Fatalf("version: %v %+v", err, v)
	}
}

// The --json tables cover exactly the core loop the compatibility guide
// names, each command once. (Each command's own test above runs it with
// --json; fix, plan and build share quick's path.)
func TestTheJSONTablesCoverTheCoreLoop(t *testing.T) {
	documented := map[string]int{}
	for _, c := range cliJSONCommands {
		for _, name := range strings.Split(c.Commands, ", ") {
			documented[name]++
		}
	}
	for _, name := range coreLoop {
		if documented[name] != 1 {
			t.Errorf("%s --json is documented %d times", name, documented[name])
		}
		delete(documented, name)
	}
	if len(documented) != 0 {
		t.Errorf("documented outside the core loop: %v", documented)
	}
	guide, err := os.ReadFile("../../stock/docs/guides/compatibility.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range coreLoop {
		if !bytes.Contains(guide, []byte("`"+name+"`")) {
			t.Errorf("the compatibility guide does not name %s", name)
		}
	}
}

// coreLoop is the compatibility guide's list of stable commands.
var coreLoop = []string{"new", "fix", "quick", "plan", "build", "land", "show", "changes", "wait", "approve", "release", "complain", "version"}

var updateCLIJSON = flag.Bool("update-cli-json", false, "rewrite the field tables in stock/docs/guides/cli-json.md")

// The guide's field tables are generated from the output types, so the
// names a script reads there are the names printed.
func TestCLIJSONIsDocumented(t *testing.T) {
	const path = "../../stock/docs/guides/cli-json.md"
	const begin, end = "<!-- generated by TestCLIJSONIsDocumented: begin -->\n", "<!-- generated by TestCLIJSONIsDocumented: end -->\n"
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	i, j := bytes.Index(doc, []byte(begin)), bytes.Index(doc, []byte(end))
	if i < 0 || j < i {
		t.Fatalf("%s has no generated section", path)
	}
	want := append(append(append([]byte{}, doc[:i+len(begin)]...), cliJSONTables()...), doc[j:]...)
	if *updateCLIJSON {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(doc, want) {
		t.Fatalf("%s is stale: go test ./cmd/forge -run TestCLIJSONIsDocumented -update-cli-json", path)
	}
}

// cliJSONTables writes each command's document as a table of fields, then
// each named type it holds, once.
func cliJSONTables() []byte {
	var b bytes.Buffer
	done := map[reflect.Type]bool{}
	var todo []reflect.Type
	table := func(t reflect.Type) {
		b.WriteString("| Field | Type | Meaning |\n|---|---|---|\n")
		for f := range t.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", name, jsonTypeName(f.Type, &todo), f.Tag.Get("doc"))
		}
		b.WriteString("\n")
	}
	for _, c := range cliJSONCommands {
		t := reflect.TypeOf(c.Value)
		done[t] = true
		fmt.Fprintf(&b, "### %s → `%s`\n\n", c.Commands, t.Name())
		table(t)
	}
	for len(todo) > 0 {
		t := todo[0]
		todo = todo[1:]
		if done[t] {
			continue
		}
		done[t] = true
		fmt.Fprintf(&b, "### `%s`\n\n", t.Name())
		table(t)
	}
	return b.Bytes()
}

// jsonTypeName is how a field's JSON reads, queuing the named object types
// it holds for their own tables.
func jsonTypeName(t reflect.Type, todo *[]reflect.Type) string {
	switch {
	case t == reflect.TypeOf(ids.ID{}):
		return "UUID string"
	case t.Kind() == reflect.Pointer:
		return jsonTypeName(t.Elem(), todo) + " or null"
	case t.Kind() == reflect.Slice:
		return "array of " + jsonTypeName(t.Elem(), todo)
	case t.Kind() == reflect.Struct:
		*todo = append(*todo, t)
		return "`" + t.Name() + "`"
	case t.Kind() == reflect.String:
		return "string"
	case t.Kind() == reflect.Bool:
		return "boolean"
	default:
		return "number"
	}
}

func mustID(t *testing.T, s string) ids.ID {
	t.Helper()
	id, err := ids.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
