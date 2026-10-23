package deployment

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

// heldDriver is a provider whose answer the test sets, and which notes
// every effect asked of it.
type heldDriver struct {
	fakeDriver
	mu       sync.Mutex
	observed deploy.Observation
	err      error
	effects  []string
}

func (d *heldDriver) Reconcile(context.Context, deploy.Placement, int64, deploy.Artifact, string, deploy.Logf, deploy.Checkpoint) (deploy.Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.effects = append(d.effects, "reconcile")
	return deploy.Observation{}, errors.New("a held deployment reached its provider")
}

func (d *heldDriver) Teardown(context.Context, deploy.Placement, int64, deploy.TakeDown) (deploy.TakenDown, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.effects = append(d.effects, "teardown")
	return deploy.TakenDown{}, nil
}

func (d *heldDriver) Observe(context.Context, deploy.Placement) (deploy.Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.observed, d.err
}

// The record of what runs is App Platform's: the handle's deployment.
func (d *heldDriver) RecordedRunning(p deploy.Placement, _ string) string {
	id, _ := p.Handle["deploymentId"].(string)
	return id
}

func (d *heldDriver) AdoptRunning(running string) map[string]any {
	return map[string]any{"deploymentId": running}
}

func (d *heldDriver) seen() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.effects...)
}

// heldFixture is a promotion-fed deployment on an App Platform target whose
// last attempt recorded running App Platform's deployment dep-1, with a
// bundle to release.
func heldFixture(t *testing.T) (*Service, DeploySelection, *Deployment, *heldDriver, *requestSources) {
	t.Helper()
	db, _, draft := releaseCreationFixture(t, ReleaseFromBundle)
	target, err := NewTargets(db, nil).Put(context.Background(), &Target{Name: "cloud", Kind: KindDOApp, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := NewDeployments(db).EnsureStanding(t.Context(), draft.ProjectID, draft.Deployment, target.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE test_release_queue(release_id UUID PRIMARY KEY REFERENCES releases(id))`)
	driver := &heldDriver{}
	sources := &requestSources{}
	s := New(context.Background(), db, assembled(db, testRuntime{Drivers: map[string]DriverFactory{KindDOApp: func(context.Context, *Target, *Deployment) (deploy.Driver, error) { return driver, nil }}},
		Dependencies{Releases: ReleaseDependencies{Authority: &requestAuthority{}, Sources: sources, Secrets: requestSecrets{}, Queue: requestQueue{}}}))
	at, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "first", Inputs: Inputs{Revision: "selected", ArtifactHash: "spec"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy",
		Handle: map[string]any{"appId": "app-1", "deploymentId": "dep-1"}}}); err != nil {
		t.Fatal(err)
	}
	return s, DeploySelection{ProjectID: draft.ProjectID, DeploymentName: draft.Deployment, BundleID: draft.BundleID}, storedDeployment(t, db, dep.ID), driver, sources
}

// After a restore, a deployment on a provider is acted on by nothing,
// release work the backup's database held queued included, until its
// provider has been read: a release asked for before the backup ends failed
// before any preparation or provider effect, a new one is refused at once,
// and so is a take-down.
//
// Kills: a hold checked only where a release is admitted (a queued one
// would run on the stale records), or after preparation.
func TestNothingActsOnADeploymentHeldAfterARestore(t *testing.T) {
	t.Parallel()
	s, in, dep, driver, sources := heldFixture(t)
	ctx := t.Context()
	queued, err := s.RequestRelease(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	held, err := s.Deployments.HoldAfterRestore(ctx)
	if err != nil || len(held) != 1 || held[0].ID != dep.ID {
		t.Fatalf("held %+v, %v", held, err)
	}
	prepared := sources.calls
	if err := s.ExecuteRelease(ctx, queued.ID); !errors.Is(err, ErrUnconfirmedSinceRestore) {
		t.Fatalf("a queued release of a held deployment ran: %v", err)
	}
	if sources.calls != prepared || len(driver.seen()) != 0 {
		t.Fatalf("a held deployment's release was prepared (%d) or reached its provider (%v)", sources.calls-prepared, driver.seen())
	}
	if r := storedRelease(t, s.db, in.ProjectID, queued.ID); r.State != ReleaseFailed {
		t.Fatalf("the queued release is %s", r.State)
	}
	if _, err := s.RequestRelease(ctx, in); !errors.Is(err, ErrUnconfirmedSinceRestore) {
		t.Fatalf("a new release of a held deployment was accepted: %v", err)
	}
	if _, _, err := s.Outcomes.Admit(ctx, Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "late", Inputs: Inputs{Revision: "selected"}}); !errors.Is(err, ErrUnconfirmedSinceRestore) {
		t.Fatalf("an attempt was admitted for a held deployment: %v", err)
	}
	if err := storedDeployment(t, s.db, dep.ID).TakeDownHeldAfterRestore(); !errors.Is(err, ErrUnconfirmedSinceRestore) {
		t.Fatalf("a take-down of an unconfirmed deployment is allowed: %v", err)
	}
}

// A built-in deployment waiting for its data from the backup is run only by
// the rebuild that fills it: a release queued before the backup, a new
// release and any other attempt are refused; the rebuild is admitted.
//
// Kills: a rebuild refused (the data never comes back), or a release
// booting the app on empty data before the fill.
func TestOnlyTheRebuildRunsADeploymentWaitingForItsData(t *testing.T) {
	t.Parallel()
	s, in, dep, driver, _ := heldFixture(t)
	ctx := t.Context()
	queued, err := s.RequestRelease(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.HoldForFill(ctx, []ids.ID{dep.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.ExecuteRelease(ctx, queued.ID); !errors.Is(err, ErrFillingSinceRestore) {
		t.Fatalf("a queued release ran before the fill: %v", err)
	}
	if _, err := s.RequestRelease(ctx, in); !errors.Is(err, ErrFillingSinceRestore) {
		t.Fatalf("a new release was accepted before the fill: %v", err)
	}
	if _, _, err := s.Outcomes.Admit(ctx, Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "landing", Inputs: Inputs{Revision: "selected"}}); !errors.Is(err, ErrFillingSinceRestore) {
		t.Fatalf("a landing's attempt was admitted before the fill: %v", err)
	}
	base := dep.DesiredGeneration
	if _, created, err := s.Outcomes.Admit(ctx, Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "rebuild", Inputs: Inputs{Revision: "selected", Rebuild: true}, BaseGeneration: &base}); err != nil || !created {
		t.Fatalf("the rebuild that fills it was refused: %v", err)
	}
	if len(driver.seen()) != 0 {
		t.Fatalf("the provider was asked %v", driver.seen())
	}
	if err := storedDeployment(t, s.db, dep.ID).TakeDownHeldAfterRestore(); err != nil {
		t.Fatalf("a take-down of a deployment waiting for its data is refused: %v", err)
	}
}

// The check after a restore reads what the provider runs, with the
// observation it already makes, and settles each held deployment: the
// recorded deployment confirms it; another is adopted as an observed
// release, never a shipped one, and becomes the record; no app at all is
// gone, for a person; an unreadable provider, or an attempt still open,
// leaves it held.
func TestTheCheckAfterARestoreSettlesWhatTheProviderSays(t *testing.T) {
	for _, scenario := range []string{"same", "other", "gone", "unreadable", "rolling"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, _, dep, driver, _ := heldFixture(t)
			ctx := t.Context()
			switch scenario {
			case "same":
				driver.observed = deploy.Observation{Rollout: "active", Running: "dep-1"}
			case "other":
				driver.observed = deploy.Observation{Rollout: "active", Running: "dep-9"}
			case "gone":
				driver.observed = deploy.Observation{Rollout: "absent"}
			case "unreadable":
				driver.err = errors.New("the platform did not answer")
			case "rolling":
				driver.observed = deploy.Observation{Rollout: "rolling"}
			}
			if _, err := s.Deployments.HoldAfterRestore(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.CheckAfterRestore(ctx); err != nil {
				t.Fatal(err)
			}
			got := storedDeployment(t, s.db, dep.ID)
			want := map[string]RestoreCheck{"same": "", "other": "", "gone": RestoreGone, "unreadable": RestoreUnconfirmed, "rolling": RestoreUnconfirmed}[scenario]
			if got.RestoreCheck != want {
				t.Fatalf("held %q, want %q", got.RestoreCheck, want)
			}
			released := everyRelease(t, s.db)
			switch scenario {
			case "same":
				wantN(t, "releases", released, 0)
				wantN(t, "confirmations", receipts(t, s.db, "deployment.confirmed-after-restore"), 1)
			case "other":
				wantN(t, "releases", released, 1)
				r := released[0]
				if r.Origin != OriginObservedAfterRestore || r.State != Released || r.ExternalRef != "dep-9" || !r.BundleID.IsZero() || r.ApprovedBy != "" {
					t.Fatalf("the adopted release: %+v", r)
				}
				if got.Handle["deploymentId"] != "dep-9" || got.ReleaseSHA != "" {
					t.Fatalf("the record of what runs: %v, revision %q", got.Handle, got.ReleaseSHA)
				}
				wantN(t, "adoptions", receipts(t, s.db, "deployment.adopted-after-restore", func(e audit.Entry) bool {
					return strings.Contains(string(e.Payload), "dep-1") && strings.Contains(string(e.Payload), "dep-9")
				}), 1)
			case "gone":
				if err := got.HeldAfterRestore(); !errors.Is(err, ErrGoneSinceRestore) {
					t.Fatalf("a gone deployment is not held: %v", err)
				}
				if err := got.TakeDownHeldAfterRestore(); err != nil {
					t.Fatalf("a gone deployment cannot be taken down: %v", err)
				}
			default:
				wantN(t, "releases", released, 0)
			}
			// Asked again, a settled deployment is not asked about.
			driver.observed, driver.err = deploy.Observation{Rollout: "active", Running: "dep-42"}, nil
			if err := s.CheckAfterRestore(ctx); err != nil {
				t.Fatal(err)
			}
			if scenario != "unreadable" && scenario != "rolling" && len(everyRelease(t, s.db)) != len(released) {
				t.Fatal("a settled deployment was checked again")
			}
		})
	}
}

// After an adoption, the release AppTown last shipped there can be deployed
// by hand, also on a landing-fed deployment: it puts back what the records
// said. Any other bundle is still refused there.
func TestTheRecordedReleaseCanBeDeployedAgainAfterAnAdoption(t *testing.T) {
	t.Parallel()
	s, in, dep, driver, _ := heldFixture(t)
	ctx := t.Context()
	shipped, err := s.RequestRelease(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// The release AppTown shipped: settled released at the deployment's
	// generation, as a completed attempt settles it.
	at, _, err := s.Outcomes.Admit(ctx, Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "shipped", Inputs: Inputs{ReleaseID: shipped.ID, Revision: "selected", BundleID: in.BundleID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Complete(ctx, Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy",
		Handle: map[string]any{"appId": "app-1", "deploymentId": "dep-1"}}}); err != nil {
		t.Fatal(err)
	}
	mutateStoredDeployment(t, s.db, in.ProjectID, in.DeploymentName, func(r *deploymentsql.Deployment) { r.Feed = "landing" })
	driver.observed = deploy.Observation{Rollout: "active", Running: "dep-9"}
	if _, err := s.Deployments.HoldAfterRestore(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckAfterRestore(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestRelease(ctx, in); err != nil {
		t.Fatalf("the recorded release cannot be deployed again: %v", err)
	}
	other := in
	other.BundleID = cutBundleFixture(t, s.db, in.ProjectID, "selected", BundleOrigin{})
	if _, err := s.RequestRelease(ctx, other); !errors.Is(err, ErrFedByLanding) {
		t.Fatalf("another bundle was taken by hand on a landing-fed deployment: %v", err)
	}
}
