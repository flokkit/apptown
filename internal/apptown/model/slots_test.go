package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/clock/clocktest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestResolveWalksMostSpecificFirst(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.New(db, nil).SetModelProfile(context.Background(), pid, "heavy"); err != nil {
		t.Fatal(err)
	}
	// A slash pattern passes through; nothing mapped resolves to no model.
	if p, slot, prof, err := s.resolve(t.Context(), pid, "heavy", "openrouter/x"); err != nil || p != "openrouter/x" || slot != "" || prof != "" {
		t.Fatalf("pass-through: %s %s %s", p, slot, prof)
	}
	if p, slot, prof, err := s.resolve(t.Context(), pid, "heavy", "review"); err != nil || p != "" || slot != "default" || prof != "" {
		t.Fatalf("unmapped: %s %s %s", p, slot, prof)
	}
	if err := s.SetDefault(t.Context(), "local/std"); err != nil {
		t.Fatal(err)
	}
	if p, _, prof, err := s.resolve(t.Context(), ids.Nil, "", ""); err != nil || p != "local/std" || prof != StandardProfile {
		t.Fatalf("the default slot: %s %s", p, prof)
	}
	// The chain: security → review → default (from the stock fallbacks).
	if err := s.SeedFallbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(context.Background(), "review", ids.Nil, "", StandardProfile, "cloud/reviewer", "alice"); err != nil {
		t.Fatal(err)
	}
	if p, slot, prof, err := s.resolve(t.Context(), pid, "heavy", "security"); err != nil || p != "cloud/reviewer" || slot != "review" || prof != StandardProfile {
		t.Fatalf("fallback chain: %s %s %s", p, slot, prof)
	}
	// The project's profile column beats standard; a project override beats both.
	if err := s.Set(context.Background(), "review", ids.Nil, "", "heavy", "cloud/heavy-reviewer", "alice"); err != nil {
		t.Fatal(err)
	}
	if p, _, prof, err := s.resolve(t.Context(), pid, "heavy", "review"); err != nil || p != "cloud/heavy-reviewer" || prof != "heavy" {
		t.Fatalf("profile cell: %s %s", p, prof)
	}
	if err := s.Set(context.Background(), "review", pid, "p", "", "local/mine", "alice"); err != nil {
		t.Fatal(err)
	}
	if p, _, prof, err := s.resolve(t.Context(), pid, "heavy", "review"); err != nil || p != "local/mine" || prof != "override" {
		t.Fatalf("override: %s %s", p, prof)
	}
	if err := s.Set(context.Background(), "review", pid, "p", "", "", "alice"); err != nil {
		t.Fatal(err)
	}
	if p, _, _, err := s.resolve(t.Context(), pid, "heavy", "review"); err != nil || p != "cloud/heavy-reviewer" {
		t.Fatalf("cleared override: %s", p)
	}
	if err := s.Set(context.Background(), "review", ids.Nil, "", "Bad Profile", "z", "alice"); !errors.Is(err, errProfileName) {
		t.Fatalf("profile shape: %v", err)
	}
	profiles, _ := s.Profiles(t.Context())
	if len(profiles) != 2 || profiles[0] != StandardProfile || profiles[1] != "heavy" {
		t.Fatalf("profiles: %v", profiles)
	}
	overrides, _ := s.Overrides(context.Background())
	if len(overrides) != 0 {
		t.Fatalf("no overrides left: %+v", overrides)
	}
	receipts, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "model-slot.set", Limit: audit.MaxLimit})
	if err != nil || len(receipts) == audit.MaxLimit {
		t.Fatalf("receipts: %d %v", len(receipts), err)
	}
	// The ledger filter is a kind prefix; the receipt kind is exact.
	set := 0
	for _, receipt := range receipts {
		if receipt.Kind == "model-slot.set" {
			set++
		}
	}
	if set != 4 {
		t.Fatalf("receipts: %d", set)
	}
}

// Setting a slot takes one reading of AppTown's clock for the cell and
// its receipt: on a clock that moves on a second at every reading, the
// receipt carries the reading the operation took, not a later one.
func TestASlotsReceiptCarriesItsCellsTime(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	clk := clocktest.NewStepping(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), time.Second)
	s := New(db)
	s.Now = clk.Now
	reading := clk.Next()
	if err := s.Set(context.Background(), "review", ids.Nil, "", StandardProfile, "cloud/reviewer", "alice"); err != nil {
		t.Fatal(err)
	}
	got, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "model-slot.set"})
	if err != nil || len(got) != 1 {
		t.Fatalf("receipts: %+v %v", got, err)
	}
	if at, err := time.Parse(time.RFC3339Nano, got[0].At); err != nil || !at.Equal(reading) {
		t.Fatalf("a slot set at %v has its receipt at %s (%v)", reading, got[0].At, err)
	}
}

// Setting a slot runs on its caller's context: a caller that has gone
// (a cancelled request) sets nothing and keeps no receipt.
func TestASlotIsNotSetForACallerThatHasGone(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Set(ctx, "review", ids.Nil, "", StandardProfile, "cloud/reviewer", "alice"); !errors.Is(err, context.Canceled) {
		t.Fatalf("set for a gone caller: %v", err)
	}
	if cells, err := s.Cells(context.Background()); err != nil || cells["review"][StandardProfile] != "" {
		t.Fatalf("cells after a gone caller's set: %v %v", cells, err)
	}
	if got, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "model-slot.set"}); err != nil || len(got) != 0 {
		t.Fatalf("receipts: %+v %v", got, err)
	}
}

// A failed read is never an unmapped cell: Resolve returns the error instead
// of a less specific model (an override's read failing must not select the
// instance mapping), "no model is set up" (a fallback read failing), or the
// standard profile (the project's profile read failing).
func TestResolveReturnsReadFailures(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db)
	selection := NewSelection(s)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.New(db, nil).SetModelProfile(context.Background(), pid, "heavy"); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedFallbacks(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][4]string{{"review", "", StandardProfile, "cloud/std"}, {"review", "p", "", "local/mine"}, {"review", "", "heavy", "cloud/heavy"}} {
		project := ids.Nil
		if c[1] != "" {
			project = pid
		}
		if err := s.Set(t.Context(), c[0], project, c[1], c[2], c[3], "alice"); err != nil {
			t.Fatal(err)
		}
	}
	// The override is the first cell read; failing it must not reach heavy.
	restore := dbtest.NextStatementFailure(db, "FROM model_slots")
	if p, _, _, err := s.resolve(t.Context(), pid, "heavy", "review"); err == nil {
		t.Fatalf("a failed override read resolved %q", p)
	}
	restore()
	// security has no cell and falls back to review: a failed fallback read
	// is not "no model".
	restore = dbtest.NextStatementFailure(db, "FROM slot_fallbacks")
	if p, slot, _, err := s.resolve(t.Context(), ids.Nil, "", "security"); err == nil {
		t.Fatalf("a failed fallback read resolved %q (slot %q)", p, slot)
	}
	restore()
	// The project's profile read failing is not the standard profile.
	restore = dbtest.NextStatementFailure(db, "FROM projects WHERE id")
	if p, _, prof, err := selection.Resolve(t.Context(), pid, "review"); err == nil {
		t.Fatalf("a failed profile read resolved %q from %q", p, prof)
	}
	restore()
	// Nothing failing: the override, and security down its chain.
	if p, _, prof, err := selection.Resolve(t.Context(), pid, "review"); err != nil || p != "local/mine" || prof != "override" {
		t.Fatalf("override: %q %q %v", p, prof, err)
	}
	if p, slot, _, err := s.resolve(t.Context(), ids.Nil, "", "security"); err != nil || p != "cloud/std" || slot != "review" {
		t.Fatalf("security falls back to review: %q %q %v", p, slot, err)
	}
	// A project that does not exist has no profile to resolve against.
	if p, _, _, err := selection.Resolve(t.Context(), ids.New(time.Now()), "review"); err == nil {
		t.Fatalf("a missing project resolved %q", p)
	}
}
