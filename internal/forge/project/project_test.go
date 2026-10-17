package project

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestProjectRecordRules(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ps := New(db, nil)
	create := func(spec Spec) (ids.ID, error) {
		return database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			id, err := CreateAt(t.Context(), tx, time.Now(), spec)
			if err != nil {
				return ids.Nil, err
			}
			return id, nil
		})
	}
	if _, err := create(Spec{Slug: "Bad Slug"}); !errors.Is(err, ErrSlug) {
		t.Fatalf("a slug outside the shape was accepted: %v", err)
	}
	id, err := create(Spec{Slug: "coin-flip", OwnerGroup: "family"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := ps.ByID(context.Background(), id)
	if err != nil || p.Name != "coin-flip" || p.OwnerGroup != "family" {
		t.Fatalf("a nameless project is named after its slug, with its facts: %+v %v", p, err)
	}
	if _, err := create(Spec{Slug: "coin-flip"}); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("a taken slug: %v", err)
	}
	other, _ := create(Spec{Slug: "other"})
	if err := ps.Rename(context.Background(), other, "coin-flip", "Other"); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("renaming onto a taken slug: %v", err)
	}
	if err := ps.Rename(context.Background(), other, "other-one", "Other One"); err != nil {
		t.Fatal(err)
	}
	if got, _ := ps.BySlug(context.Background(), "other-one"); got.ID != other || got.Name != "Other One" {
		t.Fatalf("renamed: %+v", got)
	}
	if err := ps.Remove(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err := ps.Remove(context.Background(), other); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("removing twice is never a silent success: %v", err)
	}
}

func TestGeneratedProjectQueryHonorsCanceledContext(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queries(db).ProjectByID(ctx, idstest.N(1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("query ignored context cancellation: %v", err)
	}
}

// What the project record writes carries its writer's clock: creation the
// caller's, deletion attempts the record's own, however far from the
// machine's. (No read shows a contract snapshot's time, which the record's
// clock also stamps.)
func TestTheProjectRecordIsStampedByItsClock(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	at := time.Now().Add(-72 * time.Hour).Truncate(time.Second).UTC()
	settled := at.Add(time.Hour)
	var now atomic.Int64
	now.Store(at.Unix())
	projects := NewWithClock(db, nil, func() time.Time { return time.Unix(now.Load(), 0) })
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return CreateAt(t.Context(), tx, at, Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	exported, err := ExportIn(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(exported, &row); err != nil || !row.CreatedAt.Equal(at) {
		t.Fatalf("created at %v, want the caller's %v: %v", row.CreatedAt, at, err)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return RecordDeletionIn(context.Background(), tx, idstest.N(900), "gone", access.Actor{Name: "bls"}, map[string]any{}, at)
	}); err != nil {
		t.Fatal(err)
	}
	now.Store(settled.Unix())
	if ok, err := projects.SettleDeletionAttempt(context.Background(), idstest.N(900), 0, "done", ""); err != nil || !ok {
		t.Fatalf("settle: %v %v", ok, err)
	}
	d, err := projects.Deletion(context.Background(), idstest.N(900))
	if err != nil || !d.RequestedAt.Equal(at) || d.FinishedAt == nil || !d.FinishedAt.Equal(settled) {
		t.Fatalf("deletion %+v %v, want requested %v and finished %v", d, err, at, settled)
	}
}

// A project's purpose is written with it and read back from Project; a
// purpose Project does not know is refused at creation, and a project that
// is not there has no purpose, never "ordinary".
func TestAProjectsPurposeIsWrittenWithIt(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ps := New(db, nil)
	create := func(spec Spec) (ids.ID, error) {
		return database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			return CreateAt(t.Context(), tx, time.Now(), spec)
		})
	}
	ordinary, err := create(Spec{Slug: "ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	copy, err := create(Spec{Slug: "eval-1-1", Purpose: EvalCopy})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[ids.ID]Purpose{ordinary: Ordinary, copy: EvalCopy} {
		p, err := ps.ByID(context.Background(), id)
		if err != nil || p.Purpose != want {
			t.Fatalf("project %d's record: %q %v, want %q", id, p.Purpose, err, want)
		}
		if got, err := ps.Purpose(context.Background(), id); err != nil || got != want {
			t.Fatalf("project %d's purpose: %q %v, want %q", id, got, err, want)
		}
	}
	if _, err := create(Spec{Slug: "invented", Purpose: "invented"}); err == nil {
		t.Fatal("a purpose Project does not know was written")
	}
	if got, err := ps.Purpose(context.Background(), idstest.N(999999)); err == nil {
		t.Fatalf("a missing project has purpose %q", got)
	}
}

// What each purpose allows, as the table states it: every purpose answers
// every question, and a copy is given none of what an ordinary project is.
func TestEveryPurposeAnswersEveryAllowance(t *testing.T) {
	t.Parallel()
	want := map[Purpose][5]bool{
		Ordinary: {true, false, true, true, true},
		EvalCopy: {false, true, false, false, false},
	}
	if len(want) != len(Purposes) {
		t.Fatalf("purposes %v, table %v", Purposes, want)
	}
	for _, p := range Purposes {
		if _, ok := allowances[p]; !ok {
			t.Fatalf("purpose %q has no allowances", p)
		}
		got := [5]bool{p.Integrations(), p.EvalDomainOnly(), p.Schedules(), p.Moves(), p.KeepsData()}
		if got != want[p] {
			t.Fatalf("%q allows %v, want %v", p, got, want[p])
		}
	}
}
