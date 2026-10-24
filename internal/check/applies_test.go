package check_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

// A declaration's appliesTo is a closed list of words: required, repository
// alone, tool names spelled as tools are.
func TestAppliesToIsRequiredAndClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		words []string
		want  string
	}{
		{nil, "required"},
		{[]string{}, "required"},
		{[]string{"repository", "build"}, "stands alone"},
		{[]string{"build", "build"}, "twice"},
		{[]string{"everything"}, "not one of"},
		{[]string{"tool:Go"}, "not a tool name"},
		{[]string{"tool:"}, "not a tool name"},
		{[]string{"repository"}, ""},
		{[]string{"part"}, ""},
		{[]string{"build", "static", "container", "tool:go", "tool:node-24"}, ""},
	} {
		err := check.ValidateAppliesTo(tc.words)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%v: %v, want %q", tc.words, err, tc.want)
		}
	}
	if err := check.Validate([]check.Declaration{{Name: "a", Sh: "true"}}); err == nil || !strings.Contains(err.Error(), "appliesTo is required") {
		t.Errorf("a check without appliesTo is refused: %v", err)
	}
	if err := (check.Scanner{Name: "m", Manual: true, AppliesTo: []string{"repository"}}).ValidateApplicability(); err == nil {
		t.Error("a manual scanner declaring appliesTo is refused")
	}
	if err := (check.Scanner{Name: "s"}).ValidateApplicability(); err == nil {
		t.Error("a scan-point scanner without appliesTo is refused")
	}
}

var monorepo = []check.Part{
	{Name: "api", Folder: "api", Kinds: []string{check.KindBuild, check.KindContainer}, Tools: []string{"go"}, Public: true},
	{Name: "worker", Folder: "api", Kinds: []string{check.KindBuild, check.KindContainer}, Tools: []string{"go"}},
	{Name: "web", Folder: "web", Kinds: []string{check.KindBuild, check.KindStatic}, Tools: []string{"node"}},
	{Name: "cache", Kinds: []string{check.KindContainer}},
}

// Each word matches the parts it names; parts sharing a folder are judged
// once, together; a pulled image's part is judged at the root.
func TestAppliesMatchesDeclaredParts(t *testing.T) {
	t.Parallel()
	src := check.Source{Parts: monorepo}
	for _, tc := range []struct {
		words []string
		want  string
	}{
		{[]string{"tool:go"}, "api[api worker]"},
		{[]string{"tool:node"}, "web[web]"},
		{[]string{"static"}, "web[web]"},
		{[]string{"build"}, "api[api worker] web[web]"},
		{[]string{"container"}, ".[cache] api[api worker]"},
		{[]string{"part"}, ".[cache] api[api worker] web[web]"},
		{[]string{"tool:node", "tool:go"}, "api[api worker] web[web]"},
		{[]string{"repository"}, ".[]"},
	} {
		places, why, err := check.Applies(tc.words, src)
		if err != nil || why != "" {
			t.Fatalf("%v: %v %q", tc.words, err, why)
		}
		var got []string
		for _, p := range places {
			got = append(got, p.Folder+"["+strings.Join(p.Parts, " ")+"]")
		}
		if strings.Join(got, " ") != tc.want {
			t.Errorf("%v: %v, want %s", tc.words, got, tc.want)
		}
	}
	_, why, _ := check.Applies([]string{"tool:python"}, src)
	if !strings.Contains(why, "no part is built with python") || !strings.Contains(why, "api, a container, built with go") {
		t.Errorf("the reason names what was looked for and what is declared: %q", why)
	}
	if _, why, _ := check.Applies([]string{"repository"}, check.Source{Empty: true}); why != "the repository holds no files" {
		t.Errorf("an empty tree: %q", why)
	}
	if _, why, _ := check.Applies([]string{"build"}, check.Source{NoContract: true}); !strings.Contains(why, "no .apptown/app.yaml") {
		t.Errorf("no contract: %q", why)
	}
	// A contract that cannot be read is an error for part checks, never
	// not applicable; a repository check still runs.
	broken := check.Source{PartsErr: errors.New("line 3: bad")}
	if _, _, err := check.Applies([]string{"build"}, broken); err == nil {
		t.Error("an unreadable contract is an error for a part check")
	}
	if places, _, err := check.Applies([]string{"repository"}, broken); err != nil || len(places) != 1 {
		t.Errorf("a repository check runs whatever the contract: %v %v", places, err)
	}
}

func panicking(t *testing.T) *check.Judge {
	return &check.Judge{ShImage: testShImage,
		Run: func(execution.Request) execution.Outcome {
			t.Fatal("ran a check that does not apply")
			return execution.Outcome{}
		},
		RunImage: func(string, []string, []string) (int, string) {
			t.Fatal("ran an image check that does not apply")
			return 0, ""
		},
		Probe: check.ProberFunc(func(context.Context, string) (int, error) {
			t.Fatal("probed for a check that does not apply")
			return 0, nil
		}),
		Complete: check.CompleterFunc(func(context.Context, string, string) (check.Completion, error) {
			t.Fatal("asked a model about a check that does not apply")
			return check.Completion{}, nil
		}),
		Scanners: check.ScannersFunc(func(name string) (check.Scanner, bool) {
			return check.Scanner{Name: name, Image: "x", Target: map[bool]string{true: "exec-image"}[name == "inside"], Command: []string{"true"}}, true
		}),
	}
}

// A check that applies to nothing is not applicable, whatever its
// evaluator, and nothing runs for it.
func TestNotApplicableRunsNothing(t *testing.T) {
	t.Parallel()
	staticOnly := check.Source{Tree: t.TempDir(), Parts: []check.Part{{Name: "site", Folder: ".", Kinds: []string{check.KindStatic}, Public: true}}}
	go1 := []string{"tool:go"}
	checks := []check.Declared{
		{Declaration: check.Declaration{Name: "sh", Evaluator: "sh", Sh: "exit 0", AppliesTo: go1}},
		{Declaration: check.Declaration{Name: "tool", Evaluator: "tool", Tool: "t", AppliesTo: go1}},
		{Declaration: check.Declaration{Name: "inside", Evaluator: "tool", Tool: "inside", AppliesTo: []string{"part"}}},
		{Declaration: check.Declaration{Name: "probe", Evaluator: "runtime", Probe: "page", AppliesTo: go1}},
		{Declaration: check.Declaration{Name: "rubric", Evaluator: "llm", Rubric: "r", Scope: "tree", Files: []string{"*"}, AppliesTo: go1}},
	}
	evs, err := panicking(t).Evaluate(context.Background(), checks, staticOnly, check.Gate)
	if err != nil || len(evs) != len(checks) {
		t.Fatalf("%v %d", err, len(evs))
	}
	for _, ev := range evs {
		if ev.Outcome != check.NotApplicable || ev.Detail == "" || ev.Fault {
			t.Errorf("%s: %s %q", ev.Name, ev.Outcome, ev.Detail)
		}
	}
	if ev := evs[2]; !strings.Contains(ev.Detail, "static site") {
		t.Errorf("an exec-image check on a static public part: %q", ev.Detail)
	}
	if check.NotApplicable.String() != "not-applicable" {
		t.Error(check.NotApplicable.String())
	}
}

// A per-part check runs in each folder of the parts it applies to, with the
// folder and the parts in its environment, and its one evaluation is the
// worst of them with each folder's words labelled.
func TestAPartCheckRunsInEachFolder(t *testing.T) {
	t.Parallel()
	tree := t.TempDir()
	for _, f := range []string{"api/go.mod", "web/package.json"} {
		if err := os.MkdirAll(filepath.Join(tree, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := check.Source{Tree: tree, Parts: monorepo}
	j := &check.Judge{ShImage: testShImage, Run: localShell()}
	// Fails where there is no go.mod: the check found nothing to judge.
	d := check.Declared{Identity: check.Identity{Namespace: "b", Version: 1}, Declaration: check.Declaration{
		Name: "has-go-mod", Evaluator: "sh", Severity: "high", Occurrences: "lines", AppliesTo: []string{"build"},
		Sh: `[ -f go.mod ] || { echo "no go.mod in $APPTOWN_FOLDER for $APPTOWN_PARTS_HERE"; exit 1; }; [ -d "$APPTOWN_ROOT/api" ] || exit 2`}}
	ev := j.EvaluateSh([]check.Declared{d}, src, check.Gate, "t")[0]
	if ev.Outcome != check.DoesNotConform {
		t.Fatalf("%s %q", ev.Outcome, ev.Evidence.Output)
	}
	if len(ev.Findings) != 1 || ev.Findings[0].Fingerprint != check.Fingerprint(d.Identity, d.Name, "") {
		t.Fatalf("one finding under the check's fingerprint: %+v", ev.Findings)
	}
	if got := strings.Join(ev.Occurrences, "|"); got != "web: no go.mod in web for web" {
		t.Errorf("occurrences: %q", got)
	}
	// An error in one folder is the check's error, beside a pass elsewhere.
	d.Sh = `[ -f go.mod ] && exit 0; exit 3`
	if ev := j.EvaluateSh([]check.Declared{d}, src, check.Gate, "t")[0]; ev.Outcome != check.Error {
		t.Errorf("worst outcome: %s", ev.Outcome)
	}
	// A folder the contract names and the tree lacks is a finding.
	missing := check.Source{Tree: tree, Parts: []check.Part{{Name: "gone", Folder: "gone", Kinds: []string{check.KindBuild}}}}
	d.Sh = "exit 0"
	if ev := j.EvaluateSh([]check.Declared{d}, missing, check.Gate, "t")[0]; ev.Outcome != check.DoesNotConform || !strings.Contains(ev.Evidence.Output, "not in the tree") {
		t.Errorf("a missing folder: %s %q", ev.Outcome, ev.Evidence.Output)
	}
}

// A tool runs in the folder it applies to (${PART_FOLDER}); its declared
// nothing-to-judge exit is a failure there, never a pass or an error.
func TestAToolRunsInItsFolderAndItsEmptyExitFails(t *testing.T) {
	t.Parallel()
	var argvs []string
	exit := 0
	j := &check.Judge{ShImage: testShImage,
		Scanners: check.ScannersFunc(func(string) (check.Scanner, bool) {
			return check.Scanner{Name: "lister", Image: "x", Command: []string{"ls", "${PART_FOLDER}"}, EmptyExit: 128}, true
		}),
		Run: func(req execution.Request) execution.Outcome {
			argvs = append(argvs, strings.Join(req.Argv, " "))
			return execution.Outcome{Termination: execution.Exited, ExitCode: exit}
		}}
	d := check.Declared{Declaration: check.Declaration{Name: "t", Evaluator: "tool", Tool: "lister", AppliesTo: []string{"tool:go", "tool:node"}}}
	src := check.Source{Tree: t.TempDir(), Parts: monorepo}
	if ev := j.EvaluateTool(d, src, check.Gate, "t"); ev.Outcome != check.Conforms {
		t.Fatalf("%s %q", ev.Outcome, ev.Detail)
	}
	if got := strings.Join(argvs, "|"); got != "ls /work/api|ls /work/web" {
		t.Errorf("argv: %q", got)
	}
	exit = 128
	if ev := j.EvaluateTool(d, src, check.Gate, "t"); ev.Outcome != check.DoesNotConform || !strings.Contains(ev.Detail, "found nothing to judge") {
		t.Errorf("empty exit: %s %q", ev.Outcome, ev.Detail)
	}
	exit = 2
	if ev := j.EvaluateTool(d, src, check.Gate, "t"); ev.Outcome != check.Error {
		t.Errorf("another exit over 1: %s", ev.Outcome)
	}
}
