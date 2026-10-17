package verification

import (
	"errors"
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/execution"
)

// A scan that gives no usable answer is an error, never a pass.
func TestAScanThatGivesNoAnswerIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    check.Scanner
		o    execution.Outcome
	}{
		{"negative exit", check.Scanner{}, execution.Outcome{Termination: execution.Exited, ExitCode: -1}},
		{"cleanup failure", check.Scanner{}, execution.Outcome{Termination: execution.Exited, ExitCode: 0, CleanupErr: errors.New("failed")}},
		{"report without format", check.Scanner{Report: "report.json"}, execution.Outcome{Termination: execution.Exited, ExitCode: 0, ReportErr: errors.New("missing")}},
		{"null structured report", check.Scanner{Report: "report.json", Format: "sarif"}, execution.Outcome{Termination: execution.Exited, ExitCode: 0, Report: []byte(`null`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := readScan(tc.o, tc.m, "review")
			if a.failure == "" {
				t.Fatalf("invalid result accepted: %+v", a)
			}
		})
	}
}
