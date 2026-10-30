package verification

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/toolversions"
	"github.com/flokkit/apptown/stock"
	yamlv3 "gopkg.in/yaml.v3"
)

// shippedChecks is every check the stock guidance declares.
func shippedChecks(t *testing.T) []conformance.DeclaredCheck {
	t.Helper()
	items, skipped := wow.ReadStockGuidance(stock.FS, nil)
	if skipped != 0 {
		t.Fatalf("%d stock guidance files could not be read", skipped)
	}
	var out []conformance.DeclaredCheck
	for _, it := range items {
		if it.ChecksJSON == "" {
			continue
		}
		cs, err := check.ParseDeclarations(it.ChecksJSON)
		if err != nil {
			t.Fatalf("%s: %v", it.Name, err)
		}
		for _, c := range cs {
			out = append(out, conformance.DeclaredCheck{Block: it.Name, Version: 1, Check: c})
		}
	}
	if len(out) < 60 {
		t.Fatalf("read %d shipped checks", len(out))
	}
	return out
}

// Every shipped check states what it applies to, every scanner that runs at
// a scan point does, a manual one does not, and every tool named is one
// the shipped tool versions have.
//
// Kills: a shipped check or scan-point scanner left without appliesTo, or
// naming a tool no part could be built with here.
func TestEveryShippedCheckStatesWhatItAppliesTo(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "stock", "tool-versions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	list, err := toolversions.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range shippedChecks(t) {
		for _, tool := range check.AppliesTools(sc.Check.AppliesTo) {
			if !list.HasTool(tool) {
				t.Errorf("%s · %s names tool:%s, which the tool versions do not have", sc.Block, sc.Check.Name, tool)
			}
		}
	}
	scanners := loadScannerManifests(stock.FS)
	if len(scanners) < 10 {
		t.Fatalf("read %d stock scanners", len(scanners))
	}
	for _, m := range scanners {
		if err := m.ValidateApplicability(); err != nil {
			t.Error(err)
		}
		for _, tool := range check.AppliesTools(m.AppliesTo) {
			if !list.HasTool(tool) {
				t.Errorf("scanner %s names tool:%s, which the tool versions do not have", m.Name, tool)
			}
		}
	}
}

// panickingJudge runs nothing: a check that does not apply must not reach
// a container, an image, a probe or a model.
func panickingJudge(t *testing.T, scanners []check.Scanner) *check.Judge {
	return &check.Judge{ShImage: stock.Images().Sh,
		Run: func(execution.Request) execution.Outcome {
			t.Error("ran a check against an empty project")
			return execution.Outcome{}
		},
		RunImage: func(string, []string, []string) (int, string) {
			t.Error("ran an image check against an empty project")
			return 0, ""
		},
		Probe: check.ProberFunc(func(context.Context, string) (int, error) { t.Error("probed an empty project"); return 0, nil }),
		Complete: check.CompleterFunc(func(context.Context, string, string) (check.Completion, error) {
			t.Error("asked a model about an empty project")
			return check.Completion{}, nil
		}),
		Scanners: check.ScannersFunc(func(name string) (check.Scanner, bool) {
			i := slices.IndexFunc(scanners, func(m check.Scanner) bool { return m.Name == name })
			if i < 0 {
				return check.Scanner{}, false
			}
			return scanners[i], true
		}),
	}
}

// Against an empty project, with no files and no contract, every shipped
// check, every stock scanner at its scan point and the stock landing's
// tests step is not applicable, with its reason, and nothing runs: never a
// pass on nothing.
//
// Kills: a check or scanner that passes, errors or runs on an empty
// project (each passed on nothing before applicability existed).
func TestEveryShippedCheckIsNotApplicableToAnEmptyProject(t *testing.T) {
	t.Parallel()
	src := CheckSource(t.TempDir())
	if !src.Empty || !src.NoContract {
		t.Fatalf("an empty tree reads as %+v", src)
	}
	scanners := loadScannerManifests(stock.FS)
	var declared []check.Declared
	for _, sc := range shippedChecks(t) {
		declared = append(declared, sc.Declared())
	}
	evs, err := panickingJudge(t, scanners).Evaluate(context.Background(), declared, src, check.Gate)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != len(declared) {
		t.Fatalf("%d evaluations of %d checks", len(evs), len(declared))
	}
	for _, ev := range evs {
		if ev.Outcome != check.NotApplicable || ev.Detail == "" {
			t.Errorf("%s · %s on an empty project: %s %q", ev.Namespace, ev.Name, ev.Outcome, ev.Detail)
		}
	}
	for _, m := range scanners {
		if m.Manual {
			continue
		}
		if why, err := scannerApplies(m, src); err != nil || why == "" {
			t.Errorf("scanner %s on an empty project: applies (%v)", m.Name, err)
		}
	}
}

// The stock landing's tests step, as the workflow declares it, is not
// applicable to an empty project, and is a failure for a part built with
// a tool that has tests when no test command exists.
func TestTheStockLandingsTestsStepDeclaresWhatItAppliesTo(t *testing.T) {
	t.Parallel()
	raw, err := stock.FS.ReadFile("land.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Spec struct {
			Stages []struct {
				Steps []struct {
					Uses string         `yaml:"uses"`
					With map[string]any `yaml:"with"`
				} `yaml:"steps"`
			} `yaml:"stages"`
		} `yaml:"spec"`
	}
	if err := yamlv3.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var words []string
	for _, st := range doc.Spec.Stages {
		for _, s := range st.Steps {
			if s.Uses == "tests" {
				for _, w := range s.With["appliesTo"].([]any) {
					words = append(words, w.(string))
				}
			}
		}
	}
	for _, tool := range []string{"go", "node", "python", "ruby", "java", "dotnet", "rust"} {
		if !slices.Contains(words, "tool:"+tool) {
			t.Errorf("the landing's tests step does not apply to parts built with %s: %v", tool, words)
		}
	}
	if _, why, _ := check.Applies(words, CheckSource(t.TempDir())); why == "" {
		t.Error("the tests step applies to an empty project")
	}
}

// partTree is a tree whose contract declares one public part, in folder
// app/, matching words, and nothing in that folder to judge.
func partTree(t *testing.T, words []string) string {
	t.Helper()
	dir := t.TempDir()
	part := "    build: app\n    ports: [\"8080\"]\n"
	var tools []string
	for _, w := range words {
		switch {
		case w == check.AppliesStatic:
			part = "    static: app\n"
		case strings.HasPrefix(w, "tool:"):
			tools = append(tools, strings.TrimPrefix(w, "tool:"))
		}
	}
	if len(tools) > 0 && part == "    static: app\n" {
		part = "    static: {build: app, output: dist}\n"
	}
	contract := "version: 2\napp:\n  web:\n" + part + "    public: true\n"
	if len(tools) > 0 {
		contract += "    tools: [" + tools[0] + "]\n"
	}
	writeTree(t, dir, map[string]string{".apptown/app.yaml": contract, "app/.keep": ""})
	return dir
}

// Every shipped shell check that judges parts, run in the folder of a part
// it applies to that holds nothing to judge, is a finding, never a pass:
// the contract says there is something there.
//
// Kills: a shipped script that still exits 0 when its subject is absent.
func TestEveryShippedPartCheckFindsNothingToJudgeAFailure(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	judged := 0
	for _, sc := range shippedChecks(t) {
		if check.Evaluator(sc.Check) != "sh" || slices.Equal(sc.Check.AppliesTo, []string{check.AppliesRepository}) {
			continue
		}
		dir := partTree(t, sc.Check.AppliesTo)
		if _, why, err := check.Applies(sc.Check.AppliesTo, CheckSource(dir)); why != "" || err != nil {
			t.Fatalf("%s · %s: the fixture does not apply: %q %v", sc.Block, sc.Check.Name, why, err)
		}
		results, _, _ := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{sc}, dir, "t-nothing")
		judged++
		if r := results[0]; r.Verdict != workflow.VerdictFail {
			t.Errorf("%s · %s with nothing to judge: %s %q", sc.Block, sc.Check.Name, r.Verdict, strings.TrimSpace(r.Output))
		}
	}
	if judged < 20 {
		t.Fatalf("judged %d part checks", judged)
	}
}

// The contract matches the code: every package manifest is in the folder
// of a part whose tools name its tool, or under notAppCode. A manifest
// below a part's folder counts as that part's (a Go server's embedded UI),
// node_modules, vendor and testdata are not looked in, and one the
// contract does not describe is a line naming it and the fix.
//
// Kills: the check passing a manifest no part declares, or flagging a
// declared one, an ignored folder, or a dependency folder.
func TestTheContractMatchesTheCode(t *testing.T) {
	t.Parallel()
	sc := sourcedNamed(t, "repo/contract-matches-code", "contract-matches-code")
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	for _, tc := range []struct {
		name     string
		files    map[string]string
		verdict  workflow.CheckVerdict
		mentions []string
	}{
		{"a Go part with its embedded UI", map[string]string{
			".apptown/app.yaml": "version: 2\napp:\n  web: {build: ., ports: [\"8080\"], public: true, tools: [go, node]}\n",
			"go.mod":            "module x\n", "frontend/package.json": "{}", "frontend/node_modules/dep/package.json": "{}",
			"vendor/x/go.mod": "", "testdata/fixture/package.json": "{}"}, workflow.VerdictPass, nil},
		{"a monorepo with an undeclared folder and one that is not the app's", map[string]string{
			".apptown/app.yaml": "version: 2\napp:\n  api: {build: api, ports: [\"8080\"], public: true, tools: [go]}\n  web: {build: web}\nnotAppCode: [docs/site]\n",
			"api/go.mod":        "", "web/package.json": "{}", "docs/site/package.json": "{}", "scripts/pyproject.toml": ""},
			workflow.VerdictFail, []string{"web/package.json: no part built with node holds web", "scripts/pyproject.toml: no part built with python"}},
		{"a tool removed while its code stays", map[string]string{
			".apptown/app.yaml": "version: 2\napp:\n  web: {build: ., ports: [\"8080\"], public: true}\n",
			"pyproject.toml":    ""}, workflow.VerdictFail, []string{"pyproject.toml: no part built with python holds ."}},
		{"no contract", map[string]string{"go.mod": ""}, workflow.VerdictFail, []string{"go.mod: there is no .apptown/app.yaml"}},
		{"no manifests", map[string]string{".apptown/app.yaml": "version: 2\napp:\n  game: {static: ., public: true}\n", "index.html": "<p>"}, workflow.VerdictPass, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeTree(t, dir, tc.files)
			results, _, _ := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{sc}, dir, "t-contract")
			r := results[0]
			if r.Verdict != tc.verdict {
				t.Fatalf("%s %q", r.Verdict, r.Output)
			}
			for _, m := range tc.mentions {
				if !strings.Contains(r.Output, m) {
					t.Errorf("output %q does not say %q", r.Output, m)
				}
			}
			if strings.Contains(r.Output, "docs/site") {
				t.Errorf("a notAppCode folder was flagged: %q", r.Output)
			}
		})
	}
}

// A scan-point scanner runs only where the contract declares something it
// applies to, and its declared nothing-to-judge exit is a failure there:
// osv over a .NET part with its lockfile is a scan, over a Go part that
// gave it nothing (exit 128) is a finding, and over an app with no such
// part is not applicable, without running.
//
// Kills: osv's 128 read as a clean pass, a scanner run where nothing it
// applies to is declared, or .NET left out of what osv applies to.
func TestAScanPointScannerAppliesWhereTheContractSaysAndItsEmptyExitFails(t *testing.T) {
	t.Parallel()
	osv := func() map[string]any {
		m := loadScannerManifests(stock.FS)
		i := slices.IndexFunc(m, func(s check.Scanner) bool { return s.Name == "osv" })
		return map[string]any{"image": "osv:test", "command": []string{"osv"}, "report": ".apptown/semgrep.sarif", "format": "osv", "kind": "vuln",
			"appliesTo": m[i].AppliesTo, "emptyExit": m[i].EmptyExit}
	}
	for _, tc := range []struct {
		name    string
		files   map[string]string
		exit    int
		verdict workflow.CheckVerdict
		ran     bool
		says    string
	}{
		{"a .NET part with its lockfile", map[string]string{".apptown/app.yaml": "version: 2\napp:\n  web: {build: ., ports: [\"8080\"], public: true, tools: [dotnet]}\n", "packages.lock.json": "{}"}, 0, workflow.VerdictPass, true, ""},
		{"a Go part osv found nothing in", map[string]string{".apptown/app.yaml": "version: 2\napp:\n  web: {build: ., ports: [\"8080\"], public: true, tools: [go]}\n"}, 128, workflow.VerdictFail, true, "found nothing to judge"},
		{"no part built with a tool osv reads", map[string]string{".apptown/app.yaml": "version: 2\napp:\n  site: {static: ., public: true}\n", "index.html": "<p>"}, 0, workflow.VerdictNotApplicable, false, "no part is built with"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newScannerOwnerFixture(t)
			f.bind(t, "osv", osv())
			f.Effects.report, f.Effects.exit = `{"results":[]}`, tc.exit
			src := t.TempDir()
			writeTree(t, src, tc.files)
			results, findings, errs := f.Scanners.RunSourceScans(context.Background(), src, "test", "", "")
			if errs != 0 || len(results) != 1 {
				t.Fatalf("%d errors, %+v", errs, results)
			}
			r := results[0]
			if r.Verdict != tc.verdict || f.Effects.ran("osv:test") != tc.ran || !strings.Contains(r.Output+r.Detail, tc.says) {
				t.Fatalf("%s %q %q, ran %v", r.Verdict, r.Output, r.Detail, f.Effects.ran("osv:test"))
			}
			if (tc.verdict == workflow.VerdictFail) != (len(findings) == 1) {
				t.Fatalf("findings %+v", findings)
			}
		})
	}
}

// Taking a tool out of a part to make its checks not applicable does not
// let the change through: the contract check fails at high on the head,
// with an occurrence main does not have, so the gate holds the change as
// its own finding while the language's check reads not applicable.
//
// Kills: the contract check below the gate's threshold, or without
// occurrences (a boolean failure main shares would be undecided, not
// blocking).
func TestDeclaringAToolAwayIsTheChangesOwnGatingFinding(t *testing.T) {
	t.Parallel()
	contract := sourcedNamed(t, "repo/contract-matches-code", "contract-matches-code")
	trusts := sourcedNamed(t, "lang/selection/django-fullstack", "trusts-front-door")
	if check.SeverityRank(contract.Check.Severity) < check.SeverityRank("high") || contract.Check.Occurrences != "lines" {
		t.Fatalf("the contract check is %s, occurrences %q: it would not hold a landing", contract.Check.Severity, contract.Check.Occurrences)
	}
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	judge := func(tools string) ([]check.Evaluation, Results) {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{
			".apptown/app.yaml": "version: 2\napp:\n  web: {build: ., ports: [\"8080\"], public: true" + tools + "}\n",
			"pyproject.toml":    "[project]\nname = \"app\"\n", "app/settings.py": "ALLOWED_HOSTS = ['*']\n"})
		checks := []conformance.DeclaredCheck{contract, trusts}
		results, findings, _ := evaluator.runShChecks(context.Background(), stock.Images().Sh, checks, dir, "t-away")
		var evs []check.Evaluation
		for _, f := range findings {
			for _, sc := range checks {
				if strings.HasPrefix(f.Fingerprint, "guidance|"+sc.Block+"@1|"+sc.Check.Name+"|") {
					evs = append(evs, check.Evaluation{Declared: sc.Declared(), Outcome: check.DoesNotConform, Findings: []check.Finding{f.Finding}})
				}
			}
		}
		return evs, results
	}
	base, baseResults := judge(", tools: [python]")
	head, headResults := judge("")
	verdict := func(rs Results, name string) workflow.CheckVerdict {
		for _, r := range rs {
			if r.Check == name {
				return r.Verdict
			}
		}
		return 0
	}
	if verdict(baseResults, "trusts-front-door") != workflow.VerdictFail || verdict(headResults, "trusts-front-door") != workflow.VerdictNotApplicable {
		t.Fatalf("the Django check: base %s, head %s", verdict(baseResults, "trusts-front-door"), verdict(headResults, "trusts-front-door"))
	}
	if verdict(baseResults, "contract-matches-code") != workflow.VerdictPass {
		t.Fatalf("main's contract describes its code: %+v", baseResults)
	}
	judged := check.Classify(head, base, true)
	own := 0
	for _, j := range judged {
		if j.Name == "contract-matches-code" && j.Provenance == check.Introduced && len(j.Own) == 1 {
			own++
		}
	}
	if own != 1 {
		t.Fatalf("the contract check's finding is not the change's own: %+v", judged)
	}
}

// A shipped tool whose own guard finds nothing to judge where its check
// applies says so through the check: semgrep-django over a Python part
// with no Python is the check's failure, with the guard's words and a
// finding, never a scanner error for the report it had no reason to write.
//
// Kills: the guard's exit read as a missing report (an error, which does
// not send a change back).
func TestAShippedToolsGuardIsTheChecksFailure(t *testing.T) {
	t.Parallel()
	m := loadScannerManifests(stock.FS)
	i := slices.IndexFunc(m, func(s check.Scanner) bool { return s.Name == "semgrep-django" })
	if i < 0 {
		t.Fatal("no semgrep-django")
	}
	tree := t.TempDir()
	writeTree(t, tree, map[string]string{
		".apptown/app.yaml": "version: 2\napp:\n  api: {build: api, ports: [\"8080\"], public: true, tools: [python]}\n",
		"api/Dockerfile":    "FROM python:3.12\n"})
	j := &check.Judge{ShImage: stock.Images().Sh,
		Scanners: check.ScannersFunc(func(string) (check.Scanner, bool) { return m[i], true }),
		// The container's /work is the tree here: the host shell runs the
		// shipped command as the container would.
		Run: func(req execution.Request) execution.Outcome {
			for k, a := range req.Argv {
				req.Argv[k] = strings.ReplaceAll(a, "/work", tree)
			}
			req.Image = ""
			return execution.Runner{}.Run(context.Background(), execution.Local, req)
		}}
	d := check.Declared{Identity: check.Identity{Namespace: "lang/selection/django-fullstack", Version: 1},
		Declaration: check.Declaration{Name: "python-sast", Evaluator: "tool", Tool: "semgrep-django", Severity: "medium", AppliesTo: []string{"tool:python"}}}
	ev := j.EvaluateTool(d, CheckSource(tree), check.Gate, "t-guard")
	if ev.Outcome != check.DoesNotConform || !strings.Contains(ev.Detail, "no .py file") || len(ev.Findings) != 1 || ev.Findings[0].Kind != "conformance" {
		t.Fatalf("%s %q %+v", ev.Outcome, ev.Detail, ev.Findings)
	}
}
