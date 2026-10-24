package check_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

// The deterministic evaluator: exit 0 is a pass, exit 1 is a finding carrying
// the check's message, any other code is a recorded error. These run the REAL
// sh evaluation against a real tree; the only seam is the run port, pointed at
// the local shell so the exit-code→outcome mapping can be exercised at DEFAULT
// tier (no daemon).

func localShell() func(execution.Request) execution.Outcome {
	return func(req execution.Request) execution.Outcome {
		return execution.Runner{}.Run(context.Background(), execution.Local, req)
	}
}

func sourced(block string, ver int, name, sh, sev string) check.Declared {
	return check.Declared{
		Identity:    check.Identity{Namespace: block, Version: ver},
		Declaration: check.Declaration{AppliesTo: repoWide, Name: name, Evaluator: "sh", Sh: sh, Severity: sev, Expects: "the rule holds"},
	}
}

func TestRunShChecksCleanAndViolating(t *testing.T) {
	t.Parallel()
	j := &check.Judge{ShImage: testShImage, Run: localShell()}

	// A check that fails when any file contains FIXME.
	const script = `if grep -rl FIXME . >/dev/null 2>&1; then echo "FIXME left in the tree"; exit 1; fi; exit 0`
	decl := sourced("lang/hygiene", 3, "no-fixme", script, "high")

	// Clean tree: a file with no forbidden marker → conforms, no findings.
	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, "a.txt"), []byte("all good here"), 0o644); err != nil {
		t.Fatal(err)
	}
	evs := j.EvaluateSh([]check.Declared{decl}, check.Source{Tree: clean}, check.Gate, "t-clean")
	if len(evs) != 1 || evs[0].Outcome != check.Conforms || len(evs[0].Findings) != 0 {
		t.Fatalf("clean tree should conform: %+v", evs)
	}

	// Violating tree: a file WITH the marker → does not conform, and the
	// finding carries the check's own message and provenance.
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "b.txt"), []byte("// FIXME hurry"), 0o644); err != nil {
		t.Fatal(err)
	}
	evs = j.EvaluateSh([]check.Declared{decl}, check.Source{Tree: bad}, check.Gate, "t-bad")
	if len(evs) != 1 || evs[0].Outcome != check.DoesNotConform {
		t.Fatalf("violating tree should not conform: %+v", evs)
	}
	if len(evs[0].Findings) != 1 {
		t.Fatalf("one finding expected, got %+v", evs[0].Findings)
	}
	f := evs[0].Findings[0]
	if f.Detail != "FIXME left in the tree" {
		t.Fatalf("finding must carry the check's message, got %q", f.Detail)
	}
	if f.Severity != "high" || f.Source != "guidance" || f.Kind != "conformance" {
		t.Fatalf("finding shape wrong: %+v", f)
	}
	if f.Fingerprint != "guidance|lang/hygiene@3|no-fixme|" {
		t.Fatalf("finding fingerprint wrong: %q", f.Fingerprint)
	}
}

func TestRunShChecksErrorAndMixed(t *testing.T) {
	t.Parallel()
	j := &check.Judge{ShImage: testShImage, Run: localShell()}
	src := check.Source{Tree: t.TempDir()}

	checks := []check.Declared{
		sourced("b", 1, "passes", "exit 0", "note"),
		sourced("b", 1, "errors", "exit 3", "high"),       // neither 0 nor 1 → error
		sourced("b", 1, "no-message", "exit 1", "medium"), // fail with no output → falls back to Expects
	}
	evs := j.EvaluateSh(checks, src, check.Gate, "t-mix")
	if len(evs) != 3 {
		t.Fatalf("expected 3 evaluations, got %d", len(evs))
	}
	outcome := map[string]check.Outcome{}
	var findings []check.Finding
	for _, ev := range evs {
		outcome[ev.Name] = ev.Outcome
		findings = append(findings, ev.Findings...)
	}
	if outcome["passes"] != check.Conforms || outcome["errors"] != check.Error || outcome["no-message"] != check.DoesNotConform {
		t.Fatalf("outcomes wrong: %+v", outcome)
	}
	// Only the exit-1 check produced a finding; with no output it uses Expects.
	if len(findings) != 1 || findings[0].Detail != "the rule holds" {
		t.Fatalf("empty-output fail should fall back to Expects: %+v", findings)
	}
}

// One judge's ports are its own: two judges evaluating the same check with
// different command environments and different models must not cross.
func TestCheckEvaluationsStayWithTheirApplication(t *testing.T) {
	t.Parallel()
	for _, verdict := range []string{"pass", "fail"} {
		j := &check.Judge{ShImage: testShImage,
			Run: func(req execution.Request) execution.Outcome {
				req.Env = append(req.Env, "REVIEWER="+verdict)
				return execution.Runner{}.Run(context.Background(), execution.Local, req)
			},
			Complete: check.CompleterFunc(func(context.Context, string, string) (check.Completion, error) {
				return check.Completion{
					Text:  fmt.Sprintf(`[{"item":1,"verdict":%q,"evidence":"a.txt:1","note":"judged"}]`, verdict),
					Model: verdict,
				}, nil
			}),
		}
		t.Run(verdict, func(t *testing.T) {
			t.Parallel()
			decl := sourced("hygiene", 1, "clean", `echo "$REVIEWER"; test "$REVIEWER" = pass`, "high")
			evs := j.EvaluateSh([]check.Declared{decl}, check.Source{Tree: t.TempDir()}, check.Gate, "isolated")
			want := check.Conforms
			if verdict == "fail" {
				want = check.DoesNotConform
			}
			if len(evs) != 1 || evs[0].Outcome != want || (len(evs[0].Findings) == 1) != (verdict == "fail") {
				t.Fatalf("command result crossed applications: %+v", evs)
			}
			decl.Declaration = check.Declaration{AppliesTo: repoWide, Name: "clean", Evaluator: "llm", Rubric: "clean", Scope: check.ScopeDiff}
			judged, meta, err := j.Checklist(context.Background(), check.DiffItems([]check.Declared{decl}, "diff"), "diff")
			if err != nil || meta == nil || meta.Served != verdict || judged[1].Verdict != verdict {
				t.Fatalf("model result crossed applications: %+v %+v %v", judged, meta, err)
			}
		})
	}
}

// A check that fails with nothing that arrived (a container's stdout can be
// lost at its end) keeps its verdict from the exit code and says its report
// was lost: never "no findings", and a lines check lists no occurrences
// rather than fewer; a tool's summary says the same.
//
// Kills: an empty failure read as a clean or occurrence-free report.
func TestAFailedCheckWhoseReportDidNotArriveSaysReportLost(t *testing.T) {
	t.Parallel()
	silent := func(code int) func(execution.Request) execution.Outcome {
		return func(execution.Request) execution.Outcome {
			return execution.Outcome{Termination: execution.Exited, ExitCode: code}
		}
	}
	decl := sourced("lang/hygiene", 3, "no-fixme", "grep -rn FIXME .", "high")
	decl.Occurrences = "lines"
	for _, code := range []int{1, 2} {
		ev := (&check.Judge{ShImage: testShImage, Run: silent(code)}).EvaluateSh([]check.Declared{decl}, check.Source{Tree: t.TempDir()}, check.Gate, "t-lost")[0]
		if ev.Evidence.Output != check.ReportLost("no-fixme") || ev.Outcome == check.Conforms || len(ev.Occurrences) != 0 {
			t.Fatalf("exit %d with nothing printed: %+v", code, ev)
		}
	}
	if got := check.ToolSummary("osv", " \n"); got != check.ReportLost("osv") {
		t.Fatalf("a failed tool with nothing printed: %q", got)
	}
}

// repoWide is the applicability of a check judged once over the repository.
var repoWide = []string{check.AppliesRepository}
