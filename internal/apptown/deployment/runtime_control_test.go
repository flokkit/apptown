package deployment

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/fault"
)

type runtimeDriver struct {
	fakeDriver
	actions []string
	state   string
	health  string
	failure error
}

func (d *runtimeDriver) Runtime(_ context.Context, _ deploy.Placement, action string) error {
	d.actions = append(d.actions, action)
	if d.failure != nil {
		return d.failure
	}
	d.state = "active"
	if action == "stop" {
		d.state = "stopped"
	}
	return nil
}
func (d *runtimeDriver) Observe(_ context.Context, p deploy.Placement) (deploy.Observation, error) {
	return deploy.Observation{Generation: p.Generation, Rollout: d.state, Health: d.health}, nil
}

func (d *runtimeDriver) ObserveRuntime(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	return d.Observe(ctx, p)
}

func runtimeFixture(t *testing.T, kind string) (*Service, *Deployment, *runtimeDriver, *requestAuthority) {
	t.Helper()
	s, in, authority, _ := requestFixture(t)
	s.authority = authority
	s.Outcomes.followups = noFollowups{}
	target, err := s.Targets.Put(t.Context(), &Target{Name: "runtime", Kind: kind, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, "runtime", target.ID, Attributes{Feed: "manual", Audience: "team", DataClass: "production"})
	if err != nil {
		t.Fatal(err)
	}
	driver := &runtimeDriver{state: "active"}
	s.providers.drivers[kind] = func(context.Context, *Target, *Deployment) (deploy.Driver, error) { return driver, nil }
	at, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "first", Inputs: Inputs{Revision: "one", ImageDigest: "sha256:one"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", ImageDigest: "sha256:one", URL: "https://app.example"}}); err != nil {
		t.Fatal(err)
	}
	dep, err = s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, dep, driver, authority
}

// Runtime acts preserve the placement and release, and retain actor/time in
// storage. Neither an action nor a restart creates a release or backup.
func TestRuntimeActionsPreserveDeploymentAndRecordActor(t *testing.T) {
	s, dep, driver, authority := runtimeFixture(t, KindDockerHost)
	now := time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC)
	s.setClock(func() time.Time { return now })
	for _, action := range []string{"stop", "start", "restart"} {
		got, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, action)
		if err != nil {
			t.Fatal(action, err)
		}
		want := "running"
		if action == "stop" {
			want = "stopped"
		}
		if got.RuntimeState != want || got.RuntimeActor != "alice" || !got.RuntimeAt.Time.Equal(now) {
			t.Fatalf("%s: %+v", action, got)
		}
		if got.URL != dep.URL || got.ObservedGeneration != dep.ObservedGeneration || got.ReleaseSHA != dep.ReleaseSHA || got.TargetID != dep.TargetID || got.DataClass != dep.DataClass {
			t.Fatalf("%s changed placement: %+v", action, got)
		}
		acts, err := audit.Recent(t.Context(), s.db, audit.Filter{ProjectID: dep.ProjectID, KindPrefix: "deployment." + action + "-completed", Subject: dep.Name})
		if err != nil || len(acts) != 1 || acts[0].Actor != "alice" || acts[0].At != now.Format(time.RFC3339Nano) {
			t.Fatalf("%s receipts: %+v %v", action, acts, err)
		}
		now = now.Add(time.Minute)
	}
	if len(driver.actions) != 3 {
		t.Fatal(driver.actions)
	}
	for _, p := range authority.seen {
		if p.Context.Route == "deployment.stop" || p.Context.Route == "deployment.start" || p.Context.Route == "deployment.restart" {
			if p.Capability != access.CapDeploymentOperate {
				t.Fatal(p)
			}
		}
	}
	releases, err := s.Releases.ForProject(t.Context(), dep.ProjectID, 50)
	if err != nil || len(releases) != 0 {
		t.Fatalf("runtime created releases: %+v %v", releases, err)
	}
}

func TestRuntimeActionsRequireOperateBeforeIntentOrProviderEffects(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			s, dep, driver, authority := runtimeFixture(t, KindDockerHost)
			authority.deny = fault.New(fault.Forbidden, "operate denied")
			if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, action); fault.KindOf(err) != fault.Forbidden {
				t.Fatal(err)
			}
			got, err := s.Deployments.ByID(t.Context(), dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.RuntimeState != "running" || got.RuntimeActor != "" || len(driver.actions) != 0 {
				t.Fatalf("refused act had effects: %+v %v", got, driver.actions)
			}
		})
	}
}

// A page offers only the runtime acts a target can ever take: every act on
// a Docker host or Portainer, only restart on App Platform (it cannot keep
// an app stopped), none on a bucket website, which runs nothing to start.
// Each act left out is still refused with its target's sentence.
func TestATargetOffersOnlyTheRuntimeActsItCanEverTake(t *testing.T) {
	for kind, want := range map[string][]string{
		KindDockerHost:  {"start", "stop", "restart"},
		KindPortainer:   {"start", "stop", "restart"},
		KindDOApp:       {"restart"},
		kindFlyMachines: {"restart"},
		KindS3Bucket:    {},
	} {
		target := &Target{Kind: kind}
		if got := runtimeActions(target); !slices.Equal(got, want) {
			t.Errorf("%s offers %v, want %v", kind, got, want)
		}
		controls := runtimeControls(target, &Deployment{})
		for _, action := range []string{"start", "stop", "restart"} {
			if offered := slices.Contains(want, action); offered != (controls[action] == "") {
				t.Errorf("%s %s: offered %v, refusal %q", kind, action, offered, controls[action])
			}
		}
	}
}

func TestRuntimeTargetCapabilitiesAreEnforcedBeforeEffects(t *testing.T) {
	for _, kind := range []string{KindS3Bucket, KindDOApp} {
		for _, action := range []string{"start", "stop", "restart"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				s, dep, driver, _ := runtimeFixture(t, kind)
				_, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, action)
				if kind == KindDOApp && action == "restart" {
					if err != nil || len(driver.actions) != 1 {
						t.Fatalf("documented restart: %v %v", err, driver.actions)
					}
					return
				}
				if fault.KindOf(err) != fault.Unprocessable || len(driver.actions) != 0 {
					t.Fatalf("unsupported action: %v %v", err, driver.actions)
				}
			})
		}
	}
}

func TestStoppedIntentSurvivesNewCodeAndColdRuntimeRead(t *testing.T) {
	s, dep, _, _ := runtimeFixture(t, KindDockerHost)
	if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "stop"); err != nil {
		t.Fatal(err)
	}
	at, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "next", Inputs: Inputs{Revision: "two", ImageDigest: "sha256:two"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "stopped", ImageDigest: "sha256:two"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RuntimeState != "stopped" || got.ReleaseSHA != "two" || got.Readiness != Reconciled {
		t.Fatalf("new code unstopped: %+v", got)
	}
	cold := &observations{}
	observed := cold.read(listingRecord{deployment: got}, s.now().Add(24*time.Hour))
	if observed.State != RuntimeStopped || observed.Stale {
		t.Fatalf("explicit stop became unknown: %+v", observed)
	}
	// An active observation cannot complete a stopped deployment's release.
	at, _, err = s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "bad", Inputs: Inputs{Revision: "three"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active"}}); err == nil {
		t.Fatal("silently started stopped deployment")
	}
}

func TestUnconfirmedRuntimeActionBlocksReleasesUntilObserved(t *testing.T) {
	s, dep, driver, _ := runtimeFixture(t, KindDockerHost)
	driver.failure = errors.New("connection lost")
	if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "stop"); err == nil {
		t.Fatal("lost outcome accepted")
	}
	got, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RuntimeState != "stopping" || got.RuntimeError == "" {
		t.Fatalf("lost intent: %+v", got)
	}
	if _, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "competing"}); !errors.Is(err, errRuntimeBusy) {
		t.Fatal(err)
	}
	driver.state = "stopped"
	if err := s.checkRuntimeControl(t.Context(), got); err != nil {
		t.Fatal(err)
	}
	got, err = s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || got.RuntimeState != "stopped" || got.RuntimeError != "" {
		t.Fatalf("observed: %+v %v", got, err)
	}
}

func TestRuntimeTransitionExcludesReleaseAdmission(t *testing.T) {
	s, dep, driver, _ := runtimeFixture(t, KindDockerHost)
	driver.failure = errors.New("connection lost")
	if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "stop"); err == nil {
		t.Fatal("expected unconfirmed stop")
	}
	_, err := s.Releases.Start(t.Context(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: dep.ProjectID, DeploymentID: dep.ID, Deployment: dep.Name}, "test")
	if !errors.Is(err, errRuntimeBusy) {
		t.Fatal(err)
	}
}

func TestUnconfirmedRuntimeActionCanBeRetriedWithoutAllowingAConflictingAct(t *testing.T) {
	s, dep, driver, _ := runtimeFixture(t, KindDockerHost)
	driver.failure = errors.New("connection lost")
	if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "stop"); err == nil {
		t.Fatal("expected unknown outcome")
	}
	if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "restart"); !errors.Is(err, errRuntimeBusy) {
		t.Fatal(err)
	}
	driver.failure = nil
	if got, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "stop"); err != nil || got.RuntimeState != "stopped" {
		t.Fatalf("retry: %+v %v", got, err)
	}
}

type stoppedReleaseDriver struct {
	runtimeDriver
	stopped []bool
}

func (d *stoppedReleaseDriver) Reconcile(_ context.Context, p deploy.Placement, generation int64, _ deploy.Artifact, _ string, _ deploy.Logf, _ deploy.Checkpoint) (deploy.Observation, error) {
	d.stopped = append(d.stopped, p.Stopped)
	rollout := "active"
	if p.Stopped {
		rollout = "stopped"
	}
	return deploy.Observation{Generation: generation, Rollout: rollout, ImageDigest: "sha256:next", URL: p.URL}, nil
}

// Every release feed reaches the same execution boundary. Its selected code
// advances and its successful release settles while runtime intent stays stopped.
func TestStoppedReleaseExecutionUpdatesCodeForEveryFeed(t *testing.T) {
	for _, feed := range []string{"main", "promotion", "manual"} {
		t.Run(feed, func(t *testing.T) {
			s, dep, _, _ := runtimeFixture(t, KindDockerHost)
			if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "stop"); err != nil {
				t.Fatal(err)
			}
			dep, err := s.Deployments.ByID(t.Context(), dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			// Source selection is already complete at execute; feed does not confer a
			// different runtime policy. Existing request tests cover its selected bundle.
			dep.Feed = feed
			driver := &stoppedReleaseDriver{}
			s.providers.drivers[KindDockerHost] = func(context.Context, *Target, *Deployment) (deploy.Driver, error) { return driver, nil }
			cut, err := s.Releases.Start(t.Context(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: dep.ProjectID, DeploymentID: dep.ID, Deployment: dep.Name, Revision: "next", Actor: "alice"}, "next code")
			if err != nil {
				t.Fatal(err)
			}
			target, err := s.Targets.ByID(t.Context(), dep.TargetID)
			if err != nil {
				t.Fatal(err)
			}
			c, err := appcontract.Parse([]byte("app:\n  web:\n    image: example/web\n    public: true\n    ports: [\"8080\"]\n"))
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.execute(t.Context(), cut, dep, target, preparedRelease{source: deploy.Source{Contract: c, Revision: "next"}, artifact: &deploy.Artifact{Kind: "test", Hash: "next"}, actor: "alice"})
			if err != nil || result.Outcome != Applied {
				t.Fatalf("stopped release: %+v %v", result, err)
			}
			got, err := s.Deployments.ByID(t.Context(), dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			rel, err := s.Releases.ByNumber(t.Context(), dep.ProjectID, cut.Number)
			if err != nil {
				t.Fatal(err)
			}
			if got.RuntimeState != "stopped" || got.ReleaseSHA != "next" || rel.State != Released || len(driver.stopped) != 1 || !driver.stopped[0] {
				t.Fatalf("code/runtime: %+v %+v %+v", got, rel, driver.stopped)
			}
		})
	}
}

type blockingRuntimeDriver struct {
	runtimeDriver
	entered chan struct{}
	proceed chan struct{}
}

func (d *blockingRuntimeDriver) Runtime(ctx context.Context, p deploy.Placement, action string) error {
	close(d.entered)
	<-d.proceed
	return d.runtimeDriver.Runtime(ctx, p, action)
}
func TestStatusRepairCannotSettleAnActWhoseProviderCallIsStillRunning(t *testing.T) {
	s, dep, _, _ := runtimeFixture(t, KindDockerHost)
	driver := &blockingRuntimeDriver{runtimeDriver: runtimeDriver{state: "active"}, entered: make(chan struct{}), proceed: make(chan struct{})}
	s.providers.drivers[KindDockerHost] = func(context.Context, *Target, *Deployment) (deploy.Driver, error) { return driver, nil }
	done := make(chan error, 1)
	go func() { _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "restart"); done <- err }()
	release := sync.OnceFunc(func() { close(driver.proceed) })
	defer release()
	select {
	case <-driver.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime call did not begin")
	}
	current, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkErr := s.checkRuntimeControl(t.Context(), current)
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime call did not settle")
	}
	if !errors.Is(checkErr, errRuntimeBusy) {
		t.Fatalf("active restart was repaired early: %v", checkErr)
	}
}

type vanishedRuntimeDriver struct{ runtimeDriver }

func (d *vanishedRuntimeDriver) Runtime(context.Context, deploy.Placement, string) error {
	panic("executor vanished before the provider effect")
}
func TestInterruptedBeforeProviderEffectCanBeCheckedAndRetried(t *testing.T) {
	s, dep, _, _ := runtimeFixture(t, KindDockerHost)
	vanished := &vanishedRuntimeDriver{runtimeDriver{state: "active"}}
	s.providers.drivers[KindDockerHost] = func(context.Context, *Target, *Deployment) (deploy.Driver, error) { return vanished, nil }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("fault injection did not interrupt the executor")
			}
		}()
		_, _ = s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "stop")
	}()
	current, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.RuntimeError != "" || current.RuntimeState != "stopping" {
		t.Fatalf("did not retain interrupted intent: %+v", current)
	}
	if err := s.checkRuntimeControl(t.Context(), current); err == nil {
		t.Fatal("an app still running confirmed a stop")
	}
	driver := &runtimeDriver{state: "active"}
	s.providers.drivers[KindDockerHost] = func(context.Context, *Target, *Deployment) (deploy.Driver, error) { return driver, nil }
	if got, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "stop"); err != nil || got.RuntimeState != "stopped" {
		t.Fatalf("retry: %+v %v", got, err)
	}
}
func TestFailedReplacementCanBeStoppedButCannotStartMixedReleaseParts(t *testing.T) {
	s, dep, _, _ := runtimeFixture(t, KindDockerHost)
	at, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "failed-replacement"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Fail(t.Context(), at, errors.New("part failed")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "restart"); fault.KindOf(err) != fault.Unprocessable {
		t.Fatal(err)
	}
	if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "start"); fault.KindOf(err) != fault.Unprocessable {
		t.Fatal(err)
	}
}
