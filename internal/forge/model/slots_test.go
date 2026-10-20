package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/clock/clocktest"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
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
	// A slash pattern passes through; nothing mapped falls to the instance default.
	if p, slot, prof := s.Resolve(t.Context(), pid, "heavy", "openrouter/x", "local/default"); p != "openrouter/x" || slot != "" || prof != "" {
		t.Fatalf("pass-through: %s %s %s", p, slot, prof)
	}
	if p, slot, prof := s.Resolve(t.Context(), pid, "heavy", "review", "local/default"); p != "local/default" || slot != "default" || prof != "" {
		t.Fatalf("unmapped: %s %s %s", p, slot, prof)
	}
	if err := s.SeedDefault(t.Context(), "local/std"); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedDefault(t.Context(), "local/other"); err != nil {
		t.Fatal(err)
	}
	if p, _, prof := s.Resolve(t.Context(), ids.Nil, "", "", "x"); p != "local/std" || prof != StandardProfile {
		t.Fatalf("the seed is insert-if-absent: %s %s", p, prof)
	}
	// The chain: security → review → default (from the stock fallbacks).
	if err := s.SeedFallbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(context.Background(), "review", ids.Nil, "", StandardProfile, "cloud/reviewer", "alice"); err != nil {
		t.Fatal(err)
	}
	if p, slot, prof := s.Resolve(t.Context(), pid, "heavy", "security", "x"); p != "cloud/reviewer" || slot != "review" || prof != StandardProfile {
		t.Fatalf("fallback chain: %s %s %s", p, slot, prof)
	}
	// The project's profile column beats standard; a project override beats both.
	if err := s.Set(context.Background(), "review", ids.Nil, "", "heavy", "cloud/heavy-reviewer", "alice"); err != nil {
		t.Fatal(err)
	}
	if p, _, prof := s.Resolve(t.Context(), pid, "heavy", "review", "x"); p != "cloud/heavy-reviewer" || prof != "heavy" {
		t.Fatalf("profile cell: %s %s", p, prof)
	}
	if err := s.Set(context.Background(), "review", pid, "p", "", "local/mine", "alice"); err != nil {
		t.Fatal(err)
	}
	if p, _, prof := s.Resolve(t.Context(), pid, "heavy", "review", "x"); p != "local/mine" || prof != "override" {
		t.Fatalf("override: %s %s", p, prof)
	}
	if err := s.Set(context.Background(), "review", pid, "p", "", "", "alice"); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := s.Resolve(t.Context(), pid, "heavy", "review", "x"); p != "cloud/heavy-reviewer" {
		t.Fatalf("cleared override: %s", p)
	}
	if err := s.Set(context.Background(), "review", ids.Nil, "", "Bad Profile", "z", "alice"); !errors.Is(err, ErrProfileName) {
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
