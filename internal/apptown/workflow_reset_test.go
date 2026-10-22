package apptown

import (
	"testing"

	"github.com/flokkit/apptown/internal/database"
)

func workflowResetSnapshot(t *testing.T, db *database.DB) string {
	t.Helper()
	var snapshot string
	// sql-guarantee: a failed settlement must leave every Work row and column exactly as it was, including columns no owner read returns.
	err := db.QueryRow(`SELECT jsonb_build_array(
        (SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM runs r),
        (SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM steps s),
        (SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM run_events e),
        (SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM remediation_items m))::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestLandingRecheckRequiresStateAndRevisionWrites(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"step", "revision", "receipt"} {
		t.Run(target, func(t *testing.T) {
			a, run, _ := landingRecheckFixture(t)
			db := a.DB
			req, _ := landingRemediationFixture(t, a, run)
			reset := func() error {
				_, err := a.Work.RecheckLanding(asPerson("alice"), req)
				return err
			}
			table, condition, write := "steps", "NEW.status='pending'", "UPDATE"
			if target == "revision" {
				table, condition = "runs", "NEW.retry_revision != OLD.retry_revision"
			}
			if target == "receipt" {
				table, condition, write = "run_events", "NEW.kind='step.recheck'", "INSERT"
			}
			completionExec(t, db, `CREATE FUNCTION veto_reset() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RETURN NULL; END IF; RETURN NEW; END $$`)
			completionExec(t, db, `CREATE TRIGGER veto_reset BEFORE `+write+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_reset()`)
			before := workflowResetSnapshot(t, db)
			if err := reset(); err == nil {
				t.Fatal("reported success after a required write affected no row")
			}
			if after := workflowResetSnapshot(t, db); after != before {
				t.Fatal("reset left partial state, receipts, votes or retry accounting")
			}
			completionExec(t, db, `DROP TRIGGER veto_reset ON `+table)
			if err := reset(); err != nil {
				t.Fatalf("explicit rerun after write failure: %v", err)
			}
		})
	}
}
