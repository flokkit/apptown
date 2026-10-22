package check_test

import (
	"errors"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"testing"
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
			j := &check.Judge{Scanners: oneScanner{Name: "t", Image: "x", Report: ".apptown/report", Format: tc.format}, Run: func(req execution.Request) execution.Outcome {
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
	j := &check.Judge{Run: func(execution.Request) execution.Outcome {
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0, CleanupErr: errors.New("cleanup failed")}
	}}
	ev := j.EvaluateSh([]check.Declared{sourced("b", 1, "rule", "true", "high")}, check.Source{Tree: t.TempDir()}, check.Gate, "review")
	if ev[0].Outcome != check.Error {
		t.Fatalf("cleanup failure yielded %s", ev[0].Outcome)
	}
}
