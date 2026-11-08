package identity

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

// Guarantee: defaults, live administrator authority, concurrent editor protection
// and atomic receipts are the same for every caller of instance settings.
func TestFeatureChoicesRequireAnAdministratorAndKeepTheirReceipt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	if err := s.SeedAdministrator(ctx, "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsurePerson(ctx, "reader"); err != nil {
		t.Fatal(err)
	}
	boss := admittedPrincipal(t, s, "boss")
	features, err := s.Features(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range features {
		if f.Enabled != (f.Tier != "beta") {
			t.Fatalf("wrong new-install default: %+v", f)
		}
	}
	if err := s.RequireFeature(ctx, "evals"); err == nil || !strings.Contains(err.Error(), "is turned off; an administrator can turn it on in Features") {
		t.Fatalf("beta admitted: %v", err)
	}
	if _, err := s.SetFeature(ctx, admittedPrincipal(t, s, "reader"), "evals", false, true); err == nil {
		t.Fatal("member changed a feature")
	}
	if _, err := s.SetFeature(ctx, boss, "projects", true, false); err == nil {
		t.Fatal("core disabled")
	}
	if _, err := s.SetFeature(ctx, boss, "evals", false, true); err != nil {
		t.Fatal(err)
	}
	if err := s.RequireFeature(ctx, "evals"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFeature(ctx, boss, "evals", false, false); err == nil {
		t.Fatal("stale editor overwrote a choice")
	}
	receipts := ledger(t, s.db, "feature.changed")
	if len(receipts) != 1 || receipts[0].Actor != "boss" || receipts[0].Subject != "evals" {
		t.Fatalf("receipts: %+v", receipts)
	}
	restore := dbtest.NextStatementFailure(s.db, "INSERT INTO audit_log")
	if _, err := s.SetFeature(ctx, boss, "evals", true, false); err == nil {
		t.Fatal("receipt failure was ignored")
	}
	restore()
	if err := s.RequireFeature(ctx, "evals"); err != nil {
		t.Fatalf("receipt failure changed the setting: %v", err)
	}
	if _, err := s.SetFeature(ctx, boss, "evals", true, false); err != nil {
		t.Fatal(err)
	}
	if err := s.RequireFeature(ctx, "evals"); err == nil {
		t.Fatal("off choice did not refuse access")
	}
}

// Core areas cannot be disabled through any caller's settings API.
func TestCoreWorkingAreasAreAlwaysOn(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{})
	if err := s.SeedAdministrator(t.Context(), "boss"); err != nil {
		t.Fatal(err)
	}
	boss := admittedPrincipal(t, s, "boss")
	for _, area := range []string{"ways", "previews", "complaints", "usage"} {
		f, err := s.FeatureState(t.Context(), area)
		if err != nil || f.Tier != "always" || !f.Enabled {
			t.Fatalf("%s: %+v %v", area, f, err)
		}
		if _, err := s.SetFeature(t.Context(), boss, area, true, false); err == nil {
			t.Errorf("disabled %s", area)
		}
		if err := s.RequireFeature(t.Context(), area); err != nil {
			t.Errorf("%s: %v", area, err)
		}
	}
}

// An area that works within another is off while that area is off, whatever
// its own switch says, and its own choice comes back with that area.
func TestAnAreaIsOffWhileTheAreaItNeedsIsOff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	if err := s.SeedAdministrator(ctx, "boss"); err != nil {
		t.Fatal(err)
	}
	boss := admittedPrincipal(t, s, "boss")
	for _, f := range FeatureCatalogue() {
		if f.Needs == "" {
			continue
		}
		if _, err := s.SetFeature(ctx, boss, f.Area, false, true); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetFeature(ctx, boss, f.Needs, true, false); err != nil {
			t.Fatal(err)
		}
		if err := s.RequireFeature(ctx, f.Area); err == nil {
			t.Errorf("%s admitted while %s is off", f.Area, f.Needs)
		}
		if on, err := FeatureEnabledIn(ctx, s.db, f.Area); err != nil || on {
			t.Errorf("%s background work on while %s is off: %v %v", f.Area, f.Needs, on, err)
		}
		if _, err := s.SetFeature(ctx, boss, f.Needs, false, true); err != nil {
			t.Fatal(err)
		}
		if err := s.RequireFeature(ctx, f.Area); err != nil {
			t.Errorf("%s's own choice was not kept: %v", f.Area, err)
		}
	}
}
