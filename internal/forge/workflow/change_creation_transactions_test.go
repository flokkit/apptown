package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// Ordinary admission persists the Change and its attribution before preparing
// a workspace. Keep storage and commit faults beside their owner.
func creationFixture(t *testing.T) (Creator, ChangeDraft) {
	t.Helper()
	db := testDB(t)
	id := workflowTestProject(t, db, "create", "Create", "main")
	return NewCreator(db, func(context.Context, CreatedChange) error { return nil }), ChangeDraft{ProjectID: id, Type: "feature", Title: "A title", Body: "\n Exact brief\t\n", ExternalRef: "issue-7"}
}

func TestChangeCreationRequiresAllRecordsBeforeWorkspace(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"project", "insert", "branch", "receipt", "readback", "suppressed insert", "suppressed branch", "suppressed receipt", "deferred insert", "deferred receipt"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			c, draft := creationFixture(t)
			calls := 0
			c.prepare = func(context.Context, CreatedChange) error { calls++; return nil }
			switch failure {
			case "project":
				dbtest.FailNextStatement(t, c.records.db, "-- name: ProjectForUpdate")
			case "insert":
				dbtest.FailNextStatement(t, c.records.db, "INSERT INTO changes")
			case "branch":
				dbtest.FailNextStatement(t, c.records.db, "UPDATE changes SET branch=")
			case "receipt":
				dbtest.FailNextStatement(t, c.records.db, "INSERT INTO audit_log")
			case "readback":
				dbtest.FailNextStatement(t, c.records.db, "-- name: ChangeByID")
			default:
				table, event := "changes", "INSERT"
				if strings.HasSuffix(failure, "branch") {
					event = "UPDATE"
				}
				if strings.HasSuffix(failure, "receipt") {
					table = "audit_log"
				}
				body := "RETURN NULL;"
				trigger := "CREATE TRIGGER veto_creation BEFORE " + event + " ON " + table
				if strings.HasPrefix(failure, "deferred") {
					body = "RAISE EXCEPTION 'reject creation at commit';"
					trigger = "CREATE CONSTRAINT TRIGGER veto_creation AFTER " + event + " ON " + table + " DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, c.records.db, `CREATE FUNCTION veto_creation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				completionExec(t, c.records.db, trigger+" FOR EACH ROW EXECUTE FUNCTION veto_creation()")
			}
			ch, err := c.Create(context.Background(), draft, "alice")
			if err == nil || ch != nil || calls != 0 {
				t.Fatalf("partial creation: %+v %v, workspace calls %d", ch, err, calls)
			}
			// sql-guarantee: failed creation leaves no change row in any project.
			completionCount(t, c.records.db, 0, `SELECT count(*) FROM changes`)
			expect(t, auditCount(t, c.records.db, "", nil) == 0, "failed creation left a receipt")
		})
	}
}

func TestChangeCreationKeepsTypedAcceptanceAttributionAndCommittedIdentity(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"workspace", "workspace failure", "read failure", "backlog", "no repository", "chat"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			c, draft := creationFixture(t)
			draft.Acceptance = &Acceptance{TargetDigest: "pinned", Checks: []AcceptanceCheck{{Block: "design", Check: "works", Version: 9007199254740993, Title: "Acceptance words", Expects: "usable"}}}
			if mode == "backlog" {
				draft.Propose = true
			}
			if mode == "chat" {
				draft.TitleFromChat = true
			}
			if mode == "no repository" {
				if err := project.New(c.records.db, nil).SetDefaultBranch(context.Background(), draft.ProjectID, ""); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			failure := errors.New("workspace unavailable")
			c.prepare = func(ctx context.Context, created CreatedChange) error {
				calls++
				if created.Change.ID.IsZero() || created.Change.ProjectID != draft.ProjectID || created.Slug != "create" || created.DefaultBranch != "main" {
					t.Errorf("workspace scope: %+v", created)
				}
				// Both records must already be visible on another connection, and this write
				// must not wait on an uncommitted project/Change transaction.
				seenChange(t, c.records.db, created.Change.ID)
				expect(t, createdReceipts(t, c.records.db, created.Change.ID) == 1, "creation receipt is not visible before the workspace")
				changed, err := workflowsql.New(c.records.db.SQL()).SetChangeWorkspaceBase(ctx, workflowsql.SetChangeWorkspaceBaseParams{Tip: "prepared", Branch: created.Change.Branch, ChangeID: created.Change.ID, ProjectID: created.Change.ProjectID, OldBranch: created.Change.Branch, OldBaseSha: created.Change.BaseSHA, OldLandedSha: created.Change.LandedSHA, OldState: created.Change.State.String(), OldRunID: created.Change.RunID})
				if err != nil || changed != 1 {
					return fmt.Errorf("prepare change base: changed=%d: %w", changed, err)
				}
				if mode == "workspace failure" {
					return failure
				}
				if mode == "read failure" {
					dbtest.FailNextStatement(t, c.records.db, "-- name: ChangeByID")
				}
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ch, err := c.Create(ctx, draft, "alice via mcp/test")
			failed := mode == "workspace failure" || mode == "read failure"
			if ch == nil || (err != nil) != failed {
				t.Fatalf("creation: %+v %v", ch, err)
			}
			if failed && !strings.Contains(err.Error(), fmt.Sprintf("Change #%d is recorded", ch.ID)) {
				t.Fatalf("error lost committed identity: %v", err)
			}
			if mode == "workspace failure" && !errors.Is(err, failure) {
				t.Fatalf("lost preparation error: %v", err)
			}
			if ch.Title != draft.Title || ch.Body != draft.Body || ch.ExternalRef != draft.ExternalRef || ch.ProjectID != draft.ProjectID {
				t.Fatalf("created fields: %+v", ch)
			}
			var acceptance Acceptance
			if err := json.Unmarshal([]byte(ch.AcceptanceJSON), &acceptance); err != nil || !reflect.DeepEqual(acceptance, *draft.Acceptance) {
				t.Fatalf("acceptance lost exact data: %s %v", ch.AcceptanceJSON, err)
			}
			expectedState, expectedOwner := ChangeInProgress, TitleHuman
			if mode == "backlog" {
				expectedState = ChangeProposed
			}
			if mode == "chat" {
				expectedOwner = TitleAwaitingChat
			}
			stored := seenChange(t, c.records.db, ch.ID)
			var owner TitleOwner
			if err := owner.Scan(stored.TitleOwnership); err != nil || stored.State != expectedState.String() || owner != expectedOwner {
				t.Fatalf("stored change: %s %s %v", stored.State, stored.TitleOwnership, err)
			}
			noWorkspace := mode == "backlog" || mode == "no repository"
			if noWorkspace && (calls != 0 || ch.Branch != "") || !noWorkspace && (calls != 1 || ch.Branch != fmt.Sprintf("forge/change-%d", ch.ID)) {
				t.Fatalf("workspace eligibility: %+v calls %d", ch, calls)
			}
			// sql-guarantee: creation writes exactly one change row in any project.
			completionCount(t, c.records.db, 1, `SELECT count(*) FROM changes`)
			inState := auditCount(t, c.records.db, "change.created", func(e audit.Entry) bool {
				return e.ProjectID == draft.ProjectID && jsonMatches(t, e.Payload, "state="+expectedState.String())
			})
			expect(t, inState == 1, "creation receipts in state %s: %d", expectedState, inState)
			expect(t, createdReceipts(t, c.records.db, ch.ID) == 1, "attributed creation receipts")
			if mode == "workspace" && ch.BaseSHA != "prepared" {
				t.Fatalf("response did not observe workspace: %+v", ch)
			}
		})
	}
}

func TestChangeCreationReadsCurrentProjectFactsAndCancelsLockWait(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"rename", "remove repository", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			c, draft := creationFixture(t)
			calls := 0
			c.prepare = func(_ context.Context, created CreatedChange) error {
				calls++
				if created.Slug != "renamed" || created.DefaultBranch != "current" {
					t.Errorf("stale project used: %+v", created)
				}
				return nil
			}
			gate, err := c.records.db.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Rollback() }()
			holdProjectLock(t, gate, draft.ProjectID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				ch  *Change
				err error
			}
			done := make(chan result, 1)
			go func() { ch, err := c.Create(ctx, draft, "alice"); done <- result{ch, err} }()
			dbtest.WaitForLock(t, c.records.db, "-- name: ProjectForUpdate")
			switch mode {
			case "rename":
				// sql-guarantee: the competing project edit must commit through the transaction already holding the project lock; Projects setters use a separate transaction and cannot preserve this lock race.
				_, err = gate.Exec(`UPDATE projects SET slug='renamed',default_branch='current' WHERE id=?`, draft.ProjectID)
			case "remove repository":
				// sql-guarantee: the competing project edit must commit through the transaction already holding the project lock; Projects setters use a separate transaction and cannot preserve this lock race.
				_, err = gate.Exec(`UPDATE projects SET default_branch='' WHERE id=?`, draft.ProjectID)
			case "cancel":
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := gate.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-done:
				if mode == "cancel" {
					if got.ch != nil || !errors.Is(got.err, context.Canceled) || calls != 0 {
						t.Fatalf("cancelled creation: %+v %v", got.ch, got.err)
					}
					// sql-guarantee: cancelled creation leaves no change row in any project.
					completionCount(t, c.records.db, 0, `SELECT count(*) FROM changes`)
					expect(t, auditCount(t, c.records.db, "", nil) == 0, "cancelled creation left a receipt")
				} else {
					if got.err != nil {
						t.Fatal(got.err)
					}
					if (mode == "rename") != (calls == 1) {
						t.Fatalf("workspace calls %d", calls)
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("creation held its cancelled transaction")
			}
		})
	}
}

func TestChangeCreationRejectsInvalidCommandsBeforeAnyRecord(t *testing.T) {
	t.Parallel()
	c, draft := creationFixture(t)
	for _, mutate := range []func(*ChangeDraft){func(d *ChangeDraft) { d.ProjectID = ids.Nil }, func(d *ChangeDraft) { d.Title = " \n " }, func(d *ChangeDraft) { d.Propose = true; d.TitleFromChat = true }} {
		invalid := draft
		mutate(&invalid)
		if ch, err := c.Create(context.Background(), invalid, "alice"); ch != nil || err == nil {
			t.Fatalf("invalid creation: %+v %v", ch, err)
		}
	}
	if ch, err := c.Create(context.Background(), draft, ""); ch != nil || err == nil {
		t.Fatalf("unattributed creation: %+v %v", ch, err)
	}
	// sql-guarantee: refused commands leave no change row in any project.
	completionCount(t, c.records.db, 0, `SELECT count(*) FROM changes`)
	expect(t, auditCount(t, c.records.db, "", nil) == 0, "refused creation left a receipt")
}

// createdReceipts counts creation receipts for one change attributed to the
// fixture's caller.
func createdReceipts(t *testing.T, db database.Queryer, id ids.ID) int {
	t.Helper()
	return auditCount(t, db, "change.created", func(e audit.Entry) bool {
		return e.Actor == "alice via mcp/test" && jsonMatches(t, e.Payload, fmt.Sprintf("changeId=%d", id))
	})
}
