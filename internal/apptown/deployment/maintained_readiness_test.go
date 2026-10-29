package deployment

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

func TestReadinessCacheRequiresFreshExactCandidateAndRuntime(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	dep := &Deployment{ID: ids.New(now), ProjectID: ids.New(now), TargetID: ids.New(now), Kind: Standing, Readiness: Reconciled, DesiredGeneration: 3, ObservedGeneration: 3}
	target := &Target{ID: dep.TargetID, Kind: KindPortainer, Status: TargetReady}
	r := listingRecord{deployment: dep, target: target}
	good := readinessEntry{key: observationKey(r), artifact: "admitted", observed: deploy.Observation{Generation: 3, ArtifactHash: "admitted", Health: "healthy", HealthObservedAt: now}}
	for _, tc := range []struct {
		name   string
		change func(*RuntimeObservation, *listingRecord, *readinessEntry)
		want   bool
	}{
		{"fresh", func(*RuntimeObservation, *listingRecord, *readinessEntry) {}, true},
		{"stale runtime", func(v *RuntimeObservation, _ *listingRecord, _ *readinessEntry) { v.Stale = true }, false},
		{"stale sample", func(_ *RuntimeObservation, _ *listingRecord, e *readinessEntry) {
			e.observed.HealthObservedAt = now.Add(-46 * time.Second)
		}, false},
		{"future sample", func(_ *RuntimeObservation, _ *listingRecord, e *readinessEntry) {
			e.observed.HealthObservedAt = now.Add(time.Second)
		}, false},
		{"provider unavailable", func(_ *RuntimeObservation, _ *listingRecord, e *readinessEntry) { e.err = errors.New("unavailable") }, false},
		{"another artifact", func(_ *RuntimeObservation, _ *listingRecord, e *readinessEntry) { e.observed.ArtifactHash = "other" }, false},
		{"another generation", func(_ *RuntimeObservation, _ *listingRecord, e *readinessEntry) { e.observed.Generation++ }, false},
		{"replacement admitted", func(_ *RuntimeObservation, r *listingRecord, _ *readinessEntry) { r.deployment.DesiredGeneration++ }, false},
		{"another deployment", func(_ *RuntimeObservation, r *listingRecord, _ *readinessEntry) { r.deployment.ID = ids.New(now) }, false},
		{"another target", func(_ *RuntimeObservation, r *listingRecord, _ *readinessEntry) { r.target.ID = ids.New(now) }, false},
		{"restart", func(v *RuntimeObservation, _ *listingRecord, e *readinessEntry) {
			*e = readinessEntry{}
			v.Health = "unknown"
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, c := *dep, *target
			record := listingRecord{deployment: &d, target: &c}
			e := good
			v := RuntimeObservation{State: RuntimeRunning, Rollout: "active", Health: "healthy"} // historical observation must not leak through
			tc.change(&v, &record, &e)
			got := overlayReadiness(v, record, e, now)
			if tc.want {
				if got.Health != "healthy" || got.HealthArtifact != "admitted" || got.HealthGeneration != 3 || !got.HealthObservedAt.Equal(now) {
					t.Fatalf("fresh identity lost: %+v", got)
				}
			} else if got.Health != "unknown" || got.HealthArtifact != "" || got.HealthGeneration != 0 || !got.HealthObservedAt.IsZero() {
				t.Fatalf("unqualified sample is healthy evidence: %+v", got)
			}
		})
	}
}

func TestMaintainedReadinessPermitRechecksRecordedOwner(t *testing.T) {
	for _, name := range []string{"artifact", "restore hold", "project deleting", "target settings changed", "new attempt"} {
		t.Run(name, func(t *testing.T) {
			f, dep := deploymentOutcomeFixture(t)
			s := &Service{db: f.db, Outcomes: f.outcomes, Targets: NewTargets(f.db, nil), Deployments: NewDeployments(f.db)}
			target, err := s.Targets.Put(t.Context(), &Target{Name: "remote", Kind: KindPortainer, Origin: OriginAttached, Status: TargetReady})
			if err != nil {
				t.Fatal(err)
			}
			dep, err = s.Deployments.EnsureStanding(t.Context(), dep.ProjectID, dep.Name, target.ID, Attributes{Feed: dep.Feed, Audience: dep.Audience, DataClass: dep.DataClass})
			if err != nil {
				t.Fatal(err)
			}
			at, _, err := s.Outcomes.Admit(t.Context(), deploymentStartFor(dep, "first", "first"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Outcomes.Complete(t.Context(), deploymentCompletionFor(at, "first")); err != nil {
				t.Fatal(err)
			}
			dep, err = s.Deployments.ByID(t.Context(), dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			original := listingRecord{deployment: dep, target: target}
			if err = s.requireMaintainedReadiness(t.Context(), original, "first-artifact"); err != nil {
				t.Fatalf("valid recorded workload: %v", err)
			}
			artifact := "first-artifact"
			switch name {
			case "artifact":
				artifact = "other"
			case "restore hold":
				_, err = s.Deployments.HoldAfterRestore(t.Context())
			case "project deleting":
				err = f.db.InTx(t.Context(), func(tx *database.Tx) error {
					return project.MarkDeletingIn(t.Context(), tx, dep.ProjectID, access.Actor{Name: "operator"}, ids.ID{}, true, time.Now())
				})
			case "target settings changed":
				domain := "changed.example"
				_, err = s.Targets.EditSettings(t.Context(), target.Name, TargetSettingsEdit{ExpectedRevision: &target.SettingsRevision, Domain: &domain}, "operator")
			case "new attempt":
				_, _, err = s.Outcomes.Admit(context.Background(), deploymentStartFor(dep, "second", "second"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.requireMaintainedReadiness(t.Context(), original, artifact); err == nil {
				t.Fatal("changed owner still grants diagnostic permit")
			}
		})
	}
}

func TestPortainerReleaseAndMaintenanceKeepReadsEffectFree(t *testing.T) {
	p := newPromotionTargetsWith(t, false, digestOneImageYAML)
	p.land(t)
	rel := p.releaseDefault(t)
	if rel.State != Released {
		events, _ := p.s.Releases.Events(t.Context(), rel.ID)
		t.Fatalf("release failed: %+v events=%+v", rel, events)
	}
	selected := p.chooseDefault(t)
	dep, err := p.s.Deployments.ByID(t.Context(), selected.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := p.s.Targets.ByID(t.Context(), dep.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	record := listingRecord{deployment: dep, target: target}
	if p.diagnosticCalls.Load() < 2 {
		t.Fatal("successful release lacked preflight and exact probe")
	}
	before := p.diagnosticCalls.Load()
	if err := p.s.ObserveRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := p.s.observations.read(record, p.s.now()); got.Health == "healthy" {
		t.Fatalf("historical receipt became current maintenance evidence: %+v", got)
	}
	if p.diagnosticCalls.Load() != before {
		t.Fatal("read-only observation launched a diagnostic")
	}
	if err := p.s.refreshReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := p.s.observations.read(record, p.s.now())
	in, err := p.s.Outcomes.InputsAtGeneration(t.Context(), dep.ID, dep.ObservedGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if got.Health != "healthy" || got.HealthGeneration != dep.ObservedGeneration || got.HealthArtifact != in.ArtifactHash || got.HealthObservedAt.IsZero() {
		t.Fatalf("exact owner health missing: %+v", got)
	}
	before = p.diagnosticCalls.Load()
	for range 3 {
		_ = p.s.observations.read(record, p.s.now())
	}
	if p.diagnosticCalls.Load() != before {
		t.Fatal("cached reads launched diagnostics")
	}
	if _, err := p.s.Deployments.HoldAfterRestore(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := p.s.refreshReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.diagnosticCalls.Load() != before {
		t.Fatal("held generation launched diagnostics")
	}
	if got = p.s.observations.read(record, p.s.now()); got.Health == "healthy" {
		t.Fatalf("hold retained a healthy cache: %+v", got)
	}
}

func TestPortainerRecoveryUsesCapturedReleaseAuthorityBeforeDiagnostic(t *testing.T) {
	p := newPromotionTargetsWith(t, false, digestOneImageYAML)
	p.land(t)
	rel := p.releaseDefault(t)
	if rel.State != Released {
		t.Fatalf("release: %+v", rel)
	}
	selected := p.chooseDefault(t)
	dep, err := p.s.Deployments.ByID(t.Context(), selected.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := deploymentsql.New(p.s.db.SQL()).CompletedAttempt(t.Context(), deploymentsql.CompletedAttemptParams{DeploymentID: dep.ID, Generation: dep.ObservedGeneration, Status: AttemptDone.String()})
	if err != nil {
		t.Fatal(err)
	}
	at := Attempt{ID: row.ID, DeploymentID: row.DeploymentID, Generation: row.Generation}
	// Preparing an observer is read-only; exercise the same binding shared by
	// ordinary and stranded recovery without changing a completed settlement.
	target, err := p.s.Recoverer().Source.PrepareObservation(t.Context(), at)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.s.requireRecoveryReadiness(t.Context(), at); err != nil {
		t.Fatalf("original captured principal: %v", err)
	}
	revoked := errors.New("captured release authority revoked")
	p.s.release.Still = &requestAuthority{deny: revoked}
	before := p.diagnosticCalls.Load()
	_, err = target.Observer.Resume(t.Context(), at, dep.Handle, func(string, ...any) {})
	if !errors.Is(err, revoked) {
		t.Fatalf("recovery used operator or stale authority: %v", err)
	}
	if p.diagnosticCalls.Load() != before {
		t.Fatal("revoked recovery launched a diagnostic")
	}
}

func TestUnmanagedProviderHealthDoesNotRequireMaintenanceEntry(t *testing.T) {
	now := time.Now()
	record := listingRecord{deployment: &Deployment{ID: ids.New(now), Kind: Standing, Readiness: Reconciled}, target: &Target{Kind: KindDOApp, Status: TargetReady}}
	observed := RuntimeObservation{Health: "healthy", ObservedAt: now}
	if got := overlayReadiness(observed, record, readinessEntry{}, now); got != observed {
		t.Fatalf("provider health replaced without managed sample: %+v", got)
	}
}

func TestNewerProviderInventoryCannotInheritContradictedHealth(t *testing.T) {
	at := time.Now().UTC()
	dep := &Deployment{ID: ids.New(at), ProjectID: ids.New(at), TargetID: ids.New(at), Kind: Standing, Readiness: Reconciled, DesiredGeneration: 3, ObservedGeneration: 3}
	record := listingRecord{deployment: dep, target: &Target{ID: dep.TargetID, Kind: KindDOApp, Status: TargetReady}}
	sample := readinessEntry{key: observationKey(record), artifact: "admitted", observed: deploy.Observation{Generation: 3, ArtifactHash: "admitted", Running: "dep-1", Health: "healthy", HealthObservedAt: at}}
	service := &Service{clock: func() time.Time { return at.Add(time.Second) }}
	for _, tc := range []struct {
		name, rollout, health, running string
		want                           bool
	}{
		{"matching", "active", "unknown", "dep-1", true},
		{"absent", "absent", "unknown", "", false},
		{"failed", "failed", "unhealthy", "dep-1", false},
		{"unhealthy", "active", "unhealthy", "dep-1", false},
		{"replacement", "active", "unknown", "dep-2", false},
		{"identity missing", "active", "unknown", "", false},
		{"rolling", "rolling", "unknown", "dep-1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inventory := service.observed(t.Context(), RuntimeObservation{}, record, deploy.Observation{Generation: 3, Rollout: tc.rollout, Health: tc.health, Running: tc.running}, nil)
			got := overlayReadiness(inventory, record, sample, at.Add(time.Second))
			if tc.want {
				if got.Health != "healthy" || got.HealthArtifact != "admitted" {
					t.Fatalf("matching current inventory lost health %+v", got)
				}
			} else if got.Health == "healthy" || !got.HealthObservedAt.IsZero() || got.HealthArtifact != "" {
				t.Fatalf("contradictory newer %s inventory inherited old healthy sample: %+v", tc.name, got)
			}
		})
	}
}
