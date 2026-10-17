package verification

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestRunToolChecksExecImageSkippedWithoutRun(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	if _, err := integration.New(db).Upsert(t.Context(), "django-check-deploy", ScannerCapability, "tool", `{"command":["python","manage.py","check","--deploy"],"target":"exec-image","manual":true}`, true); err != nil {
		t.Fatal(err)
	}
	scanners := NewScanners(ScannerDependencies{DB: db})
	sc := []conformance.DeclaredCheck{{Block: "lang/django", Version: 3,
		Check: conformance.CheckDeclaration{Name: "django-check-deploy", Evaluator: "tool", Tool: "django-check-deploy", Severity: "high", Expects: "deploy checks pass"}}}

	// Read-only sweep (AllowBuild=false) with no cached image → exec-image tool
	// is SKIPPED, never a false pass or a false fail.
	results, findings, errs := scanners.RunToolChecks(t.Context(), sc, ToolContext{Context: context.Background(), BuildDir: t.TempDir(), AllowBuild: false}, "test")
	if len(findings) != 0 || errs != 0 {
		t.Fatalf("skipped tool must produce no finding/error, got findings=%+v errs=%d", findings, errs)
	}
	if results[0].Verdict != workflow.VerdictSkipped {
		t.Fatalf("want skipped, got %+v", results)
	}
}
