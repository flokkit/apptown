package verification

import (
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

// A scanner's answer is read from its outcome alone, and fails closed: only
// a normal exit 0 or 1 with the declared structured report, well formed, is
// an answer. Every other outcome says why there is none, whichever machine
// ran the scanner (an exit 1 from a worker carries no *exec.ExitError).
func TestAScannersAnswerFailsClosed(t *testing.T) {
	t.Parallel()
	structured := check.Scanner{Image: "semgrep:test", Report: ".apptown/r.sarif", Format: "sarif"}
	clean := []byte(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"x"}},"results":[]}]}`)
	finding := []byte(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"x","rules":[{"id":"r"}]}},"results":[{"ruleId":"r","level":"error","message":{"text":"m"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"a.go"},"region":{"startLine":1}}}]}]}]}`)
	exited := func(code int, report []byte, reportErr error) execution.Outcome {
		return execution.Outcome{Termination: execution.Exited, ExitCode: code, Report: report, ReportErr: reportErr}
	}
	for _, c := range []struct {
		name     string
		m        check.Scanner
		outcome  execution.Outcome
		failure  string // a substring; "" for an answer
		findings int
	}{
		{"exit 0 with a clean report", structured, exited(0, clean, nil), "", 0},
		{"exit 1 with findings", structured, exited(1, finding, nil), "", 1},
		{"exit 0 with no report", structured, exited(0, nil, errors.New("missing")), "without a readable report", 0},
		{"exit 1 with no report", structured, exited(1, nil, errors.New("missing")), "without a readable report", 0},
		{"an empty report", structured, exited(0, []byte{}, nil), "without a readable report", 0},
		{"a malformed report", structured, exited(0, []byte("{not json"), nil), "could not be read", 0},
		{"exit 2", structured, exited(2, clean, nil), "exit 2", 0},
		{"timed out", structured, execution.Outcome{Termination: execution.TimedOut, ExitCode: -1}, "timed out", 0},
		{"never ran", structured, execution.Outcome{Termination: execution.LaunchFailed, ExitCode: -1, RunErr: errors.New("worker unreachable")}, "did not run", 0},
		{"cancelled", structured, execution.Outcome{Termination: execution.Canceled, ExitCode: -1}, "did not run", 0},
		{"a zero outcome", structured, execution.Outcome{}, "did not run", 0},
		{"no structured report: exit 1 is findings by exit code", check.Scanner{Image: "x"}, exited(1, nil, nil), "", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := readScan(c.outcome, c.m, "b")
			if c.failure == "" && a.failure != "" || c.failure != "" && !strings.Contains(a.failure, c.failure) || len(a.findings) != c.findings {
				t.Fatalf("answer %+v; want failure %q and %d findings", a, c.failure, c.findings)
			}
		})
	}
	// The nothing-to-judge exit is what the manifest declares, never read
	// from the image's name: undeclared, 128 is a scanner error.
	if a := readScan(exited(128, nil, nil), check.Scanner{Image: "osv-scanner:test", Report: ".apptown/o.json", Format: "osv", EmptyExit: 128}, "osv"); !a.empty || a.failure != "" {
		t.Fatalf("osv with no package sources: %+v", a)
	}
	if a := readScan(exited(128, nil, nil), check.Scanner{Image: "osv-scanner:test", Report: ".apptown/o.json", Format: "osv"}, "osv"); a.empty || a.failure == "" {
		t.Fatalf("an undeclared 128: %+v", a)
	}
}
