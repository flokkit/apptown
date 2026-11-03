package workflowexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

// Every declaration runs, including later commands after a failure and parts
// outside applicability. Keep the repository root so mise and sibling files exist.
func TestEveryDeclaredCommandRunsInItsPartsFolder(t *testing.T) {
	t.Parallel()
	f, ch, folder := testsWorld(t, map[string]any{"appliesTo": []any{"tool:go", "tool:node"}})
	for name, body := range map[string]string{
		"mise.toml":                 "root toolchain",
		"api/frontend/package.json": "{}",
	} {
		at := filepath.Join(folder, name)
		if err := os.MkdirAll(filepath.Dir(at), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(at, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	commitAppDeclaration(t, folder, `app:
  web:
    build: api
    public: true
    tools: [go, node]
    test: ["go test ./...", "npm --prefix frontend test", "echo after"]
  other:
    build: .
    tools: [php]
    test: ["echo outside applicability"]
`)
	bin := t.TempDir()
	for name, body := range map[string]string{
		"mise": "exit 0",
		"go":   "cat ../mise.toml; echo go-suite:$PWD",
		"npm":  "test -f frontend/package.json || exit 9; echo frontend-suite:$PWD; exit 3",
	} {
		if err := execfile.Write(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	var calls int
	f.checks = func(req execution.Request) execution.Outcome {
		calls++
		if _, err := os.Stat(filepath.Join(req.Workspace, "mise.toml")); err != nil {
			t.Error("lost staged repository root", err)
		}
		out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
			Workspace: req.Workspace, Argv: req.Argv, Timeout: time.Minute,
			Env: []string{"PATH=" + bin + ":/usr/bin:/bin", "MISE_DATA_DIR=" + bin},
		})
		out.Report = []byte(goStream)
		return out
	}
	step := f.execute(t, testsRun(t, f, ch), "tests")
	ev := stepEvidence(t, step)
	results, _ := ev["commands"].([]any)
	if step.Status != workflow.StatusFail || calls != 4 || len(results) != 4 || ev["testCounts"] != nil {
		t.Fatalf("%s %s, calls %d", step.Status, step.Evidence, calls)
	}
	// Parts are sorted by name; command order within each declaration is preserved.
	frontend := results[2].(map[string]any)
	if frontend["testCounts"] == nil || frontend["exitCode"] != float64(3) || !strings.Contains(fmt.Sprint(frontend["output"]), "frontend-suite:") || !strings.Contains(fmt.Sprint(frontend["output"]), "/api") || frontend["command"] != "npm --prefix frontend test" {
		t.Fatalf("frontend result: %+v", frontend)
	}
	if !strings.Contains(fmt.Sprint(results[1]), "root toolchain") || !strings.Contains(fmt.Sprint(results[3]), "after") {
		t.Fatalf("results: %+v", results)
	}
}

// A suite on another part, even in the same folder, cannot satisfy a missing
// declaration. Missing suites still let every declared command run.
func TestAnotherPartsDeclarationDoesNotExcuseAMissingSuite(t *testing.T) {
	t.Parallel()
	f, ch, folder := testsWorld(t, map[string]any{"appliesTo": []any{"tool:go"}})
	commitAppDeclaration(t, folder, `app:
  web: {build: ., public: true, tools: [go]}
  worker: {build: ., tools: [go], test: ["echo tested"]}
`)
	seen := &markersSeen{}
	f.checks = seen.run
	step := f.execute(t, testsRun(t, f, ch), "tests")
	want := "web is built with go and declares no tests: add `test:` to the part in .apptown/app.yaml"
	if step.Status != workflow.StatusFail || stepEvidence(t, step)["reason"] != want || len(seen.all()) != 1 {
		t.Fatalf("%s %s, ran %d", step.Status, step.Evidence, len(seen.all()))
	}
}

func TestStockPartsWithoutSuitesAreNotApplicable(t *testing.T) {
	t.Parallel()
	for _, seed := range []string{"wordpress", "godot-3d", "django", "html-game"} {
		t.Run(seed, func(t *testing.T) {
			body, err := stock.FS.ReadFile("seeds/" + seed + "/.apptown/app.yaml")
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			commitAppDeclaration(t, dir, string(body))
			declarations, status, reason := declaredTests(testsConfig{AppliesTo: []string{"tool:go", "tool:node", "tool:python", "tool:ruby", "tool:java", "tool:dotnet", "tool:rust"}}, dir)
			if status != workflow.StatusNotApplicable || len(declarations) != 0 {
				t.Fatalf("%s: %s %s %+v", seed, status, reason, declarations)
			}
		})
	}
}

func TestBothStaticFormsRunTestsAndRequireMatchingTools(t *testing.T) {
	for _, source := range []string{"static: site", "static: {build: site, output: dist}"} {
		dir := t.TempDir()
		body := "app:\n  web:\n    public: true\n    tools: [node]\n    " + source + "\n"
		commitAppDeclaration(t, dir, body)
		cfg := testsConfig{AppliesTo: []string{"tool:node"}}
		if _, status, _ := declaredTests(cfg, dir); status != workflow.StatusFail {
			t.Fatalf("%s: %s", source, status)
		}
		commitAppDeclaration(t, dir, body+"    test: [first, second]\n")
		declarations, status, reason := declaredTests(cfg, dir)
		if status != workflow.StatusPass || len(declarations) != 2 || declarations[0].Folder != "site" || declarations[1].Command != "second" {
			t.Fatalf("%s: %s %s %+v", source, status, reason, declarations)
		}
	}
}

// A later passing command cannot turn an unanswered execution into a pass,
// and an ordinary failure cannot conceal that execution error.
func TestAnUnansweredDeclarationStillRunsTheRemainingCommands(t *testing.T) {
	t.Parallel()
	f, ch, folder := testsWorld(t, nil)
	commitAppDeclaration(t, folder, `app: {web: {build: ., public: true, test: [first, second, third]}}`)
	seen := &markersSeen{answer: func(n int, _ execution.Request) execution.Outcome {
		switch n {
		case 1:
			return execution.Outcome{Termination: execution.TimedOut, ExitCode: -1}
		case 2:
			return execution.Outcome{Termination: execution.Exited, ExitCode: 1}
		default:
			return execution.Outcome{Termination: execution.Exited, ExitCode: 0}
		}
	}}
	f.checks = seen.run
	step := f.execute(t, testsRun(t, f, ch), "tests")
	if step.Status != workflow.StatusError || len(seen.all()) != 3 || stepEvidence(t, step)["outcome"] != "timed_out" {
		t.Fatalf("%s %s, ran %d", step.Status, step.Evidence, len(seen.all()))
	}
}

// The summary keeps the first command at the worst status, with any missing
// declaration named alongside that command's reason.
func TestTestsSummaryKeepsTheFirstWorstCommand(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		missing bool
		exits   []int // -1 is an unanswered execution.
		status  workflow.StepState
		command string
		reason  string
	}{
		{"passes", false, []int{0, 0}, workflow.StatusPass, "first", ""},
		{"failures", false, []int{0, 1, 1}, workflow.StatusFail, "second", "tests failed: web: second"},
		{"errors", false, []int{1, -1, -1}, workflow.StatusError, "second", "the tests could not be started: unavailable"},
		{"missing only", true, []int{0, 0}, workflow.StatusFail, "", ""},
		{"missing and failures", true, []int{1, 1}, workflow.StatusFail, "first", "tests failed: web: first"},
		{"missing and errors", true, []int{-1, -1, 1}, workflow.StatusError, "first", "the tests could not be started: unavailable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f, ch, folder := testsWorld(t, map[string]any{"appliesTo": []any{"tool:go"}})
			body := "app:\n  web: {build: ., public: true, tools: [go], test: [first, second"
			if len(c.exits) == 3 {
				body += ", third"
			}
			body += "]}\n"
			wantReason := c.reason
			if c.missing {
				body += "  worker: {build: ., tools: [go]}\n"
				wantReason = "worker is built with go and declares no tests: add `test:` to the part in .apptown/app.yaml"
				if c.reason != "" {
					wantReason += "; " + c.reason
				}
			}
			commitAppDeclaration(t, folder, body)
			seen := &markersSeen{answer: func(n int, _ execution.Request) execution.Outcome {
				if c.exits[n-1] == -1 {
					return execution.Outcome{Termination: execution.LaunchFailed, ExitCode: -1, RunErr: fmt.Errorf("unavailable")}
				}
				return execution.Outcome{Termination: execution.Exited, ExitCode: c.exits[n-1]}
			}}
			f.checks = seen.run
			step := f.execute(t, testsRun(t, f, ch), "tests")
			ev := stepEvidence(t, step)
			command, _ := ev["command"].(string)
			reason, _ := ev["reason"].(string)
			if step.Status != c.status || command != c.command || reason != wantReason || len(seen.all()) != len(c.exits) {
				t.Fatalf("status %s, evidence %s, ran %d", step.Status, step.Evidence, len(seen.all()))
			}
		})
	}
}
