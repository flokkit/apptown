package check_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

func TestAToolsReportFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, format string
		report       []byte
		err          error
	}{
		{"SARIF null", "sarif", []byte(`null`), nil},
		{"SARIF error object", "sarif", []byte(`{"error":"scanner failed"}`), nil},
		{"OSV error object", "osv", []byte(`{"error":"scanner failed"}`), nil},
		{"declared unstructured report missing", "", nil, errors.New("missing")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := &check.Judge{ShImage: testShImage, Scanners: oneScanner{Name: "t", Image: "x", Report: ".apptown/report", Format: tc.format}, Run: func(req execution.Request) execution.Outcome {
				return execution.Outcome{Termination: execution.Exited, ExitCode: 0, Report: tc.report, ReportErr: tc.err}
			}}
			ev := j.EvaluateTool(check.Declared{Declaration: check.Declaration{Name: "tool", Tool: "t"}}, check.Source{Tree: t.TempDir()}, check.Gate, "review")
			if ev.Outcome != check.Error {
				t.Fatalf("invalid declared report yielded %s", ev.Outcome)
			}
		})
	}
}

func TestAShellCheckWhoseCleanupFailedIsAnError(t *testing.T) {
	j := &check.Judge{ShImage: testShImage, Run: func(execution.Request) execution.Outcome {
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0, CleanupErr: errors.New("cleanup failed")}
	}}
	ev := j.EvaluateSh([]check.Declared{sourced("b", 1, "rule", "true", "high")}, check.Source{Tree: t.TempDir()}, check.Gate, "review")
	if ev[0].Outcome != check.Error {
		t.Fatalf("cleanup failure yielded %s", ev[0].Outcome)
	}
}

// A tool's report is this run's: one an earlier run left that cannot be
// removed stops the check before the tool runs, never read as its answer.
//
// Kills: the unchecked removal of a stale report (fallback audit F15).
func TestAStaleReportThatCannotBeRemovedStopsTheTool(t *testing.T) {
	t.Parallel()
	tree := t.TempDir()
	// A non-empty directory where the report goes: os.Remove fails on it.
	if err := os.MkdirAll(filepath.Join(tree, ".apptown", "report", "kept"), 0o755); err != nil {
		t.Fatal(err)
	}
	ran := false
	j := &check.Judge{ShImage: testShImage, Scanners: oneScanner{Name: "t", Image: "x", Report: ".apptown/report", Format: "sarif"}, Run: func(execution.Request) execution.Outcome {
		ran = true
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0, Report: []byte(`{"version":"2.1.0","runs":[]}`)}
	}}
	ev := j.EvaluateTool(check.Declared{Declaration: check.Declaration{Name: "tool", Tool: "t"}}, check.Source{Tree: tree}, check.Gate, "review")
	if ran || ev.Outcome != check.Error {
		t.Fatalf("a stale report left in place: tool ran %v, outcome %s (%s)", ran, ev.Outcome, ev.Detail)
	}
}
