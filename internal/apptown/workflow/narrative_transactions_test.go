package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func narrativeFixture(t *testing.T) (narratives, NarrativeSource) {
	t.Helper()
	db, ch := changeEditFixture(t, ChangeDraft{Title: "automatic", Body: "brief", TitleFromChat: true})
	nameChangeForTest(t, db, ch, "automatic")
	owner := newNarratives(db, narrativeNotesFunc(func(context.Context, *database.Tx, ids.ID, ids.ID, string) error { return nil }))
	source, err := owner.Source(context.Background(), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Publish(context.Background(), NarrativePublication{Source: source, Draft: NarrativeDraft{Note: "previous"}}); err != nil {
		t.Fatal(err)
	}
	source, err = owner.Source(context.Background(), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	return owner, source
}

func assertNarrative(t *testing.T, owner narratives, id ids.ID, title, note string) {
	t.Helper()
	got, err := owner.Source(context.Background(), id)
	if err != nil || got.Change.Title != title || got.Change.Summary != note {
		t.Fatalf("narrative: %+v %v, want %q / %q", got.Change, err, title, note)
	}
}

func TestNarrativePublicationRequiresEveryWriteAndReceipt(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"project read", "change write", "receipt", "suppressed change", "suppressed receipt", "deferred change", "deferred receipt"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			s, source := narrativeFixture(t)
			switch failure {
			case "project read":
				dbtest.FailNextStatement(t, s.db, "-- name: ProjectForUpdate")
			case "change write":
				dbtest.FailNextStatement(t, s.db, "UPDATE changes SET title=")
			case "receipt":
				dbtest.FailNextStatement(t, s.db, "INSERT INTO audit_log")
			default:
				table, event := "changes", "UPDATE"
				if strings.HasSuffix(failure, "receipt") {
					table, event = "audit_log", "INSERT"
				}
				body := "RETURN NULL;"
				trigger := "CREATE TRIGGER veto_narrative BEFORE " + event + " ON " + table
				if strings.HasPrefix(failure, "deferred") {
					body = "RAISE EXCEPTION 'reject narrative at commit';"
					trigger = "CREATE CONSTRAINT TRIGGER veto_narrative AFTER " + event + " ON " + table + " DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, s.db, `CREATE FUNCTION veto_narrative() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				completionExec(t, s.db, trigger+" FOR EACH ROW EXECUTE FUNCTION veto_narrative()")
			}
			if err := s.Publish(context.Background(), NarrativePublication{Source: source, Draft: NarrativeDraft{Title: "New title", Note: "New note"}, Slot: "fast", Model: "openrouter/test"}); err == nil {
				t.Fatal("partial narrative published")
			}
			assertNarrative(t, s, source.Change.ID, "automatic", "previous")
			changeReceipts(t, s.db, "change.narrative-updated", 1)
		})
	}
}

func TestNarrativePublicationsDoNotOverwriteAnotherCompletedObservation(t *testing.T) {
	t.Parallel()
	s, source := narrativeFixture(t)
	results := race(8, func(i int) int {
		err := s.Publish(context.Background(), NarrativePublication{Source: source, Draft: NarrativeDraft{Title: fmt.Sprintf("title-%d", i), Note: fmt.Sprintf("note-%d", i)}, Slot: "fast", Model: "test"})
		if errors.Is(err, ErrNarrativeChanged) {
			return 0
		}
		if err != nil {
			t.Errorf("publication: %v", err)
			return -1
		}
		return 1
	})
	if count(results, 1) != 1 {
		t.Fatalf("multiple observations overwrote: %v", results)
	}
	fresh, err := s.Source(context.Background(), source.Change.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimPrefix(fresh.Change.Title, "title-") != strings.TrimPrefix(fresh.Change.Summary, "note-") {
		t.Fatalf("mixed publication: %+v", fresh.Change)
	}
	changeReceipts(t, s.db, "change.narrative-updated", 2)
}

func TestNarrativeSourceReadFailuresReturnNoPartialResult(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"change", "project", "runs", "artifacts", "missing"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			s, source := narrativeFixture(t)
			switch failure {
			case "change":
				dbtest.FailNextStatement(t, s.db, "SELECT id, project_id, number, type, title")
			case "project":
				dbtest.FailNextStatement(t, s.db, "-- name: ProjectByID")
			case "runs":
				dbtest.FailNextStatement(t, s.db, "-- name: NarrativeRunsMeta")
			case "artifacts":
				dbtest.FailNextStatement(t, s.db, "-- name: NarrativeArtifacts")
			case "missing":
				source.Change.ID = idstest.N(999999)
			}
			got, err := s.Source(context.Background(), source.Change.ID)
			if err == nil || got.Change != nil || len(got.Runs) != 0 || len(got.Artifacts) != 0 {
				t.Fatalf("partial %s: %+v %v", failure, got, err)
			}
			if (failure == "missing") != errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("read failure confused with missing: %s %v", failure, err)
			}
		})
	}
}

func TestNarrativeTitleObservesCurrentOwnership(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"automatic", "human", "different automatic title"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s, source := narrativeFixture(t)
			want := "generated"
			switch mode {
			case "human":
				// Pinning the very same words must still take ownership.
				title := "automatic"
				if _, err := editChangeAs(context.Background(), s.db, "alice", ChangeEdit{ProjectID: source.Change.ProjectID, ID: source.Change.ID, Title: &title, ExpectedRevision: revision(0)}); err != nil {
					t.Fatal(err)
				}
				want = title
			case "different automatic title":
				if err := s.Publish(context.Background(), NarrativePublication{Source: source, Draft: NarrativeDraft{Title: "different", Note: "previous"}}); err != nil {
					t.Fatal(err)
				}
				want = "different"
			}
			if err := s.Publish(context.Background(), NarrativePublication{Source: source, Draft: NarrativeDraft{Title: "generated", Note: "note"}, Slot: "fast", Model: "test"}); err != nil {
				t.Fatal(err)
			}
			assertNarrative(t, s, source.Change.ID, want, "note")
		})
	}
}

func TestNarrativePublicationPreservesConcurrentHumanTitle(t *testing.T) {
	t.Parallel()
	owner, source := narrativeFixture(t)
	// People editing from the same reading race each other and the model's
	// publication: one person's title is saved, the rest are refused, and the
	// publication never overwrites it (it does not advance the edit revision).
	codes := race(8, func(i int) int {
		if i == 0 {
			if err := owner.Publish(context.Background(), NarrativePublication{Source: source, Draft: NarrativeDraft{Title: "Model title", Note: "Generated note"}, Slot: "fast", Model: "openrouter/test"}); err != nil {
				t.Error(err)
			}
			return 0
		}
		title := fmt.Sprintf("human-%d", i)
		_, err := editChangeAs(context.Background(), owner.db, "alice", ChangeEdit{ID: source.Change.ID, Title: &title, ExpectedRevision: revision(0)})
		switch {
		case errors.Is(err, errChangeEditMoved):
			return 2
		case err != nil:
			t.Error(err)
		}
		return 1
	})
	if count(codes, 1) != 1 {
		t.Fatalf("want one human title saved: %v", codes)
	}
	fresh, err := owner.Source(context.Background(), source.Change.ID)
	if err != nil || !strings.HasPrefix(fresh.Change.Title, "human-") || fresh.Change.Summary != "Generated note" {
		t.Fatalf("human edit lost: %+v %v", fresh, err)
	}
	assertChangeTitle(t, owner.db, source.Change.ID, fresh.Change.Title, titleHuman)
	entries := changeReceipts(t, owner.db, "change.narrative-updated", 2)
	var receipt narrativeReceipt
	if err := json.Unmarshal(entries[0].Payload, &receipt); err != nil {
		t.Fatal(err)
	}
	if entries[0].Actor != "apptown-system" || receipt.Slot != "fast" || receipt.Model != "openrouter/test" || receipt.Chars != 14 {
		t.Fatalf("publication attribution: %+v %+v", entries[0], receipt)
	}
}
