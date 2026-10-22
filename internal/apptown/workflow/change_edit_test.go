package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

func TestChangeEditAtomicFailures(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"project read", "scope read", "write", "receipt", "readback", "suppressed write", "suppressed receipt", "deferred write", "deferred receipt"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			w, ch := changeEditFixture(t, ChangeDraft{Title: "original", Body: "brief", ExternalRef: "ref", TitleFromChat: true})
			nameChangeForTest(t, w, ch, "original")
			switch failure {
			case "project read":
				dbtest.FailNextStatement(t, w, "-- name: ProjectForUpdate")
			case "scope read":
				dbtest.FailNextStatement(t, w, "-- name: ChangeForUpdate")
			case "write":
				dbtest.FailNextStatement(t, w, "UPDATE changes SET title=")
			case "receipt":
				dbtest.FailNextStatement(t, w, "INSERT INTO audit_log")
			case "readback":
				dbtest.FailNextStatement(t, w, "-- name: ChangeByID")
			default:
				table, event := "changes", "UPDATE"
				if failure == "suppressed receipt" || failure == "deferred receipt" {
					table, event = "audit_log", "INSERT"
				}
				body := "RETURN NULL;"
				trigger := "CREATE TRIGGER veto_edit BEFORE " + event + " ON " + table
				if failure == "deferred receipt" || failure == "deferred write" {
					body = "RAISE EXCEPTION 'rejected edit at commit';"
					trigger = "CREATE CONSTRAINT TRIGGER veto_edit AFTER " + event + " ON " + table + " DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, w, `CREATE FUNCTION veto_edit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				completionExec(t, w, trigger+" FOR EACH ROW EXECUTE FUNCTION veto_edit()")
			}
			title, body, ref := "new", "new brief", "new ref"
			got, err := editChangeAs(context.Background(), w, "alice", ChangeEdit{ProjectID: ch.ProjectID, ID: ch.ID, Title: &title, Body: &body, ExternalRef: &ref, ExpectedRevision: revision(0)})
			if err == nil || got != nil {
				t.Fatalf("failed %s returned %+v, %v", failure, got, err)
			}
			if failure == "suppressed write" && !errors.Is(err, errChangeEditConflict) {
				t.Fatalf("suppressed write: %v", err)
			}
			got, err = readChange(context.Background(), w, ch.ID)
			if err != nil || got.Title != "original" || got.Body != "brief" || got.ExternalRef != "ref" {
				t.Fatalf("partial edit: %+v %v", got, err)
			}
			assertChangeTitle(t, w, ch.ID, "original", titleGenerated)
			changeReceipts(t, w, "change.edited", 0)
		})
	}
}

func TestChangeEditSelectedFieldsAndHumanTitle(t *testing.T) {
	t.Parallel()
	w, ch := changeEditFixture(t, ChangeDraft{Title: "original", Body: "brief", ExternalRef: "ref", TitleFromChat: true})
	nameChangeForTest(t, w, ch, "automatic")
	if err := w.InTx(context.Background(), func(tx *database.Tx) error {
		if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(context.Background(), ch.ProjectID); err != nil {
			return err
		}
		_, err := TransitionChangeIn(context.Background(), tx, ch.ProjectID, ch.ID, ChangeMove{Action: ShipChange}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	blank, body, ref := " \n ", "\n Exact brief\t\n", "  issue-7 \n"
	got, err := editChangeAs(context.Background(), w, "alice", ChangeEdit{ProjectID: ch.ProjectID, ID: ch.ID, Title: &blank, Body: &body, ExternalRef: &ref, ExpectedRevision: revision(0)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "automatic" || got.Body != body || got.ExternalRef != "issue-7" || got.State != ChangeShipped || !got.ClosedAt.Valid {
		t.Fatalf("selected edit: %+v", got)
	}
	assertChangeTitle(t, w, ch.ID, "automatic", titleGenerated)
	title := "  Human words  "
	got, err = editChangeAs(context.Background(), w, "bob", ChangeEdit{ProjectID: ch.ProjectID, ID: ch.ID, Title: &title, ExpectedRevision: revision(1)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Human words" || got.Body != body || got.ExternalRef != "issue-7" {
		t.Fatalf("omitted fields lost: %+v", got)
	}
	assertChangeTitle(t, w, ch.ID, "Human words", titleHuman)
	empty := ""
	got, err = editChangeAs(context.Background(), w, "carol", ChangeEdit{ProjectID: ch.ProjectID, ID: ch.ID, Body: &empty, ExternalRef: &empty, ExpectedRevision: revision(2)})
	if err != nil || got.Body != "" || got.ExternalRef != "" || got.Title != "Human words" {
		t.Fatalf("clear: %+v %v", got, err)
	}
	receipts := changeReceipts(t, w, "change.edited", 3)
	if receipts[1].Actor != "bob" || receipts[1].ProjectID != ch.ProjectID || receipts[1].Subject != fmt.Sprintf("change %d", ch.ID) {
		t.Fatalf("edit attribution: %+v", receipts)
	}
}

// Edits started from one reading: exactly one is saved whole, the others are
// refused and leave nothing behind.
func TestChangeEditsFromOneRevisionOneWins(t *testing.T) {
	t.Parallel()
	w, ch := changeEditFixture(t, ChangeDraft{Title: "original", Body: "brief", ExternalRef: "ref", TitleFromChat: true})
	const size = 12
	codes := race(size, func(i int) int {
		title, body, ref := fmt.Sprintf("title-%d", i), fmt.Sprintf("body-%d", i), fmt.Sprintf("ref-%d", i)
		got, err := editChangeAs(context.Background(), w, fmt.Sprintf("editor-%d", i), ChangeEdit{ProjectID: ch.ProjectID, ID: ch.ID, Title: &title, Body: &body, ExternalRef: &ref, ExpectedRevision: revision(0)})
		switch {
		case errors.Is(err, errChangeEditMoved):
			return 1
		case err != nil:
			t.Errorf("edit: %v", err)
			return 2
		case got.Title != title || got.Body != body || got.ExternalRef != ref || got.EditRevision != 1:
			t.Errorf("mixed edit return: %+v", got)
		}
		return 0
	})
	if count(codes, 0) != 1 || count(codes, 1) != size-1 {
		t.Fatalf("want one saved and the rest refused: %v", codes)
	}
	got, err := readChange(context.Background(), w, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	var winner int
	if _, err := fmt.Sscanf(got.Title, "title-%d", &winner); err != nil {
		t.Fatal(err)
	}
	if got.Body != fmt.Sprintf("body-%d", winner) || got.ExternalRef != fmt.Sprintf("ref-%d", winner) || got.EditRevision != 1 {
		t.Fatalf("partial winner: %+v", got)
	}
	changeReceipts(t, w, "change.edited", 1)
}

// An edit that names no revision is refused; one that names the current
// revision changes only the fields it sends.
func TestChangeEditNeedsItsRevisionAndKeepsOmittedFields(t *testing.T) {
	t.Parallel()
	w, ch := changeEditFixture(t, ChangeDraft{Title: "original", Body: "brief", ExternalRef: "ref", TitleFromChat: true})
	body := "new brief"
	if _, err := editChangeAs(context.Background(), w, "alice", ChangeEdit{ProjectID: ch.ProjectID, ID: ch.ID, Body: &body}); !errors.Is(err, errChangeEditRevisionRequired) {
		t.Fatalf("edit without a revision: %v", err)
	}
	for i, edit := range []ChangeEdit{{Body: &body}, {ExternalRef: &body}} {
		edit.ProjectID, edit.ID, edit.ExpectedRevision = ch.ProjectID, ch.ID, revision(int64(i))
		if _, err := editChangeAs(context.Background(), w, "alice", edit); err != nil {
			t.Fatal(err)
		}
	}
	got, err := readChange(context.Background(), w, ch.ID)
	if err != nil || got.Body != body || got.ExternalRef != body || got.EditRevision != 2 {
		t.Fatalf("omitted fields or revision: %+v %v", got, err)
	}
	changeReceipts(t, w, "change.edited", 2)
}

func TestChangeEditScopeAndCancellation(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"foreign", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			w, ch := changeEditFixture(t, ChangeDraft{Title: "original", Body: "brief", ExternalRef: "ref", TitleFromChat: true})
			other := changeTestProject(t, w, "other")
			title := "human"
			in := ChangeEdit{ProjectID: ch.ProjectID, ID: ch.ID, Title: &title, ExpectedRevision: revision(0)}
			if mode == "foreign" {
				in.ProjectID = other
				got, err := editChangeAs(context.Background(), w, "alice", in)
				var denied *access.Denied
				if got != nil || !errors.As(err, &denied) {
					t.Fatalf("foreign scope: %+v %v", got, err)
				}
			} else {
				gate, err := w.Begin(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = gate.Rollback() }()
				holdProjectLock(t, gate, ch.ProjectID)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() {
					got, err := editChangeAs(ctx, w, "alice", in)
					if got != nil {
						result <- fmt.Errorf("returned unowned result: %+v", got)
					} else {
						result <- err
					}
				}()
				dbtest.WaitForLock(t, w, "-- name: ProjectForUpdate")
				cancel()
				select {
				case err := <-result:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("%s: %v", mode, err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("edit did not release transaction")
				}
			}
			assertChangeTitle(t, w, ch.ID, "original", titleAwaitingChat)
			changeReceipts(t, w, "change.edited", 0)
		})
	}
}
