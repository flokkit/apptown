package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// diagnosticsWorld is a multipart app landed on an attached Docker host,
// read through a clock the test moves.
type diagnosticsWorld struct {
	*noLocalWorld
	s      *Service
	main   *Deployment
	worker string // the worker part's container
	mu     sync.Mutex
	at     time.Time
}

func newDiagnosticsWorld(t *testing.T) *diagnosticsWorld {
	t.Helper()
	w := &diagnosticsWorld{noLocalWorld: noLocalDockerWorld(t, multipartApp)}
	w.s = w.service(true)
	dep := w.deployment(t, w.s, "main", w.remote)
	w.land(t, w.s, dep, idstest.N(7))
	w.main = w.reread(t)
	w.worker = hostNamesOf(t, w.s, w.project, "game", "main").Service("worker")
	w.at = time.Now().UTC().Truncate(time.Microsecond)
	w.s.clock = func() time.Time { w.mu.Lock(); defer w.mu.Unlock(); return w.at }
	w.s.authority = &requestAuthority{}
	return w
}

func (w *diagnosticsWorld) reread(t *testing.T) *Deployment {
	t.Helper()
	d, err := w.s.Deployments.ByName(t.Context(), w.project, "main")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (w *diagnosticsWorld) advance(d time.Duration) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.at = w.at.Add(d)
	return w.at
}

func (w *diagnosticsWorld) read(t *testing.T) Diagnostics {
	t.Helper()
	d, err := w.s.Diagnostics(t.Context(), w.project, "main")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func partNamed(t *testing.T, d Diagnostics, name string) PartDiagnosis {
	t.Helper()
	for _, p := range d.Parts {
		if p.Part == name {
			return p
		}
	}
	t.Fatalf("no part %q in %+v", name, d.Parts)
	return PartDiagnosis{}
}

// failInspect makes the daemon fail to answer an inspection of one
// container, as a host that times out does, with words that name its
// address.
func (w *diagnosticsWorld) failInspect(container string, err error) {
	w.daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) == 4 && args[0] == "inspect" && args[2] == docker.RuntimeFormat && args[3] == container {
			return []byte("error during connect: ssh://deploy:hunter2@10.0.0.7"), err, true
		}
		return nil, nil, false
	})
}

// A background part killed out of memory is visible, with its exit, its
// restarts and its limit event, while the public part runs: each part is
// read from its own container of the settled generation. A running
// container shows no exit code, a container with no health check is "not
// configured", not healthy, and a stopped part is not called held.
//
// Kills: the inspect format without the process state; an exit code
// given for a running container; health check absence read as healthy;
// limit events stored without their generation.
func TestDiagnosticsShowAPartsFailureWhileThePublicPartRuns(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	w.daemon.SetRunning(w.worker, false)
	w.daemon.SetExitCode(w.worker, 137)
	w.daemon.SetOOMKilled(w.worker, true)
	w.daemon.SetRestartCount(w.worker, 3)
	gen := w.main.ObservedGeneration
	if err := w.s.recordLimitEvent(t.Context(), w.main.ID, LimitEvent{Part: "worker", Kind: LimitOutOfMemory, Container: w.worker, At: w.at.Add(-time.Minute), Generation: &gen}); err != nil {
		t.Fatal(err)
	}

	d := w.read(t)
	if d.Generation != gen || d.Desired != gen || d.Commit != w.revision || !d.Containers || d.Unsupported != "" || d.Unsettled != "" {
		t.Fatalf("the generation the facts are about: %+v", d)
	}
	web := partNamed(t, d, "web")
	if !web.Public || web.Presence != PresenceRunning || web.Stale || web.Facts == nil || !web.Facts.Running || web.Facts.ExitCode != nil || web.Facts.Health != "not configured" || !web.ObservedAt.Equal(w.at) {
		t.Fatalf("the public part runs: %+v %+v", web, web.Facts)
	}
	worker := partNamed(t, d, "worker")
	f := worker.Facts
	if web.Generation == nil || *web.Generation != gen || worker.Generation == nil || *worker.Generation != gen {
		t.Fatalf("a settled deployment's parts are its generation's: %v %v", web.Generation, worker.Generation)
	}
	if worker.Presence != PresenceStopped || worker.Stale || f == nil || f.Running || f.ExitCode == nil || *f.ExitCode != 137 || !f.OOMKilled || f.Restarts != 3 {
		t.Fatalf("the worker's out-of-memory kill: %+v %+v", worker, f)
	}
	if len(d.Events) != 1 || d.Events[0].Part != "worker" || d.Events[0].Generation == nil || *d.Events[0].Generation != gen || !d.EventsKnown {
		t.Fatalf("its limit event and generation: %+v", d.Events)
	}
	if d.Hold != nil {
		t.Fatalf("a stopped part read as held: %+v", d.Hold)
	}
	if d.LogMaxLines != 200 || d.LogMaxBytes != 64<<10 {
		t.Fatalf("the log bounds: %d %d", d.LogMaxLines, d.LogMaxBytes)
	}
}

// A read that fails is unknown now, never stopped, absent or a clean
// exit: the part keeps the facts last read for this generation's
// container, with when they were read and when the failed read was tried,
// and the failure in AppTown's words, not the host's. With nothing read
// before, there are no facts at all. A container under the public part's
// name that another generation's labels name is shown as that generation's.
//
// Kills: a failed read kept as the old presence; old facts shown as
// current (not stale, or with the failed read's time); the host's raw
// words in the reason; a container of another generation named as the
// settled one's.
func TestAFailedDiagnosticReadKeepsAgedFactsAndIsNeverStopped(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	w.daemon.SetRunning(w.worker, false)
	w.daemon.SetExitCode(w.worker, 137)
	w.daemon.SetOOMKilled(w.worker, true)
	first := w.at
	if p := partNamed(t, w.read(t), "worker"); p.Presence != PresenceStopped {
		t.Fatalf("the first read: %+v", p)
	}

	tried := w.advance(2 * time.Minute)
	w.failInspect(w.worker, context.DeadlineExceeded)
	p := partNamed(t, w.read(t), "worker")
	if p.Presence != PresenceUnknown || !p.Stale || !strings.Contains(p.Reason, "did not answer in time") {
		t.Fatalf("a timed-out read: %+v", p)
	}
	if p.Facts == nil || p.Facts.ExitCode == nil || *p.Facts.ExitCode != 137 || !p.Facts.OOMKilled || !p.ObservedAt.Equal(first) || !p.AttemptedAt.Equal(tried) {
		t.Fatalf("the last facts and their age: %+v %+v", p, p.Facts)
	}
	if strings.Contains(p.Reason, "hunter2") || strings.Contains(p.Reason, "10.0.0.7") {
		t.Fatalf("the host's words reached the reason: %q", p.Reason)
	}
	// A failure whose own words name the host's address and login.
	w.failInspect(w.worker, errors.New("error during connect: ssh://deploy:hunter2@10.0.0.7: permission denied"))
	if p := partNamed(t, w.read(t), "worker"); p.Presence != PresenceUnknown || strings.Contains(p.Reason, "hunter2") || strings.Contains(p.Reason, "10.0.0.7") {
		t.Fatalf("the host's words reached the reason: %+v", p)
	}

	// Another AppTown process (a restart) has read nothing: no facts.
	fresh := w.service(true)
	fresh.authority = &requestAuthority{}
	d, err := fresh.Diagnostics(t.Context(), w.project, "main")
	if err != nil {
		t.Fatal(err)
	}
	if p := partNamed(t, d, "worker"); p.Presence != PresenceUnknown || p.Facts != nil || !p.Stale || !p.ObservedAt.IsZero() {
		t.Fatalf("a failed first read: %+v", p)
	}
	w.daemon.SetReply(nil)

	// The public part's name holding another generation's container.
	w.daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) == 4 && args[0] == "inspect" && args[2] == docker.RuntimeFormat && args[3] == w.main.Container {
			out, err := json.Marshal(docker.Runtime{ID: "old", Running: true, Labels: map[string]string{dockerlabel.Deployment: string(w.main.Label()), dockerlabel.Generation: "99"}, State: &docker.ContainerState{Status: "running"}})
			return out, err, true
		}
		return nil, nil, false
	})
	if p := partNamed(t, fresh.mustDiagnose(t, w.project), "web"); p.Presence != PresenceRunning || p.Generation == nil || *p.Generation != 99 || p.Facts == nil || p.Facts.ID != "old" {
		t.Fatalf("another generation's container: %+v", p)
	}
}

func (s *Service) mustDiagnose(t *testing.T, project ids.ID) Diagnostics {
	t.Helper()
	d, err := s.Diagnostics(t.Context(), project, "main")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A new generation replaces the old one's facts: once the deployment
// settles on another generation, a failed read shows nothing read of the
// one before, and the view names the new generation.
//
// Kills: keeping part facts across generations.
func TestANewGenerationDropsTheOldOnesFacts(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	if p := partNamed(t, w.read(t), "worker"); p.Facts == nil {
		t.Fatalf("the first read: %+v", p)
	}
	w.land(t, w.s, w.main, idstest.N(8))
	next := w.reread(t)
	if next.ObservedGeneration == w.main.ObservedGeneration {
		t.Fatalf("the second landing settled no new generation: %d", next.ObservedGeneration)
	}
	w.failInspect(w.worker, errors.New("exit status 1"))
	d := w.read(t)
	if p := partNamed(t, d, "worker"); d.Generation != next.ObservedGeneration || p.Presence != PresenceUnknown || p.Facts != nil {
		t.Fatalf("the old generation's facts survived: generation %d %+v", d.Generation, p)
	}
}

// A crash-loop hold on the settled generation is shown from its receipt,
// a stop that was only attempted apart from one that was confirmed; a hold
// on another generation is not this one's.
//
// Kills: reading every hold as confirmed; a hold of another generation
// shown on this one.
func TestACrashLoopHoldIsShownAsItsReceiptSaysIt(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	held := func(generation int64, ok bool, detail string) {
		w.advance(time.Second)
		w.s.recordHealth(t.Context(), docker.Watched{DeploymentID: w.main.ID, ProjectID: w.project, Identity: deploy.DockerIdentity{Generation: generation}, Slug: "game", Name: "main"},
			"deployment.crash-loop-held", map[string]any{"ok": ok, "detail": detail, "restarts": 5, "window": "5m0s", "exitCode": "1", "container": w.main.Container})
	}
	gen := w.main.ObservedGeneration
	held(gen, false, "stop timed out")
	d := w.read(t)
	if d.Hold == nil || d.Hold.Confirmed || d.Hold.Detail != "stop timed out" || d.Hold.Restarts != 5 || d.Hold.Receipt.IsZero() {
		t.Fatalf("an attempted hold: %+v", d.Hold)
	}
	held(gen, true, "")
	if d := w.read(t); d.Hold == nil || !d.Hold.Confirmed {
		t.Fatalf("a confirmed hold: %+v", d.Hold)
	}
	held(gen+1, true, "")
	if d := w.read(t); d.Hold == nil || !d.Hold.Confirmed || d.Hold.Receipt.IsZero() {
		t.Fatalf("this generation's hold: %+v", d.Hold)
	}
	other := newDiagnosticsWorld(t)
	other.s.recordHealth(t.Context(), docker.Watched{DeploymentID: other.main.ID, ProjectID: other.project, Identity: deploy.DockerIdentity{Generation: other.main.ObservedGeneration + 1}, Slug: "game", Name: "main"},
		"deployment.crash-loop-held", map[string]any{"ok": true})
	if d := other.read(t); d.Hold != nil {
		t.Fatalf("another generation's hold: %+v", d.Hold)
	}
}

// A target that runs no containers for AppTown says so, and lists the
// generation's parts as unknown: it never shows Docker facts it did not
// read.
func TestDiagnosticsOnATargetWithoutContainersSayWhy(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	platform := *w.main
	platform.Container = ""
	var d Diagnostics
	if err := w.s.diagnoseParts(t.Context(), &platform, &d); err != nil {
		t.Fatal(err)
	}
	if d.Containers || !strings.Contains(d.Unsupported, "does not report each part's container") || len(d.Parts) == 0 {
		t.Fatalf("a container-less deployment: %+v", d)
	}
	for _, p := range d.Parts {
		if p.Presence != PresenceUnknown || p.Facts != nil {
			t.Fatalf("an unreported part: %+v", p)
		}
	}
}

// A part's log is read only by someone who may operate the deployment,
// only for a part or need of its settled generation, from the container
// AppTown names for it there; the bounds come back with it. A refused or
// unknown request never reaches the daemon.
//
// Kills: the read grant taken for logs; a part name used as a container
// name; a part outside the generation reaching Docker.
func TestALogIsReadOnlyForTheRunningGenerationsParts(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	var asked [][]string
	w.daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "logs" && !w.daemon.IsDiskRead(args) {
			asked = append(asked, args)
			return []byte("2026-10-16T01:00:00Z worker: out of memory\n"), nil, true
		}
		// The container under the worker's name, by its own ID.
		if len(args) == 4 && args[0] == "inspect" && args[2] == docker.RuntimeFormat && args[3] == w.worker {
			out, err := json.Marshal(docker.Runtime{ID: "c0ffee-worker", Running: true, State: &docker.ContainerState{Status: "running"}})
			return out, err, true
		}
		return nil, nil, false
	})
	authority := &requestAuthority{deny: fault.New(fault.Forbidden, "no")}
	w.s.authority = authority
	if _, err := w.s.RecentLog(t.Context(), w.project, "main", "worker"); fault.KindOf(err) != fault.Forbidden {
		t.Fatalf("a refused read: %v", err)
	}
	if len(authority.seen) != 1 || authority.seen[0].Capability != access.CapDeploymentOperate {
		t.Fatalf("the grant asked for: %+v", authority.seen)
	}
	w.s.authority = &requestAuthority{}
	for _, part := range []string{"", "web-other", w.worker, "../worker", "migrate"} {
		if _, err := w.s.RecentLog(t.Context(), w.project, "main", part); fault.KindOf(err) != fault.NotFound {
			t.Fatalf("part %q: %v", part, err)
		}
	}
	if len(asked) != 0 {
		t.Fatalf("a refused or unknown part reached the daemon: %v", asked)
	}
	got, err := w.s.RecentLog(t.Context(), w.project, "main", "worker")
	if err != nil || got.Text != "2026-10-16T01:00:00Z worker: out of memory" || got.Generation == nil || *got.Generation != w.main.ObservedGeneration || got.Container != "c0ffee-worker" || got.MaxLines != 200 || got.MaxBytes != 64<<10 || !got.ReadAt.Equal(w.at) {
		t.Fatalf("the worker's log: %+v %v", got, err)
	}
	if len(asked) != 1 || asked[0][len(asked[0])-1] != "c0ffee-worker" {
		t.Fatalf("the container asked: %v", asked)
	}
}

// An event is the generation's whose container its labels name, or else
// the settled generation's when it came after that settled and before the
// check that found it still settled; one before, or after that check (a
// rollout may have begun), from a container no generation names, is not
// known.
//
// Kills: dropping either rule; attributing an earlier event to the
// settled generation.
func TestALimitEventIsAttributedToItsGeneration(t *testing.T) {
	t.Parallel()
	dep, settled := idstest.N(1), time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	o := containerOwners{exact: map[string]partOf{"apptown-env-shop-main": {deployment: dep, part: "web"}},
		services: map[string]ids.ID{docker.Current.Service("env-shop-main", ""): dep},
		settled:  map[ids.ID]settledAs{dep: {label: "01a1", generation: 5, since: settled, until: settled.Add(time.Hour)}}}
	worker := docker.Current.Service("env-shop-main", "worker")
	got := limitEventsOf([]docker.ContainerEvent{
		{Container: "apptown-env-shop-main", Action: "oom", At: settled.Add(-time.Minute), Deployment: "01a1", Generation: "4"},
		{Container: worker, Action: "oom", At: settled.Add(time.Minute)},
		{Container: worker, Action: "oom", At: settled.Add(-time.Minute)},
		{Container: "apptown-env-shop-main", Action: "oom", At: settled.Add(-time.Minute), Deployment: "other", Generation: "9"},
		{Container: worker, Action: "oom", At: settled.Add(time.Hour + time.Second)},
	}, o)
	want := []any{int64(4), int64(5), nil, nil, nil}
	if len(got) != len(want) {
		t.Fatalf("events: %+v", got)
	}
	for i, e := range got {
		switch g := want[i].(type) {
		case nil:
			if e.Generation != nil {
				t.Errorf("event %d: generation %d, want none", i, *e.Generation)
			}
		case int64:
			if e.Generation == nil || *e.Generation != g {
				t.Errorf("event %d: generation %v, want %d", i, e.Generation, g)
			}
		}
	}
}

// While a deploy is under way (or after one failed part way), the
// containers under the parts' names may already be the next generation's:
// a rollout replaces the other parts first. Their facts are still shown,
// but only the public part's container, by its labels, names a
// generation; the others' and their logs are not called the settled
// generation's. Limit events then recorded from an unlabelled container
// are attributed to no generation.
//
// Kills: naming every part's container as the settled generation's; a
// log of an unproven container labelled with the settled generation; the
// settled-time fallback applied while a deploy is open.
func TestPartsDuringADeployAreNotCalledTheSettledGenerations(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	gen := w.main.ObservedGeneration
	unlabelled := func() *int64 {
		t.Helper()
		deps, err := w.s.Deployments.All(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		owners, err := w.s.ownersOn(t.Context(), deps)
		if err != nil {
			t.Fatal(err)
		}
		events := limitEventsOf([]docker.ContainerEvent{{Container: w.worker, Action: "oom", At: w.s.now()}}, owners)
		if len(events) != 1 {
			t.Fatalf("events: %+v", events)
		}
		return events[0].Generation
	}
	if g := unlabelled(); g == nil || *g != gen {
		t.Fatalf("an unlabelled event while settled: %v", g)
	}
	// The sweep samples the settled state; a rollout begins and its new
	// worker is killed before the events are read: that event is after the
	// check, so it is not the old generation's.
	deps, err := w.s.Deployments.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sampled, err := w.s.ownersOn(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if events := limitEventsOf([]docker.ContainerEvent{{Container: w.worker, Action: "oom", At: w.advance(time.Second)}}, sampled); len(events) != 1 || events[0].Generation != nil {
		t.Fatalf("an event after the settled check: %+v", events)
	}
	at, created, err := w.s.Outcomes.Admit(t.Context(), deploymentStartFor(w.main, "next", "next"))
	if err != nil || !created {
		t.Fatalf("opening the next deploy: %v %v", created, err)
	}
	w.daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "logs" && !w.daemon.IsDiskRead(args) {
			return []byte("a line\n"), nil, true
		}
		return nil, nil, false
	})
	d := w.read(t)
	if d.Unsettled == "" || d.Generation != gen {
		t.Fatalf("a deploy under way: %+v", d)
	}
	if web := partNamed(t, d, "web"); web.Generation == nil || *web.Generation != gen {
		t.Fatalf("the public container names its own generation: %v", web.Generation)
	}
	if worker := partNamed(t, d, "worker"); worker.Generation != nil || worker.Facts == nil || worker.Presence != PresenceRunning {
		t.Fatalf("the worker under a deploy: %+v", worker)
	}
	if got, err := w.s.RecentLog(t.Context(), w.project, "main", "worker"); err != nil || got.Generation != nil {
		t.Fatalf("the worker's log under a deploy: %+v %v", got, err)
	}
	if got, err := w.s.RecentLog(t.Context(), w.project, "main", "web"); err != nil || got.Generation == nil || *got.Generation != gen {
		t.Fatalf("the front door's log under a deploy: %+v %v", got, err)
	}
	if g := unlabelled(); g != nil {
		t.Fatalf("an unlabelled event during a deploy: %d", *g)
	}

	// The deploy fails part way: its worker may still be the one it started.
	if _, err := w.s.Outcomes.Fail(t.Context(), at, errors.New("the front door did not start")); err != nil {
		t.Fatal(err)
	}
	if d := w.read(t); d.Unsettled == "" || partNamed(t, d, "worker").Generation != nil {
		t.Fatalf("after a failed deploy: %q %+v", d.Unsettled, partNamed(t, d, "worker"))
	}
	if g := unlabelled(); g != nil {
		t.Fatalf("an unlabelled event after a failed deploy: %d", *g)
	}
}
