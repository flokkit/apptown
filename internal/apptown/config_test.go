package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/worker/workertest"

	"github.com/flokkit/apptown/internal/database"
)

func TestConstructionHasNoDatabaseOrFilesystemEffects(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	// SQL with a word separator, including a swallowed seeding failure, consumes
	// this fault. The fault injector reserves an empty fragment for disabled faults.
	restore := db.FailNextStatement(" ")
	defer restore()
	dir := filepath.Join(t.TempDir(), "not-created")
	a, err := New(context.Background(), db, Config{DataDir: dir, AuthMode: "internal", Stock: testStock(t)})
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: a statement that must still find the pending fault, proving construction ran no SQL; not a read
	if _, err := db.SQL().ExecContext(context.Background(), "SELECT 42"); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("construction consumed the statement fault: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("construction touched filesystem: %v", err)
	}
	if a.evaluator == nil || a.health == nil || a.jobs == nil || a.inventory == nil || a.verification == nil {
		t.Fatal("owner left unconstructed")
	}
	if fallbacks, err := a.Models.Fallbacks(t.Context()); err != nil || len(fallbacks) != 0 {
		t.Fatalf("construction seeded slot fallbacks: %v %v", fallbacks, err)
	}
	if users, err := a.Access.Users(context.Background()); err != nil || len(users) != 0 {
		t.Fatalf("construction created accounts: %v %v", users, err)
	}
}

func TestConfigurationIsAnIsolatedSnapshot(t *testing.T) {
	t.Parallel()
	input := Config{AuthMode: "internal", PublicHost: "first.example", ListenAddr: "0.0.0.0:7777",
		TierDomains: []string{"preview=first.example"}, TrustedProxies: []string{"192.0.2.0/24"}, HostAddresses: []net.IP{net.IPv4(198, 18, 0, 1).To4()},
		InferenceURL: "http://worker:7779", InferenceNetworks: []string{"198.51.100.0/24"}}
	first := testApp(t, input)
	input.HostAddresses[0][3] = 2
	input.TierDomains[0] = "preview=second.example"
	input.TrustedProxies[0] = "203.0.113.0/24"
	input.InferenceNetworks[0] = "203.0.113.0/24"
	input.AuthMode = "open"
	second := testApp(t, input)
	if first.cfg.routing().PreviewURL("change-00000000-0001-7000-8000-000000000000", 42) != "https://change-00000000-0001-7000-8000-000000000000.first.example" || second.cfg.routing().PreviewURL("change-00000000-0001-7000-8000-000000000000", 42) != "https://change-00000000-0001-7000-8000-000000000000.second.example" {
		t.Fatal("routing settings shared")
	}
	ip := net.ParseIP("192.0.2.1")
	if !first.cfg.proxies.isTrustedProxy(ip) || second.cfg.proxies.isTrustedProxy(ip) {
		t.Fatal("proxy settings shared")
	}
	// Mutating the original IP bytes must not change which instance trusts
	// forwarded identity headers from that host.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "198.18.0.1:4321"
	r.Header.Set("X-Real-IP", "203.0.113.9")
	if first.cfg.proxies.ClientIP(r).String() != "203.0.113.9" || second.cfg.proxies.ClientIP(r).String() != "198.18.0.1" {
		t.Fatal("host address snapshots shared or forwarded-header policy changed")
	}
	r.RemoteAddr = "198.18.0.2:4321"
	if first.cfg.proxies.ClientIP(r).String() != "198.18.0.2" || second.cfg.proxies.ClientIP(r).String() != "203.0.113.9" {
		t.Fatal("new instance did not take its own host snapshot")
	}
	peerStatus := func(a *App) int {
		r := httptest.NewRequest("POST", "/providers/test/chat/completions", nil)
		r.RemoteAddr = "198.51.100.1:1234"
		w := httptest.NewRecorder()
		a.InferenceHandler().ServeHTTP(w, r)
		return w.Code
	}
	if peerStatus(first) != 401 || peerStatus(second) != 403 {
		t.Fatal("inference settings shared")
	}
	// Different authentication modes on one database retain their own behavior.
	for i, a := range []*App{first, second} {
		w := httptest.NewRecorder()
		a.AuthMiddleware(a.Handler()).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/me", nil))
		if (w.Code == 401) != (i == 0) {
			t.Fatalf("instance %d authentication: %d", i, w.Code)
		}
	}
	first.setup.agentImage = "failed"
	if second.setup.agentImage != "" {
		t.Fatal("setup status shared")
	}
}

func TestInvalidConfigurationFailsBeforeConstruction(t *testing.T) {
	t.Parallel()
	for _, cfg := range []Config{
		{AuthMode: "typo"}, {TierDomains: []string{"bogus=example.com"}},
		{InferenceURL: "http://worker:7779", InferenceNetworks: []string{"0.0.0.0/0"}},
		{InferenceURL: "https://worker/path", InferenceNetworks: []string{"192.0.2.0/24"}},
	} {
		// Nil DB is safe because configuration must fail before wiring a component.
		if a, err := New(context.Background(), nil, cfg); a != nil || err == nil {
			t.Fatalf("invalid configuration reached construction: %v %v", a, err)
		}
	}
}

func TestBuiltinDatabaseOwnershipIsInstanceLocal(t *testing.T) {
	t.Parallel()
	var apps []*App
	var dumps [2]int
	for i, enabled := range []bool{true, false} {
		f := newFakeDocker()
		dir := t.TempDir()
		if enabled {
			dir = builtinDataDir(t) // db.env names the database, as its first start leaves it
		}
		a := testApp(t, Config{UsingBuiltinDB: enabled, DockerClient: f, DataDir: dir})
		place, _ := dumpsOn(t, false, func(execution.TaskSpec) ([]byte, int, string) {
			dumps[i]++
			return gz(t, "-- fixture database dump\n"), 0, ""
		})
		a.dumps = *place
		a.SeedTargets(t.Context())
		apps = append(apps, a)
	}
	for i, a := range apps {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/infrastructure", nil))
		if w.Code != 200 || strings.Contains(w.Body.String(), `"name":"apptown-db"`) != (i == 0) {
			t.Fatalf("App %d database inventory: %d %s", i, w.Code, w.Body.String())
		}
		receipt, err := apptownJobKinds(a)["db-backup"].Run(t.Context(), "daily", nil)
		if err != nil || (receipt["took"] != nil) != (i == 0) || (receipt["skipped"] != nil) != (i == 1) {
			t.Fatalf("App %d database backup: %v %v", i, receipt, err)
		}
	}
	if dumps != [2]int{1, 0} {
		t.Fatalf("database backup ownership crossed instances: %v", dumps)
	}
}

// Construction leaves the key alone; the startup step that opens it refuses
// a key it cannot read, and creates one it can reuse.
func TestRuntimeTokenKeyIsAStartupStepThatRefusesAnUnreadableKey(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, identity.RuntimeTokenKeyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	a, err := New(context.Background(), db, Config{DataDir: dir, Stock: testStock(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RecoverInterruptedWork(t.Context()); err == nil {
		t.Fatal("started with a key it cannot read")
	}
	fresh, err := New(context.Background(), db, Config{DataDir: t.TempDir(), Stock: testStock(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fresh.cfg.dataDir, identity.RuntimeTokenKeyFile)); err != nil {
		t.Fatalf("no key kept: %v", err)
	}
}

// The live links in the guides name the addresses this AppTown actually
// serves, at paths its route table answers with the MCP and Git doors, and
// only when -public-host stated the whole origin. A bare name says nothing
// about the scheme, and the tier domains describe deployed apps, not the
// a TLS AppTown with no tier domains, or with http ones, must not be
// linked as http. Those cases leave the guides to say "your AppTown's address".
func TestGuideLinksNameTheAddressesAppTownServes(t *testing.T) {
	t.Parallel()
	links := func(c Config) httpapi.DocLinks {
		t.Helper()
		cfg, err := c.prepare()
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", "/api/v1/docs/guides/quickstart", nil)
		r.SetPathValue("slug", "quickstart")
		w := httptest.NewRecorder()
		docsForRoutes(cfg.publicOrigin()).GuideDoc(w, r)
		var got httpapi.DocGuide
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Links
	}
	got := links(Config{PublicHost: "https://forge.example", TierDomains: []string{"preview=http://preview.example"}})
	if got.Origin.URL != "https://forge.example" {
		t.Errorf("stated origin with http tiers = %q", got.Origin.URL)
	}
	served := func(url string) string {
		t.Helper()
		path, ok := strings.CutPrefix(url, got.Origin.URL)
		if !ok {
			t.Fatalf("%s is not on AppTown's origin", url)
		}
		return servedPattern(path)
	}
	if p := served(got.MCP.URL); p != mcpPath {
		t.Errorf("MCP link %s reaches route %q, not the MCP door", got.MCP.URL, p)
	}
	if p := served(got.Git.URL + "/demo/info/refs"); p != "/git/{slug}/{gitpath...}" {
		t.Errorf("Git link %s/demo reaches route %q, not the Git door", got.Git.URL, p)
	}
	if port := links(Config{PublicHost: "http://forge.example:8080/"}); port.MCP.URL != "http://forge.example:8080/mcp/user" {
		t.Errorf("stated http origin with a port: %q", port.MCP.URL)
	}
	for name, c := range map[string]Config{
		"no public host":          {},
		"bare name, no tiers":     {PublicHost: "forge.example"},
		"bare name, https tiers":  {PublicHost: "forge.example", TierDomains: []string{"preview=preview.example"}},
		"bare name, http tiers":   {PublicHost: "forge.example", TierDomains: []string{"preview=http://preview.example"}},
		"bare name with its port": {PublicHost: "forge.example:8443"},
	} {
		if l := links(c); l.Origin.URL != "" || l.MCP.URL != "" || l.Git.URL != "" {
			t.Errorf("%s: guessed %+v", name, l)
		}
	}
}

// -public-host takes a bare name or a whole origin; anything else is refused
// at start-up rather than half-used, and every other consumer of the setting
// keeps getting the bare host.
func TestPublicHostTakesANameOrAnOrigin(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string][2]string{
		"forge.example":             {"", "forge.example"},
		"forge.example:8443":        {"", "forge.example:8443"},
		"https://forge.example":     {"https", "forge.example"},
		"https://forge.example/":    {"https", "forge.example"},
		"http://forge.example:8080": {"http", "forge.example:8080"},
	} {
		cfg, err := Config{PublicHost: raw}.prepare()
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if cfg.publicHostScheme != want[0] || cfg.publicHost != want[1] || cfg.routing().PublicHost != want[1] {
			t.Errorf("%s: scheme %q host %q routing %q, want %v", raw, cfg.publicHostScheme, cfg.publicHost, cfg.routing().PublicHost, want)
		}
	}
	for _, raw := range []string{"ftp://forge.example", "https://", "https://forge.example/forge", "https://u:p@forge.example", "https://forge.example?x=1"} {
		if err := (Config{PublicHost: raw}).Validate(); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
}

// servedPattern is the route-table pattern a GET of path reaches.
func servedPattern(path string) string {
	mux := http.NewServeMux()
	for _, route := range appRoutes(nil) {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Pattern) })
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w.Body.String()
}

// Every owner the App assembles reads AppTown's clock: no clock it holds
// (a func() time.Time field, by any name) is left nil, which would read the
// machine's instead. The walk follows the App's owners through pointers,
// structs and interfaces within AppTown's own packages, breadth first.
// It proves a binding is present, not that it is the App's clock, and sees
// only clocks held in fields, not ones a function value captured. An App
// with a worker is walked too: what it builds only for a worker (the
// worker's chats) is not in an App without one.
func TestEveryOwnerTheAppAssemblesHasAppTownsClock(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	srv := workertest.Start(t, ca, t.TempDir(), nil)
	for name, cfg := range map[string]Config{
		"without a worker": {},
		"with a worker":    {ConsoleCLI: fixtureCLI(t), Worker: workerClientConfig(t, ca, srv.URL)},
	} {
		if unwired := unwiredClocks(reflect.ValueOf(testApp(t, cfg)), "App"); len(unwired) > 0 {
			t.Errorf("%s: owners holding no clock, so reading the machine's:\n%s", name, strings.Join(unwired, "\n"))
		}
	}
}

// lazyOwners are the root's constructors called when an operation needs the
// owner (func xFor(a *App) T, called outside app.go), so the App's graph
// never holds what they build: the walk reaches them only through this
// table. One called only by New is built once and held by the App.
var lazyOwners = map[string]func(*App) any{
	"infrastructureChecksFor": func(a *App) any { return infrastructureChecksFor(a) },
	"changePreviewViewsFor":   func(a *App) any { return changePreviewViewsFor(a) },
	"chatsFor":                func(a *App) any { return chatsFor(a) },
	"checksFor":               func(a *App) any { return checksFor(a) },
	"connectorHealthFor":      func(a *App) any { return connectorHealthFor(a) },
	"contributionsFor":        func(a *App) any { return contributionsFor(a) },
	"fleetReviewsFor":         func(a *App) any { return fleetReviewsFor(a) },
	"holderPresenceFor":       func(a *App) any { return holderPresenceFor(a) },
	"inferenceIdentityFor":    func(a *App) any { return inferenceIdentityFor(a) },
	"inferenceRuntimeFor":     func(a *App) any { return inferenceRuntimeFor(a) },
	"mcpToolsFor":             func(a *App) any { return mcpToolsFor(a) },
	"mergeEffectsFor":         func(a *App) any { return mergeEffectsFor(a) },
	"piHomeFor":               func(a *App) any { return piHomeFor(a) },
	"projectCleanupFor":       func(a *App) any { return projectCleanupFor(a) },
	"runtimeFor":              func(a *App) any { return runtimeFor(a) },
	"selfErrorsFor":           func(a *App) any { return selfErrorsFor(a) },
	"upliftSettlementFor":     func(a *App) any { return upliftSettlementFor(a) },
	"workResourcesFor":        func(a *App) any { return workResourcesFor(a) },
}

// What the root builds on demand reads AppTown's clock too. The table is
// held to the source: every func xFor(a *App) T the root's code calls
// outside app.go is in it.
func TestEveryOwnerTheRootBuildsOnDemandHasAppTownsClock(t *testing.T) {
	t.Parallel()
	declared := map[string]bool{}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasSuffix(fn.Name.Name, "For") || len(fn.Type.Params.List) != 1 || fn.Type.Results == nil {
				continue
			}
			if star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok && id.Name == "App" {
					declared[fn.Name.Name] = true
				}
			}
		}
	}
	var outside strings.Builder
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "app.go" {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		outside.Write(body)
	}
	for name := range declared {
		if !strings.Contains(outside.String(), name+"(a)") {
			delete(declared, name) // built once by New, and held by the App
		}
	}
	for name := range declared {
		if lazyOwners[name] == nil {
			t.Errorf("%s builds an owner on demand and is not in lazyOwners", name)
		}
	}
	for name := range lazyOwners {
		if !declared[name] {
			t.Errorf("lazyOwners names %s, which the root no longer builds on demand", name)
		}
	}
	a := testApp(t)
	var unwired []string
	for name, build := range lazyOwners {
		unwired = append(unwired, unwiredClocks(reflect.ValueOf(build(a)), name)...)
	}
	if len(unwired) > 0 {
		sort.Strings(unwired)
		t.Fatalf("owners built on demand holding no clock:\n%s", strings.Join(unwired, "\n"))
	}
}

// unwiredClocks walks v breadth first through pointers, structs and
// interfaces in AppTown's own packages and names every func() time.Time
// field left nil.
func unwiredClocks(root reflect.Value, name string) []string {
	clockType := reflect.TypeOf((func() time.Time)(nil))
	type item struct {
		v    reflect.Value
		path string
	}
	seen := map[[2]uintptr]bool{}
	queue := []item{{root, name}}
	var unwired []string
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		v := it.v
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				continue
			}
			key := [2]uintptr{v.Pointer(), reflect.ValueOf(v.Type()).Pointer()}
			if seen[key] {
				continue
			}
			seen[key] = true
			queue = append(queue, item{v.Elem(), it.path})
		case reflect.Interface:
			if !v.IsNil() {
				queue = append(queue, item{v.Elem(), it.path})
			}
		case reflect.Struct:
			if !strings.HasPrefix(v.Type().PkgPath(), "github.com/flokkit/apptown/") {
				continue
			}
			for i := range v.NumField() {
				f, ft := v.Field(i), v.Type().Field(i)
				if ft.Type == clockType {
					if f.IsNil() {
						unwired = append(unwired, it.path+"."+ft.Name)
					}
					continue
				}
				queue = append(queue, item{f, it.path + "." + ft.Name})
			}
		}
	}
	sort.Strings(unwired)
	return unwired
}

// No container runs unbounded: an AppTown given no limits bounds every step,
// check and task container (here and in what it sends a worker) by the
// execution defaults, and a chat's container on its own host by the chat
// defaults; given limits, it uses them.
func TestEveryContainerAppTownStartsIsBounded(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	if a.processRunner.Limits != execution.DefaultTaskLimits || a.tasks.Profile.Limits != execution.DefaultTaskLimits || a.cfg.chatLimits != execution.DefaultChatLimits {
		t.Fatalf("defaults: runner %+v, tasks %+v, chats %+v", a.processRunner.Limits, a.tasks.Profile.Limits, a.cfg.chatLimits)
	}
	given := execution.Limits{CPUs: 1, MemoryBytes: 1 << 30, PIDs: 64}
	b := testApp(t, Config{TaskLimits: given, ChatLimits: given})
	if b.processRunner.Limits != given || b.tasks.Profile.Limits != given || b.cfg.chatLimits != given {
		t.Fatalf("given: runner %+v, tasks %+v, chats %+v", b.processRunner.Limits, b.tasks.Profile.Limits, b.cfg.chatLimits)
	}
}

// hostInfoDocker answers docker info as a daemon with cpus CPUs, and nothing else.
type hostInfoDocker struct{ cpus string }

func (d hostInfoDocker) Run(_ context.Context, _ string, req docker.Command) ([]byte, error) {
	if len(req.Args) > 0 && req.Args[0] == "info" {
		return []byte("4294967296\t" + d.cpus + "\t/var/lib/docker\n"), nil
	}
	return nil, errors.New("unexpected docker call")
}

// Steps and chats on AppTown's own Docker never ask for more CPUs than its
// host has: Docker refuses such a container, so the default 4 on a 2-CPU
// server refused every scan (one-server test, 22 Oct). A limit within the
// host is kept, and steps on a worker are the worker's host's, not this
// one's.
//
// Kills: the task limit passed to Docker as configured whatever the host.
func TestStepLimitsFitTheDockerHostsCPUs(t *testing.T) {
	t.Parallel()
	small := Config{DockerClient: hostInfoDocker{"2"}}
	small.FitLimitsToHost(t.Context())
	if small.TaskLimits.CPUs != 2 || small.ChatLimits != (execution.Limits{}) {
		t.Fatalf("on a 2-CPU host: steps %v CPUs, chats %+v (their default 2 fits)", small.TaskLimits.CPUs, small.ChatLimits)
	}
	tiny := Config{DockerClient: hostInfoDocker{"1"}}
	tiny.FitLimitsToHost(t.Context())
	if tiny.TaskLimits.CPUs != 1 || tiny.ChatLimits.CPUs != 1 {
		t.Fatalf("on a 1-CPU host: steps %v, chats %v CPUs", tiny.TaskLimits.CPUs, tiny.ChatLimits.CPUs)
	}
	if small.TaskLimits.MemoryBytes != execution.DefaultTaskLimits.MemoryBytes {
		t.Fatalf("fitting the CPUs changed the memory limit: %+v", small.TaskLimits)
	}

	explicit := Config{DockerClient: hostInfoDocker{"8"}, TaskLimits: execution.Limits{CPUs: 3, MemoryBytes: 1 << 30, PIDs: 10}}
	explicit.FitLimitsToHost(t.Context())
	if explicit.TaskLimits.CPUs != 3 {
		t.Fatalf("a limit within the host was changed: %+v", explicit.TaskLimits)
	}

	onWorker := Config{DockerClient: hostInfoDocker{"1"}, Worker: &WorkerConfig{URL: "https://worker:8443"}}
	onWorker.FitLimitsToHost(t.Context())
	if onWorker.TaskLimits != (execution.Limits{}) {
		t.Fatalf("steps on a worker were fitted to this host: %+v", onWorker.TaskLimits)
	}

	unread := Config{DockerClient: hostInfoDocker{"not a number"}}
	unread.FitLimitsToHost(t.Context())
	if unread.TaskLimits != (execution.Limits{}) {
		t.Fatalf("a host that could not be read changed the limits: %+v", unread.TaskLimits)
	}
}

// The maintenance sweep removes leftovers unless the machine's owner
// started AppTown with -disable-reap; then it only logs them.
//
// Kills: Config.DisableReap not carried into the configuration the
// scheduler reads, or cleanup off by default.
func TestTheSweepRemovesUnlessStartedWithDisableReap(t *testing.T) {
	t.Parallel()
	if testApp(t).cfg.disableReap {
		t.Fatal("an AppTown started without -disable-reap only logs leftovers")
	}
	if !testApp(t, Config{DisableReap: true}).cfg.disableReap {
		t.Fatal("an AppTown started with -disable-reap removes leftovers")
	}
}

// A backup whose record cannot be written fails its job: the file is a
// good backup, but nothing says it was taken.
func TestABackupWithoutItsRecordFailsItsJob(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{UsingBuiltinDB: true, DockerClient: newFakeDocker(), DataDir: builtinDataDir(t)})
	place, _ := dumpsOn(t, false, func(execution.TaskSpec) ([]byte, int, string) { return gz(t, "-- fixture database dump\n"), 0, "" })
	a.dumps = *place
	restore := a.DB.FailNextStatement("-- name: AppendReceipt")
	_, err := apptownJobKinds(a)["db-backup"].Run(t.Context(), "daily", nil)
	restore()
	if err == nil {
		t.Fatal("a backup whose record failed reported success")
	}
}
