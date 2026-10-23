package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestChatTitleClaimsExactlyOnceWithItsReceipt(t *testing.T) {
	t.Parallel()
	db, ch := changeEditFixture(t, ChangeDraft{Title: "automatic", TitleFromChat: true})
	results := race(12, func(i int) int {
		applied, err := (changeStore{db: db}).nameFromChat(context.Background(), ch.ProjectID, ch.ID, fmt.Sprintf("message %d. More text.", i), fmt.Sprintf("person-%d", i))
		if err != nil {
			t.Errorf("claim: %v", err)
			return -1
		}
		if applied {
			return i + 1
		}
		return 0
	})
	winner := -1
	for _, result := range results {
		if result > 0 {
			if winner >= 0 {
				t.Fatal("multiple first-message winners")
			}
			winner = result - 1
		}
	}
	if winner < 0 {
		t.Fatal("no first-message winner")
	}
	assertChangeTitle(t, db, ch.ID, fmt.Sprintf("message %d.", winner), titleGenerated)
	receipts := changeReceipts(t, db, "change.titled", 1)
	if receipts[0].Actor != fmt.Sprintf("person-%d", winner) || receipts[0].ProjectID != ch.ProjectID {
		t.Fatalf("title attribution: %+v", receipts)
	}
	if applied, err := (changeStore{db: db}).nameFromChat(context.Background(), ch.ProjectID, ch.ID, "a later message", "later"); err != nil || applied {
		t.Fatalf("later message renamed: %v %v", applied, err)
	}
}

func TestChatTitleCannotUndoHumanOwnership(t *testing.T) {
	t.Parallel()
	for _, racePin := range []bool{false, true} {
		t.Run(fmt.Sprint(racePin), func(t *testing.T) {
			t.Parallel()
			db, ch := changeEditFixture(t, ChangeDraft{Title: "Chat change (human words)", TitleFromChat: true})
			title := "Chat change (human words)"
			pin := func() {
				if _, err := editChangeAs(context.Background(), db, "alice", ChangeEdit{ProjectID: ch.ProjectID, ID: ch.ID, Title: &title, ExpectedRevision: revision(0)}); err != nil {
					t.Error(err)
				}
			}
			if !racePin {
				pin()
			}
			race(8, func(i int) int {
				if racePin && i == 0 {
					pin()
				} else if applied, err := (changeStore{db: db}).nameFromChat(context.Background(), ch.ProjectID, ch.ID, "a message", "chat"); err != nil || (!racePin && applied) {
					t.Errorf("human title claimed: %v %v", applied, err)
				}
				return 0
			})
			assertChangeTitle(t, db, ch.ID, title, titleHuman)
		})
	}
}

func TestChatTitleRequiresItsCompleteTransaction(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"project read", "title read", "write", "receipt", "suppressed write", "suppressed receipt", "deferred write", "deferred receipt"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			db, ch := changeEditFixture(t, ChangeDraft{Title: "automatic", TitleFromChat: true})
			switch failure {
			case "project read":
				dbtest.FailNextStatement(t, db, "-- name: ProjectForUpdate")
			case "title read":
				dbtest.FailNextStatement(t, db, "-- name: ChangeForUpdate")
			case "write":
				dbtest.FailNextStatement(t, db, "UPDATE changes SET title=")
			case "receipt":
				dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
			default:
				table, event := "changes", "UPDATE"
				if strings.HasSuffix(failure, "receipt") {
					table, event = "audit_log", "INSERT"
				}
				body := "RETURN NULL;"
				trigger := "CREATE TRIGGER veto_chat_title BEFORE " + event + " ON " + table
				if strings.HasPrefix(failure, "deferred") {
					body = "RAISE EXCEPTION 'rejected chat title at commit';"
					trigger = "CREATE CONSTRAINT TRIGGER veto_chat_title AFTER " + event + " ON " + table + " DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, db, `CREATE FUNCTION veto_chat_title() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				completionExec(t, db, trigger+" FOR EACH ROW EXECUTE FUNCTION veto_chat_title()")
			}
			if applied, err := (changeStore{db: db}).nameFromChat(context.Background(), ch.ProjectID, ch.ID, "first words", "alice"); err == nil || applied {
				t.Fatalf("unrecorded title: %v %v", applied, err)
			}
			assertChangeTitle(t, db, ch.ID, "automatic", titleAwaitingChat)
			changeReceipts(t, db, "change.titled", 0)
		})
	}
}

func TestChatTitleRequiresCurrentScopeAndCancellation(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"foreign", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			db, ch := changeEditFixture(t, ChangeDraft{Title: "automatic", TitleFromChat: true})
			other := changeTestProject(t, db, "other")
			if mode == "foreign" {
				if applied, err := (changeStore{db: db}).nameFromChat(context.Background(), other, ch.ID, "new", "alice"); applied || !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("foreign title: %v %v", applied, err)
				}
			} else {
				gate, err := db.Begin(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = gate.Rollback() }()
				holdProjectLock(t, gate, ch.ProjectID)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() {
					_, err := (changeStore{db: db}).nameFromChat(ctx, ch.ProjectID, ch.ID, "new", "alice")
					result <- err
				}()
				dbtest.WaitForLock(t, db, "-- name: ProjectForUpdate")
				cancel()
				select {
				case err := <-result:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancel: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("naming held cancelled lock wait")
				}
			}
			assertChangeTitle(t, db, ch.ID, "automatic", titleAwaitingChat)
			changeReceipts(t, db, "change.titled", 0)
		})
	}
}
