package fly

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/fly/flyfake"
)

// releasedWithPostgres is a world whose deployment runs a web part and a
// Postgres need, one Machine each, and those Machines.
func releasedWithPostgres(t *testing.T) (w *world, app string, web, pg *flyfake.Machine) {
	t.Helper()
	w = newWorld(t)
	catalogueNeeds(t, w)
	if _, err := w.release(source(t, webApp+"needs: {postgres: {}}\n", "a"), 1, nil); err != nil {
		t.Fatal(err)
	}
	app = appName(installation, deploy.HostKey(w.p.ID))
	a := w.api.App(app)
	pg = needMachine(t, a)
	for _, m := range a.Machines {
		if m.Metadata()[metaRole] == "part" {
			web = m
		}
	}
	if web == nil {
		t.Fatal("no part Machine")
	}
	return w, app, web, pg
}

func partNamed(t *testing.T, parts []deploy.PartRuntime, name string) deploy.PartRuntime {
	t.Helper()
	for _, p := range parts {
		if p.Part == name {
			return p
		}
	}
	t.Fatalf("no part %q in %+v", name, parts)
	return deploy.PartRuntime{}
}

// Observe reports each part's and need's Machine from the Machine list it
// already reads: its state, image, size, when it last started (the newest
// "started" event) and how it exited (an out-of-memory kill, a requested
// stop). Kills: Parts left nil, needs left out, the size or image not read,
// the oldest start taken, requested_stop or oom_killed not read.
func TestObserveReportsEachPartsMachine(t *testing.T) {
	t.Parallel()
	w, app, web, pg := releasedWithPostgres(t)
	w.api.AddEvent(app, web.ID, map[string]any{"type": "start", "status": "started", "timestamp": int64(1_700_000_000_000)})
	w.api.AddEvent(app, web.ID, map[string]any{"type": "exit", "status": "stopped", "timestamp": int64(1_700_000_100_000),
		"request": map[string]any{"exit_event": map[string]any{"exit_code": 137, "oom_killed": true, "requested_stop": false, "signal": -1}}})
	w.api.AddEvent(app, web.ID, map[string]any{"type": "start", "status": "started", "timestamp": int64(1_700_000_101_000)})
	w.api.AddEvent(app, pg.ID, map[string]any{"type": "exit", "status": "stopped", "timestamp": int64(1_700_000_050_000),
		"request": map[string]any{"exit_event": map[string]any{"exit_code": 0, "oom_killed": false, "requested_stop": true, "signal": -1}}})

	o, err := w.driver.Observe(context.Background(), w.p)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Parts) != 2 {
		t.Fatalf("parts %+v, want web and postgres", o.Parts)
	}
	got := partNamed(t, o.Parts, "web")
	if got.ID != web.ID || got.Status != "started" || !got.Running || got.Unreachable || got.Generation != 1 || got.Health != "healthy" {
		t.Errorf("web %+v", got)
	}
	if got.Image != web.Config["image"] || got.CPULimit != 1 || got.CPUKind != "shared" || got.MemoryLimit != 512<<20 {
		t.Errorf("web image %q, size %v %s CPUs %d bytes; want %v, 1 shared CPU, 512 MiB", got.Image, got.CPULimit, got.CPUKind, got.MemoryLimit, web.Config["image"])
	}
	if !got.StartedAt.Equal(time.UnixMilli(1_700_000_101_000)) {
		t.Errorf("web started %v, want its newest start", got.StartedAt)
	}
	if len(got.Exits) != 1 || !got.Exits[0].OOM || got.Exits[0].Code != 137 || got.Exits[0].Requested || !got.Exits[0].At.Equal(time.UnixMilli(1_700_000_100_000)) {
		t.Errorf("web exits %+v, want one out-of-memory kill", got.Exits)
	}
	need := partNamed(t, o.Parts, "postgres")
	if need.ID != pg.ID || need.Generation != 0 || len(need.Exits) != 1 || !need.Exits[0].Requested {
		t.Errorf("postgres %+v, want its Machine and one requested stop", need)
	}
	if need.Use != nil || got.Use != nil {
		t.Error("Observe reads no use")
	}

	w.api.Set(app, web.ID, "", "unreachable", "")
	o, err = w.driver.Observe(context.Background(), w.p)
	if err != nil {
		t.Fatal(err)
	}
	if got := partNamed(t, o.Parts, "web"); !got.Unreachable {
		t.Errorf("a Machine whose host Fly cannot reach reads %+v", got)
	}
}

// ReadUsage asks the org's metrics for the app's CPU (the guest's own
// modes, centiseconds per second over the last minute, as cores) and the
// guest's memory in use, and matches them to each part's Machine by its
// instance label. A Machine missing either sample has no use and says why.
// Kills: another query or unit, matching by anything but the Machine id,
// total memory taken for used, a missing sample read as zero.
func TestReadUsageMatchesMetricsToMachines(t *testing.T) {
	t.Parallel()
	w, app, web, pg := releasedWithPostgres(t)
	w.api.SetCPU(web.ID, 25)
	w.api.SetMemory(web.ID, 480<<20, 380<<20)
	w.api.SetCPU(pg.ID, 10) // no memory sample
	parts, err := w.driver.ReadUsage(context.Background(), w.p)
	if err != nil {
		t.Fatal(err)
	}
	scope := `app="` + app + `"`
	want := []string{
		`sum by (instance) (rate(fly_instance_cpu{` + scope + `,mode!~"idle|iowait|steal"}[1m])) / 100`,
		`fly_instance_memory_mem_total{` + scope + `} - fly_instance_memory_mem_available{` + scope + `}`,
	}
	if got := w.api.Queries(); !slices.Equal(got, want) {
		t.Errorf("queries %q, want %q", got, want)
	}
	got := partNamed(t, parts, "web")
	if got.Use == nil || got.Use.CPUCores != 0.25 || got.Use.MemoryBytes != 100<<20 || got.MemoryLimit != 512<<20 {
		t.Errorf("web %+v (use %+v), want 0.25 cores and 100 MiB of 512 MiB", got, got.Use)
	}
	need := partNamed(t, parts, "postgres")
	if need.ID != pg.ID || need.Use != nil || !strings.Contains(need.UseReason, "no sample") {
		t.Errorf("a Machine with no memory sample reads %+v (use %+v)", need, need.Use)
	}
	// Memory without CPU is no use either.
	w2, _, web2, _ := releasedWithPostgres(t)
	w2.api.SetMemory(web2.ID, 480<<20, 380<<20)
	parts, err = w2.driver.ReadUsage(context.Background(), w2.p)
	if err != nil {
		t.Fatal(err)
	}
	if got := partNamed(t, parts, "web"); got.Use != nil || got.UseReason == "" {
		t.Errorf("a Machine with no CPU sample reads %+v (use %+v)", got, got.Use)
	}
}

// Metrics Fly does not give leave every part's use unknown with Fly's
// words: never zero. Kills: an error dropped, a failed query read as empty.
func TestReadUsageKeepsUnknownWhenMetricsFail(t *testing.T) {
	t.Parallel()
	w, _, web, pg := releasedWithPostgres(t)
	for _, m := range []*flyfake.Machine{web, pg} {
		w.api.SetCPU(m.ID, 25)
		w.api.SetMemory(m.ID, 480<<20, 380<<20)
	}
	w.api.Fail("GET", "/prometheus/"+org+"/api/v1/query", 503, 1)
	parts, err := w.driver.ReadUsage(context.Background(), w.p)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		if p.Use != nil || !strings.Contains(p.UseReason, "Fly's metrics did not answer") || !strings.Contains(p.UseReason, "503") {
			t.Errorf("%s reads %+v (use %+v) when a query failed", p.Part, p, p.Use)
		}
	}
}

// A Machine whose host Fly cannot reach is answered with config null: it is
// known by its last known config and reported unreachable, never left out
// (which would read as absent). One Fly cannot name even so is reported
// with no name. Kills: identity read only from config, the unnamed Machine
// dropped.
func TestAnUnreachableMachineIsReportedNotDropped(t *testing.T) {
	t.Parallel()
	w, app, web, pg := releasedWithPostgres(t)
	w.api.Unreachable(app, web.ID, false)
	w.api.Unreachable(app, pg.ID, true)
	o, err := w.driver.Observe(context.Background(), w.p)
	if err != nil {
		t.Fatal(err)
	}
	if got := partNamed(t, o.Parts, "web"); got.ID != web.ID || !got.Unreachable {
		t.Errorf("an unreachable web reads %+v", got)
	}
	if got := partNamed(t, o.Parts, ""); got.ID != pg.ID || !got.Unreachable {
		t.Errorf("an unreachable Machine with no known config reads %+v", got)
	}
}

// Every generation's Machines are reported, each with its generation, so
// an exit of a release's new Machine is seen beside the old generation.
// Kills: reporting only the placement's generation.
func TestPartsOfEveryGenerationAreReported(t *testing.T) {
	t.Parallel()
	w, app, web, _ := releasedWithPostgres(t)
	old := w.p
	if _, err := w.release(source(t, webApp+"needs: {postgres: {}}\n", "b"), 2, nil); err != nil {
		t.Fatal(err)
	}
	// The old generation is retired once the new is witnessed; put one back
	// as a release in flight leaves it, with its requested stop.
	w.api.Adopt(app, web)
	o, err := w.driver.Observe(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	gens := map[int64]bool{}
	for _, p := range o.Parts {
		if p.Part == "web" {
			gens[p.Generation] = true
		}
	}
	if !gens[1] || !gens[2] {
		t.Errorf("web reported in generations %v, want 1 and 2", gens)
	}
}

// Fly's metrics take a `fly tokens create` token in the form it is printed
// in and any other as a bearer token; the token never enters an error.
// Kills: always sending Bearer, the token in a refusal's words.
func TestMetricsQuerySendsTheTokenInFlysForm(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ token, header string }{
		{"FlyV1 fm2_secret-macaroon", "FlyV1 fm2_secret-macaroon"},
		{"fo1_secret-token", "Bearer fo1_secret-token"},
	} {
		var got string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Authorization")
			http.Error(w, "bad token "+c.token, http.StatusUnauthorized)
		}))
		_, err := NewClient(c.token, server.URL).query(context.Background(), org, "up")
		server.Close()
		if got != c.header {
			t.Errorf("token %q sent as %q, want %q", c.token, got, c.header)
		}
		if err == nil || strings.Contains(err.Error(), c.token) {
			t.Errorf("refusal %v must be an error without the token", err)
		}
	}
}
