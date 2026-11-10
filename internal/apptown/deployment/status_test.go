package deployment

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// A running front door never hides an unread need. Every read path consumes
// the same answer, including project health and the target's placement list.
func TestDeploymentAndProjectShareQualifiedStatus(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorldFor(t, multipartApp+"needs:\n  mariadb: {}\n  s3: {}\n")
	w.s.authority = &listingWorld{projects: []ids.ID{w.project}}
	if err := w.s.ObserveRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.read(t)
	w.failContainerListing(context.DeadlineExceeded)
	key, err := w.s.DetailObservationKey(t.Context(), w.main.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.s.RunDetailObservation(t.Context(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.s.readHostContainersForTest(t, w.main.TargetID)
	rows, err := w.s.List(t.Context(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	want := rows[0].Observation.Status
	if want.Word != progressLabelUnknown || !strings.Contains(want.Reason, "container listing") || !strings.Contains(want.Reason, "the host reading timed out") || want.CheckedAt.IsZero() || !want.RetryAt.Equal(w.at.Add(hostReadInterval)) {
		t.Fatalf("qualified answer: %+v", want)
	}
	projects, err := w.s.ProjectRuntime(t.Context(), []ids.ID{w.project})
	if err != nil {
		t.Fatal(err)
	}
	got := projects[w.project].Status
	if got.Word != want.Word || got.CheckedAt != want.CheckedAt || got.Reason != "main: "+want.Reason {
		t.Fatalf("project disagrees: %+v / %+v", got, want)
	}
	refreshed, err := w.s.refreshRuntime(t.Context(), w.main)
	if err != nil || refreshed.Status != want {
		t.Fatalf("refresh disagrees: %+v %v", refreshed.Status, err)
	}
	target, err := w.s.Status(t.Context(), w.main.ID)
	if err != nil || target != want {
		t.Fatalf("target disagrees: %+v %v", target, err)
	}
	fleet, err := w.s.Runtime(t.Context())
	if err != nil || len(fleet) != 1 || fleet[0].Observation.Status != want || fleet[0].ProjectStatus != got {
		t.Fatalf("Home/fleet disagree: %+v %v", fleet, err)
	}
	// The ordinary host sweep recovers every view without a detail job.
	w.daemon.SetReply(nil)
	w.s.readHostContainersForTest(t, w.main.TargetID)
	answer, err := w.s.Status(t.Context(), w.main.ID)
	if err != nil || answer.Word != progressLabelWorking || !answer.RetryAt.IsZero() {
		t.Fatalf("recovered answer: %+v %v", answer, err)
	}
}

func TestDeploymentAnswerQualifiesPartsAndIntent(t *testing.T) {
	now := time.Now().UTC()
	dep := Deployment{Container: "web", ObservedGeneration: 1}
	o := RuntimeObservation{State: RuntimeRunning, ObservedAt: now, AttemptedAt: now}
	good := Diagnostics{Reading: DetailReading{ObservedAt: now, AttemptedAt: now}, Parts: []PartDiagnosis{{Part: "web", Presence: presenceRunning}}}
	for _, test := range []struct {
		name    string
		change  func(*Deployment, *RuntimeObservation, *Diagnostics)
		release bool
		word    ProgressLabel
	}{
		{"working", func(*Deployment, *RuntimeObservation, *Diagnostics) {}, false, progressLabelWorking},
		{"old parts", func(_ *Deployment, _ *RuntimeObservation, d *Diagnostics) { d.Reading.Stale = true }, false, progressLabelUnknown},
		{"unreachable", func(_ *Deployment, o *RuntimeObservation, _ *Diagnostics) { o.State = runtimeUnreachable }, false, progressLabelUnreachable},
		{"old runtime", func(_ *Deployment, o *RuntimeObservation, _ *Diagnostics) { o.Stale = true }, false, progressLabelUnknown},
		{"missing parts", func(_ *Deployment, _ *RuntimeObservation, d *Diagnostics) { d.Parts = nil }, false, progressLabelUnknown},
		{"absent part", func(_ *Deployment, _ *RuntimeObservation, d *Diagnostics) {
			d.Parts = []PartDiagnosis{{Part: "storage", Presence: presenceAbsent}}
		}, false, progressLabelFailed},
		{"old stopped part", func(_ *Deployment, _ *RuntimeObservation, d *Diagnostics) {
			d.Reading.Stale = true
			d.Parts = []PartDiagnosis{{Part: "storage", Presence: presenceStopped}}
		}, false, progressLabelUnknown},
		{"stopped part", func(_ *Deployment, _ *RuntimeObservation, d *Diagnostics) {
			d.Parts = []PartDiagnosis{{Part: "storage", Presence: presenceStopped}}
		}, false, progressLabelFailed},
		{"unhealthy need", func(_ *Deployment, _ *RuntimeObservation, d *Diagnostics) {
			d.Parts = []PartDiagnosis{{Part: "storage", Presence: presenceRunning, Facts: &ContainerFacts{Health: "unhealthy"}}}
		}, false, progressLabelFailed},
		{"releasing", func(*Deployment, *RuntimeObservation, *Diagnostics) {}, true, progressLabelDeploying},
		{"starting", func(d *Deployment, _ *RuntimeObservation, _ *Diagnostics) { d.RuntimeState = "starting" }, false, progressLabelDeploying},
		{"stopping", func(d *Deployment, _ *RuntimeObservation, _ *Diagnostics) { d.RuntimeState = "stopping" }, false, progressLabelDeploying},
		{"restarting", func(d *Deployment, _ *RuntimeObservation, _ *Diagnostics) { d.RuntimeState = "restarting" }, false, progressLabelDeploying},
		{"open release wins", func(d *Deployment, _ *RuntimeObservation, _ *Diagnostics) {
			d.RuntimeState = "stopped"
			d.RuntimeActor = "Sam"
		}, true, progressLabelDeploying},
		{"bucket", func(d *Deployment, _ *RuntimeObservation, v *Diagnostics) { d.Container = ""; v.Parts = nil }, false, progressLabelWorking},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, o, v := dep, o, good
			test.change(&d, &o, &v)
			var releasing *releaseProgress
			if test.release {
				releasing = &releaseProgress{Number: 7, Stage: "starting"}
			}
			if a := deploymentAnswer(&d, o, v, releasing, nil); a.Word != test.word {
				t.Fatalf("%+v", a)
			}
		})
	}
}

// N deployments with M parts use one host listing, never per-part inspections.
func TestHostSweepSuppliesAllDeploymentPartsWithOneListing(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	for _, name := range []string{"second", "third"} {
		dep := w.deployment(t, w.s, name, w.remote)
		w.land(t, w.s, dep, ids.New(w.at))
	}
	w.daemon.ResetCommands()
	// The lap begins after the landings settled; one begun at the same
	// instant could not witness them.
	w.advance(time.Second)
	reader := HostReaders{Snapshots: NewHostSnapshots(w.s.db, w.s.now), Targets: w.s.Targets, Hosts: w.s.hosts, Providers: w.s.providers}
	if err := reader.lap(t.Context(), w.main.TargetID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main", "second", "third"} {
		d, err := w.s.Diagnostics(t.Context(), w.project, name)
		if err != nil || len(d.Parts) < 3 {
			t.Fatalf("%s: %+v %v", name, d, err)
		}
		for _, p := range d.Parts {
			if p.Presence != presenceRunning {
				t.Fatalf("%s: %+v", name, p)
			}
		}
	}
	listings := 0
	for _, args := range w.daemon.Commands() {
		if args[0] == "ps" {
			listings++
		}
		if args[0] == "inspect" && (len(args) < 2 || args[1] != "--type") {
			t.Fatalf("per-part inspection: %v", args)
		}
	}
	if listings != 1 {
		t.Fatalf("host sweep made %d listings, want 1", listings)
	}
}

type statusInspectClient func(context.Context, string, docker.Command) ([]byte, error)

func (f statusInspectClient) Run(ctx context.Context, host string, cmd docker.Command) ([]byte, error) {
	return f(ctx, host, cmd)
}

func TestUnreachableStatusNamesAnUnnamedHost(t *testing.T) {
	a := deploymentAnswer(&Deployment{}, RuntimeObservation{State: runtimeUnreachable}, Diagnostics{}, nil, nil)
	if a.Reason != "cannot reach the deployment host; the app may still be running" {
		t.Fatalf("%+v", a)
	}
}

// Runtime readers agree at the freshness boundary, including the observation
// used for release serving, change evidence and MCP's container answer.
func TestStaleRuntimeIsUnknownToEveryReader(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	w.s.authority = &listingWorld{projects: []ids.ID{w.project}}
	if err := w.s.ObserveRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.advance(50 * time.Second)
	container := w.s.ObserveContainer(t.Context(), w.main)
	rows, err := w.s.List(t.Context(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := w.s.ProjectRuntime(t.Context(), []ids.ID{w.project})
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := w.s.Runtime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if container.State != ContainerUnknown || !container.Stale || len(rows) != 1 || rows[0].Observation.Running() != nil || projects[w.project].ProductionRunning != nil || len(fleet) != 1 || fleet[0].Observation.Running() != nil {
		t.Fatalf("container %+v / listing %+v / projects %+v / fleet %+v", container, rows, projects, fleet)
	}
	// Even a detached stale sample cannot bypass the owner's decision.
	if (RuntimeObservation{State: RuntimeRunning, Stale: true}).Running() != nil {
		t.Fatal("stale sample claims running")
	}
}

// A completed apply waits for its first reading, without claiming health. The first matching
// read replaces it, including a failed read; an old generation cannot do so.
func TestAppliedReleaseWaitsForFirstReading(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	dep := Deployment{Container: "web", ObservedGeneration: 16, LastApplied: &AppliedAttempt{At: at, Generation: 16}}
	release := &Release{Number: 18, Generation: 16}
	for _, o := range []RuntimeObservation{{State: RuntimeUnknown, Stale: true}, {State: RuntimeRunning, ObservedAt: at.Add(-time.Second), AttemptedAt: at.Add(-time.Second), Stale: true}} {
		a := deploymentAnswer(&dep, o, Diagnostics{}, nil, release)
		if a.Word != progressLabelWaiting || a.Reason != "release 18 live — first check pending" || !a.AppliedAt.Equal(at) || !a.CheckedAt.IsZero() || a.Stale || a.ServingRelease != "Serving release 18" {
			t.Fatalf("after apply: %+v", a)
		}
	}
	good := Diagnostics{Reading: DetailReading{ObservedAt: at, AttemptedAt: at}, Parts: []PartDiagnosis{{Part: "web", Presence: presenceRunning}}}
	o := RuntimeObservation{State: RuntimeRunning, ObservedAt: at, AttemptedAt: at}
	if a := deploymentAnswer(&dep, o, good, nil, release); a.Word != progressLabelWorking || !a.AppliedAt.IsZero() || a.ServingRelease != "Running release 18" {
		t.Fatalf("first read: %+v", a)
	}
	good.Parts[0].Presence = presenceAbsent
	if a := deploymentAnswer(&dep, o, good, nil, release); a.Word != progressLabelFailed {
		t.Fatalf("failed first read: %+v", a)
	}
	o = RuntimeObservation{State: RuntimeUnknown, AttemptedAt: at, Err: context.DeadlineExceeded, Stale: true}
	if a := deploymentAnswer(&dep, o, Diagnostics{}, nil, release); a.Word == progressLabelDeploying || !a.AppliedAt.IsZero() {
		t.Fatalf("failed check still starting: %+v", a)
	}
}

func TestDeploymentReasonsDescribeOneReading(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	for _, o := range []RuntimeObservation{
		{State: RuntimeUnknown, Stale: true},
		{State: RuntimeUnknown, Stale: true, ObservedAt: at, AttemptedAt: at},
		{State: RuntimeUnknown, Stale: true, ObservedAt: at, AttemptedAt: at, Err: context.DeadlineExceeded},
	} {
		a := deploymentAnswer(&Deployment{}, o, Diagnostics{}, nil, nil)
		if strings.Count(a.Reason, "not checked yet") > 1 || strings.Count(a.Reason, "old reading") > 1 || (strings.Contains(a.Reason, "not checked yet") && strings.Contains(a.Reason, "old reading")) {
			t.Fatalf("contradictory reason: %+v", a)
		}
		if o.ObservedAt.IsZero() && (a.Stale || a.Reason != "not checked yet") {
			t.Fatalf("never read: %+v", a)
		}
		if !o.ObservedAt.IsZero() && !strings.Contains(a.Reason, "old reading") {
			t.Fatalf("old read: %+v", a)
		}
	}
}

func TestAppliedStatusIsSharedBeforeObservation(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	rel, err := w.promote(t, b.ID, "production", w.lab2)
	if err != nil {
		t.Fatal(err)
	}
	w.s.authority = &listingWorld{projects: []ids.ID{w.project}}
	rows, err := w.s.List(t.Context(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	var want DeploymentStatus
	for _, row := range rows {
		if row.ID == rel.DeploymentID {
			want = row.Observation.Status
			if row.ServingRelease == nil || row.ServingRelease.ID != rel.ID || row.ServingRelease.Generation != row.Generation || row.LastApplied == nil || !want.AppliedAt.Equal(row.LastApplied.At) {
				t.Fatalf("applied attribution: %+v", row)
			}
		}
	}
	w.s.setClock(func() time.Time { return time.Now().UTC().Add(24 * time.Hour) })
	later, err := w.s.Status(t.Context(), rel.DeploymentID)
	if err != nil || later != want {
		t.Fatalf("clock ended first-check wait: %+v %v", later, err)
	}
	if want.Word != progressLabelWaiting || want.Reason != "release 1 live — first check pending" {
		t.Fatalf("pending answer: %+v", want)
	}
	target, err := w.s.Status(t.Context(), rel.DeploymentID)
	if err != nil || target != want {
		t.Fatalf("target: %+v %v", target, err)
	}
	projects, err := w.s.ProjectRuntime(t.Context(), []ids.ID{w.project})
	if err != nil || projects[w.project].Status.Word != want.Word || projects[w.project].Status.Reason != "production: "+want.Reason || projects[w.project].Status.AppliedAt != want.AppliedAt {
		t.Fatalf("project: %+v %v", projects, err)
	}
	fleet, err := w.s.Runtime(t.Context())
	if err != nil || len(fleet) != 1 || fleet[0].Observation.Status != want {
		t.Fatalf("fleet: %+v %v", fleet, err)
	}
}

// The release ledger, rather than runtime readings or elapsed time, owns the
// answer throughout an attempt. A failure retains its recorded reason.
func TestOpenReleaseStatusIsItsRecordedStage(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	rel, err := w.s.Releases.Start(t.Context(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: w.project, DeploymentID: w.main.ID, Deployment: w.main.Name, Revision: "selected-revision", Actor: "sam"}, "starting")
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := w.s.Outcomes.Admit(t.Context(), Start{DeploymentID: w.main.ID, TargetID: w.main.TargetID, Key: "status-release", Inputs: Inputs{ReleaseID: rel.ID, Revision: "selected-revision"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []deploy.DeployStage{deploy.Starting, deploy.Checking} {
		if err := w.s.recordReleaseStage(t.Context(), rel.ID, stage); err != nil {
			t.Fatal(err)
		}
		first, err := w.s.Status(t.Context(), w.main.ID)
		if err != nil {
			t.Fatal(err)
		}
		// The release on its way is not the one served.
		if first.Word != progressLabelDeploying || first.Reason != fmt.Sprintf("release %d %s", rel.Number, stage) || strings.Contains(first.ServingRelease, fmt.Sprint(rel.Number)) || first.ServingReleaseNumber == rel.Number || first.Stale || !first.CheckedAt.IsZero() {
			t.Fatalf("stage: %+v", first)
		}
		w.advance(time.Hour)
		later, err := w.s.Status(t.Context(), w.main.ID)
		if err != nil || later != first {
			t.Fatalf("clock changed attempt: %+v / %+v: %v", first, later, err)
		}
	}
	if _, err := w.s.Outcomes.Fail(t.Context(), attempt, deploy.Terminal(errors.New("readiness check failed"))); err != nil {
		t.Fatal(err)
	}
	failed, err := w.s.Status(t.Context(), w.main.ID)
	if err != nil || failed.Word != progressLabelFailed || strings.Count(failed.Reason, "readiness check failed") != 1 {
		t.Fatalf("attempt failure: %+v %v", failed, err)
	}
}

// A deploy names the release the deployment serves, never the one still on
// its way; with nothing applied there is no serving release to name.
func TestReleasingAnswerNamesTheServedRelease(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	dep := Deployment{Container: "web", ObservedGeneration: 16}
	running := RuntimeObservation{State: RuntimeRunning, ObservedAt: at, AttemptedAt: at}
	a := deploymentAnswer(&dep, running, Diagnostics{}, &releaseProgress{Number: 19, Stage: "building"}, &Release{Number: 18, Generation: 16})
	if a.Word != progressLabelDeploying || a.Reason != "release 19 building" || a.ServingRelease != "Running release 18" || a.ServingReleaseNumber != 18 {
		t.Fatalf("redeploy: %+v", a)
	}
	absent := RuntimeObservation{State: RuntimeStopped, ObservedAt: at, AttemptedAt: at}
	a = deploymentAnswer(&Deployment{}, absent, Diagnostics{}, &releaseProgress{Number: 1, Stage: "building"}, nil)
	if a.ServingRelease != "No serving release recorded" || a.ServingReleaseNumber != 0 {
		t.Fatalf("first deploy: %+v", a)
	}
}

// A failed first deploy stays failed after the next runtime reading: an
// absent runtime is all there is, and nothing older runs in its place.
func TestFailedFirstDeployStaysFailedAfterAReading(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	first := w.deployment(t, w.s, "first", w.remote)
	rel, err := w.s.Releases.Start(t.Context(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: w.project, DeploymentID: first.ID, Deployment: first.Name, Revision: "selected-revision", Actor: "sam"}, "starting")
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := w.s.Outcomes.Admit(t.Context(), Start{DeploymentID: first.ID, TargetID: first.TargetID, Key: "first-release", Inputs: Inputs{ReleaseID: rel.ID, Revision: "selected-revision"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Outcomes.Fail(t.Context(), attempt, deploy.Terminal(errors.New("the app container exited during startup"))); err != nil {
		t.Fatal(err)
	}
	w.advance(time.Minute)
	if err := w.s.ObserveRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := w.s.Status(t.Context(), first.ID)
	if err != nil || got.Word != progressLabelFailed || !strings.Contains(got.Reason, "exited during startup") || got.ServingReleaseNumber != 0 {
		t.Fatalf("failed first deploy after a reading: %+v %v", got, err)
	}
}

// While a release is on its way the listing refuses a second deploy and the
// runtime actions with admission's own reason; once it ends they are offered.
func TestListingRefusesControlsWhileAReleaseIsInFlight(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	controls := func() map[string]string {
		t.Helper()
		w.s.authority = &listingWorld{projects: []ids.ID{w.project}}
		rows, err := w.s.List(t.Context(), w.project)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == w.main.ID {
				return row.RuntimeControls
			}
		}
		t.Fatal("main is not listed")
		return nil
	}
	if got := controls(); got["deploy"] != "" || got["stop"] != "" {
		t.Fatalf("idle controls: %v", got)
	}
	rel, err := w.s.Releases.Start(t.Context(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: w.project, DeploymentID: w.main.ID, Deployment: w.main.Name, Revision: "selected-revision", Actor: "sam"}, "starting")
	if err != nil {
		t.Fatal(err)
	}
	got := controls()
	for _, action := range []string{"deploy", "start", "stop", "restart"} {
		if got[action] != errReleaseInFlight.Error() {
			t.Fatalf("%s while release %d is in flight: %v", action, rel.Number, got)
		}
	}
	attempt, _, err := w.s.Outcomes.Admit(t.Context(), Start{DeploymentID: w.main.ID, TargetID: w.main.TargetID, Key: "controls-release", Inputs: Inputs{ReleaseID: rel.ID, Revision: "selected-revision"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Outcomes.Fail(t.Context(), attempt, deploy.Terminal(errors.New("readiness check failed"))); err != nil {
		t.Fatal(err)
	}
	if got := controls(); got["deploy"] != "" {
		t.Fatalf("after the release ended: %v", got)
	}
}

// An older release on a deployment is replaced by the later one released
// there; the newest is replaced by nothing.
func TestAnOlderReleaseNamesTheReleaseThatReplacedIt(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	first, err := w.promote(t, b.ID, "production", w.lab2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.promote(t, b.ID, "production", w.lab2)
	if err != nil {
		t.Fatal(err)
	}
	first, err = w.s.Releases.ByID(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if by, err := w.s.Releases.ReplacedBy(t.Context(), first); err != nil || by != second.Number {
		t.Fatalf("release %d replaced by %d (%v), want %d", first.Number, by, err, second.Number)
	}
	if by, err := w.s.Releases.ReplacedBy(t.Context(), second); err != nil || by != 0 {
		t.Fatalf("newest release replaced by %d (%v)", by, err)
	}
}

// The built-in host records no provider, yet is shown under the stock
// Docker deploy connector, which the seeds define as a deploy connector; a
// remote target is shown under its provider.
func TestTheBuiltInHostIsShownUnderASeededDeployConnector(t *testing.T) {
	t.Parallel()
	builtIn := &Target{Origin: OriginBuiltIn, Kind: KindDockerHost}
	found := false
	for _, s := range integration.Seeds(integration.SeedConfig{}) {
		found = found || s.Name == builtIn.Connector() && s.Capability == integration.CapDeploy
	}
	if !found {
		t.Fatalf("the built-in host's connector %q is not a seeded deploy connector", builtIn.Connector())
	}
	if got := (&Target{Provider: "portainer-deploy"}).Connector(); got != "portainer-deploy" {
		t.Fatalf("remote target's connector: %q", got)
	}
}
