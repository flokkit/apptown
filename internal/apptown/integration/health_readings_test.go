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
	if err := s.observeHealth(t.Context(), "registry", check); err != nil {
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
	if err := s.observeHealth(t.Context(), "registry", check); err != nil {
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
	if err := s.observeHealth(t.Context(), "infisical", healthCheck(func(context.Context, Binding) Health {
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
	if err := s.observeHealth(t.Context(), "infisical", healthCheck(func(context.Context, Binding) Health { return Health{NotSetUp: true}.ForDisplay() })); err != nil {
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

func (f savedHealthTest) CheckBinding(ctx context.Context, b Binding) Health { return f(ctx, b) }

func TestSavedConnectionUsesTheObservationCheckAndExpires(t *testing.T) {
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
	if err := s.observeHealth(t.Context(), "cloud", healthCheck(func(context.Context, Binding) Health { return Health{} })); err != nil {
		t.Fatal(err)
	}
	h, err = s.RecordedHealth(t.Context(), "cloud")
	if err != nil || h.StateLabel != HealthUnknown || !h.Stale || h.CheckedAt != "2026-11-01T00:00:00Z" {
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

func TestFailedBackgroundCheckKeepsLastGoodResultAcrossRestart(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	deps := Dependencies{Now: func() time.Time { return now }}
	s := NewService(db, deps)
	if _, err := s.Bindings.Upsert(t.Context(), "models", CapLLM, "openrouter", `{}`, false); err != nil {
		t.Fatal(err)
	}
	checks := 0
	checker := healthCheck(func(context.Context, Binding) Health {
		checks++
		if checks == 1 {
			return Health{OK: true, Detail: "key accepted"}.At(now)
		}
		return Health{Detail: "allowance service refused the key"}.At(now)
	})
	if err := s.observeHealth(t.Context(), "models", checker); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	if _, err := s.TestSaved(t.Context(), "models", checker); err != nil {
		t.Fatal(err)
	}
	s = NewService(db, deps)
	h, err := s.RecordedHealth(t.Context(), "models")
	if err != nil || !h.Automatic || h.StateLabel != HealthFailed || h.LastGood == nil || !h.LastGood.OK || h.LastGood.Detail != "key accepted" || h.LastGood.CheckedAt != "2026-11-01T00:00:00Z" || h.Failure != "allowance service refused the key" || h.FailedAt != now.Format(time.RFC3339) || checks != 2 {
		t.Fatalf("retained failure: %+v err=%v calls=%d", h, err, checks)
	}
}

func TestEveryMeaningfulIntegrationCheckIsAutomatic(t *testing.T) {
	t.Parallel()
	for _, cap := range []string{CapFeedback, CapRegistry, CapSCM, CapSecrets, CapModel, CapExecutor, CapDeploy, CapLLM, CapErrors, CapContainerRegistry, "scanner", "cloud-account"} {
		if !(Binding{Capability: cap, Provider: "docker"}).HasAutomaticCheck() {
			t.Errorf("%s has no automatic check", cap)
		}
	}
	for _, cap := range []string{"custom-service"} {
		if (Binding{Capability: cap}).HasAutomaticCheck() {
			t.Errorf("%s claims an automatic check", cap)
		}
	}
}

func TestShutdownDoesNotReplaceAConnectionReading(t *testing.T) {
	s := NewService(testDB(t), Dependencies{})
	if _, err := s.Bindings.Upsert(t.Context(), "feedback", CapFeedback, "feedback", `{}`, false); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	check := healthCheck(func(context.Context, Binding) Health { return Health{OK: true}.At(now) })
	if _, err := s.TestSaved(t.Context(), "feedback", check); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	err := s.observeHealth(ctx, "feedback", healthCheck(func(context.Context, Binding) Health { cancel(); return Health{}.At(now.Add(time.Minute)) }))
	if err != context.Canceled {
		t.Fatalf("shutdown: %v", err)
	}
	h, err := s.RecordedHealth(t.Context(), "feedback")
	if err != nil || !h.OK || h.CheckedAt != now.Format(time.RFC3339) {
		t.Fatalf("shutdown changed reading: %+v %v", h, err)
	}
}
func TestScannerChecksAllowFirstPullForBackgroundAndTestConnection(t *testing.T) {
	t.Parallel()
	s := NewService(testDB(t), Dependencies{})
	_, err := s.Bindings.Upsert(t.Context(), "scanner", "scanner", "trivy", `{}`, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Bindings.ByName(t.Context(), "scanner")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	checker := healthCheck(func(ctx context.Context, _ Binding) Health {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 90*time.Second {
			t.Errorf("scanner cannot finish its first pull: %v", deadline)
		}
		calls++
		return Health{OK: true}.At(s.now())
	})
	if err := s.RunHealthObservation(t.Context(), b.HealthObservationKey(), checker); err != nil {
		t.Fatal(err)
	}
	if h, err := s.TestSaved(t.Context(), b.Name, checker); err != nil || !h.OK || calls != 2 {
		t.Fatalf("shared scanner check: %+v %v calls=%d", h, err, calls)
	}
}
