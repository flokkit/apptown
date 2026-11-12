package deployment

import (
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/fly/flyfake"
)

const flyWebAndPostgres = "app:\n  web:\n    public: true\n    build: worker\n    ports: ['8080']\nneeds: {postgres: {}}\n"

// flyDeployment is the promotion world with "cloud" released to Fly: a web
// part and a Postgres need, one Machine each.
func flyDeployment(t *testing.T) (p *promotionTargets, app string, web, pg *flyfake.Machine) {
	t.Helper()
	p = newPromotionTargetsWith(t, false, flyWebAndPostgres)
	b := p.land(t)
	if rel, err := p.promote(t, b.ID, "cloud", p.target(t, "fly-syd")); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	apps := p.fly.Apps()
	if len(apps) != 1 {
		t.Fatal(apps)
	}
	app = apps[0]
	for _, m := range p.fly.App(app).Machines {
		switch m.Metadata()["apptown_role"] {
		case "part":
			web = m
		case "need":
			pg = m
		}
	}
	if web == nil || pg == nil {
		t.Fatal("no web or Postgres Machine")
	}
	return p, app, web, pg
}

func diagnosed(t *testing.T, p *promotionTargets) Diagnostics {
	t.Helper()
	d, err := p.s.diagnostics(t.Context(), p.project, "cloud", p.s.diagnoseParts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func diagnosedPart(t *testing.T, d Diagnostics, name string) PartDiagnosis {
	t.Helper()
	for _, p := range d.Parts {
		if p.Part == name {
			return p
		}
	}
	t.Fatalf("no part %q in %+v", name, d.Parts)
	return PartDiagnosis{}
}

// A Fly deployment's parts are diagnosed from the observation sweep's
// Machine list: running, when each started, the web part's generation;
// a Machine whose host Fly cannot reach is unknown, never absent, and an
// observation out of date makes every part unknown, never stopped. Kills:
// the Fly branch, Parts not carried from the observation, staleness or
// reachability ignored.
func TestFlyDeploymentDiagnosesEachPartFromItsObservation(t *testing.T) {
	t.Parallel()
	p, app, web, pg := flyDeployment(t)
	started := time.UnixMilli(1_762_000_000_000).UTC()
	p.fly.AddEvent(app, web.ID, map[string]any{"type": "start", "status": "started", "timestamp": started.UnixMilli()})
	if err := p.s.ObserveRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	d := diagnosed(t, p)
	if d.Unsupported != "" || d.Reading.Unknown {
		t.Fatalf("a Fly deployment's parts read as unsupported or unknown: %q %+v", d.Unsupported, d.Reading)
	}
	w := diagnosedPart(t, d, "web")
	if w.Presence != presenceRunning || w.Facts == nil || w.Facts.ID != web.ID || !w.Facts.StartedAt.Equal(started) || w.Generation == nil || *w.Generation != 1 {
		t.Errorf("web %+v facts %+v", w, w.Facts)
	}
	if n := diagnosedPart(t, d, "postgres"); n.Presence != presenceRunning || n.Facts == nil || n.Facts.ID != pg.ID {
		t.Errorf("postgres %+v", n)
	}

	// A release in flight: generation 2's web Machine stands beside 1's,
	// already killed for memory. The page shows the observed generation.
	next := p.fly.Adopt(app, web)
	p.fly.SetMetadata(app, next, "apptown_generation", "2")
	p.fly.Set(app, next, "stopped", "", "")
	if err := p.s.ObserveRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w := diagnosedPart(t, diagnosed(t, p), "web"); w.Presence != presenceRunning || w.Facts == nil || w.Facts.ID != web.ID {
		t.Errorf("with a new generation beside it, web reads %+v facts %+v, want generation 1's Machine", w, w.Facts)
	}

	// Stopped after an out-of-memory kill; a Machine Fly is still starting
	// is not stopped.
	p.fly.Set(app, web.ID, "stopped", "", "")
	p.fly.AddEvent(app, web.ID, map[string]any{"type": "exit", "status": "stopped", "timestamp": started.UnixMilli() + 1000,
		"request": map[string]any{"exit_event": map[string]any{"exit_code": 137, "oom_killed": true, "requested_stop": false, "signal": -1}}})
	p.fly.Set(app, pg.ID, "starting", "", "")
	if err := p.s.ObserveRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	d = diagnosed(t, p)
	if w := diagnosedPart(t, d, "web"); w.Presence != presenceStopped || w.Facts == nil || !w.Facts.OOMKilled || w.Facts.ExitCode == nil || *w.Facts.ExitCode != 137 {
		t.Errorf("web killed for memory reads %+v facts %+v", w, w.Facts)
	}
	if n := diagnosedPart(t, d, "postgres"); n.Presence != presenceUnknown || n.Reason != "it is starting" {
		t.Errorf("a starting Machine reads %+v, want unknown", n)
	}

	p.fly.Unreachable(app, web.ID, false)
	p.fly.Unreachable(app, pg.ID, true)
	if err := p.s.ObserveRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	d = diagnosed(t, p)
	if w := diagnosedPart(t, d, "web"); w.Presence != presenceUnknown || w.Reason != "its provider cannot reach the host it runs on" {
		t.Errorf("a Machine Fly cannot reach reads %+v, want unknown", w)
	}
	if n := diagnosedPart(t, d, "postgres"); n.Presence != presenceUnknown || n.Reason != unnamedReason {
		t.Errorf("a part beside a Machine Fly cannot reach or name reads %+v, want unknown", n)
	}

	later := p.s.now().Add(observationStaleAfter + time.Second)
	p.s.setClock(func() time.Time { return later })
	d = diagnosed(t, p)
	if w := diagnosedPart(t, d, "web"); w.Presence != presenceUnknown || !w.Stale || !d.Reading.Unknown {
		t.Errorf("an out-of-date observation reads %+v (reading %+v), want unknown", w, d.Reading)
	}
}

// A Fly deployment's use is its Machines' use from Fly's metrics, against
// the size each runs on, through the stored Resources reading. When the
// metrics stop answering, the stored reading says so and no earlier use is
// shown as current. Kills: the Fly branch in observedMemory, the reading's
// failure ignored for a Fly deployment.
func TestFlyDeploymentResourcesReportUse(t *testing.T) {
	t.Parallel()
	p, app, web, pg := flyDeployment(t)
	p.fly.SetCPU(web.ID, 25)
	p.fly.SetMemory(web.ID, 480<<20, 380<<20)
	p.fly.SetCPU(pg.ID, 5)
	p.fly.SetMemory(pg.ID, 240<<20, 200<<20)
	dep, err := p.s.Deployments.ByName(t.Context(), p.project, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.s.observeDetails(t.Context(), dep.ID); err != nil {
		t.Fatal(err)
	}
	r, err := p.s.Resources(t.Context(), p.project, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	use := map[string]PartMemory{}
	for _, part := range r.Parts {
		use[part.Part] = part.Observed
	}
	if w := use["web"]; !w.Known || w.CPUCores != 0.25 || w.UsedBytes != 100<<20 || w.LimitBytes <= 0 || w.CPULimit != 1 || w.Image == "" {
		t.Errorf("web use %+v", w)
	}
	if n := use["postgres"]; !n.Known || n.UsedBytes != 40<<20 || n.LimitBytes != 256<<20 || !n.Need {
		t.Errorf("postgres use %+v", n)
	}
	if r.Reading.Failure != "" {
		t.Errorf("a full reading failed: %q", r.Reading.Failure)
	}

	p.fly.Fail("GET", "/prometheus/acme/api/v1/query", 503, 2)
	_ = p.s.observeDetails(t.Context(), dep.ID)
	r, err = p.s.Resources(t.Context(), p.project, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Reading.Failure, "Fly's metrics did not answer") {
		t.Errorf("a failed metrics read leaves the reading %+v", r.Reading)
	}
	for _, part := range r.Parts {
		if part.Observed.Known || !strings.Contains(part.Observed.Reason, "Fly's metrics did not answer") {
			t.Errorf("%s reads %+v after the metrics stopped answering, want unknown with why", part.Part, part.Observed)
		}
	}

	// A Machine still starting has no known use; one stopped uses nothing.
	p.fly.Set(app, pg.ID, "starting", "", "")
	p.fly.Set(app, web.ID, "stopped", "", "")
	got, err := p.s.observedMemory(t.Context(), dep)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		switch m.Part {
		case "postgres":
			if m.Known || m.Stopped || m.Reason != "it is starting" {
				t.Errorf("a starting Machine's use reads %+v, want unknown", m)
			}
		case "web":
			if !m.Known || !m.Stopped || m.UsedBytes != 0 || m.LimitBytes <= 0 {
				t.Errorf("a stopped Machine's use reads %+v, want known, nothing used", m)
			}
		}
	}

	// A Machine whose host Fly cannot reach has no use now; one Fly cannot
	// name makes any part it did not list unknown, never absent.
	p.fly.Unreachable(app, web.ID, false)
	p.fly.Unreachable(app, pg.ID, true)
	dep, err = p.s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err = p.s.observedMemory(t.Context(), dep)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		want := map[string]string{"web": "its provider cannot reach the host it runs on", "postgres": unnamedReason}[m.Part]
		if m.Known || m.Reason != want {
			t.Errorf("%s reads %+v, want unknown: %s", m.Part, m, want)
		}
	}
}

// Fly's Machine exits become the deployment's limit events: an
// out-of-memory kill and an exit nobody asked for, each once however many
// sweeps see it; a requested stop is none. The source reads as known.
// Kills: the Fly branch in the sweep, requested stops recorded, the source
// still "not reported".
func TestFlyLimitEventsAreRecordedFromMachineEvents(t *testing.T) {
	t.Parallel()
	p, app, web, pg := flyDeployment(t)
	at := p.s.now().Add(-time.Minute).Truncate(time.Millisecond).UTC()
	exit := func(id string, at time.Time, code int, oom, requested bool) {
		p.fly.AddEvent(app, id, map[string]any{"type": "exit", "status": "stopped", "timestamp": at.UnixMilli(),
			"request": map[string]any{"exit_event": map[string]any{"exit_code": code, "oom_killed": oom, "requested_stop": requested, "signal": -1}}})
	}
	// A release's new web Machine (generation 2) killed beside the old.
	next := p.fly.Adopt(app, web)
	p.fly.SetMetadata(app, next, "apptown_generation", "2")
	exit(next, at.Add(3*time.Second), 137, true, false)
	exit(web.ID, at, 137, true, false)
	exit(pg.ID, at.Add(time.Second), 1, false, false)
	exit(web.ID, at.Add(2*time.Second), 0, false, true)
	for range 2 {
		if err := p.s.ObserveRuntime(t.Context()); err != nil {
			t.Fatal(err)
		}
		p.s.RecordLimitEvents(t.Context())
	}
	dep, err := p.s.Deployments.ByName(t.Context(), p.project, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	events, err := p.s.limitEvents(t.Context(), dep, at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events %+v, want two out-of-memory kills and one exit", events)
	}
	oom, exited, newer := events[0], events[1], events[2]
	if newer.Container != next || newer.Kind != LimitOutOfMemory || newer.Generation == nil || *newer.Generation != 2 {
		t.Errorf("the new generation's kill %+v", newer)
	}
	if oom.Part != "web" || oom.Kind != LimitOutOfMemory || oom.Container != web.ID || !oom.At.Equal(at) || oom.Generation == nil || *oom.Generation != 1 {
		t.Errorf("out-of-memory kill %+v", oom)
	}
	if exited.Part != "postgres" || exited.Kind != LimitExited || exited.ExitCode == nil || *exited.ExitCode != 1 || exited.Generation != nil {
		t.Errorf("exit %+v", exited)
	}
	if known, words := p.s.limitEventsSource(t.Context(), dep); !known || !strings.Contains(words, "Fly's Machine events") {
		t.Errorf("source %v %q", known, words)
	}
}
