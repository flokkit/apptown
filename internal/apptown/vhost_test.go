package apptown

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/worker"
)

// The vhost router: pass-through for AppTown's own host, a plain-words
// answer for dead names, and a real proxy to a live backend.
func TestVHostRouting(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{TierDomains: []string{"preview=preview.example.com", "main=main.example.com"}})
	shop := engineProject(t, a, "shop")
	dead, live := engineChange(t, a, shop), engineChange(t, a, shop)

	h := a.Handler()

	// AppTown's own hostname falls through to the API.
	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.Host = "forge.example.com"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("non-tier host must reach AppTown: %d", w.Code)
	}

	// A tier hostname with nothing behind it gets the plain-words page.
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = fmt.Sprintf("change-%d.preview.example.com", dead.ID)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "Nothing is running at") {
		t.Fatalf("dead preview: %d %s", w.Code, w.Body.String())
	}
	// A name that belongs to no project is served nothing.
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = "change-00000006-7932-7000-8000-000000000000.preview.example.com"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("a preview of no change: %d %s", w.Code, w.Body.String())
	}

	// A live backend: inject the port cache the way docker would fill it.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from the app")
	}))
	defer backend.Close()
	var port int
	fmt.Sscanf(backend.URL[strings.LastIndex(backend.URL, ":")+1:], "%d", &port)
	a.vhostMu.Lock()
	a.vhostCache = map[string]vhostEntry{fmt.Sprintf("apptown-preview-change-%d", live.ID): {port: port, at: time.Now()}}
	a.vhostMu.Unlock()
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = fmt.Sprintf("change-%d.preview.example.com", live.ID)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != "hello from the app" {
		t.Fatalf("proxy to live backend: %d %q", w.Code, w.Body.String())
	}
}

// Tier domains (vhost.go): the hostname is the lookup key; only
// deterministic AppTown container names are reachable; unset domains change
// nothing.
func TestTierDomains(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{TierDomains: []string{"preview=preview.example.com", "main=https://main.example.com"}})

	// Unset: URLs mint as host:port, VHost is a no-op wrapper.
	if u := (configuration{}).routing().PreviewURL("change-00000000-0061-7000-8000-000000000000", 5000); !strings.Contains(u, ":5000") {
		t.Fatalf("unset domains must keep port URLs: %s", u)
	}

	if u := a.cfg.routing().PreviewURL("change-00000000-0061-7000-8000-000000000000", 5000); u != "https://change-00000000-0061-7000-8000-000000000000.preview.example.com" {
		t.Fatalf("preview URL: %s", u)
	}
	if u := a.cfg.routing().DeploymentURL("reading-list", 5000); u != "https://reading-list.main.example.com" {
		t.Fatalf("env URL: %s", u)
	}
	if err := (Config{TierDomains: []string{"intranet=x.example.com"}}).Validate(); err == nil {
		t.Fatal("unknown tier accepted")
	}
	if err := (Config{TierDomains: []string{"eval=eval.example.com"}}).Validate(); err != nil {
		t.Fatalf("the eval tier: %v", err)
	}

	cases := []struct {
		host, container string
		ok              bool
	}{
		{"change-00000000-0061-7000-8000-000000000000.preview.example.com", "apptown-preview-change-00000000-0061-7000-8000-000000000000", true},
		{"run-00000000-0038-7000-8000-000000000000.preview.example.com:443", "apptown-preview-run-00000000-0038-7000-8000-000000000000", true},
		{"reading-list.main.example.com", "", true}, // standing: its record names the container
		{"forge.example.com", "", false},
		{"evil..preview.example.com", "", false},
		// A hostile label must not reach beyond AppTown-owned names.
		{"change-97x.preview.example.com", "", false},
		{"Sneaky.main.example.com", "", false},
	}
	for _, c := range cases {
		got, ok := a.vhostContainer(c.host)
		standing := ok && c.container == ""
		if ok != c.ok || got.container != c.container || got.standing != standing || (standing && got.label != "reading-list") {
			t.Errorf("%s: got (%q,%v, standing %v %q) want (%q,%v)", c.host, got.container, ok, got.standing, got.label, c.container, c.ok)
		}
	}
}

// A tier domain may carry a port, for an AppTown reached on one (the desktop
// trial's preview=http://preview.localhost:7777): AppTown mints URLs with
// it and routes requests by hostname, as it does a request on :443.
func TestTierDomainWithAPortRoutesByHostname(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{TierDomains: []string{"preview=http://preview.localhost:7777", "main=http://main.localhost:7777"}})
	const label = "change-00000000-0061-7000-8000-000000000000"
	if u := a.cfg.routing().PreviewURL(label, 5000); u != "http://"+label+".preview.localhost:7777" {
		t.Fatalf("preview URL: %s", u)
	}
	cases := []struct {
		host, container string
		ok              bool
	}{
		{label + ".preview.localhost:7777", "apptown-preview-" + label, true},
		{label + ".preview.localhost", "apptown-preview-" + label, true},
		{"reading-list.main.localhost:7777", "", true}, // standing: its record names the container
		{"localhost:7777", "", false},
		{label + ".preview.example.com:7777", "", false},
	}
	for _, c := range cases {
		got, ok := a.vhostContainer(c.host)
		standing := ok && c.container == ""
		if ok != c.ok || got.container != c.container || got.standing != standing || (standing && got.label != "reading-list") {
			t.Errorf("%s: got (%q,%v, standing %v %q) want (%q,%v)", c.host, got.container, ok, got.standing, got.label, c.container, c.ok)
		}
	}
}

// With previews on a worker, a change's and a run's preview are forwarded
// there, with the visitor's Host and request target, and still never
// cached; a release preview AppTown did not boot there and a standing
// deployment stay on this machine.
func TestChangeAndRunPreviewsAreForwardedToTheWorker(t *testing.T) {
	t.Parallel()
	// AppTown has a worker for previews (Deployment decides what is served
	// there); its forwarding client is pointed at this test's plain server.
	a := testApp(t, withPreviewWorker(t, &fakePreviewWorker{}, Config{TierDomains: []string{"preview=preview.example.com", "main=main.example.com"}}))
	shop := createReadTestProject(t, a, "shop")
	change := struct{ ID ids.ID }{createReadTestChange(t, a, shop, "previewed")}
	run := startReadTestRun(t, a, createReadTestChange(t, a, shop, "run"), waitingStage).Run
	var mu sync.Mutex
	var atWorker []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		atWorker = append(atWorker, r.URL.Path+" "+r.Header.Get(worker.PreviewHostHeader)+" "+r.Header.Get(worker.PreviewTargetHeader))
		mu.Unlock()
		fmt.Fprint(w, "from the worker")
	}))
	defer fake.Close()
	a.workerPreviews = &worker.Previews{URL: fake.URL, Client: fake.Client()}
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "from here") }))
	defer local.Close()
	port := local.Listener.Addr().(*net.TCPAddr).Port
	a.vhostMu.Lock()
	a.vhostCache = map[string]vhostEntry{"apptown-preview-shop-release-2": {port: port, at: time.Now()}, "apptown-env-shop-main": {port: port, at: time.Now()}}
	a.vhostMu.Unlock()
	recordServing(t, a, shop, "apptown-env-shop-main")

	h := a.Handler()
	for host, want := range map[string]string{
		fmt.Sprintf("change-%d.preview.example.com", change.ID): "from the worker", fmt.Sprintf("run-%d.preview.example.com", run.ID): "from the worker",
		"shop-release-2.preview.example.com": "from here", "shop.main.example.com": "from here",
	} {
		req := httptest.NewRequest("GET", "/a%2Fb?x=1", nil)
		req.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 || w.Body.String() != want {
			t.Errorf("%s: %d %q, want %q", host, w.Code, w.Body.String(), want)
		}
		if want == "from the worker" && w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: a preview from the worker may be cached: %q", host, w.Header().Get("Cache-Control"))
		}
	}
	mu.Lock()
	defer mu.Unlock()
	sort.Strings(atWorker)
	want := []string{
		fmt.Sprintf("/v1/previews/apptown-preview-change-%d/http change-%d.preview.example.com /a%%2Fb?x=1", change.ID, change.ID),
		fmt.Sprintf("/v1/previews/apptown-preview-run-%d/http run-%d.preview.example.com /a%%2Fb?x=1", run.ID, run.ID),
	}
	sort.Strings(want)
	if !slices.Equal(atWorker, want) {
		t.Fatalf("the worker was asked %q, want %q", atWorker, want)
	}
}

// AppTown's own proxy passes an app only the forwarding headers it sets
// itself: whatever a visitor sent of the X-Forwarded-* family, Forwarded or
// X-Real-Ip never reaches the app.
func TestTheLocalProxyPassesOnlyItsOwnForwardingHeaders(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{TierDomains: []string{"preview=preview.example.com"}})
	change := engineChange(t, a, engineProject(t, a, "shop"))
	var got http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer backend.Close()
	a.vhostMu.Lock()
	a.vhostCache = map[string]vhostEntry{fmt.Sprintf("apptown-preview-change-%d", change.ID): {port: backend.Listener.Addr().(*net.TCPAddr).Port, at: time.Now()}}
	a.vhostMu.Unlock()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = fmt.Sprintf("change-%d.preview.example.com", change.ID)
	for h, v := range map[string]string{"X-Forwarded-Prefix": "/attacker", "X-Forwarded-Proto": "http", "Forwarded": "for=6.6.6.6", "X-Real-Ip": "6.6.6.6"} {
		req.Header.Set(h, v)
	}
	a.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if got == nil {
		t.Fatal("the app was not reached")
	}
	for _, h := range []string{"X-Forwarded-Prefix", "Forwarded", "X-Real-Ip"} {
		if got.Get(h) != "" {
			t.Errorf("a visitor's %s reached the app: %q", h, got.Get(h))
		}
	}
	if got.Get("X-Forwarded-Proto") != "https" {
		t.Errorf("AppTown's own proto: %q", got.Get("X-Forwarded-Proto"))
	}
}

// waitingStage is a run that stays where it is: one approval no one gives.
var waitingStage = schema.Stage{Name: "wait", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"means": "I accept this work"}}}}

// evalCopyForTest makes projectID, made by the lifecycle as an ordinary
// project, an eval copy for eval 1: Project's purpose and Evals' copy
// record. These tests need the lifecycle's ownership and adoption (changes,
// runs, previews, history), which only Create (ordinary) or CreateCopy
// (which needs a source repository) compose.
func evalCopyForTest(t *testing.T, a *App, projectID ids.ID) {
	t.Helper()
	// sql-guarantee: no operation changes a purpose, and CreateCopy needs a source repository these tests have no use for (see above).
	if _, err := a.DB.SQL().ExecContext(context.Background(), `UPDATE projects SET purpose = 'eval-copy' WHERE id = $1`, projectID); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: the copy record, written with the purpose as above.
	if _, err := a.DB.SQL().ExecContext(context.Background(), `INSERT INTO eval_copies (project_id, eval_id, arm_id, arm) VALUES ($1, '00000000-0001-7000-8000-000000000000', '00000000-0001-7000-8000-000000000000', 'baseline')`, projectID); err != nil {
		t.Fatal(err)
	}
}

// The eval domain serves eval copies only, and the preview and main
// domains never serve one: a copy's standing deployment, change, run and
// release previews answer at their eval-domain names and are refused at
// their ordinary ones; a real project's are refused on the eval domain.
// The copy's addresses are minted on the eval domain.
func TestTheEvalDomainServesCopiesAndNothingElse(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{TierDomains: []string{"preview=preview.example.com", "main=main.example.com", "eval=eval.example.com"}})
	real, copy := createReadTestProject(t, a, "shop"), createReadTestProject(t, a, "shop-copy")
	evalCopyForTest(t, a, copy)
	realChange, copyChange := struct{ ID ids.ID }{createReadTestChange(t, a, real, "real")}, struct{ ID ids.ID }{createReadTestChange(t, a, copy, "copy")}
	copyRun := startReadTestRun(t, a, createReadTestChange(t, a, copy, "run"), waitingStage).Run
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "the app") }))
	defer backend.Close()
	port := backend.Listener.Addr().(*net.TCPAddr).Port
	cache := map[string]vhostEntry{}
	for _, name := range []string{"apptown-env-shop-main", "apptown-env-shop-copy-main", "apptown-preview-shop-copy-release-1",
		fmt.Sprintf("apptown-preview-change-%d", realChange.ID), fmt.Sprintf("apptown-preview-change-%d", copyChange.ID), fmt.Sprintf("apptown-preview-run-%d", copyRun.ID)} {
		cache[name] = vhostEntry{port: port, at: time.Now()}
	}
	a.vhostMu.Lock()
	a.vhostCache = cache
	a.vhostMu.Unlock()
	recordServing(t, a, real, "apptown-env-shop-main")
	recordServing(t, a, copy, "apptown-env-shop-copy-main")
	h := a.Handler()
	for host, served := range map[string]bool{
		"shop-copy.eval.example.com": true, "shop-copy.main.example.com": false,
		"shop.main.example.com": true, "shop.eval.example.com": false,
		fmt.Sprintf("change-%d.eval.example.com", copyChange.ID): true, fmt.Sprintf("change-%d.preview.example.com", copyChange.ID): false,
		fmt.Sprintf("change-%d.preview.example.com", realChange.ID): true, fmt.Sprintf("change-%d.eval.example.com", realChange.ID): false,
		fmt.Sprintf("run-%d.eval.example.com", copyRun.ID): true, fmt.Sprintf("run-%d.preview.example.com", copyRun.ID): false,
		"shop-copy-release-1.eval.example.com": true, "shop-copy-release-1.preview.example.com": false,
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if served != (w.Code == 200 && w.Body.String() == "the app") || (!served && w.Code != 404) {
			t.Errorf("%s: %d %q, served=%v wanted", host, w.Code, w.Body.String(), served)
		}
	}
	if u := a.environment.RoutesFor(t.Context(), copy).PreviewURL(fmt.Sprintf("change-%d", copyChange.ID), 0); u != fmt.Sprintf("https://change-%d.eval.example.com", copyChange.ID) {
		t.Errorf("a copy's preview address: %s", u)
	}
	if u := a.environment.RoutesFor(t.Context(), copy).DeploymentURL("shop-copy", 0); u != "https://shop-copy.eval.example.com" {
		t.Errorf("a copy's deployment address: %s", u)
	}
	if u := a.environment.RoutesFor(t.Context(), real).DeploymentURL("shop", 0); u != "https://shop.main.example.com" {
		t.Errorf("a real project's deployment address: %s", u)
	}
}

// A request to a preview through AppTown is a look at it: Preview's
// tending keeps a preview someone looked at, and stops one nobody has.
func TestARequestToAPreviewIsALook(t *testing.T) {
	t.Parallel()
	f := newFakeDocker()
	looked, unseen := "apptown-preview-shop-release-3", "apptown-preview-shop-release-2"
	f.reply = func(args []string) (string, error, bool) {
		if args[0] == "ps" && slices.Contains(args, "label=apptown.class=preview") {
			return looked + "\tpvrel-shop-3\n" + unseen + "\tpvrel-shop-2\n", nil, true
		}
		return "", nil, false
	}
	a := testApp(t, Config{DockerClient: f, TierDomains: []string{"preview=preview.example.com"}})
	createReadTestProject(t, a, "shop")
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "the app") }))
	defer local.Close()
	a.vhostMu.Lock()
	a.vhostCache = map[string]vhostEntry{looked: {port: local.Listener.Addr().(*net.TCPAddr).Port, at: time.Now()}}
	a.vhostMu.Unlock()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "shop-release-3.preview.example.com"
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("the preview was not served: %d %s", w.Code, w.Body.String())
	}
	a.Preview.Tend(context.Background(), false)
	if got := f.matching("rm -f -v "); !slices.Equal(got, []string{"rm -f -v " + unseen}) {
		t.Fatalf("stopped %v, want only the preview nobody looked at", got)
	}
}

// A TLS proxy on this machine asks before getting a certificate (Caddy's
// on-demand TLS in the one-server compose file). AppTown says yes only to
// names its records bound: its own host, a change's preview, a project's
// main deployment and a recorded release's preview. Made-up names, such as
// any release number for an existing project, get no. The proxy has no
// credential, so the question is answered under every sign-in mode
// (internal here, the one the compose file uses).
func TestTLSAskAllowsOnlyNamesAppTownsRecordsBound(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{AuthMode: "internal", PublicHost: "https://apptown.example.com", TierDomains: []string{"preview=preview.example.com", "main=main.example.com"}})
	shop := engineProject(t, a, "shop")
	engineProject(t, a, "empty")
	ch := engineChange(t, a, shop)
	if _, err := a.Deployment.Releases.Start(t.Context(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: shop.ID, Deployment: "main", Revision: "first", Actor: "bls"}, "a release to look at"); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.InTx(t.Context(), func(tx *database.Tx) error {
		held, err := project.ByIDIn(t.Context(), tx, shop.ID)
		if err != nil {
			return err
		}
		next := "renamed"
		_, err = project.UpdateSettingsIn(t.Context(), tx, shop.ID, held.Slug, project.SettingsPatch{ExpectedRevision: &held.SettingsRevision, Slug: &next})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	ask := func(name, from string) int {
		req := httptest.NewRequest("GET", "/tls/ask?domain="+url.QueryEscape(name), nil)
		req.RemoteAddr = from
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	for name, want := range map[string]int{
		"apptown.example.com":                                             200,
		"APPTOWN.example.com.":                                            200,
		fmt.Sprintf("change-%s.preview.example.com", ch.ID):               200,
		"renamed.main.example.com":                                        404,
		"renamed-release-1.preview.example.com":                           404,
		"shop.main.example.com":                                           200,
		"shop-release-1.preview.example.com":                              200,
		"shop-release-2.preview.example.com":                              404, // no such release
		"shop-release-01.preview.example.com":                             404, // not how release 1 is written
		"shop-release-99999999999999999999.preview.example.com":           404,
		"empty-release-1.preview.example.com":                             404, // a project with no releases
		"shop-release-7.main.example.com":                                 404,
		"nobody.main.example.com":                                         404,
		"change-00000006-7932-7000-8000-000000000000.preview.example.com": 404,
		"anything.example.com":                                            404,
		"":                                                                404,
	} {
		if got := ask(name, "127.0.0.1:41234"); got != want {
			t.Errorf("ask %q: %d, want %d", name, got, want)
		}
	}
	if got := ask("apptown.example.com", "[::1]:41234"); got != 200 {
		t.Errorf("IPv6 loopback: %d", got)
	}
	if got := ask("apptown.example.com", "203.0.113.9:41234"); got != 403 {
		t.Errorf("a caller off this machine: %d, want 403", got)
	}
}

// A main hostname reaches the container the project's main deployment
// records serving from. The name is never computed from the Host.
func TestAMainHostnameReachesTheContainerItsDeploymentRecords(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{TierDomains: []string{"main=main.example.com"}})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "served") }))
	defer backend.Close()
	var port int
	fmt.Sscanf(backend.URL[strings.LastIndex(backend.URL, ":")+1:], "%d", &port)
	const container = "apptown-dep-recorded"
	shop := engineProject(t, a, "shop")
	recordServing(t, a, shop.ID, container)
	if err := a.DB.InTx(t.Context(), func(tx *database.Tx) error {
		held, err := project.ByIDIn(t.Context(), tx, shop.ID)
		if err != nil {
			return err
		}
		next := "renamed"
		_, err = project.UpdateSettingsIn(t.Context(), tx, shop.ID, held.Slug, project.SettingsPatch{ExpectedRevision: &held.SettingsRevision, Slug: &next})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a.vhostMu.Lock()
	if a.vhostCache == nil {
		a.vhostCache = map[string]vhostEntry{}
	}
	a.vhostCache[container] = vhostEntry{port: port, at: time.Now()}
	a.vhostMu.Unlock()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "shop.main.example.com"
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != "served" {
		t.Fatalf("%s did not reach %s: %d %q", req.Host, container, w.Code, w.Body.String())
	}
}

// recordServing records a project's main deployment serving from container,
// as its boot would.
func recordServing(t *testing.T, a *App, projectID ids.ID, container string) {
	t.Helper()
	a.SeedTargets(t.Context())
	target, err := a.Deployment.Targets.ByName(t.Context(), deployment.BuiltInTargetName)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: a serving container is recorded by a boot on Docker; this test serves from a plain HTTP backend, so no boot can.
	if _, err := a.DB.Exec(`UPDATE deployments SET container = $1 WHERE id = $2`, container, dep.ID); err != nil {
		t.Fatal(err)
	}
}

func TestServingIdentityDoesNotFollowCanonicalProjectAddress(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{})
	id := plainTestProject(t, a.DB, "original", "Original")
	if err := a.DB.InTx(t.Context(), func(tx *database.Tx) error {
		held, err := project.ByIDIn(t.Context(), tx, id)
		if err != nil {
			return err
		}
		next := "renamed"
		_, err = project.UpdateSettingsIn(t.Context(), tx, id, held.Slug, project.SettingsPatch{ExpectedRevision: &held.SettingsRevision, Slug: &next})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rules := vhostRules{db: a.DB, projects: a.Projects}
	for _, route := range []vhostRoute{{label: "original", standing: true}, {label: "original-release-1"}} {
		if got, err := rules.project(t.Context(), route); err != nil || got != id {
			t.Errorf("old serving label lost its project: got %v want %v: %v", got, id, err)
		}
	}
	if got, err := rules.project(t.Context(), vhostRoute{label: "renamed", standing: true}); err != nil || !got.IsZero() {
		t.Errorf("canonical address became an unassigned serving label: %v %v", got, err)
	}
}

func TestStandingServingNamesDoNotEnterPreviewNamespaces(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{TierDomains: []string{"main=main.example", "preview=preview.example"}})
	source := engineProject(t, a, "source")
	change := engineChange(t, a, source)
	for _, label := range []string{fmt.Sprintf("change-%s", change.ID), "source-release-1"} {
		owner := engineProject(t, a, label)
		rules := vhostRules{db: a.DB, projects: a.Projects}
		route := vhostRoute{label: label, standing: true}
		if got, err := rules.project(t.Context(), route); err != nil || got != owner.ID {
			t.Fatalf("standing name entered preview namespace: %v want %v: %v", got, owner.ID, err)
		}
		names := certificateNames{publicHost: a.cfg.publicHost, tiers: a.cfg.tierDomains, rules: &rules, projects: a.Projects, deployment: a.Deployment}
		if yes, err := names.certifiable(t.Context(), label+".main.example"); err != nil || !yes {
			t.Fatalf("standing certificate treated as preview: %t %v", yes, err)
		}
	}
}
