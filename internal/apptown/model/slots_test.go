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
	if p, slot, prof, err := s.resolve(t.Context(), "heavy", "openrouter/x"); err != nil || p != "openrouter/x" || slot != "" || prof != "" {
		t.Fatalf("pass-through: %s %s %s", p, slot, prof)
	}
	if p, slot, prof, err := s.resolve(t.Context(), "heavy", "review"); err != nil || p != "" || slot != "default" || prof != "" {
		t.Fatalf("unmapped: %s %s %s", p, slot, prof)
	}
	if err := s.SetDefault(t.Context(), "local/std"); err != nil {
		t.Fatal(err)
	}
	if p, _, prof, err := s.resolve(t.Context(), "", ""); err != nil || p != "local/std" || prof != StandardProfile {
		t.Fatalf("the default slot: %s %s", p, prof)
	}
	// The chain: security → review → default (from the stock fallbacks).
	if err := s.SeedFallbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(context.Background(), "review", StandardProfile, "cloud/reviewer", "alice"); err != nil {
		t.Fatal(err)
	}
	if p, slot, prof, err := s.resolve(t.Context(), "heavy", "security"); err != nil || p != "cloud/reviewer" || slot != "review" || prof != StandardProfile {
		t.Fatalf("fallback chain: %s %s %s", p, slot, prof)
	}
	// The project's profile column beats standard; the receipt names that profile.
	if err := s.Set(context.Background(), "review", "heavy", "cloud/heavy-reviewer", "alice"); err != nil {
		t.Fatal(err)
	}
	if p, _, prof, err := s.resolve(t.Context(), "heavy", "review"); err != nil || p != "cloud/heavy-reviewer" || prof != "heavy" {
		t.Fatalf("profile cell: %s %s", p, prof)
	}
	if err := s.Set(context.Background(), "review", "Bad Profile", "z", "alice"); !errors.Is(err, errProfileName) {
		t.Fatalf("profile shape: %v", err)
	}
	profiles, _ := s.Profiles(t.Context())
	if len(profiles) != 2 || profiles[0] != StandardProfile || profiles[1] != "heavy" {
		t.Fatalf("profiles: %v", profiles)
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
	if set != 2 {
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
	if err := s.Set(context.Background(), "review", StandardProfile, "cloud/reviewer", "alice"); err != nil {
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
	if err := s.Set(ctx, "review", StandardProfile, "cloud/reviewer", "alice"); !errors.Is(err, context.Canceled) {
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
// of a less specific model (a profile read failing must not select the
// standard mapping), "no model is set up" (a fallback read failing), or the
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
	for _, c := range [][2]string{{StandardProfile, "cloud/std"}, {"heavy", "cloud/heavy"}} {
		if err := s.Set(t.Context(), "review", c[0], c[1], "alice"); err != nil {
			t.Fatal(err)
		}
	}
	// A failed profile cell read must not reach standard.
	restore := dbtest.NextStatementFailure(db, "FROM model_slots")
	if p, _, _, err := s.resolve(t.Context(), "heavy", "review"); err == nil {
		t.Fatalf("a failed profile cell read resolved %q", p)
	}
	restore()
	// security has no cell and falls back to review: a failed fallback read
	// is not "no model".
	restore = dbtest.NextStatementFailure(db, "FROM slot_fallbacks")
	if p, slot, _, err := s.resolve(t.Context(), "", "security"); err == nil {
		t.Fatalf("a failed fallback read resolved %q (slot %q)", p, slot)
	}
	restore()
	// The project's profile read failing is not the standard profile.
	restore = dbtest.NextStatementFailure(db, "FROM projects WHERE id")
	if p, _, prof, err := selection.Resolve(t.Context(), pid, "review"); err == nil {
		t.Fatalf("a failed profile read resolved %q from %q", p, prof)
	}
	restore()
	// Nothing failing: the profile cell, and security down its chain.
	if p, _, prof, err := selection.Resolve(t.Context(), pid, "review"); err != nil || p != "cloud/heavy" || prof != "heavy" {
		t.Fatalf("profile: %q %q %v", p, prof, err)
	}
	if p, slot, _, err := s.resolve(t.Context(), "", "security"); err != nil || p != "cloud/std" || slot != "review" {
		t.Fatalf("security falls back to review: %q %q %v", p, slot, err)
	}
	// A project that does not exist has no profile to resolve against.
	if p, _, _, err := selection.Resolve(t.Context(), ids.New(time.Now()), "review"); err == nil {
		t.Fatalf("a missing project resolved %q", p)
	}
}

// Every task uses its basic choice until an explicit task cell overrides it.
// Independent expectations keep a changed stock fallback from silently passing.
func TestTasksUseBasicChoicesUntilIndividuallySet(t *testing.T) {
	t.Parallel()
	s := New(testDB(t))
	if err := s.SeedFallbacks(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault(t.Context(), "vendor/strong"); err != nil {
		t.Fatal(err)
	}
	for _, slot := range StockSlots {
		if got, _, _, err := s.resolve(t.Context(), "standard", slot); err != nil || got != "vendor/strong" {
			t.Fatalf("default-only %s: %q %v", slot, got, err)
		}
	}
	for _, base := range []string{"primary-fast", "secondary-strong", "secondary-fast"} {
		if err := s.Set(t.Context(), base, "standard", "vendor/"+base, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	for task, base := range map[string]string{
		"default": "default", "plan": "default", "implement": "default", "chat": "default", "worker": "default",
		"fast": "primary-fast", "scout": "primary-fast", "review": "secondary-strong", "security": "secondary-strong",
		"primary-fast": "primary-fast", "secondary-strong": "secondary-strong", "secondary-fast": "secondary-fast",
	} {
		want := "vendor/" + base
		if base == "default" {
			want = "vendor/strong"
		}
		if got, _, _, err := s.resolve(t.Context(), "high", task); err != nil || got != want {
			t.Fatalf("%s basic: %q, want %q (%v)", task, got, want, err)
		}
		if err := s.Set(t.Context(), task, "high", "vendor/task", "fixture"); err != nil {
			t.Fatal(err)
		}
		if got, _, profile, err := s.resolve(t.Context(), "high", task); err != nil || got != "vendor/task" || profile != "high" {
			t.Fatalf("%s override: %q %q %v", task, got, profile, err)
		}
		if err := s.Set(t.Context(), task, "high", "", "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	// With no independent fast choice, quick checks use the primary fast choice.
	if err := s.Set(t.Context(), "secondary-fast", "standard", "", "fixture"); err != nil {
		t.Fatal(err)
	}
	if got, _, _, err := s.resolve(t.Context(), "high", "secondary-fast"); err != nil || got != "vendor/primary-fast" {
		t.Fatalf("unset secondary fast: %q %v", got, err)
	}
}
