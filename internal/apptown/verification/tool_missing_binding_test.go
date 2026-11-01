package verification

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestRunToolChecksMissingBinding(t *testing.T) {
	t.Parallel()
	scanners := NewScanners(ScannerDependencies{DB: testDB(t)})
	sc := []conformance.DeclaredCheck{{Block: "lang/django", Version: 3,
		Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "sast", Evaluator: "tool", Tool: "not-attached", Severity: "high", Expects: "x"}}}
	results, findings, errs := scanners.runToolChecks(t.Context(), sc, toolContext{Context: context.Background(), BuildDir: t.TempDir()}, "test")
	if errs != 1 || len(findings) != 0 {
		t.Fatalf("missing binding must error (never silently pass): errs=%d findings=%+v", errs, findings)
	}
	if results[0].Verdict != workflow.VerdictError {
		t.Fatalf("want error verdict, got %+v", results)
	}
}
