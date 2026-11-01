package deployment

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
)

func TestDetailReadingsSurviveAdmissionAndReadinessChanges(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	if err := w.s.observeDetails(t.Context(), w.main.ID); err != nil {
		t.Fatal(err)
	}
	key, err := w.s.DetailObservationKey(t.Context(), w.main.ID)
	if err != nil {
		t.Fatal(err)
	}
	at, _, err := w.s.Outcomes.Admit(t.Context(), deploymentStartFor(w.main, "next-reading", "next"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.Outcomes.NeedsAttention(t.Context(), at, errors.New("lost settlement")); err != nil {
		t.Fatal(err)
	}
	next, err := w.s.DetailObservationKey(t.Context(), w.main.ID)
	if err != nil || next != key {
		t.Fatalf("admission invalidated settled facts: %s %s %v", key, next, err)
	}
	d, err := w.s.Diagnostics(t.Context(), w.project, "main")
	if err != nil || d.Reading.ObservedAt.IsZero() || d.Reading.Unknown || len(d.Parts) == 0 {
		t.Fatalf("reading lost: %+v %v", d, err)
	}
}

func TestBackgroundTargetChecksRequireRepeatedFailureAndAuditTransitions(t *testing.T) {
	t.Parallel()
	s, db, daemon, _, _ := hostWorld(t, "")
	target, err := s.Targets.ByName(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	failures := false
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "info" {
			if failures {
				return nil, errors.New("temporary outage"), true
			}
			return []byte("28.0 1/1 containers"), nil, true
		}
		return nil, nil, false
	})
	check := func(who string) {
		t.Helper()
		target, err = s.Targets.ByID(t.Context(), target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.providers.ObserveTarget(t.Context(), target, who); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		t.Helper()
		rows, err := audit.Recent(t.Context(), db, audit.Filter{KindPrefix: "target.checked"})
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	check("background check")
	check("background check")
	if n := count(); n != 0 {
		t.Fatalf("unchanged checks audited: %d", n)
	}
	failures = true
	check("background check")
	got, _ := s.Targets.ByID(t.Context(), target.ID)
	if got.Status != TargetReady || count() != 0 {
		t.Fatal("one transient failure withdrew the target")
	}
	check("background check")
	check("background check")
	got, _ = s.Targets.ByID(t.Context(), target.ID)
	if got.Status != TargetUnreachable || count() != 1 {
		t.Fatal("repeated failure did not record exactly one transition")
	}
	failures = false
	check("background check")
	check("background check")
	if count() != 2 {
		t.Fatal("recovery was not audited once")
	}
	check("alice")
	if count() != 3 {
		t.Fatal("explicit check was not audited")
	}
	ctx, cancel := context.WithCancel(t.Context())
	daemon.SetReply(func([]string) ([]byte, error, bool) { cancel(); return nil, ctx.Err(), true })
	if _, err := s.providers.ObserveTarget(ctx, target, "background check"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if count() != 3 {
		t.Fatal("shutdown audited a failure")
	}
}
