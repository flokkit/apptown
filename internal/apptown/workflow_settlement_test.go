package apptown

import (
	"testing"
)

func TestWorkflowCompletionRequiresReleaseMembership(t *testing.T) {
	t.Parallel()
	a, ctx, run, _ := completionReleaseScenario(t)
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
		completionCount(t, a.DB, 0, `SELECT COUNT(*) FROM `+table)
	}
	completionExec(t, a.DB, `DROP TRIGGER veto_completion ON release_changes`)
	if out, err := a.Work.AdvanceFrom(ctx, run); err != nil || !out.Advanced {
		t.Fatalf("retry: %+v %v", out, err)
	}
	requireChangeReleases(t, a, run.ProjectID, run.ChangeID, 1)
}
