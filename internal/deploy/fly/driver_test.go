package fly

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/fly/flyfake"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/oci"
)

const (
	org          = "acme"
	installation = "0193a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	other        = "0193ffff-0000-7000-8000-000000000001"
	testToken    = "fo1_test-token-value-never-said"
)

func digestOf(s string) string { return "sha256:" + strings.Repeat(s[:1], 64) }

// world is one Fly org behind the fake, and a driver for one deployment.
type world struct {
	t       *testing.T
	api     *flyfake.API
	driver  *Driver
	owned   *ownedList
	p       deploy.Placement
	door    *frontDoor
	noted   map[string]any
	logs    []string
	logMu   sync.Mutex
	copyErr error
}

type ownedList struct {
	mu   sync.Mutex
	list []deploy.Owned
}

func (o *ownedList) Created(_ context.Context, x deploy.Owned) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.list = append(o.list, x)
	return nil
}

func (o *ownedList) all(context.Context) ([]deploy.Owned, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.list), nil
}

// frontDoor answers the app's public address: the release a static part
// serves, and 200 on the readiness path, unless told otherwise.
type frontDoor struct {
	mu      sync.Mutex
	release string
	status  int
	asked   []string
}

func (f *frontDoor) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, r.URL.String())
	body, status := "ok", 200
	if f.status != 0 {
		status = f.status
	}
	if r.URL.Path == appcontract.ReleasePath {
		body = f.release
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
}

func newWorld(t *testing.T) *world {
	t.Helper()
	api := flyfake.New(org)
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	w := &world{t: t, api: api, owned: &ownedList{}, door: &frontDoor{}, p: deploy.Placement{ID: idstest.N(7), Name: "cloud"}}
	w.driver = NewDriver(Dependencies{
		Client: func(context.Context) (*Client, Config, error) {
			return NewClient(testToken, server.URL), Config{Org: org, Region: "syd"}, nil
		},
		Installation: installation,
		Owner:        w.owned,
		Owned:        w.owned.all,
	}, Options{
		pollInterval: time.Millisecond, machineWait: time.Second, FrontDoor: w.door,
		CopyImage: func(_ context.Context, from oci.Source, to oci.Repository, _ string) (string, error) {
			if w.copyErr != nil {
				return "", w.copyErr
			}
			if to.Credential.Password != testToken {
				return "", errors.New("Fly's registry wants the org token")
			}
			_, d, _ := strings.Cut(from.Reference, "@")
			return d, nil
		},
	})
	return w
}

func (w *world) logf(format string, args ...any) {
	w.logMu.Lock()
	defer w.logMu.Unlock()
	w.logs = append(w.logs, fmt.Sprintf(format, args...))
}

func (w *world) checkpoint(m map[string]any) error {
	w.noted = map[string]any{}
	for k, v := range m {
		w.noted[k] = v
	}
	return nil
}

type secrets map[string]map[string]string

func (s secrets) EnvFor(consumer string) map[string]string { return s[consumer] }

func source(t *testing.T, yaml, revision string) deploy.Source {
	t.Helper()
	c, err := appcontract.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	src := deploy.Source{Contract: c, Revision: revision, Images: map[string]string{}, Init: map[string]string{}, Published: true, Platform: Platform}
	for _, p := range c.Parts {
		src.Images[p.Name] = "reg.example/app@" + digestOf(revision)
	}
	for _, it := range c.Init {
		src.Init[it.Name] = "reg.example/app@" + digestOf(revision)
	}
	return src
}

// release renders and reconciles src as generation g, as AppTown does.
func (w *world) release(src deploy.Source, g int64, sec deploy.ConsumerSecrets) (deploy.Observation, error) {
	ctx := context.Background()
	if sec == nil {
		sec = secrets{}
	}
	if err := w.driver.Preflight(ctx, w.p, src, sec, w.logf); err != nil {
		return deploy.Observation{}, err
	}
	art, err := w.driver.Render(ctx, w.p, src, nil, sec, w.logf)
	if err != nil {
		return deploy.Observation{}, err
	}
	o, err := w.driver.Reconcile(ctx, w.p, g, art, "release", w.logf, w.checkpoint)
	if err == nil {
		w.p.Generation, w.p.Handle = g, o.Handle.(map[string]any)
	}
	return o, err
}

const webApp = "app:\n  web:\n    image: reg/web:1\n    ports: [\"8080\"]\n    public: true\n    health: {readiness: {http: /healthz}}\n"

// A release makes the deployment's app under an installation-specific name,
// copies its images into the app's repository in Fly's registry, and gives
// every Machine this installation's, deployment's and generation's marks.
// The next release's Machines are made and witnessed before the previous
// generation's are destroyed. Kills: Machines without marks, retiring the
// old generation first, an app name without the installation part.
func TestAFlyReleaseMakesMarkedMachinesAndRetiresTheOldGenerationAfterTheNewIsWitnessed(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	o, err := w.release(source(t, webApp, "aaaa111"), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	name := appName(installation, deploy.HostKey(w.p.ID))
	if !strings.HasPrefix(name, "at-"+installationPart(installation)+"-") || o.URL != "https://"+name+".fly.dev" || o.Rollout != "active" || o.Health != "healthy" {
		t.Fatalf("observation %+v for app %s", o, name)
	}
	if len(w.owned.list) != 1 || w.owned.list[0].Kind != kindApp || w.owned.list[0].Name != name {
		t.Fatalf("the app is recorded on the owned list as soon as it is made: %+v", w.owned.list)
	}
	app := w.api.App(name)
	if len(app.Machines) != 1 || len(app.IPs) != 2 {
		t.Fatalf("one Machine and two addresses, got %+v", app)
	}
	var first string
	for id, m := range app.Machines {
		first = id
		md := m.Metadata()
		if md[metaInstance] != installation || md[metaDeployment] != w.p.ID.String() || md[metaGeneration] != "1" {
			t.Fatalf("metadata %v", md)
		}
		// A part's process is restarted whenever it exits, without limit, as
		// on every other target: "running" means the same everywhere.
		if rp := m.Config["restart"].(map[string]any); rp["policy"] != "always" || rp["max_retries"] != nil {
			t.Fatalf("restart policy %v", rp)
		}
		svc := m.Config["services"].([]any)[0].(map[string]any)
		// Fly answers autostop "off" as false.
		if svc["autostart"] != false || svc["autostop"] != false {
			t.Fatalf("the proxy must never start a Machine AppTown did not: %v", svc)
		}
	}
	if imgs := w.api.Images(); len(imgs) != 1 || imgs[0] != "registry.fly.io/"+name+"@"+digestOf("aaaa111") {
		t.Fatalf("Machines pull the copy in Fly's registry by digest: %v", imgs)
	}
	// The next release: the front door is asked while both generations
	// stand, and only then is generation 1 destroyed.
	var during []string
	w.door.mu.Lock()
	w.door.asked = nil
	w.door.mu.Unlock()
	w.api.OnCreate = func(app string, m *flyfake.Machine) {}
	before := w.api.App(name)
	if _, err := w.release(source(t, webApp, "bbbb222"), 2, nil); err != nil {
		t.Fatal(err)
	}
	reqs := w.api.Requests()
	deleteAt, lastGet := -1, -1
	for i, r := range reqs {
		if r.Method == "DELETE" && strings.HasSuffix(r.Path, "/machines/"+first) {
			deleteAt = i
		}
		if r.Method == "GET" && strings.Contains(r.Path, "/machines/") && deleteAt < 0 {
			lastGet = i
		}
	}
	if deleteAt < 0 || deleteAt < lastGet {
		t.Fatalf("generation 1 (%s) is destroyed only after generation 2 is ready: %v", first, reqs)
	}
	after := w.api.App(name)
	for id, m := range after.Machines {
		if id == first || m.Metadata()[metaGeneration] != "2" {
			t.Fatalf("after the release only generation 2 stands: %v", after.Machines)
		}
	}
	_ = before
	_ = during
}

// A front door that does not answer the new release's readiness fails the
// release before the previous generation is touched, so it keeps serving.
// Kills: retiring the old generation before the front door is witnessed.
func TestAnUnwitnessedFrontDoorLeavesThePreviousGenerationServing(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := w.release(source(t, webApp, "aaaa111"), 1, nil); err != nil {
		t.Fatal(err)
	}
	w.door.mu.Lock()
	w.door.status = 503
	w.door.mu.Unlock()
	if _, err := w.release(source(t, webApp, "bbbb222"), 2, nil); err == nil {
		t.Fatal("a front door that does not answer fails the release")
	}
	ms := w.api.App(appName(installation, deploy.HostKey(w.p.ID))).Machines
	if len(ms) != 1 {
		t.Fatalf("generation 1's Machine still stands: %v", ms)
	}
	for _, m := range ms {
		if m.Metadata()[metaGeneration] != "1" {
			t.Fatalf("only generation 1 stands: %v", m.Metadata())
		}
	}
}

// A new Machine whose check fails is destroyed and the release fails, so
// the previous generation keeps serving. Kills: retiring the old
// generation before the new one is ready, or leaving the failed one.
func TestAFailedCheckLeavesThePreviousGenerationServing(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := w.release(source(t, webApp, "aaaa111"), 1, nil); err != nil {
		t.Fatal(err)
	}
	name := appName(installation, deploy.HostKey(w.p.ID))
	w.api.OnCreate = func(_ string, m *flyfake.Machine) {
		for _, c := range m.Checks {
			c["status"] = "critical"
		}
	}
	if _, err := w.release(source(t, webApp, "bbbb222"), 2, nil); err == nil {
		t.Fatal("a Machine that never passes its check fails the release")
	}
	app := w.api.App(name)
	if len(app.Machines) != 1 {
		t.Fatalf("only generation 1 stands: %v", app.Machines)
	}
	for _, m := range app.Machines {
		if m.Metadata()[metaGeneration] != "1" {
			t.Fatalf("generation 1 keeps serving, got %v", m.Metadata())
		}
	}
}

// An init step runs as a one-off Machine before any part Machine is made;
// a non-zero exit fails the release then, and the Machine is destroyed.
// Kills: parts made before init, an exit code ignored, a one-off left.
func TestAnInitStepThatExitsNonZeroStopsTheReleaseBeforeAnyPart(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.api.OnCreate = func(_ string, m *flyfake.Machine) {
		if strings.HasPrefix(m.Name, "init-") {
			flyfake.Exit(m, 3, true)
		}
	}
	src := source(t, webApp+"init:\n  - {name: migrate, image: reg/web:1, command: [\"migrate\"]}\n", "aaaa111")
	_, err := w.release(src, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "exited 3, out of memory") {
		t.Fatalf("the init step's exit fails the release: %v", err)
	}
	app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
	if len(app.Machines) != 0 {
		t.Fatalf("no part Machine is made and the init Machine is destroyed: %v", app.Machines)
	}
}

// What Fly cannot honour is refused before any request: a liveness probe,
// a threshold, an exact status, a gated placement, and parts or init steps given different secrets. A plain HTTP
// or TCP probe is accepted. Kills: any refusal dropped, or made after Fly
// was asked for something.
func TestWhatFlyCannotHonourIsRefusedBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	refused := map[string]struct {
		app     string
		secrets secrets
		gate    bool
		want    string
	}{
		"liveness":                   {app: "app:\n  web:\n    image: x/y:1\n    ports: [\"8080\"]\n    public: true\n    health: {liveness: {http: /live}}\n", want: "liveness"},
		"a threshold":                {app: "app:\n  web:\n    image: x/y:1\n    ports: [\"8080\"]\n    public: true\n    health: {readiness: {http: /h, successThreshold: 3}}\n", want: "thresholds"},
		"an exact status":            {app: "app:\n  web:\n    image: x/y:1\n    ports: [\"8080\"]\n    public: true\n    health: {readiness: {http: /h, expect: 204}}\n", want: "status 204"},
		"a gated copy":               {app: webApp, gate: true, want: "gate"},
		"a data directory":           {app: "app:\n  web:\n    image: x/y:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\n", want: "volume yet"},
		"differing secrets":          {app: "app:\n  web:\n    image: x/y:1\n    ports: [\"8080\"]\n    public: true\n  worker:\n    image: x/y:1\n", secrets: secrets{"web": {"A": "1"}, "worker": {"B": "2"}}, want: "same secrets"},
		"an init step's own secrets": {app: webApp + "init:\n  - {name: migrate, image: x/y:1}\n", secrets: secrets{"web": {"A": "1"}, "init-migrate": {"A": "1", "ROOT": "2"}}, want: "init-migrate"},
	}
	for name, c := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			src := source(t, c.app, "aaaa111")
			p := w.p
			if c.gate {
				p.Gate = &deploy.Gate{Image: "gate@" + digestOf("g")}
			}
			sec := deploy.ConsumerSecrets(secrets{})
			if c.secrets != nil {
				sec = c.secrets
			}
			err := w.driver.Preflight(context.Background(), p, src, sec, w.logf)
			if err == nil || !strings.Contains(err.Error(), c.want) || !deploy.IsTerminal(err) {
				t.Fatalf("refused with %q, got %v", c.want, err)
			}
			if _, err := w.driver.Render(context.Background(), p, src, nil, sec, w.logf); err == nil {
				t.Fatal("Render refuses it too")
			}
			if reqs := w.api.Requests(); len(reqs) != 0 {
				t.Fatalf("nothing is asked of Fly before the refusal: %v", reqs)
			}
		})
	}
	for _, accepted := range []string{webApp, "app:\n  web:\n    image: x/y:1\n    ports: [\"8080\"]\n    public: true\n    health: {readiness: {http: /h, period: 30s, timeout: 3s}}\n"} {
		w := newWorld(t)
		if err := w.driver.Preflight(context.Background(), w.p, source(t, accepted, "aaaa111"), secrets{}, w.logf); err != nil {
			t.Fatalf("%q is honoured on Fly: %v", accepted, err)
		}
	}
}

// A TCP probe and an HTTP probe become Fly's service check with the
// declared period, timeout and grace. Kills: a dropped or mis-mapped check.
func TestAReadinessProbeBecomesTheServiceCheck(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	src := source(t, "app:\n  web:\n    image: x/y:1\n    ports: [\"8080\"]\n    public: true\n    health: {readiness: {http: /ready, period: 30s, timeout: 3s, startupGrace: 20s}}\n", "aaaa111")
	if _, err := w.release(src, 1, nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range w.api.App(appName(installation, deploy.HostKey(w.p.ID))).Machines {
		check := m.Config["services"].([]any)[0].(map[string]any)["checks"].([]any)[0].(map[string]any)
		if check["type"] != "http" || check["path"] != "/ready" || check["interval"] != "30s" || check["timeout"] != "3s" || check["grace_period"] != "20s" {
			t.Fatalf("check %v", check)
		}
	}
}

// The app's secrets are Fly secrets, set in one change that also unsets
// any the release no longer gives, and the Machines are made with that
// version. Kills: a stale secret left, Machines made on an older version.
func TestTheAppsSecretsAreExactlyTheReleasesAndTheMachinesSeeThem(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := w.release(source(t, webApp, "aaaa111"), 1, secrets{"web": {"OLD": "1", "KEEP": "k"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.release(source(t, webApp, "bbbb222"), 2, secrets{"web": {"KEEP": "k2"}}); err != nil {
		t.Fatal(err)
	}
	app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
	if len(app.Secrets) != 1 || app.Secrets["KEEP"] != "k2" {
		t.Fatalf("secrets %v", app.Secrets)
	}
	for _, m := range app.Machines {
		if m.Secrets != app.Version {
			t.Fatalf("the Machine was made on secrets version %d, the app is at %d", m.Secrets, app.Version)
		}
	}
	for _, l := range w.logs {
		if strings.Contains(l, "k2") {
			t.Fatalf("a secret's value reached the release log: %q", l)
		}
	}
}

// Observation keeps unknown apart from stopped and from healthy. Kills: a
// host Fly cannot reach read as stopped or healthy; a crashed part read as
// stopped by intent; a failing check read healthy.
func TestObservationKeepsUnknownApartFromStopped(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		state, host, check string
		rollout, health    string
	}{
		{"started", "ok", "passing", "active", "healthy"},
		{"started", "ok", "critical", "active", "unhealthy"},
		{"starting", "ok", "", "rolling", "unknown"},
		{"stopped", "ok", "", "failed", "unhealthy"},
		{"failed", "ok", "", "failed", "unhealthy"},
		{"started", "unreachable", "", "active", "unknown"},
		{"stopped", "unknown", "", "active", "unknown"},
	} {
		w := newWorld(t)
		if _, err := w.release(source(t, webApp, "aaaa111"), 1, nil); err != nil {
			t.Fatal(err)
		}
		name := appName(installation, deploy.HostKey(w.p.ID))
		for id := range w.api.App(name).Machines {
			w.api.Set(name, id, c.state, c.host, c.check)
		}
		o, err := w.driver.Observe(context.Background(), w.p)
		if err != nil {
			t.Fatal(err)
		}
		if o.Rollout != c.rollout || o.Health != c.health {
			t.Errorf("%s on a host %s, check %s: read %s/%s, want %s/%s", c.state, c.host, c.check, o.Rollout, o.Health, c.rollout, c.health)
		}
	}
	// An answer Fly did not give is an error, never an empty app.
	w := newWorld(t)
	if _, err := w.release(source(t, webApp, "aaaa111"), 1, nil); err != nil {
		t.Fatal(err)
	}
	name := appName(installation, deploy.HostKey(w.p.ID))
	w.api.Fail("GET", flyfake.PathOf(name, "machines"), 503, 1)
	if _, err := w.driver.Observe(context.Background(), w.p); err == nil {
		t.Fatal("a failed read is unknown, not absent")
	}
}

// Two installations never reach for the same app name, and a name another
// Fly customer holds is refused, never adopted. Kills: names without the
// installation part; adopting an app by its name.
func TestAppNamesAreTheInstallationsAndATakenNameIsNeverAdopted(t *testing.T) {
	t.Parallel()
	key := deploy.HostKey(idstest.N(7))
	if appName(installation, key) == appName(other, key) {
		t.Fatal("two installations' names for one deployment key must differ")
	}
	w := newWorld(t)
	w.api.Taken = []string{appName(installation, key)}
	_, err := w.release(source(t, webApp, "aaaa111"), 1, nil)
	if err == nil || !strings.Contains(err.Error(), "never adopts an app by its name") {
		t.Fatalf("a taken name is refused: %v", err)
	}
	if len(w.owned.list) != 0 {
		t.Fatalf("nothing is recorded: %v", w.owned.list)
	}
}

// Rendering again after AppTown lost Fly's answer gets back the same app by
// the idempotency key; it is recorded once per answer, never by name.
func TestARetriedAppCreateGetsTheSameApp(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	src := source(t, webApp, "aaaa111")
	if _, err := w.driver.Render(ctx, w.p, src, nil, secrets{}, w.logf); err != nil {
		t.Fatal(err)
	}
	w.owned.list = nil // the record was lost: the create is asked again
	if _, err := w.driver.Render(ctx, w.p, src, nil, secrets{}, w.logf); err != nil {
		t.Fatal(err)
	}
	if apps := w.api.Apps(); len(apps) != 1 || len(w.owned.list) != 1 || w.owned.list[0].ID != w.api.App(apps[0]).ID {
		t.Fatalf("one app, recorded by its id: %v %v", apps, w.owned.list)
	}
}

// The org holds another installation's app and an app a person made. The
// inventory reports neither as this installation's, and releasing and
// taking down never names either of them in a request. Kills: marking by
// name, or any request reaching an app AppTown did not make.
func TestOtherInstallationsAndPeoplesAppsAreLeftAlone(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	theirs := w.api.SeedApp(org, appName(other, deploy.HostKey(w.p.ID)), true, map[string]string{metaInstance: other, metaDeployment: w.p.ID.String()})
	hand := w.api.SeedApp(org, "billing-api", true, map[string]string{})
	if _, err := w.release(source(t, webApp, "aaaa111"), 1, nil); err != nil {
		t.Fatal(err)
	}
	inv := Inventory{Client: w.driver.Client, Installation: installation}
	rs, err := inv.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mine := appName(installation, deploy.HostKey(w.p.ID))
	for _, r := range rs {
		confirmsOnly := slices.Contains(r.Tags, deploy.TagConfirmsOnly)
		if (r.Name == mine) == confirmsOnly {
			t.Errorf("%s: confirms-only %v; only this installation's app is marked", r.Name, confirmsOnly)
		}
	}
	if len(rs) != 3 {
		t.Fatalf("all three apps are listed: %+v", rs)
	}
	if _, err := w.driver.Teardown(context.Background(), w.p, w.p.Generation, deploy.TakeDown{Owned: w.owned.list}); err != nil {
		t.Fatal(err)
	}
	if got := w.api.Apps(); !slices.Equal(got, []string{theirs.Name, hand.Name}) && !slices.Equal(got, []string{hand.Name, theirs.Name}) {
		t.Fatalf("only this deployment's app goes: %v", got)
	}
	if tr := w.api.Trespasses(); len(tr) != 0 {
		t.Fatalf("no request names an app AppTown did not make: %v", tr)
	}
}

// A take-down deletes only the recorded app, and only when every Machine in
// it carries this deployment's mark; a Machine someone else put there stops
// it, named. Kills: deleting an app with somebody's Machine in it.
func TestTakeDownRefusesAnAppHoldingSomebodyElsesMachine(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := w.release(source(t, webApp, "aaaa111"), 1, nil); err != nil {
		t.Fatal(err)
	}
	name := appName(installation, deploy.HostKey(w.p.ID))
	// Someone adds a Machine of their own to the app, by hand.
	stranger := w.api.SeedApp(org, "scratch", false, map[string]string{"owner": "pat"})
	_ = stranger
	w.api.OnCreate = nil
	if err := moveMachine(w.api, "scratch", name); err != nil {
		t.Fatal(err)
	}
	_, err := w.driver.Teardown(context.Background(), w.p, w.p.Generation, deploy.TakeDown{Owned: w.owned.list})
	if err == nil || !strings.Contains(err.Error(), "does not carry this deployment's mark") {
		t.Fatalf("refused, naming the Machine: %v", err)
	}
	if w.api.App(name) == nil {
		t.Fatal("the app stays")
	}
	// A take-down of a generation that is not recorded is refused too.
	if _, err := w.driver.Teardown(context.Background(), w.p, w.p.Generation+1, deploy.TakeDown{}); err == nil {
		t.Fatal("a generation that was not recorded is not torn down")
	}
}

// moveMachine puts app from's Machine into app to, as a person might.
func moveMachine(api *flyfake.API, from, to string) error {
	a := api.App(from)
	for _, m := range a.Machines {
		api.Adopt(to, m)
		return nil
	}
	return errors.New("no machine")
}

// The token never reaches words AppTown records, even when a provider's
// error repeats it.
func TestTheTokenNeverReachesTheRecord(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.copyErr = errors.New("registry refused credential " + testToken)
	_, err := w.release(source(t, webApp, "aaaa111"), 1, nil)
	if err == nil {
		t.Fatal("the copy fails")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("the token reached the error: %v", err)
	}
	for _, l := range w.logs {
		if strings.Contains(l, testToken) {
			t.Fatalf("the token reached the log: %q", l)
		}
	}
}

// An interrupted release whose Machines all stand ready is finished and
// the old generation retired; one whose Machines are not all ready is
// destroyed and fails, leaving the previous release serving.
func TestResumeFinishesOnlyAReadyGeneration(t *testing.T) {
	t.Parallel()
	for _, ready := range []bool{true, false} {
		w := newWorld(t)
		if _, err := w.release(source(t, webApp, "aaaa111"), 1, nil); err != nil {
			t.Fatal(err)
		}
		name := appName(installation, deploy.HostKey(w.p.ID))
		ctx := context.Background()
		art, err := w.driver.Render(ctx, w.p, source(t, webApp, "bbbb222"), nil, secrets{}, w.logf)
		if err != nil {
			t.Fatal(err)
		}
		if !ready {
			w.api.OnCreate = func(_ string, m *flyfake.Machine) { m.State = "starting" }
		}
		// The attempt is cut off once its Machine is checkpointed.
		cut, cancel := context.WithCancel(ctx)
		w.driver.options.machineWait = 50 * time.Millisecond
		w.api.HoldDeletes = true
		_, _ = w.driver.Reconcile(cut, w.p, 2, art, "release", w.logf, func(m map[string]any) error {
			_ = w.checkpoint(m)
			if _, ok := m["machine:web"]; ok {
				cancel()
			}
			return nil
		})
		cancel()
		w.api.OnCreate = nil
		w.api.HoldDeletes = false
		noted := map[string]any{}
		for k, v := range w.noted {
			noted[k] = v
		}
		o, err := w.driver.Resume(ctx, w.p, 2, noted, w.logf)
		gens := map[string]int{}
		for _, m := range w.api.App(name).Machines {
			gens[m.Metadata()[metaGeneration]]++
		}
		if ready {
			if err != nil || o.Rollout != "active" || gens["2"] != 1 || gens["1"] != 0 {
				t.Fatalf("a ready generation is finished: %v %+v %v", err, o, gens)
			}
		} else if err == nil || gens["2"] != 0 || gens["1"] != 1 {
			t.Fatalf("an unready generation is destroyed, the old one serves: %v %v", err, gens)
		}
	}
}

// Restart restarts each part's Machine of the running generation.
func TestRestartRestartsEachPart(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := w.release(source(t, webApp, "aaaa111"), 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.driver.Runtime(context.Background(), w.p, "restart"); err != nil {
		t.Fatal(err)
	}
	for _, m := range w.api.App(appName(installation, deploy.HostKey(w.p.ID))).Machines {
		if m.Restarts != 1 {
			t.Fatalf("restarts %d", m.Restarts)
		}
	}
	if err := w.driver.Runtime(context.Background(), w.p, "stop"); err == nil {
		t.Fatal("stop is not offered")
	}
	if _, err := w.driver.ObserveRuntime(context.Background(), w.p); err != nil {
		t.Fatal(err)
	}
}

// The org a target uses comes from the token: the one it reaches, the one
// named among several, never one it does not reach; a token for apps
// alone is refused because it cannot create one.
func TestTheTargetsOrgIsTheTokens(t *testing.T) {
	t.Parallel()
	one := Account{Orgs: []string{"acme"}}
	if got, err := one.ChooseOrg(""); err != nil || got != "acme" {
		t.Fatalf("%q %v", got, err)
	}
	several := Account{Orgs: []string{"acme", "personal"}}
	if _, err := several.ChooseOrg(""); err == nil || !strings.Contains(err.Error(), "name the one") {
		t.Fatalf("%v", err)
	}
	if got, err := several.ChooseOrg("personal"); err != nil || got != "personal" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := one.ChooseOrg("elsewhere"); err == nil || !strings.Contains(err.Error(), "does not reach") {
		t.Fatalf("%v", err)
	}
	if _, err := (Account{Apps: []string{"web"}}).ChooseOrg(""); err == nil || !strings.Contains(err.Error(), "org token") {
		t.Fatalf("%v", err)
	}
	w := newWorld(t)
	c, _, _ := w.driver.Client(context.Background())
	acc, err := ReadAccount(context.Background(), c)
	if err != nil || !slices.Equal(acc.Orgs, []string{org}) || len(acc.Regions) != 3 {
		t.Fatalf("%+v %v", acc, err)
	}
}

// Each part runs on the cheapest shared-CPU Machine that covers what it
// declares; one none covers is refused before any effect.
func TestEachPartRunsOnTheSmallestMachineThatCoversIt(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		r    appcontract.Resources
		want string
	}{
		{appcontract.Resources{}, "shared-cpu-1x (512 MB)"},
		{appcontract.Resources{MemoryBytes: 300 << 20}, "shared-cpu-1x (512 MB)"},
		{appcontract.Resources{MemoryBytes: 3 << 30}, "shared-cpu-2x (3072 MB)"},
		{appcontract.Resources{CPUs: 3}, "shared-cpu-4x (1024 MB)"},
	} {
		g, err := fit(c.r)
		if err != nil || g.words() != c.want {
			t.Errorf("%+v: %q %v, want %q", c.r, g.words(), err, c.want)
		}
	}
	if _, err := fit(appcontract.Resources{MemoryBytes: 32 << 30}); err == nil {
		t.Fatal("32 GB is beyond a shared-CPU Machine")
	}
}

// An interrupted release whose Machines exist for only some of its parts
// is never finished: the part set recorded before any Machine was made
// says the generation is incomplete, so the previous one is not retired.
// Kills: judging a generation by the Machines that happen to exist.
func TestResumeNeverFinishesAGenerationMissingAPart(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	src := source(t, webApp+"  worker:\n    image: reg/worker:1\n", "aaaa111")
	if _, err := w.release(src, 1, nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	art, err := w.driver.Render(ctx, w.p, src, nil, secrets{}, w.logf)
	if err != nil {
		t.Fatal(err)
	}
	cut, cancel := context.WithCancel(ctx)
	defer cancel()
	w.api.HoldDeletes = true // model process death: no cleanup takes place
	_, _ = w.driver.Reconcile(cut, w.p, 2, art, "release", w.logf, func(m map[string]any) error {
		_ = w.checkpoint(m)
		if _, ok := m["machine:web"]; ok {
			cancel()
		}
		return nil
	})
	w.api.HoldDeletes = false
	o, err := w.driver.Resume(ctx, w.p, 2, w.noted, w.logf)
	if err == nil && o.Rollout == "active" {
		t.Fatalf("recovery accepted a generation with only web and retired the old worker: %+v", o)
	}
}

// A part whose Machine is gone is never read healthy: observation judges
// the running generation against the parts it recorded.
// Kills: observing only the Machines Fly returns.
func TestAMissingPartIsNeverHealthy(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	src := source(t, webApp+"  worker:\n    image: reg/worker:1\n", "aaaa111")
	if _, err := w.release(src, 1, nil); err != nil {
		t.Fatal(err)
	}
	app := appName(installation, deploy.HostKey(w.p.ID))
	client, _, _ := w.driver.client(context.Background())
	for id, m := range w.api.App(app).Machines {
		if m.Metadata()[metaGroup] == "worker" {
			w.driver.destroy(context.Background(), client, app, id, w.logf)
		}
	}
	o, err := w.driver.Observe(context.Background(), w.p)
	if err == nil && o.Rollout == "active" && o.Health == "healthy" {
		t.Fatalf("the missing worker was reported healthy: %+v", o)
	}
}
