package forge

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// fakePreviewWorker answers the worker's preview endpoints on the wire (the
// real worker's are tested in internal/worker): it records each boot with
// its spec, prepared value and tree, each removal, and each request
// forwarded to a preview, and answers from a script. down makes it refuse
// everything, as a worker that cannot be asked.
type fakePreviewWorker struct {
	mu        sync.Mutex
	boots     []fakePreviewBoot
	removed   []string // names; "*" is every preview
	running   map[string]worker.PreviewState
	down      bool
	stateDown bool                                              // the list answers, a preview's state does not
	refused   int                                               // requests refused while down
	answer    func(spec worker.PreviewSpec) worker.PreviewState // nil: running, as asked
	forwarded []http.Header
	states    int                                                     // GETs answered
	onState   func(n int, st worker.PreviewState) worker.PreviewState // rewrites the n-th state answer
	app       http.Handler
}

type fakePreviewBoot struct {
	spec     worker.PreviewSpec
	prepared docker.Prepared
	files    map[string]string
}

func (f *fakePreviewWorker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/preview-admission" {
			f.mu.Lock()
			down := f.down
			if down {
				f.refused++
			}
			f.mu.Unlock()
			if down {
				http.Error(w, "the worker is down", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r) // real admission checks the fake worker daemon
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/previews") {
			next.ServeHTTP(w, r)
			return
		}
		f.serve(w, r)
	})
}

func (f *fakePreviewWorker) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	down := f.down
	if down {
		f.refused++
	}
	f.mu.Unlock()
	if down {
		http.Error(w, "the worker is down", http.StatusServiceUnavailable)
		return
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v1/previews"), "/")
	name, sub, _ := strings.Cut(rest, "/")
	answer := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case sub == "http":
		f.mu.Lock()
		f.forwarded = append(f.forwarded, r.Header.Clone())
		app := f.app
		f.mu.Unlock()
		target, err := url.ParseRequestURI(r.Header.Get(worker.PreviewTargetHeader))
		if err != nil || app == nil {
			http.Error(w, "no target", http.StatusBadRequest)
			return
		}
		r.URL = target
		app.ServeHTTP(w, r)
	case r.Method == http.MethodPut:
		boot, err := readFakeBoot(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.boots = append(f.boots, boot)
		st := worker.PreviewState{Running: true, Incarnation: boot.spec.Incarnation, ContainerID: "c-" + name}
		if f.answer != nil {
			st = f.answer(boot.spec)
		}
		if st.Running {
			if f.running == nil {
				f.running = map[string]worker.PreviewState{}
			}
			f.running[name] = st
		}
		f.mu.Unlock()
		answer(st)
	case r.Method == http.MethodGet && name == "":
		f.mu.Lock()
		names := slices.Sorted(maps.Keys(f.running))
		f.mu.Unlock()
		if names == nil {
			names = []string{}
		}
		answer(names)
	case r.Method == http.MethodGet:
		f.mu.Lock()
		if f.stateDown {
			f.mu.Unlock()
			http.Error(w, "the preview's state cannot be read", http.StatusServiceUnavailable)
			return
		}
		f.states++
		st := f.running[name]
		if f.onState != nil {
			st = f.onState(f.states, st)
		}
		f.mu.Unlock()
		answer(st)
	case r.Method == http.MethodDelete:
		f.mu.Lock()
		if name == "" {
			f.removed = append(f.removed, "*")
			f.running = nil
		} else {
			f.removed = append(f.removed, name)
			delete(f.running, name)
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "not a preview request", http.StatusNotFound)
	}
}

// readFakeBoot reads a boot request as the worker does: spec, prepared
// value, then the tree.
func readFakeBoot(r *http.Request) (fakePreviewBoot, error) {
	var b fakePreviewBoot
	mr, err := r.MultipartReader()
	if err != nil {
		return b, err
	}
	for _, into := range []any{&b.spec, &b.prepared} {
		p, err := mr.NextPart()
		if err != nil {
			return b, err
		}
		if err := json.NewDecoder(p).Decode(into); err != nil {
			return b, err
		}
	}
	p, err := mr.NextPart()
	if err != nil {
		return b, err
	}
	b.files = map[string]string{}
	tr := tar.NewReader(p)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return b, err
		}
		if h.Typeflag == tar.TypeReg {
			body, _ := io.ReadAll(io.LimitReader(tr, 1<<20))
			b.files[strings.TrimPrefix(h.Name, "./")] = string(body)
		}
	}
	return b, nil
}

func (f *fakePreviewWorker) bootsNamed(name string) []fakePreviewBoot {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakePreviewBoot
	for _, b := range f.boots {
		if b.spec.Name == name {
			out = append(out, b)
		}
	}
	return out
}

func (f *fakePreviewWorker) removals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

func (f *fakePreviewWorker) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

// withPreviewWorker configures cfg with a worker served by fake over the
// worker's mutual TLS, as a forge is configured with one (-worker-*): its
// address, this installation's client certificate, and the CA.
func withPreviewWorker(t *testing.T, fake *fakePreviewWorker, cfg Config) Config {
	t.Helper()
	ca := workertest.NewCA(t)
	srv := workertest.StartServerWith(t, ca, &worker.Server{State: t.TempDir(), Previews: newFakeDocker()}, fake.wrap)
	cfg.Worker = workerClientConfig(t, ca, srv.URL)
	return cfg
}

const staticSiteContract = "version: 1\napp:\n  web:\n    static: .\n    public: true\n"

// previewWorkerWorld is a forge with a worker for previews (and a preview
// domain unless noDomain), a local Docker that records everything and runs
// nothing real, and a change of the game project whose folder declares a
// static site.
func previewWorkerWorld(t *testing.T, noDomain bool) (*App, *fakeDocker, *fakePreviewWorker, *workflow.Change) {
	t.Helper()
	fake := &fakePreviewWorker{}
	local := newFakeDocker()
	cfg := Config{DockerClient: local, DockerProbe: &docker.Probe{Settle: 1}}
	if !noDomain {
		cfg.TierDomains = []string{"preview=preview.example.com"}
	}
	a := workAuthorityFixture(t, withPreviewWorker(t, fake, cfg))
	if !a.Preview.PreviewsOnWorker() {
		t.Fatal("an AppTown with a worker keeps its previews on this machine")
	}
	ch, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "Try it on the worker"})
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if err := os.MkdirAll(filepath.Join(folder, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{".apptown/app.yaml": staticSiteContract, "index.html": "<html>the game</html>"} {
		if err := os.WriteFile(filepath.Join(folder, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return a, local, fake, ch
}

// With a worker, the try-it button boots the change's preview there, with
// the change's tree and the values prepared here, and reaches it through
// the preview domain; this machine's Docker runs nothing.
func TestTheTryItButtonBootsOnTheWorkerAndNowhereElse(t *testing.T) {
	t.Parallel()
	a, local, fake, ch := previewWorkerWorld(t, false)
	resp := authzCall(a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/preview", ch.ID), `{}`)
	if resp.Code != 200 {
		t.Fatalf("try it: %d %s", resp.Code, resp.Body.String())
	}
	var out struct{ URL string }
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil || out.URL != fmt.Sprintf("https://change-%d.preview.example.com", ch.ID) {
		t.Fatalf("the preview's address: %q %v", out.URL, err)
	}
	boots := fake.bootsNamed(docker.PreviewChangeName(ch.ID))
	if len(boots) != 1 {
		t.Fatalf("boots on the worker: %d", len(boots))
	}
	b := boots[0]
	if b.spec.StackID != docker.ChangeStackID(ch.ID) || b.spec.Incarnation == "" || b.prepared.Contract == nil || b.prepared.Contract.PublicPart() == nil ||
		b.prepared.Contract.PublicPart().Static == "" || b.files[".apptown/app.yaml"] != staticSiteContract || b.files["index.html"] == "" {
		t.Fatalf("the boot sent: %+v %+v", b.spec, b.files)
	}
	if runs := local.matching("run "); len(runs) != 0 {
		t.Fatalf("this machine's Docker ran: %q", runs)
	}
}

// Previews on a worker are reached only through the preview domain: a forge
// without one refuses them with that reason, and boots nothing anywhere.
func TestWithoutAPreviewDomainAWorkerPreviewIsRefused(t *testing.T) {
	t.Parallel()
	a, local, fake, ch := previewWorkerWorld(t, true)
	resp := authzCall(a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/preview", ch.ID), `{}`)
	if resp.Code == 200 || !strings.Contains(resp.Body.String(), "preview domain") {
		t.Fatalf("try it without a preview domain: %d %s", resp.Code, resp.Body.String())
	}
	if len(fake.bootsNamed(docker.PreviewChangeName(ch.ID))) != 0 || len(local.matching("run ")) != 0 {
		t.Fatal("a preview booted without a preview domain")
	}
}

// The change page says what the worker says: live, at the preview domain,
// when the preview runs there; not known, with the reason, when the worker
// cannot be asked, never "cold" or "broken".
func TestTheChangePageShowsTheWorkersPreviewState(t *testing.T) {
	t.Parallel()
	a, _, fake, ch := previewWorkerWorld(t, false)
	state := func() (st struct{ State, URL, Reason string }) {
		t.Helper()
		resp := authzCall(a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/preview", ch.ID), "")
		if resp.Code != 200 {
			t.Fatalf("preview state: %d %s", resp.Code, resp.Body.String())
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := state(); st.State != "cold" {
		t.Fatalf("before any boot: %+v", st)
	}
	if resp := authzCall(a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/preview", ch.ID), `{}`); resp.Code != 200 {
		t.Fatalf("try it: %d %s", resp.Code, resp.Body.String())
	}
	if st := state(); st.State != "live" || st.URL != fmt.Sprintf("https://change-%d.preview.example.com", ch.ID) {
		t.Fatalf("running on the worker: %+v", st)
	}
	fake.setDown(true)
	if st := state(); st.State != "unavailable" || st.Reason == "" {
		t.Fatalf("a worker that cannot be asked: %+v", st)
	}
}

// The tier map and the departures board list the previews running on the
// worker, asked through their owner; a worker that cannot be asked makes
// the preview collection unknown on both, never an empty one.
func TestTheTierViewsListTheWorkersPreviewsOrSayTheyAreUnknown(t *testing.T) {
	t.Parallel()
	a, _, fake, ch := previewWorkerWorld(t, false)
	if resp := authzCall(a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/preview", ch.ID), `{}`); resp.Code != 200 {
		t.Fatalf("try it: %d %s", resp.Code, resp.Body.String())
	}
	testAdministrator(t, a, "boss")
	views := func() (tiers httpapi.TierMap, board httpapi.Departures) {
		t.Helper()
		for path, out := range map[string]any{"/api/v1/tiers": &tiers, "/api/v1/tiers/departures": &board} {
			resp := authzCall(a.Handler(), "boss", "GET", path, "")
			if err := json.Unmarshal(resp.Body.Bytes(), out); err != nil || resp.Code != 200 {
				t.Fatalf("%s: %d %s", path, resp.Code, resp.Body.String())
			}
		}
		return tiers, board
	}
	tiers, board := views()
	// The preview names its change by number, and carries the change's id
	// for the link rather than leaving the page to read it out of prose.
	p := tiers.Tiers[0]
	if p.Name != "preview" || p.Observed != "" || len(p.Apps) != 1 || p.Apps[0].ChangeID != ch.ID || p.Apps[0].ChangeNumber != ch.Number ||
		!strings.HasPrefix(p.Apps[0].Detail, fmt.Sprintf("change %d: ", ch.Number)) {
		t.Fatalf("preview tier: %+v", p)
	}
	want := fmt.Sprintf("preview for change %s#%d", p.Apps[0].Slug, ch.Number)
	if board.PreviewsObserved != "" || !slices.ContainsFunc(board.Departures, func(d httpapi.Departure) bool { return d.Label == want }) {
		t.Fatalf("departures: %+v", board)
	}
	fake.setDown(true)
	tiers, board = views()
	if p := tiers.Tiers[0]; p.Observed != "unknown" || p.Reason == "" || len(p.Apps) != 0 {
		t.Fatalf("preview tier with the worker down: %+v", p)
	}
	if board.PreviewsObserved != "unknown" || board.PreviewsReason == "" || slices.ContainsFunc(board.Departures, func(d httpapi.Departure) bool { return d.Label == want }) {
		t.Fatalf("departures with the worker down: %+v", board)
	}
}

// Everything that ends a preview ends it on the worker: closing its change,
// finishing its run, removing its project, and the forge starting again
// (which discards every preview there).
func TestEndingAPreviewRemovesItOnTheWorker(t *testing.T) {
	t.Parallel()
	a, _, fake, ch := previewWorkerWorld(t, false)
	if err := workResourcesFor(a).Change(context.Background(), "game", ch.ID); err != nil {
		t.Fatal(err)
	}
	workResourcesFor(a).Run(context.Background(), idstest.N(41))
	if failed := projectCleanupFor(a).Clean(context.Background(), idstest.N(1), projectlifecycle.CleanupPlan{Slug: "game", RunIDs: []ids.ID{idstest.N(42)}, ChangeIDs: []ids.ID{idstest.N(43)}}); len(failed) != 0 {
		t.Fatalf("cleanup: %q", failed)
	}
	a.ReapPreviewOrphans(context.Background())
	got := strings.Join(fake.removals(), " ")
	for _, want := range []string{docker.PreviewChangeName(ch.ID), docker.PreviewRunName(idstest.N(41)), docker.PreviewRunName(idstest.N(42)), docker.PreviewChangeName(idstest.N(43)), "*"} {
		if !strings.Contains(" "+got+" ", " "+want+" ") {
			t.Errorf("not removed on the worker: %s (removed: %s)", want, got)
		}
	}
}

// gatePlace is where a runtime gate's preview runs, and what happens to it
// between its boot and the gate.
type gatePlace struct {
	onWorker  bool
	afterBoot func(t *testing.T, ws string, local *fakeDocker, fake *fakePreviewWorker)
	booted    func(boot *workflow.Step) // the preview step, as it passed
}

// runRuntimeGate runs a change's lane: its preview step boots the run's
// tree (here, or on the worker), afterBoot does what it does, and the
// guidance step judges the static page's browser values against that
// preview. It answers the saved gate and the check's detail.
func runRuntimeGate(t *testing.T, place gatePlace) (*workflow.Step, string, string) {
	t.Helper()
	const given = `{"GLITCHTIP_DSN":"https://key@errors.example/3","SENTRY_DSN":"https://key@errors.example/3"}`
	const loads = `<html><head><script src="/_apptown/env.js"></script><script src="game.js"></script></head></html>`
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_apptown/env.js" {
			w.Write([]byte(docker.BrowserEnvScript(given)))
			return
		}
		w.Write([]byte(loads))
	})
	server := httptest.NewServer(page)
	t.Cleanup(server.Close)
	daemon := newFakeDocker()
	daemon.port = server.Listener.Addr().(*net.TCPAddr).Port
	cfg := Config{DockerClient: daemon, DockerProbe: &docker.Probe{Settle: time.Millisecond}}
	var fake *fakePreviewWorker
	if place.onWorker {
		var values map[string]string
		_ = json.Unmarshal([]byte(given), &values)
		fake = &fakePreviewWorker{app: page, answer: func(spec worker.PreviewSpec) worker.PreviewState {
			return worker.PreviewState{Running: true, Incarnation: spec.Incarnation, ContainerID: "c1", BrowserValues: values}
		}}
		cfg.TierDomains = []string{"preview=preview.example.com"}
		cfg = withPreviewWorker(t, fake, cfg)
	}
	app := testApp(t, cfg)
	app.SyncStockDependencies(context.Background()) // error reporting's definition says what the page is given
	stages := []schema.Stage{{Name: "wire", Steps: []schema.Step{{Name: "boot", Uses: "preview"}, {Name: "guidance-conformance", Uses: "guidance/conformance"}}}}
	a, ctx, ch := changeScenarioIn(t, app, schema.Spec{Kind: "pipeline", Role: "working", Stages: stages})
	adopt(t, a, ch.ProjectID, "deploy/browser-values", 1, `[{"name":"page-loads-browser-values","expects":"loads the public values","evaluator":"runtime","probe":"browser-env","severity":"high"}]`)
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"guidance/conformance", "preview"}
		deps.Runners = a.workDependencies().Runners
		deps.Report = a.workDependencies().Report
	})
	a.Work.Stop()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "test"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if err := a.Integrations.Attachments.Remember(ctx, ch.ProjectID, "errors", "glitchtip", `{"dsn":"https://key@errors.example/3"}`); err != nil {
		t.Fatal(err)
	}
	source, err := workflow.ReadSourceIn(context.Background(), a.DB, workflow.RunRef{ID: run.ID, ProjectID: ch.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	ws := source.Workspace(a.cfg.dataDir)
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte("app:\n  web: {static: ., public: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitFolder(t, ws)
	snap, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var gate, boot ids.ID
	for _, st := range snap.Steps {
		switch st.Name {
		case "guidance-conformance":
			gate = st.ID
		case "boot":
			boot = st.ID
		}
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), boot); err != nil {
		t.Fatal(err)
	}
	if booted, err := workflow.ReadStepIn(context.Background(), a.DB, boot); err != nil || booted.Status != workflow.StatusPass {
		t.Fatalf("the preview did not boot: %v %+v", err, booted)
	}
	name := docker.PreviewRunName(run.ID)
	if !place.onWorker {
		daemon.markStartedWithBrowser(name, given)
	}
	if place.afterBoot != nil {
		place.afterBoot(t, ws, daemon, fake)
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), gate); err != nil {
		t.Fatal(err)
	}
	saved, err := workflow.ReadStepIn(context.Background(), a.DB, gate)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct{ Checks verification.Results }
	if err := json.Unmarshal(saved.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	for _, r := range evidence.Checks {
		if r.Check == "page-loads-browser-values" {
			return saved, r.Verdict.String(), r.Detail
		}
	}
	t.Fatalf("the check was not judged: %s", saved.Evidence)
	return nil, "", ""
}

// editTree commits a change to the run's tree after its preview booted: the
// preview now serves an earlier commit than the one checked.
func editTree(t *testing.T, ws string, _ *fakeDocker, _ *fakePreviewWorker) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws, "later.txt"), []byte("an edit after the boot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitFolder(t, ws)
}

// A runtime check judges only a preview that serves the run's tree as it is
// now (the source its incarnation names), here or on the worker; one that
// serves another version, or is replaced while it is probed, is not judged
// (cannot assess), never passed or failed.
func TestRuntimeChecksJudgeOnlyThePreviewOfTheRunsTree(t *testing.T) {
	t.Parallel()
	replacedHere := func(t *testing.T, _ string, local *fakeDocker, _ *fakePreviewWorker) {
		asked := 0
		local.reply = func(args []string) (string, error, bool) {
			if len(args) > 3 && args[0] == "inspect" && args[2] == docker.RuntimeFormat {
				asked++
				if asked == 2 { // the preview opened; then another container of its name
					local.mu.Lock()
					if local.ids == nil {
						local.ids = map[string]string{}
					}
					local.ids[args[len(args)-1]] = "fake-replaced"
					local.mu.Unlock()
				}
			}
			return "", nil, false
		}
	}
	replacedThere := func(t *testing.T, _ string, _ *fakeDocker, fake *fakePreviewWorker) {
		fake.mu.Lock()
		first := fake.states
		fake.onState = func(n int, st worker.PreviewState) worker.PreviewState {
			if n > first+1 && st.Running {
				st.ContainerID = "c2"
			}
			return st
		}
		fake.mu.Unlock()
	}
	for _, tc := range []struct {
		name    string
		place   gatePlace
		verdict string
		detail  string
	}{
		{"here, the run's tree", gatePlace{}, "pass", ""},
		{"here, another version", gatePlace{afterBoot: editTree}, "cannot-assess", "is not the checked commit"},
		{"here, replaced while probed", gatePlace{afterBoot: replacedHere}, "cannot-assess", "replaced"},
		{"on the worker, the run's tree", gatePlace{onWorker: true}, "pass", ""},
		{"on the worker, another version", gatePlace{onWorker: true, afterBoot: editTree}, "cannot-assess", "is not the checked commit"},
		{"on the worker, replaced while probed", gatePlace{onWorker: true, afterBoot: replacedThere}, "cannot-assess", "replaced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			saved, verdict, detail := runRuntimeGate(t, tc.place)
			if verdict != tc.verdict || !strings.Contains(detail, tc.detail) {
				t.Fatalf("want %s (%q), got %s (%q): %s", tc.verdict, tc.detail, verdict, detail, saved.Evidence)
			}
		})
	}
}

// rehearsalWorld is a chat on the worker holding its change, with previews
// on the worker too (and a preview domain), and a saved commit declaring a
// static site.
func rehearsalWorld(t *testing.T) (workerChatWorld, *fakePreviewWorker, *console.Session) {
	t.Helper()
	fake := &fakePreviewWorker{}
	w := newWorkerChatWorldConfigured(t, func(cfg *Config) Config {
		cfg.TierDomains = []string{"preview=preview.example.com"}
		return withPreviewWorker(t, fake, *cfg)
	})
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Send(context.Background(), s.ID, "go"); err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, ".apptown/app.yaml", staticSiteContract)
	return w, fake, s
}

// rehearsed is the saved commit a chat's last rehearsal booted: the source
// part of the incarnation its marker holds.
func rehearsed(s *console.Session) string {
	b, _ := os.ReadFile(filepath.Join(s.Dir, console.RehearsedFile))
	commit, _, _ := strings.Cut(strings.TrimSpace(string(b)), "+")
	return commit
}
