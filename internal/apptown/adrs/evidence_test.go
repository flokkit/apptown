package adrs

import (
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestEditNotifiesEvidenceOnlyAfterCommit(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"receipt", "commit"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			s, project := fixture(t)
			record := create(t, s, project, "decision")
			notified := 0
			s = New(s.records.db, s.authority, func(changed Record) {
				notified++
				current, err := s.Get(testCaller(), changed.ID)
				if err != nil || current.ADR.Title != "edited" {
					t.Fatalf("notified before commit: %+v %v", current, err)
				}
				if receipts := receiptsBy(t, s.records.db, "adr.updated", ""); receipts != 1 {
					t.Fatalf("receipt count=%d", receipts)
				}
			}, nil)
			if fault == "receipt" {
				dbtest.FailNextStatement(t, s.records.db, "INSERT INTO audit_log")
			} else {
				completionExec(t, s.records.db, `CREATE FUNCTION veto_edit_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'edit commit rejected'; END $$`)
				completionExec(t, s.records.db, `CREATE CONSTRAINT TRIGGER veto_edit_commit AFTER UPDATE ON adrs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION veto_edit_commit()`)
			}
			title := "edited"
			if _, err := s.Edit(testCaller(), Edit{ID: record.ID, Title: &title, Expected: TextFields{Title: &record.Title}}); err == nil {
				t.Fatal("fault did not reject edit")
			}
			if notified != 0 {
				t.Fatal("rolled-back edit notified")
			}
			if fault == "commit" {
				completionExec(t, s.records.db, `DROP TRIGGER veto_edit_commit ON adrs`)
			}
			if _, err := s.Edit(testCaller(), Edit{ID: record.ID, Title: &title, Expected: TextFields{Title: &record.Title}}); err != nil {
				t.Fatal(err)
			}
			if notified != 1 {
				t.Fatalf("notifications=%d", notified)
			}
		})
	}
}
