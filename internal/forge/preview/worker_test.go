package preview

import (
	"context"
	"errors"
	"flokkit.io/forge/internal/deploy/appcontract"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/worker"
)

// fakePreviewWorker is the worker's side of previews as the forge sees it:
// it records what it was asked, and answers as scripted.
type fakePreviewWorker struct {
	mu        sync.Mutex
	boots     []worker.PreviewSpec
	prepared  []docker.Prepared
	trees     []string
	removed   []string
	images    []string // the previews whose kept images were removed
	answer    worker.PreviewState
	state     worker.PreviewState
	err       error // every call fails, as a worker that cannot be asked
	app       *httptest.Server
	has       map[string]bool // the previews the worker has
	afterList func()          // runs once a List has answered, as the next thing to happen
	refuse    error           // what Admit answers: the worker's daemon cannot hold the contract
	admitted  int             // how many contracts were asked about
}

func (f *fakePreviewWorker) Admit(context.Context, *appcontract.Contract) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.admitted++
	if f.err != nil {
		return f.err
	}
	return f.refuse
}

func (f *fakePreviewWorker) Boot(_ context.Context, spec worker.PreviewSpec, p docker.Prepared, tree string) (worker.PreviewState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return worker.PreviewState{}, f.err
	}
	f.boots, f.prepared, f.trees = append(f.boots, spec), append(f.prepared, p), append(f.trees, tree)
	if f.has == nil {
		f.has = map[string]bool{}
	}
	f.has[spec.Name] = true
	return f.answer, nil
}

func (f *fakePreviewWorker) State(context.Context, string) (worker.PreviewState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.err
}

func (f *fakePreviewWorker) Remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.removed = append(f.removed, name)
	if name == "" {
		f.has = nil
	}
	delete(f.has, name)
	return nil
}

func (f *fakePreviewWorker) RemoveImages(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.images = append(f.images, name)
	return nil
}

func (f *fakePreviewWorker) List(context.Context) ([]string, error) {
	f.mu.Lock()
	if f.err != nil {
		f.mu.Unlock()
		return nil, f.err
	}
	names := slices.Sorted(maps.Keys(f.has))
	after := f.afterList
	f.afterList = nil
	f.mu.Unlock()
	if after != nil {
		after()
	}
	return names, nil
}

func (f *fakePreviewWorker) put(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.has == nil {
		f.has = map[string]bool{}
	}
	f.has[name] = true
}

func (f *fakePreviewWorker) holds(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.has[name]
}

func (f *fakePreviewWorker) Forward(name, publicScheme string) *httputil.ReverseProxy {
	target, _ := url.Parse(f.app.URL)
	return &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		worker.StripForwarding(r.Out.Header)
		if publicScheme != "" {
			r.SetXForwarded()
		}
	}}
}

// workerPreviewWorld is the preview world with previews on a fake worker and
// a preview domain.
func workerPreviewWorld(t *testing.T) (*Service, *fakePreviewWorker, func() []string, string, ids.ID) {
	t.Helper()
	return onWorker(t, newWorld(t, "", ""))
}

// onWorker is the preview owner over w with previews on a fake worker and
// a preview domain.
func onWorker(t *testing.T, w *world) (*Service, *fakePreviewWorker, func() []string, string, ids.ID) {
	t.Helper()
	fake := &fakePreviewWorker{answer: worker.PreviewState{Running: true, ContainerID: "c1", Incarnation: "inc", Image: "app:1", Digest: "app@sha256:1"}}
	w.env.Routes.PreviewScheme, w.env.Routes.PreviewDomain = "https", "preview.example"
	s := w.service(Dependencies{Worker: fake})
	local := func() []string {
		var runs []string
		for _, c := range previewCommands(w.client) {
			if strings.HasPrefix(c, "run ") || strings.HasPrefix(c, "build ") || strings.HasPrefix(c, "pull ") {
				runs = append(runs, c)
			}
		}
		return runs
	}
	return s, fake, local, w.dir, w.projectID
}

// With a worker, a change's preview boots there, from values prepared here
// for the contract the tree declares, and nothing runs on this machine's
// Docker; its address is the preview domain's.
func TestAChangePreviewBootsOnTheWorkerAndNowhereElse(t *testing.T) {
	t.Parallel()
	s, w, local, dir, projectID := workerPreviewWorld(t)
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	out, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://change-00000000-0009-7000-8000-000000000000.preview.example" || out.Incarnation == "" || out.Public == nil {
		t.Fatalf("the answer: %+v", out)
	}
	if len(w.boots) != 1 || w.boots[0].Name != "forge-preview-change-00000000-0009-7000-8000-000000000000" || w.boots[0].StackID != docker.ChangeStackID(idstest.N(9)) ||
		w.boots[0].Incarnation != out.Incarnation || w.prepared[0].Contract == nil || w.prepared[0].Public().Digest() != out.Public.Digest() {
		t.Fatalf("the worker was asked %+v", w.boots)
	}
	if runs := local(); len(runs) != 0 {
		t.Fatalf("this machine's Docker ran %q", runs)
	}
}

// A worker preview needs a preview domain, and the boot's own failure comes
// back as the app's, with its phase; a worker that cannot be asked is not
// the app's failure.
func TestAWorkerPreviewsFailuresSayWhoseTheyAre(t *testing.T) {
	t.Parallel()
	wd := newWorld(t, "", "")
	s, w, _, dir, projectID := onWorker(t, wd)
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	ch := Change{ID: idstest.N(9), ProjectID: projectID}

	wd.reconfigure(s, nil, func(e *deployment.EnvironmentDependencies) { e.Routes.PreviewDomain = "" })
	if _, err := s.RehearseChange(context.Background(), ch, false); !errors.Is(err, ErrNoDomain) || len(w.boots) != 0 {
		t.Fatalf("without a preview domain: %v, boots %d", err, len(w.boots))
	}
	wd.reconfigure(s, nil, func(e *deployment.EnvironmentDependencies) { e.Routes.PreviewDomain = "preview.example" })

	w.answer = worker.PreviewState{Phase: docker.PhaseProbe, Error: "no answer", LogTail: "crashed"}
	_, err := s.RehearseChange(context.Background(), ch, false)
	var be *docker.BootError
	if !errors.As(err, &be) || be.Phase != docker.PhaseProbe || !strings.Contains(err.Error(), "didn't come up") {
		t.Fatalf("a boot's own failure: %v", err)
	}

	w.err = errors.New("connection refused")
	_, err = s.RehearseChange(context.Background(), ch, false)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || errors.As(err, &be) {
		t.Fatalf("a worker that cannot be asked: %v", err)
	}
}

// A restart removes the preview on the worker first, and stops when it
// cannot; a pinned change previews its saved commit, not its folder.
func TestAWorkerPreviewRestartsAndPinsAsAsked(t *testing.T) {
	t.Parallel()
	s, w, _, dir, projectID := workerPreviewWorld(t)
	folder := filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000")
	writePreviewBootFixture(t, folder)
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, true); err != nil {
		t.Fatal(err)
	}
	if len(w.removed) != 1 || w.removed[0] != "forge-preview-change-00000000-0009-7000-8000-000000000000" || len(w.boots) != 1 {
		t.Fatalf("a restart: removed %q, boots %d", w.removed, len(w.boots))
	}
	w.err = errors.New("worker gone")
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, true); err == nil {
		t.Fatal("a restart that could not remove the preview went on")
	}
	w.err = nil

	repo := s.hosts.ProjectRepository("p")
	writePreviewBootFixture(t, repo)
	commit := gitCommitAll(t, repo, "saved")
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID, Pinned: commit}, false); err != nil {
		t.Fatal(err)
	}
	if got := w.trees[len(w.trees)-1]; got == folder || filepath.Base(got) != commit {
		t.Fatalf("a pinned preview booted %s, not the saved commit %s", got, commit)
	}
}

type claimedPreviewStep struct{ project, run, step ids.ID }

func (c claimedPreviewStep) ClaimedStep(kind string) (ids.ID, ids.ID, ids.ID, error) {
	if kind != "preview" {
		return ids.Nil, ids.Nil, ids.Nil, fmt.Errorf("claimed %s", kind)
	}
	return c.project, c.run, c.step, nil
}

// A run's preview boots on the worker with the workflow's services in its
// prepared value (and so in its identity), and reports the stack, image
// and digest the worker ran; finishing the run removes it there.
func TestARunPreviewBootsOnTheWorkerWithItsServices(t *testing.T) {
	t.Parallel()
	s, w, local, dir, projectID := workerPreviewWorld(t)
	ws := filepath.Join(dir, "workspaces", "run-00000000-0004-7000-8000-000000000000")
	writePreviewBootFixture(t, ws)
	gitCommitAll(t, ws, "the run's work")
	c, err := docker.ReadWorkspaceContract(ws)
	if err != nil {
		t.Fatal(err)
	}
	services := []deploy.ServiceSpec{{Name: "cache", Image: "redis:7"}}
	res, err := s.RunPreview(context.Background(), claimedPreviewStep{projectID, idstest.N(4), idstest.N(11)}, RunInput{Source: deploy.Source{Workspace: ws, Revision: "HEAD", Slug: "p", Contract: c}, Services: services})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.boots) != 1 || w.boots[0].Name != "forge-preview-run-00000000-0004-7000-8000-000000000000" || w.boots[0].StackID != docker.RunStepStackID(idstest.N(11)) || !w.boots[0].PageCheck {
		t.Fatalf("the worker was asked %+v", w.boots)
	}
	if w.prepared[0].Services[0].Resources.MemoryBytes != 512<<20 {
		t.Fatalf("workflow sidecar reached worker uncapped: %+v", w.prepared[0].Services)
	}
	if len(w.prepared[0].Services) != 1 || w.prepared[0].Public().Services[0].Image != "redis:7" {
		t.Fatalf("the services are not in the prepared value: %+v", w.prepared[0].Services)
	}
	if res.Digest != "app@sha256:1" || res.Boot.Stack.Plan.PublicPart().Image != "app:1" || res.Incarnation != w.boots[0].Incarnation {
		t.Fatalf("the result: %+v", res)
	}
	if runs := local(); len(runs) != 0 {
		t.Fatalf("this machine's Docker ran %q", runs)
	}
	without, err := s.preparePreview(t.Context(), projectID, "p", w.boots[0].Name, ws, c, nil)
	if err != nil || without.Public().Digest() == w.prepared[0].Public().Digest() {
		t.Fatal("the services are not part of the identity")
	}
	s.DiscardRunPreview(context.Background(), idstest.N(4), []ids.ID{idstest.N(11)})
	s.RemoveWorkerPreviews(context.Background())
	if strings.Join(w.removed, ",") != "forge-preview-run-00000000-0004-7000-8000-000000000000," {
		t.Fatalf("removed on the worker: %q", w.removed)
	}
}

// A preview on the worker is opened for probing through a naked door on
// this machine's loopback, which carries no forwarding headers; it is the
// same preview only while the worker reports the same running container.
func TestAWorkerPreviewOpensForProbing(t *testing.T) {
	t.Parallel()
	s, w, _, _, _ := workerPreviewWorld(t)
	var got http.Header
	w.app = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer w.app.Close()
	w.state = worker.PreviewState{Running: true, ContainerID: "c1", Incarnation: "tree+digest", BrowserValues: map[string]string{"A": "1"}}
	p, ok, err := s.OpenPreview(context.Background(), ids.Nil, "change-00000000-0009-7000-8000-000000000000")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	defer p.Close()
	if p.FrontDoor != "https://change-00000000-0009-7000-8000-000000000000.preview.example" || p.Incarnation != "tree+digest" || !strings.HasPrefix(p.Naked, "http://127.0.0.1:") {
		t.Fatalf("opened %+v", p)
	}
	req, _ := http.NewRequest("GET", p.Naked+"/ready", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got.Get("X-Forwarded-Proto") != "" || got.Get("X-Forwarded-For") != "" {
		t.Fatalf("the naked door carried forwarding headers: %v", got)
	}
	if b, err := p.Browser(context.Background()); err != nil || b.ID != "c1" || b.Values["A"] != "1" {
		t.Fatalf("browser values: %+v %v", b, err)
	}
	if same, err := p.Same(context.Background()); !same || err != nil {
		t.Fatal("the same preview reads as replaced")
	}
	w.state.ContainerID = "c2"
	if same, _ := p.Same(context.Background()); same {
		t.Fatal("a replaced preview reads as the same")
	}
	w.state = worker.PreviewState{}
	if _, ok, err := s.OpenPreview(context.Background(), ids.Nil, "change-00000000-0009-7000-8000-000000000000"); ok || err != nil {
		t.Fatalf("a preview that is not running opened: %v %v", ok, err)
	}
	w.err = errors.New("worker gone")
	if _, _, err := s.OpenPreview(context.Background(), ids.Nil, "change-00000000-0009-7000-8000-000000000000"); err == nil {
		t.Fatal("a worker that cannot be asked read as no preview")
	}
}

// gitCommitAll makes repo a repository holding its files, committed, and
// answers the commit.
func gitCommitAll(t *testing.T, repo, message string) string {
	t.Helper()
	if repo == "" {
		t.Fatal("no repository folder")
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", message}} {
		if out, err := gitexec.Output(t.Context(), repo, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	out, err := gitexec.Output(t.Context(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// The forge's start-up removal of the worker's previews is tried until the
// worker confirms it, and until then no preview boots there: a boot could
// otherwise keep one from before the start, or be removed by the retry.
func TestNoPreviewBootsOnTheWorkerUntilItsStartUpClearIsConfirmed(t *testing.T) {
	t.Parallel()
	s, w, _, dir, projectID := workerPreviewWorld(t)
	// The worker previews' loop, as the daemon runs it, at a test's pace.
	s.startClear.every = time.Millisecond
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	w.mu.Lock()
	w.err = errors.New("worker gone")
	w.mu.Unlock()
	s.RemoveWorkerPreviews(context.Background())
	if !s.startClearPending() {
		t.Fatal("a removal the worker did not confirm was not left pending")
	}
	w.mu.Lock()
	w.err = nil
	w.mu.Unlock()
	loop, stop := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() {
		s.RunWorkerPreviewReconciliation(loop)
		close(stopped)
	}()
	// The loop tries it again; until it lands, a boot waits for it.
	if !poll.Until(t.Context(), 5*time.Second, time.Millisecond, func() bool { return !s.startClearPending() }) {
		t.Fatal("the start-up removal was never tried again")
	}
	// The loop stops here, so the pending removal set below stays pending.
	stop()
	<-stopped
	w.mu.Lock()
	cleared := len(w.removed) == 1 && w.removed[0] == ""
	w.mu.Unlock()
	if !cleared {
		t.Fatalf("removed on the worker: %q", w.removed)
	}
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false); err != nil {
		t.Fatalf("a boot after the confirmed removal: %v", err)
	}

	// A start whose removal is not yet confirmed, with the worker answering
	// boots: the boot still waits.
	s.startClear.mu.Lock()
	s.startClear.pending = true
	s.startClear.mu.Unlock()
	_, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || len(w.boots) != 1 {
		t.Fatalf("a boot before the removal was confirmed: %v, boots %d", err, len(w.boots))
	}
}

// The forge keeps only the previews on the worker it has a record of: a
// reconciliation removes any other, and leaves the recorded ones and one
// that is booting now.
func TestTheForgeKeepsOnlyTheWorkerPreviewsItRecorded(t *testing.T) {
	t.Parallel()
	s, w, _, dir, projectID := workerPreviewWorld(t)
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.has["forge-preview-change-00000000-0005-7000-8000-000000000000"] = true // one the forge never booted, or forgot
	w.has["forge-preview-run-00000000-0003-7000-8000-000000000000"] = true    // one booting now
	w.mu.Unlock()
	release := s.holdPreview("forge-preview-run-00000000-0003-7000-8000-000000000000")
	if err := s.ReconcileWorkerPreviews(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.holds("forge-preview-change-00000000-0005-7000-8000-000000000000") || !w.holds("forge-preview-change-00000000-0009-7000-8000-000000000000") || !w.holds("forge-preview-run-00000000-0003-7000-8000-000000000000") {
		t.Fatalf("after reconciling: %v", w.has)
	}
	release()
	if err := s.ReconcileWorkerPreviews(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.holds("forge-preview-run-00000000-0003-7000-8000-000000000000") {
		t.Fatal("a preview no boot holds and AppTown has no record of was kept")
	}
}

// A removal the worker never received, however long it was away, is made
// by a later reconciliation: discarding a preview forgets its record first.
func TestALostRemovalIsMadeWhenTheWorkerReturns(t *testing.T) {
	t.Parallel()
	s, w, _, dir, projectID := workerPreviewWorld(t)
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.err = errors.New("worker gone")
	w.mu.Unlock()
	if err := s.DiscardChangePreview(context.Background(), idstest.N(9)); err == nil {
		t.Fatal("a removal the worker never received was acknowledged")
	}
	if err := s.ReconcileWorkerPreviews(context.Background()); err == nil {
		t.Fatal("a reconciliation the worker could not answer succeeded")
	}
	w.mu.Lock()
	w.err = nil
	w.mu.Unlock()
	if !w.holds("forge-preview-change-00000000-0009-7000-8000-000000000000") {
		t.Fatal("fixture: the preview is gone already")
	}
	if err := s.ReconcileWorkerPreviews(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.holds("forge-preview-change-00000000-0009-7000-8000-000000000000") {
		t.Fatal("the lost removal was not made when the worker returned")
	}
}

// reconcilingWorker reconciles while a boot is on the worker, as the
// minute's pass may.
type reconcilingWorker struct {
	*fakePreviewWorker
	s *Service
}

func (w *reconcilingWorker) Boot(ctx context.Context, spec worker.PreviewSpec, p docker.Prepared, tree string) (worker.PreviewState, error) {
	st, err := w.fakePreviewWorker.Boot(ctx, spec, p, tree)
	if rerr := w.s.ReconcileWorkerPreviews(ctx); rerr != nil {
		return st, rerr
	}
	return st, err
}

// A change's and a run's boot on the worker hold the preview's name until
// its record is kept: a reconciliation during the boot leaves it alone.
func TestABootOnTheWorkerIsNeverTakenForAnUnknownPreview(t *testing.T) {
	t.Parallel()
	s, base, _, dir, projectID := workerPreviewWorld(t)
	s.worker = &reconcilingWorker{fakePreviewWorker: base, s: s}
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(dir, "workspaces", "run-00000000-0004-7000-8000-000000000000")
	writePreviewBootFixture(t, ws)
	gitCommitAll(t, ws, "the run's work")
	if _, err := s.RunPreview(context.Background(), claimedPreviewStep{projectID, idstest.N(4), idstest.N(11)}, RunInput{Source: deploy.Source{Workspace: ws, Revision: "HEAD", Slug: "p"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"forge-preview-change-00000000-0009-7000-8000-000000000000", "forge-preview-run-00000000-0004-7000-8000-000000000000"} {
		if !base.holds(name) {
			t.Fatalf("a reconciliation during its boot removed %s", name)
		}
	}
}

// claimRequiredWorker is a worker that lost the order of its previews'
// requests: it refuses every change but a claim, until one is made.
type claimRequiredWorker struct {
	*fakePreviewWorker
	claimed bool
}

func (w *claimRequiredWorker) Boot(ctx context.Context, spec worker.PreviewSpec, p docker.Prepared, tree string) (worker.PreviewState, error) {
	if !w.claimed {
		return worker.PreviewState{}, worker.ErrClaimRequired
	}
	return w.fakePreviewWorker.Boot(ctx, spec, p, tree)
}

func (w *claimRequiredWorker) Remove(ctx context.Context, name string) error {
	if name == "" {
		w.claimed = true
	} else if !w.claimed {
		return worker.ErrClaimRequired
	}
	return w.fakePreviewWorker.Remove(ctx, name)
}

// A worker that lost the order of its previews' requests is claimed again,
// as the forge does when it starts; the preview asked for is unavailable
// meanwhile, never booted by another path, and boots once asked again.
func TestAWorkerThatLostItsOrderIsClaimedAgain(t *testing.T) {
	t.Parallel()
	s, base, _, dir, projectID := workerPreviewWorld(t)
	w := &claimRequiredWorker{fakePreviewWorker: base}
	s.worker = w
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	_, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("a boot the worker refused for its lost order: %v", err)
	}
	if !w.claimed || s.startClearPending() {
		t.Fatalf("the worker was not claimed again: claimed %v, pending %v", w.claimed, s.startClearPending())
	}
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false); err != nil {
		t.Fatalf("a boot after the claim: %v", err)
	}
}

// A preview the worker's daemon cannot hold is refused by the worker before
// anything happens: a restart removes nothing there, and no change or run
// preview is booted. The forge asks the worker first, since only it knows
// what its daemon has.
func TestAWorkerPreviewTheWorkerCannotHoldIsRefusedBeforeAnything(t *testing.T) {
	t.Parallel()
	s, w, _, dir, projectID := workerPreviewWorld(t)
	w.refuse = &worker.AdmissionRefused{Reason: "part web declares 16G of memory; this Docker host has 8G"}
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, true); err == nil || !strings.Contains(err.Error(), "this Docker host has 8G") {
		t.Fatalf("an oversized restart on the worker: %v", err)
	}
	if len(w.removed) != 0 || len(w.boots) != 0 || w.admitted != 1 {
		t.Fatalf("the worker was asked to remove %q and boot %d before the refusal (%d admissions)", w.removed, len(w.boots), w.admitted)
	}

	ws := filepath.Join(dir, "workspaces", "run-00000000-0004-7000-8000-000000000000")
	writePreviewBootFixture(t, ws)
	gitCommitAll(t, ws, "the run's work")
	c, err := docker.ReadWorkspaceContract(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunPreview(context.Background(), claimedPreviewStep{projectID, idstest.N(4), idstest.N(11)}, RunInput{Source: deploy.Source{Workspace: ws, Revision: "HEAD", Slug: "p", Contract: c}}); err == nil {
		t.Fatal("a run preview the worker cannot hold was booted")
	}
	if len(w.boots) != 0 || w.admitted != 2 {
		t.Fatalf("a refused run preview: boots %d, admissions %d", len(w.boots), w.admitted)
	}
}
