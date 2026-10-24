package check_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

type oneScanner check.Scanner

func (s oneScanner) Scanner(string) (check.Scanner, bool) { return check.Scanner(s), true }

// A judge reads a run's answer from its outcome, as any machine answers it:
// a normal exit's code, with no *exec.ExitError, and a declared report
// carried back. A tool whose declared report is missing is an error.
func TestAJudgeReadsTheOutcomeNotTheProcess(t *testing.T) {
	t.Parallel()
	answer := func(code int, report []byte, reportErr error) *check.Judge {
		return &check.Judge{ShImage: testShImage,
			Run: func(execution.Request) execution.Outcome {
				return execution.Outcome{Termination: execution.Exited, ExitCode: code, Report: report, ReportErr: reportErr}
			},
			Scanners: oneScanner{Name: "t", Image: "t:1", Report: ".apptown/r.sarif", Format: "sarif"},
		}
	}
	sh := sourced("b", 1, "rule", "true", "high")
	if evs := answer(1, nil, nil).EvaluateSh([]check.Declared{sh}, check.Source{Tree: t.TempDir()}, check.Gate, "t"); len(evs) != 1 || evs[0].Outcome != check.DoesNotConform {
		t.Fatalf("an exit 1 with no process error: %+v", evs)
	}
	tool := check.Declared{Identity: check.Identity{Namespace: "b", Version: 1}, Declaration: check.Declaration{Name: "tool", Tool: "t", Severity: "high", Expects: "clean"}}
	finding := []byte(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"x","rules":[{"id":"r"}]}},"results":[{"ruleId":"r","level":"error","message":{"text":"m"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"a.go"},"region":{"startLine":1}}}]}]}]}`)
	for _, c := range []struct {
		name, says string
		j          *check.Judge
		want       check.Outcome
	}{
		{"findings in the report", "", answer(1, finding, nil), check.DoesNotConform},
		{"exit 0 with no report", "no readable report", answer(0, nil, errors.New("missing")), check.Error},
		{"exit 1 with no report", "no readable report", answer(1, nil, errors.New("missing")), check.Error},
		{"a malformed report", "could not be read", answer(0, []byte("{"), nil), check.Error},
	} {
		if ev := c.j.EvaluateTool(tool, check.Source{Tree: t.TempDir()}, check.Gate, "t"); ev.Outcome != c.want || !strings.Contains(ev.Detail, c.says) {
			t.Errorf("%s: %s, want %s saying %q (%s)", c.name, ev.Outcome, c.want, c.says, ev.Detail)
		}
	}
}
