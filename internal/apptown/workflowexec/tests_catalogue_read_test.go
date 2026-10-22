package workflowexec

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

// A failed catalogue read is not an absent kind: tests cannot claim
// success after AppTown fails to read the setting they declared.
func TestACatalogueReadFailureCannotBecomePassingTests(t *testing.T) {
	f, ch, folder := testsWorld(t, map[string]any{"command": "make test"})
	if _, _, err := dependency.NewCatalog(f.db, nil).SyncStock(t.Context(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	commitAppDeclaration(t, folder, postgresApp)
	ran := false
	f.checks = func(req execution.Request) execution.Outcome {
		ran = true
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0}
	}
	run := testsRun(t, f, ch)
	t.Cleanup(f.db.FailNextStatement("-- name: DependencyKindByCapability"))
	step := f.execute(t, run, "tests")
	if step.Status != workflow.StatusError || ran {
		t.Fatalf("failed postgres read: status=%s ran=%v evidence=%s", step.Status, ran, step.Evidence)
	}
}
