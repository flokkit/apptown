package deployment

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/deploy"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/dbtest"
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

// A changed identity keeps the prior payload and its age for display, but
// remains unknown until a successful check of the new identity.
func TestChangedDetailKeepsOldValuesWithoutClaimingCurrentEvidence(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	ctx := t.Context()
	if err := w.s.observeDetails(ctx, w.main.ID); err != nil {
		t.Fatal(err)
	}
	old, err := w.s.Diagnostics(ctx, w.project, "main")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := w.s.Deployments.ByID(ctx, w.main.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := w.s.detailIdentity(ctx, dep)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadDeploymentDetail[Diagnostics](ctx, w.s, dep, identity+"changed", "Diagnostics")
	if err != nil || !got.Reading.Waiting || !got.Reading.Unknown || !got.Reading.Stale || got.Reading.Failure != "" || len(got.Value.Parts) != len(old.Parts) || !got.Reading.ObservedAt.Equal(old.Reading.ObservedAt) || got.Reading.Identity != identity {
		t.Fatalf("old display lost or current: %+v %v", got, err)
	}
	if got.Value.Generation != old.Generation || got.Value.Parts[0].Facts == nil {
		t.Fatalf("old identity and facts lost: %+v", got)
	}
}

func TestLastAppliedSurvivesAddressEditsAndAdvancesOnRebuild(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	w.s.setClock(w.s.clock)
	ctx := t.Context()
	// Deployment reads must select application evidence with the row, rather
	// than issuing the old per-row CompletedAttempt query.
	clear := dbtest.NextStatementFailure(w.s.db, "-- name: CompletedAttempt")
	t.Cleanup(clear)
	byID, err := w.s.Deployments.ByID(ctx, w.main.ID)
	if err != nil || byID.LastApplied == nil {
		t.Fatalf("deployment application evidence: %+v %v", byID, err)
	}
	first := byID.LastApplied
	byName, err := w.s.Deployments.ByName(ctx, w.main.ProjectID, w.main.Name)
	if err != nil || byName.LastApplied == nil {
		t.Fatalf("named deployment application evidence: %+v %v", byName, err)
	}
	project, err := w.s.Deployments.ForProject(ctx, w.main.ProjectID)
	if err != nil || len(project) == 0 || project[0].LastApplied == nil {
		t.Fatalf("project application evidence: %+v %v", project, err)
	}
	rows, err := w.s.listRecords(ctx, nil)
	if err != nil || len(rows) == 0 || rows[0].summary.LastApplied == nil {
		t.Fatalf("listing application evidence: %+v %v", rows, err)
	}
	clear()
	if first == nil {
		t.Fatal("successful attempt not exposed")
	}
	w.advance(time.Minute)
	if err := w.s.Deployments.RecordAddress(ctx, w.main.ID, "https://new.example"); err != nil {
		t.Fatal(err)
	}
	if got := w.reread(t).LastApplied; got == nil || *got != *first {
		t.Fatalf("address edit restarted application time: %+v %+v", first, got)
	}
	attempt, _, err := w.s.Outcomes.Admit(ctx, Start{DeploymentID: w.main.ID, TargetID: w.main.TargetID, Key: "rebuild", Inputs: Inputs{Revision: w.main.ReleaseSHA}})
	if err != nil {
		t.Fatal(err)
	}
	finished := w.advance(time.Minute)
	if _, err := w.s.Outcomes.Complete(ctx, Completion{Attempt: attempt, Observed: deploy.Observation{Generation: attempt.Generation, Rollout: "active"}}); err != nil {
		t.Fatal(err)
	}
	if got := w.reread(t).LastApplied; got == nil || !got.At.Equal(finished) || got.Generation != attempt.Generation {
		t.Fatalf("rebuild not exposed: %+v", got)
	}
}
