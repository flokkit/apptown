package apptown

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestWorkflowCompletionDoesNotWriteReleaseMembership(t *testing.T) {
	t.Parallel()
	a, ctx, run, _ := completionReleaseScenario(t)
	completionExec(t, a.DB, `CREATE FUNCTION veto_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, a.DB, `CREATE TRIGGER veto_completion BEFORE INSERT ON release_changes FOR EACH ROW EXECUTE FUNCTION veto_completion()`)
	if out, err := a.Work.AdvanceFrom(ctx, run); err != nil || out.RunStatus != workflow.RunDone {
		t.Fatalf("release membership blocked completion: %+v %v", out, err)
	}
	requireReleases(t, a, run.ProjectID, 0, func(*deployment.Release) bool { return true })
}
