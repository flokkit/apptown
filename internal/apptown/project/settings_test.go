package project

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func settingsFixture(t *testing.T) (*database.DB, ids.ID) {
	t.Helper()
	db := testDB(t)
	create := func(slug string) ids.ID {
		id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			return CreateAt(t.Context(), tx, time.Now(), Spec{Slug: slug, Name: slug, Icon: "old", Blurb: "old"})
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := create("settings")
	create("taken")
	return db, id
}

func patchSettings(db *database.DB, id ids.ID, reserved string, in SettingsPatch) error {
	return db.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := UpdateSettingsIn(context.Background(), tx, id, reserved, in)
		return err
	})
}

func TestSettingsPatchIsAtomicAndPreservesOmissions(t *testing.T) {
	t.Parallel()
	db, id := settingsFixture(t)
	ctx := context.Background()
	rev := int64(0)
	changed, taken := "new", "taken"
	if err := patchSettings(db, id, "settings", SettingsPatch{ExpectedRevision: &rev, Slug: &taken, Icon: &changed, Blurb: &changed}); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("a rename: %v", err)
	}
	before, err := ByIDIn(ctx, db, id)
	if err != nil || before.Icon != "old" || before.Blurb != "old" || before.Slug != "settings" || before.SettingsRevision != 0 {
		t.Fatalf("partial edit: %+v %v", before, err)
	}
	// A caller's later transaction failure must roll back the whole participant.
	rollback := errors.New("later participant failed")
	err = db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := UpdateSettingsIn(ctx, tx, id, "settings", SettingsPatch{ExpectedRevision: &rev, Icon: &changed}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if after, err := ByIDIn(ctx, db, id); err != nil || after != before {
		t.Fatalf("participant committed early: %+v %v", after, err)
	}
	// A field the edit leaves out keeps its value; an empty value is sent on purpose.
	empty := ""
	if err := patchSettings(db, id, "settings", SettingsPatch{ExpectedRevision: &rev, Icon: &changed, Blurb: &empty}); err != nil {
		t.Fatal(err)
	}
	after, err := ByIDIn(ctx, db, id)
	if err != nil || after.Icon != changed || after.Blurb != "" || after.Name != "settings" || after.SettingsRevision != 1 {
		t.Fatalf("omitted or emptied fields: %+v %v", after, err)
	}
	next := after.SettingsRevision
	if err := patchSettings(db, id, "old-reservation", SettingsPatch{ExpectedRevision: &next, Icon: &changed}); !errors.Is(err, errSettingsChanged) {
		t.Fatalf("stale reservation: %v", err)
	}
}

// Two edits started from the same reading: exactly one is saved, the other
// is refused, and the saved values stay as the winner left them.
func TestTwoEditsFromOneRevisionOneWins(t *testing.T) {
	t.Parallel()
	db, id := settingsFixture(t)
	rev := int64(0)
	icon, blurb := "from-alice", "from-bob"
	patches := []SettingsPatch{{ExpectedRevision: &rev, Icon: &icon}, {ExpectedRevision: &rev, Blurb: &blurb}}
	errs := make([]error, len(patches))
	var wg sync.WaitGroup
	for i, patch := range patches {
		wg.Go(func() { errs[i] = patchSettings(db, id, "settings", patch) })
	}
	wg.Wait()
	won, moved := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrSettingsMoved):
			moved++
		default:
			t.Fatal(err)
		}
	}
	if won != 1 || moved != 1 {
		t.Fatalf("want one saved and one refused: %v", errs)
	}
	after, err := ByIDIn(context.Background(), db, id)
	if err != nil || after.SettingsRevision != 1 {
		t.Fatalf("revision: %+v %v", after, err)
	}
	if errs[0] == nil && (after.Icon != icon || after.Blurb != "old") || errs[1] == nil && (after.Blurb != blurb || after.Icon != "old") {
		t.Fatalf("the refused edit leaked into the winner's values: %+v", after)
	}
}

// An edit that does not say which reading it started from is refused, so no
// client can save over changes it never saw.
func TestSettingsEditNeedsItsRevision(t *testing.T) {
	t.Parallel()
	db, id := settingsFixture(t)
	icon := "new"
	if err := patchSettings(db, id, "settings", SettingsPatch{Icon: &icon}); !errors.Is(err, errSettingsRevisionRequired) {
		t.Fatalf("edit without a revision: %v", err)
	}
	if after, err := ByIDIn(context.Background(), db, id); err != nil || after.Icon != "old" || after.SettingsRevision != 0 {
		t.Fatalf("refused edit wrote: %+v %v", after, err)
	}
}
