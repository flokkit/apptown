package verification

import (
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

// scanAnswer is what one scanner run answered, read from its outcome alone
// (execution.Outcome): its exit, and its report as the run carried it back,
// never a file off this machine's disk, so it reads the same wherever the
// scanner ran (design/operations/workers.md, "Checks on the worker").
type scanAnswer struct {
	exit     int    // a normal exit's code; -1 when the run did not exit normally
	report   []byte // the declared report, when it was carried back
	findings []conformance.Finding
	// failure says why the run gives no answer: it did not run, timed out,
	// errored, or its declared structured report is missing, unreadable or
	// malformed. "" when it answered.
	failure string
	// empty: the scanner gave its declared nothing-to-judge exit
	// (Scanner.EmptyExit), where it applies a failure: the contract says
	// there is something for it to judge.
	empty bool
}

// readScan reads a scanner run's answer. A scanner that declares a structured
// report answers with it: a run that exited 0 or 1 without a readable,
// well-formed report is a failure, never a clean pass.
func readScan(outcome execution.Outcome, m check.Scanner, binding string) scanAnswer {
	a := scanAnswer{exit: -1}
	switch {
	case outcome.Answered():
		a.exit = outcome.ExitCode
	case outcome.Termination == execution.TimedOut:
		a.failure = "the scan timed out"
		return a
	case outcome.Termination == execution.Exited:
		a.failure = fmt.Sprintf("the scan's run gave no answer (exit %d): %v", outcome.ExitCode, outcome.CleanupErr)
		return a
	default:
		a.failure = fmt.Sprintf("the scan did not run: %v", outcome.RunErr)
		return a
	}
	if m.EmptyExit != 0 && a.exit == m.EmptyExit {
		a.empty = true
		return a
	}
	if a.exit > 1 {
		a.failure = fmt.Sprintf("scanner error (exit %d)", a.exit)
		return a
	}
	if m.Report == "" {
		return a
	}
	// A declared report is required, whatever its format: exit 1 with no
	// report is not findings, and exit 0 with none is not clean. The
	// scanner fell over before it wrote one (trivy could not download its
	// database on a full disk, say).
	if outcome.ReportErr != nil || len(outcome.Report) == 0 {
		a.failure = fmt.Sprintf("%s exited %d without a readable report (%s): %v", binding, a.exit, m.Report, outcome.ReportErr)
		return a
	}
	a.report = outcome.Report
	if m.Format == "" {
		return a
	}
	fs, err := conformance.ReadReport(m.Format, outcome.Report, binding, m.Kind)
	if err != nil {
		a.failure = fmt.Sprintf("%s's report could not be read: %v", binding, err)
		return a
	}
	a.findings = fs
	return a
}
