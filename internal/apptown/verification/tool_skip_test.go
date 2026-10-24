package verification

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// An exec-image tool check runs inside the public part's image: an app
// whose contract declares no part has no image, so the check is not
// applicable, with the reason, and no image is looked for or built: never a
// false pass, a false fail or an error.
func TestRunToolChecksExecImageIsNotApplicableWithoutAnImage(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	if _, err := integration.New(db).Upsert(t.Context(), "django-check-deploy", ScannerCapability, "tool", `{"command":["python","manage.py","check","--deploy"],"target":"exec-image","manual":true}`, true); err != nil {
		t.Fatal(err)
	}
	// No executor: looking for an image would fail the test.
	scanners := NewScanners(ScannerDependencies{DB: db})
	sc := []conformance.DeclaredCheck{{Block: "lang/django", Version: 3,
		Check: conformance.CheckDeclaration{AppliesTo: []string{"tool:python"}, Name: "django-check-deploy", Evaluator: "tool", Tool: "django-check-deploy", Severity: "high", Expects: "deploy checks pass"}}}
	for name, files := range map[string]map[string]string{
		"no contract":    {"manage.py": ""},
		"a static site":  {".apptown/app.yaml": "version: 2\napp:\n  site: {static: ., public: true}\n"},
		"no python part": {".apptown/app.yaml": "version: 2\napp:\n  web: {build: ., ports: [\"8080\"], public: true, tools: [go]}\n"},
	} {
		tree := t.TempDir()
		writeTree(t, tree, files)
		results, findings, errs := scanners.RunToolChecks(t.Context(), sc, ToolContext{Context: context.Background(), BuildDir: tree, AllowBuild: false}, "test")
		if len(findings) != 0 || errs != 0 || results[0].Verdict != workflow.VerdictNotApplicable || results[0].Detail == "" {
			t.Errorf("%s: %+v findings=%d errs=%d", name, results, len(findings), errs)
		}
	}
}
