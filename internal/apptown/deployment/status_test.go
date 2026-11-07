package deployment

import (
	"context"
	"strings"
	"testing"
	"time"

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
	if want.Word != progressLabelUnknown || !strings.Contains(want.Reason, "container listing") || !strings.Contains(want.Reason, "did not answer in time") || want.CheckedAt.IsZero() || !want.RetryAt.Equal(w.at.Add(hostReadInterval)) {
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
		{"explicit stop wins", func(d *Deployment, _ *RuntimeObservation, _ *Diagnostics) {
			d.RuntimeState = "stopped"
			d.RuntimeActor = "Sam"
		}, true, progressLabelStopped},
		{"bucket", func(d *Deployment, _ *RuntimeObservation, v *Diagnostics) { d.Container = ""; v.Parts = nil }, false, progressLabelWorking},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, o, v := dep, o, good
			test.change(&d, &o, &v)
			var releasing *releaseProgress
			if test.release {
				releasing = &releaseProgress{Number: 7, At: now}
			}
			if a := deploymentAnswer(&d, o, v, releasing); a.Word != test.word {
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
	a := deploymentAnswer(&Deployment{}, RuntimeObservation{State: runtimeUnreachable}, Diagnostics{}, nil)
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
