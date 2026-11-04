package apptown

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
)

func TestWorkflowCompletionRequiresReleaseMembership(t *testing.T) {
	t.Parallel()
	a, ctx, run, _ := completionReleaseScenario(t)
	// The fixture committed an approval wait after deployment, including its
	// narrative request. A later failed completion must preserve those jobs.
	priorJobs := len(operationJobs(t, a, project.DescriptionJob)) + len(operationJobs(t, a, narrativeJob))
	completionExec(t, a.DB, `CREATE FUNCTION veto_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, a.DB, `CREATE TRIGGER veto_completion BEFORE INSERT ON release_changes FOR EACH ROW EXECUTE FUNCTION veto_completion()`)
	before := workflowResetSnapshot(t, a.DB)
	if _, err := a.Work.AdvanceFrom(ctx, run); err == nil {
		t.Fatal("completion succeeded without release membership")
	}
	if after := workflowResetSnapshot(t, a.DB); after != before {
		t.Fatal("failed settlement left Work records")
	}
	for _, table := range []string{"releases", "release_changes", "river_job"} {
		// sql-guarantee: rollback must leave no hidden row in any of these tables, including rows no owner read would list.
		want := 0
		if table == "river_job" {
			want = priorJobs
		}
		completionCount(t, a.DB, want, `SELECT COUNT(*) FROM `+table)
	}
	completionExec(t, a.DB, `DROP TRIGGER veto_completion ON release_changes`)
	if out, err := a.Work.AdvanceFrom(ctx, run); err != nil || !out.Advanced {
		t.Fatalf("retry: %+v %v", out, err)
	}
	requireChangeReleases(t, a, run.ProjectID, run.ChangeID, 1)
}
