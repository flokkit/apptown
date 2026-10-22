package apptown

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// Selection details live in Deployment and release eligibility lives in Work.
// Keep the composition guarantee: a required participant's malformed input
// aborts completion, while an unused bundle cannot prevent non-release work.
func TestCompletionMalformedBundleOnlyBlocksRequiredRelease(t *testing.T) {
	t.Parallel()
	for _, deploy := range []bool{true, false} {
		name := "no deployment"
		if deploy {
			name = "deployed"
		}
		t.Run(name, func(t *testing.T) {
			var a *App
			var run *workflow.Run
			ctx := asPerson("alice")
			if deploy {
				a, _, run, _ = completionReleaseScenario(t)
			} else {
				a, _, run = completionScenario(t, schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish"}}})
				if _, err := a.Deployment.Bundles.Cut(ctx, run.ProjectID, "tree", nil, nil, nil, deployment.BundleOrigin{RunID: run.ID}); err != nil {
					t.Fatal(err)
				}
			}
			// Deliberate corruption is the failure under test, not normal fixture setup.
			// sql-guarantee: Corrupt only the recorded bundle Run ID to distinguish optional from required release completion; bundle creation validates typed provenance and its identity is immutable.
			completionExec(t, a.DB, `UPDATE release_bundles SET built_from_json='{"runId":"corrupt"}'`)
			before, err := a.Work.Snapshot(ctx, run.ID, workflow.TraceView)
			if err != nil {
				t.Fatal(err)
			}
			out, err := a.Work.AdvanceFrom(ctx, run)
			if deploy {
				if err == nil {
					t.Fatal("required malformed bundle allowed completion")
				}
				after, err := a.Work.Snapshot(ctx, run.ID, workflow.TraceView)
				if err != nil || after.Run.Status != workflow.RunRunning || len(after.Events) != len(before.Events) {
					t.Fatalf("failed completion left receipts: %+v %v", after, err)
				}
			} else if err != nil || !out.Advanced || out.RunStatus != workflow.RunDone {
				t.Fatalf("unused bundle blocked completion: %+v %v", out, err)
			}
			requireReleases(t, a, run.ProjectID, 0, func(r *deployment.Release) bool { return r.RunID == run.ID })
		})
	}
}
