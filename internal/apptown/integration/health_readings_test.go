package integration

import (
	"context"
	"testing"
	"time"
)

type healthCheck func(context.Context, Binding) Health

func (f healthCheck) CheckBinding(ctx context.Context, b Binding) Health { return f(ctx, b) }

func TestIntegrationReadingsKeepResultAgeAndConfigurationIdentity(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	deps := Dependencies{Now: func() time.Time { return now }}
	s := NewService(db, deps)
	if _, err := s.Bindings.Upsert(t.Context(), "registry", CapContainerRegistry, "direct", `{"url":"https://packages.test"}`, false); err != nil {
		t.Fatal(err)
	}
	calls := 0
	check := healthCheck(func(context.Context, Binding) Health {
		calls++
		return Health{Detail: "Connection refused. Check credentials."}.At(now)
	})
	if err := s.ObserveHealth(t.Context(), "registry", check); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the owner as a restart would. Reads never invoke the checker.
	s = NewService(db, deps)
	h, err := s.RecordedHealth(t.Context(), "registry")
	if err != nil || h.StateLabel != HealthFailed || h.CheckedAt != now.Format(time.RFC3339) || calls != 1 {
		t.Fatalf("reading: %+v %v calls=%d", h, err, calls)
	}
	now = now.Add(30 * time.Minute)
	h, err = s.RecordedHealth(t.Context(), "registry")
	if err != nil || !h.Stale || h.StateLabel != HealthFailed {
		t.Fatalf("old result: %+v %v", h, err)
	}
	now = now.Add(6 * time.Hour)
	h, err = s.RecordedHealth(t.Context(), "registry")
	if err != nil || h.StateLabel != HealthUnknown || h.CheckedAt == "" {
		t.Fatalf("expired: %+v %v", h, err)
	}
	// A provider answer that races a configuration edit cannot bless the new one.
	check = healthCheck(func(ctx context.Context, b Binding) Health {
		if _, err := s.Bindings.Upsert(ctx, b.Name, b.Capability, b.Provider, `{"url":"https://new.test"}`, false); err != nil {
			t.Fatal(err)
		}
		return Health{OK: true}.At(now)
	})
	if err := s.ObserveHealth(t.Context(), "registry", check); err != nil {
		t.Fatal(err)
	}
	h, err = s.RecordedHealth(t.Context(), "registry")
	if err != nil || h.StateLabel != HealthUnknown || h.CheckedAt != "" {
		t.Fatalf("new config inherited reading: %+v %v", h, err)
	}
}

func TestUnusedUnconfiguredIntegrationIsNotAFailure(t *testing.T) {
	t.Parallel()
	s := NewService(testDB(t), Dependencies{})
	if _, err := s.Bindings.Upsert(t.Context(), "infisical", CapSecrets, "infisical", `{}`, false); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveHealth(t.Context(), "infisical", healthCheck(func(context.Context, Binding) Health {
		return Health{NotSetUp: true, Detail: "An administrator adds the Infisical credentials."}.At(time.Now())
	})); err != nil {
		t.Fatal(err)
	}
	h, err := s.RecordedHealth(t.Context(), "infisical")
	if err != nil || h.StateLabel != HealthNotSetUp {
		t.Fatalf("unused setup: %+v %v", h, err)
	}
}

func TestRequiredMissingIntegrationFollowsCurrentAttachments(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := NewService(db, Dependencies{})
	if _, err := s.Bindings.Upsert(t.Context(), "infisical", CapSecrets, "infisical", `{}`, false); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveHealth(t.Context(), "infisical", healthCheck(func(context.Context, Binding) Health { return Health{NotSetUp: true}.ForDisplay() })); err != nil {
		t.Fatal(err)
	}
	project := createTestProject(t, db, "calendar", "Calendar")
	if err := s.Attachments.Attach(t.Context(), project, CapSecrets, "infisical", `{}`, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	h, err := s.RecordedHealth(t.Context(), "infisical")
	if err != nil || h.StateLabel != HealthFailed {
		t.Fatalf("required: %+v %v", h, err)
	}
	if err := s.Attachments.Detach(t.Context(), project, CapSecrets, "fixture"); err != nil {
		t.Fatal(err)
	}
	h, err = s.RecordedHealth(t.Context(), "infisical")
	if err != nil || h.StateLabel != HealthNotSetUp {
		t.Fatalf("unused again: %+v %v", h, err)
	}
}

type savedHealthTest func(context.Context, Binding) Health

func (f savedHealthTest) TestBinding(ctx context.Context, b Binding) Health { return f(ctx, b) }

func TestSavedConnectionTestSatisfiesUnknownAndSurvivesAnUnsupportedAutomaticCheck(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s := NewService(db, Dependencies{Now: func() time.Time { return now }})
	if _, err := s.Bindings.Upsert(t.Context(), "cloud", "cloud-account", "provider", `{"revision":1}`, false); err != nil {
		t.Fatal(err)
	}
	h, err := s.RecordedHealth(t.Context(), "cloud")
	if err != nil || h.StateLabel != HealthUnknown {
		t.Fatalf("initial: %+v %v", h, err)
	}
	tester := savedHealthTest(func(_ context.Context, b Binding) Health {
		if b.Name != "cloud" || !sameJSON(b.ConfigJSON, `{"revision":1}`) {
			t.Fatalf("test did not use saved configuration: %+v", b)
		}
		return Health{Detail: "Connection refused. Check its settings."}.At(now)
	})
	if _, err := s.TestSaved(t.Context(), "cloud", tester); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	if err := s.ObserveHealth(t.Context(), "cloud", healthCheck(func(context.Context, Binding) Health { return Health{} })); err != nil {
		t.Fatal(err)
	}
	h, err = s.RecordedHealth(t.Context(), "cloud")
	if err != nil || h.StateLabel != HealthFailed || !h.Stale || h.CheckedAt != "2026-11-01T00:00:00Z" {
		t.Fatalf("manual result erased or renewed: %+v %v", h, err)
	}
	h, err = s.TestSaved(t.Context(), "cloud", savedHealthTest(func(context.Context, Binding) Health { return Health{OK: true}.At(now) }))
	if err != nil || h.StateLabel != HealthPassed {
		t.Fatalf("repair: %+v %v", h, err)
	}
	// The named test uses the same version fence as collection jobs.
	h, err = s.TestSaved(t.Context(), "cloud", savedHealthTest(func(ctx context.Context, b Binding) Health {
		if _, err := s.Bindings.Upsert(ctx, b.Name, b.Capability, b.Provider, `{"revision":2}`, false); err != nil {
			t.Fatal(err)
		}
		return Health{OK: true}.At(now)
	}))
	if err != nil || h.StateLabel != HealthUnknown || h.CheckedAt != "" {
		t.Fatalf("late saved test renewed new settings: %+v %v", h, err)
	}
}
