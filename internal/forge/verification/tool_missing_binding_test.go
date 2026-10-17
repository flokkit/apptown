package verification

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestRunToolChecksMissingBinding(t *testing.T) {
	t.Parallel()
	scanners := NewScanners(ScannerDependencies{DB: testDB(t)})
	sc := []conformance.DeclaredCheck{{Block: "lang/django", Version: 3,
		Check: conformance.CheckDeclaration{Name: "sast", Evaluator: "tool", Tool: "not-attached", Severity: "high", Expects: "x"}}}
	results, findings, errs := scanners.RunToolChecks(t.Context(), sc, ToolContext{Context: context.Background(), BuildDir: t.TempDir()}, "test")
	if errs != 1 || len(findings) != 0 {
		t.Fatalf("missing binding must error (never silently pass): errs=%d findings=%+v", errs, findings)
	}
	if results[0].Verdict != workflow.VerdictError {
		t.Fatalf("want error verdict, got %+v", results)
	}
}
