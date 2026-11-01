package dataprotection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/gate"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

const testGateImage = "registry.example/forge/gate@sha256:2222222222222222222222222222222222222222222222222222222222222222"

// gatedPlatform is the restored copy's app on App Platform as AppTown
// meets it from outside: the real gate (internal/gate), run with the
// secret AppTown rendered into the gate's spec, in front of the app; the
// gate redeems tickets with AppTown's own RedeemGateTicket. An app with
// no gate secret in its spec answers 404. The broken-gate switches stand
// in for a gate that fails AppTown's check of it.
type gatedPlatform struct {
	w *dataWorld
	// answer is the app's status for a path ("*" any other); 200 when unset.
	answer map[string]int
	mu     sync.Mutex
	// asked are the paths asked with the right secret; refused those
	// asked without it.
	asked, refused []string
	// tickets are the tickets presented at the gate's entry.
	tickets []string
	// during runs at each request until it answers true.
	during func(*http.Request) bool
	app    *httptest.Server
	gates  map[string]http.Handler
	// broken is how the gate fails AppTown's check of it ("" it works):
	// one of the gate... constants below.
	broken  string
	entered map[string]*httptest.ResponseRecorder
}

// The ways a gate can fail AppTown's check of it.
const (
	gateDown         = "does not answer"
	gateLetsAnyoneIn = "lets in a request with no token"
	gateCacheable    = "lets its refusals be stored"
	gateAnyTicket    = "lets any ticket in"
	gateReplays      = "lets a ticket in twice"
	gateNoAppTown    = "cannot reach AppTown"
	gateForgets      = "forgets its sessions"
	gateForwards     = "passes the ticket on to the app"
	gateNoSession    = "lets in without a session"
	gateTalks        = "says something when it refuses"
	gateSoftRefusal  = "answers its refusals 200"
	gateGuardsRoot   = "guards only its front page"
	gateForwardsAll  = "passes every entry on to the app"
)

func newGatedPlatform(t *testing.T, w *dataWorld) *gatedPlatform {
	g := &gatedPlatform{w: w, answer: map[string]int{}, gates: map[string]http.Handler{}, entered: map[string]*httptest.ResponseRecorder{}}
	g.app = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		status := http.StatusOK
		g.mu.Lock()
		if n, ok := g.answer[r.URL.Path]; ok {
			status = n
		} else if n, ok := g.answer["*"]; ok {
			status = n
		}
		g.mu.Unlock()
		// The app says no-store itself, and its 404 is empty, so only the
		// gate's mark can tell the gate's refusal from the app's answer.
		rw.Header().Set("Cache-Control", "no-store")
		rw.WriteHeader(status)
		if status != http.StatusNotFound {
			// The body holds a row: AppTown must never keep it.
			io.WriteString(rw, "order 7 for carol@example.com")
		}
	}))
	t.Cleanup(g.app.Close)
	return g
}

func (g *gatedPlatform) RoundTrip(r *http.Request) (*http.Response, error) {
	g.mu.Lock()
	during := g.during
	g.mu.Unlock()
	if during != nil && during(r) {
		g.mu.Lock()
		g.during = nil
		g.mu.Unlock()
	}
	app := strings.TrimSuffix(r.URL.Host, "-abc.ondigitalocean.app")
	secret := g.secretFor(app)
	g.mu.Lock()
	if secret != "" && r.Header.Get(deploy.GateProbeHeader) == secret {
		g.asked = append(g.asked, r.URL.Path)
	} else {
		g.refused = append(g.refused, r.URL.Path)
	}
	if r.URL.Path == gate.EnterPath {
		g.tickets = append(g.tickets, r.URL.Query().Get("ticket"))
	}
	broken := g.broken
	g.mu.Unlock()
	if broken == gateDown {
		return nil, errors.New("dial tcp: connection refused")
	}
	if secret == "" {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
	}
	entry := r.URL.Path == gate.EnterPath
	ticket := r.URL.Query().Get("ticket")
	switch {
	case (broken == gateLetsAnyoneIn || broken == gateGuardsRoot && r.URL.Path != "/") && !entry && r.Header.Get(deploy.GateProbeHeader) == "":
		r = r.Clone(r.Context())
		r.Header.Set(deploy.GateProbeHeader, secret)
	case broken == gateForgets:
		r = r.Clone(r.Context())
		r.Header.Del("Cookie")
	case broken == gateForwardsAll && entry:
		return http.Get(g.app.URL + r.URL.Path)
	case broken == gateAnyTicket && entry:
		rec := httptest.NewRecorder()
		http.SetCookie(rec, &http.Cookie{Name: gate.CookieName, Value: "made-by-a-broken-gate", Path: "/", Secure: true, HttpOnly: true})
		rec.Header().Set("Location", "/")
		rec.WriteHeader(http.StatusSeeOther)
		return rec.Result(), nil
	case broken == gateReplays && entry:
		g.mu.Lock()
		first := g.entered[ticket]
		g.mu.Unlock()
		if first != nil {
			return first.Result(), nil
		}
	}
	rec := httptest.NewRecorder()
	g.gateFor(app, secret).ServeHTTP(rec, r)
	if broken == gateReplays && entry && rec.Code == http.StatusSeeOther {
		g.mu.Lock()
		g.entered[ticket] = rec
		g.mu.Unlock()
	}
	res := rec.Result()
	switch {
	case broken == gateCacheable && res.StatusCode == http.StatusNotFound:
		res.Header.Del("Cache-Control")
	case broken == gateForwards && entry && res.StatusCode == http.StatusSeeOther:
		// The session is set, but the entry (and its ticket) went on to
		// the app, which answered it.
		res.StatusCode = http.StatusOK
		res.Header.Del("Location")
	case broken == gateNoSession && entry:
		res.Header.Del("Set-Cookie")
	case broken == gateTalks && res.StatusCode == http.StatusNotFound:
		res.Body = io.NopCloser(strings.NewReader("no such page"))
	case broken == gateSoftRefusal && res.Header.Get(gate.RefusedHeader) != "":
		res.StatusCode = http.StatusOK
	}
	return res, nil
}

// gateFor is the app's gate, kept, since it holds its sessions.
func (g *gatedPlatform) gateFor(app, secret string) http.Handler {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := app + " " + secret
	if h, ok := g.gates[key]; ok {
		return h
	}
	upstream, _ := url.Parse(g.app.URL)
	origin, _ := url.Parse("https://forge.example")
	h := gate.New(gate.Config{Upstream: upstream, Secret: secret, AppTown: origin}, gate.Options{Client: &http.Client{Transport: apptownRedeemer{g}}})
	g.gates[key] = h
	return h
}

// apptownRedeemer is AppTown's POST /gate/redeem as the gate reaches it:
// the gate's secret as a bearer credential, the ticket in the body, and
// AppTown's RedeemGateTicket deciding.
type apptownRedeemer struct{ g *gatedPlatform }

func (f apptownRedeemer) RoundTrip(r *http.Request) (*http.Response, error) {
	f.g.mu.Lock()
	unreachable := f.g.broken == gateNoAppTown
	f.g.mu.Unlock()
	if unreachable || r.URL.String() != "https://forge.example"+gate.RedeemPath {
		return nil, errors.New("dial tcp: no route to host")
	}
	answer := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}
	}
	secret, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	var in struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return answer(http.StatusBadRequest, ""), nil
	}
	grant, err := f.g.w.s.RedeemGateTicket(r.Context(), secret, in.Ticket)
	if err != nil {
		return answer(http.StatusForbidden, ""), nil
	}
	raw, _ := json.Marshal(map[string]string{"person": grant.Person, "until": grant.Until.Format(time.RFC3339)})
	return answer(http.StatusOK, string(raw)), nil
}

// secretFor is the gate secret AppTown put in app's spec, "" when its
// gate has none.
func (g *gatedPlatform) secretFor(app string) string {
	for _, spec := range g.w.api.Snapshot().Specs {
		if spec["name"] != app {
			continue
		}
		services, _ := spec["services"].([]any)
		for _, sv := range services {
			c, _ := sv.(map[string]any)
			if c["name"] != digitalocean.GateComponent {
				continue
			}
			envs, _ := c["envs"].([]any)
			for _, e := range envs {
				m, _ := e.(map[string]any)
				if m["key"] == deploy.GateEnvSecret {
					v, _ := m["value"].(string)
					return v
				}
			}
		}
	}
	return ""
}

// copySecrets gives a restored copy what the rehearsal profile would: here
// it withholds the mail password, or refuses an inheriting profile.
type copySecrets struct{ inherits bool }

func (c copySecrets) ResolveRestoredCopy(_ context.Context, _ deployment.Application, source, _ string, _ *appcontract.Contract) (deploy.ConsumerSecrets, []string, error) {
	if c.inherits {
		return nil, nil, secret.ErrRehearsalInherits
	}
	if source != "prod" {
		return nil, nil, fmt.Errorf("resolved against %s, not the source", source)
	}
	return secret.Delivery{}, []string{"SMTP_PASSWORD"}, nil
}

// restoredWorld is a production deployment "prod" on App Platform, released,
// holding records, with a good copy in the store, and an AppTown that has a
// gate image.
type restoredWorld struct {
	*dataWorld
	platform *gatedPlatform
	prod     *deployment.Deployment
	source   string // prod's cluster id
	set      ids.ID
}

func newRestoredWorld(t *testing.T, contract []byte) *restoredWorld {
	t.Helper()
	return newRestoredWorldWith(t, dataWorldOptions{contract: contract})
}
func newRestoredWorldWith(t *testing.T, opts dataWorldOptions) *restoredWorld {
	t.Helper()
	w := &restoredWorld{dataWorld: newDataWorldWith(t, opts)}
	// The people tickets are issued to have accounts, so each ticket names
	// a principal.
	people := identity.New(w.db, identity.Config{})
	for _, name := range []string{"alice", "bob", "ops"} {
		if _, err := people.EnsureUser(t.Context(), name); err != nil {
			t.Fatal(err)
		}
	}
	// A gated copy needs a second repository, the gate's: the account's
	// registry is on the basic plan, whose five repositories have room.
	w.api.SeedRegistry("forge-test", "basic", 5)
	w.protect()
	w.platform = newGatedPlatform(t, w.dataWorld)
	w.s.restored = RestoredCopyDependencies{GateImage: testGateImage, AppTown: "https://forge.example", Secrets: copySecrets{},
		HTTP: &http.Client{Transport: w.platform, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Grace: time.Millisecond}
	// prod serves a release published to a registry, so it can run again
	// by its images.
	claim := &claimedAttempt{db: w.db, project: w.project, run: idstest.N(7), step: idstest.N(70), at: 1}
	bid, err := w.s.Bundles.Cut(t.Context(), w.project, w.bundle.ContractRevision, map[string]string{"web": releaseRepo + digestA}, nil, w.contract, digestOrigin(claim))
	if err != nil {
		t.Fatal(err)
	}
	if w.bundle, err = w.s.Bundles.ByID(context.Background(), bid); err != nil {
		t.Fatal(err)
	}
	if w.prod, err = w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("prod's release %s:\n%s", rel.State, log)
	}
	w.source, _ = w.clusterOf(w.prod)["id"].(string)
	w.clusters.put(w.source+".fake", "app", "ledger: 41, 42")
	op, err := w.s.RequestCopy(context.Background(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	copied := w.run(op)
	set, _ := copied.Result["backup"].(string)
	if w.set, _ = ids.Parse(set); w.set.IsZero() {
		t.Fatalf("no copy: %+v", copied)
	}
	return w
}

// start starts a restored copy and runs its start, answering the
// operation and the copy.
func (w *restoredWorld) start(in RestoredCopyRequest) (*operations.Operation, *RestoredCopy) {
	w.t.Helper()
	op, err := w.s.RequestRestoredCopy(context.Background(), w.project, "prod", in)
	if err != nil {
		w.t.Fatal(err)
	}
	op = w.run(op)
	copies, err := w.s.restoredCopiesOf(context.Background(), w.prod, 5)
	if err != nil || len(copies) == 0 {
		w.t.Fatalf("restored copies: %v %v", copies, err)
	}
	return op, &copies[0]
}

// standing starts a restore test whose take-down cannot list the
// platform's apps, so the checked copy is left standing (as a start
// AppTown stopped or whose take-down failed leaves one), then lets the
// platform answer again.
func (w *restoredWorld) standing() *RestoredCopy {
	w.t.Helper()
	w.platform.during = func(r *http.Request) bool {
		if r.Header.Get("Cookie") == "" {
			return false
		}
		w.api.Fail("GET", "/v2/apps", 500)
		return true
	}
	op, c := w.start(RestoredCopyRequest{})
	w.api.Fail("GET", "/v2/apps", 0)
	if op.State != operations.Failed || c.EndedAt.Valid || c.Outcome != RehearsalPassedNoData {
		w.t.Fatalf("the copy left standing %s: %s; %+v", op.State, op.Detail, c)
	}
	return c
}

// noSecretIn fails when a gate secret AppTown rendered, or a row the
// app answered, appears in what a project reader sees.
func (w *restoredWorld) noSecretIn(what string, v any) {
	w.t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, spec := range w.api.Snapshot().Specs {
		if s := w.platform.secretFor(fmt.Sprint(spec["name"])); s != "" && strings.Contains(string(raw), s) {
			w.t.Fatalf("the gate secret is in %s", what)
		}
	}
	if strings.Contains(string(raw), "carol@example.com") {
		w.t.Fatalf("a row the app answered is in %s", what)
	}
	w.platform.mu.Lock()
	tickets := append([]string(nil), w.platform.tickets...)
	w.platform.mu.Unlock()
	for _, ticket := range tickets {
		if ticket != "" && strings.Contains(string(raw), ticket) {
			w.t.Fatalf("a ticket presented at the gate is in %s", what)
		}
	}
}

// A restore test proves production comes back on its real runtime: a
// temporary deployment of prod's release on prod's own App Platform target,
// its new cluster filled from the copy before its app starts, behind the
// gate (the only public component), checked through the gate with its
// secret, recorded on prod apart from any rehearsal, and taken down at
// once: its app and cluster deleted, no final copy taken, prod untouched.
func TestARestoreTestComesBackOnItsOwnPlatformBehindTheGateAndIsTakenDown(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	setsBefore, err := w.s.backups.ForDeployment(ctx, w.project, "prod", 50)
	if err != nil {
		t.Fatal(err)
	}
	var filledWithApps = -1
	w.clusters.atFill = func() { filledWithApps = len(w.api.Snapshot().Apps) }
	op, c := w.start(RestoredCopyRequest{})
	if !endedAs(op, operationVerified) {
		t.Fatalf("restore test %s: %s\n%+v", op.State, op.Detail, op.Steps)
	}
	if filledWithApps != 1 {
		t.Fatalf("the copy's app existed when its data was filled (apps then: %d, prod's is 1)", filledWithApps)
	}
	// The copy ran prod's release on prod's target, from the set asked.
	if c.Source != "prod" || c.Backup != w.set || c.Target != "do-syd" || c.Outcome != RehearsalPassedNoData || !c.EndedAt.Valid {
		t.Fatalf("the copy: %+v", c)
	}
	if c.Facts.Withheld[0] != "SMTP_PASSWORD" || c.Facts.Egress != egressNotBlocked {
		t.Fatalf("what was withheld: %+v", c.Facts)
	}
	// AppTown asked through the gate with the secret, and without it only
	// to check the gate itself.
	if len(w.platform.refused) != len(c.Facts.Gate) || len(w.platform.asked) < 2 {
		t.Fatalf("checks: asked %v, refused %v", w.platform.asked, w.platform.refused)
	}
	// It checked the gate itself, with no person: refusals, then a ticket
	// of its own let in once, refused again, and served the app.
	want := []RehearsalCheck{
		{Name: "refuses a request with no token", Path: "/", Status: 404, Passed: true},
		{Name: "refuses a wrong token", Path: "/", Status: 404, Passed: true},
		{Name: "refuses a made-up ticket", Path: gate.EnterPath, Status: 403, Passed: true},
		{Name: "lets AppTown's own ticket in", Path: gate.EnterPath, Status: 303, Passed: true},
		{Name: "refuses that ticket a second time", Path: gate.EnterPath, Status: 403, Passed: true},
		{Name: "serves the app to that session", Path: "/", Status: 200, Passed: true},
	}
	if fmt.Sprint(c.Facts.Gate) != fmt.Sprint(want) || !strings.Contains(op.Detail, "the gate passed its 6 checks") {
		t.Fatalf("the gate check: %+v; %s", c.Facts.Gate, op.Detail)
	}
	for _, ch := range want {
		if !slices.ContainsFunc(op.Steps, func(st operations.OperationStep) bool {
			return st.Name == gateCheckStep+": "+ch.Name && !st.Failed && st.Ended != nil
		}) {
			t.Fatalf("no step for the gate check %q: %+v", ch.Name, op.Steps)
		}
	}
	// AppTown's redemption is its own receipt.
	checked := operationReceipts(t, w.db, "deployment.gate-checked", w.project)
	if len(checked) != 1 || checked[0].Actor != operations.SystemActor || !strings.Contains(string(checked[0].Payload), "nobody entered") {
		t.Fatalf("gate-checked receipts: %+v", checked)
	}
	// Its app had the gate as its only public component: the one app
	// besides prod's (the copy's record went with its take-down).
	var spec map[string]any
	for _, s := range w.api.Snapshot().Specs {
		if s["name"] != w.appOf("prod") {
			spec = s
		}
	}
	raw, _ := json.Marshal(spec)
	if spec == nil || !strings.Contains(string(raw), `"ingress":{"rules":[{"component":{"name":"apptown-gate"}`) || !strings.Contains(string(raw), `"internal_ports":[8080]`) {
		t.Fatalf("the copy's app spec: %s", raw)
	}
	// Taken down: its app and its own cluster are gone, prod's stand, and
	// prod's data is as it was.
	state := w.api.Snapshot()
	if len(state.Apps) != 1 || len(state.DeletedClusters) != 1 || state.DeletedClusters[0] == w.source {
		t.Fatalf("after the test: apps %d, deleted clusters %v (prod's %s)", len(state.Apps), state.DeletedClusters, w.source)
	}
	if got, _ := w.clusters.get(w.source+".fake", "app"); got != "ledger: 41, 42" {
		t.Fatalf("prod's data changed: %q", got)
	}
	if _, err := w.s.Deployments.ByName(ctx, w.project, c.Deployment); err == nil {
		t.Fatal("the copy's deployment record stands after its take-down")
	}
	// No final copy: its data is a copy of a copy.
	setsAfter, err := w.s.backups.ForDeployment(ctx, w.project, "prod", 50)
	if err != nil || len(setsAfter) != len(setsBefore) {
		t.Fatalf("sets of prod before %d, after %d (%v)", len(setsBefore), len(setsAfter), err)
	}
	if copySets, err := w.s.backups.ForDeployment(ctx, w.project, c.Deployment, 50); err != nil || len(copySets) != 0 {
		t.Fatalf("the copy was copied: %v %v", copySets, err)
	}
	// prod's card shows the test apart from its rehearsal.
	collectProtectionForTest(t, w.s, w.project, "prod")
	prot, err := w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if prot.RestoreTest == nil || prot.RestoreTest.ID != c.ID || prot.RestoreTestNote != "" || prot.Rehearsal != nil {
		t.Fatalf("prod's protection: test %+v note %q rehearsal %+v", prot.RestoreTest, prot.RestoreTestNote, prot.Rehearsal)
	}
	tested := operationReceipts(t, w.db, "deployment.restore-tested", w.project)
	if len(tested) != 1 || !strings.Contains(string(tested[0].Payload), `"outcome":"`+RehearsalPassedNoData) || !strings.Contains(string(tested[0].Payload), `"gate":[{"name":"refuses a request with no token"`) {
		t.Fatalf("restore-tested receipts: %+v", tested)
	}
	// Deployment recorded the start from the copy the fill read, naming
	// the restored copy it started.
	started := operationReceipts(t, w.db, "deployment.started-from-copy", w.project)
	if len(started) != 1 || !strings.Contains(string(started[0].Payload), `"restoredCopy":"`) {
		t.Fatalf("started-from-copy receipts: %+v", started)
	}
	w.noSecretIn("the operation", op)
	w.noSecretIn("the card", prot)
	for _, kind := range []string{"deployment.restore-tested", "deployment.gate-checked", "deployment.restored-copy-started", "deployment.started-from-copy", "deployment.operation-ended", "deployment.torn-down"} {
		w.noSecretIn(kind, operationReceipts(t, w.db, kind, w.project))
	}
	// A list of the project's deployments never showed the copy.
	listed, err := w.s.Deployments.ForProject(ctx, w.project)
	if err != nil || len(listed) != 1 || listed[0].Name != "prod" {
		t.Fatalf("listed: %v %v", listed, err)
	}
}

// AppTown's check of a gate that does not hold fails the restore: the
// check that caught it is recorded failed with its status, the copy is
// taken down.
//
// Kills, by the case that catches it: the refusal checks not demanding
// 404 or the entry checks 403 (soft refusal); not demanding the gate's own
// mark on a 404 (an app that answers 404) or on an entry refusal (passes
// every entry on); not demanding an empty body (talks) or no-store
// (cacheable); asking / and not the readiness path (guards only its front
// page); AppTown's own ticket not required to redirect (forwards) or to
// give a session (no session, no AppTown); a second redemption not refused
// (replays); the session's read not judged (forgets); a gate failure that
// does not fail the copy (every case).
func TestARestoreFailsWhenItsGateDoesNotHold(t *testing.T) {
	t.Parallel()
	// An API whose root is not a page: readiness at /healthz, 404 at /.
	api := []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    health:\n      readiness: { http: /healthz }\nneeds:\n  postgres:\n    version: \"16\"\n")
	for _, tc := range []struct {
		name     string
		broken   string
		contract []byte
		answer   map[string]int
		failed   []string // the gate checks that fail
		step     string
	}{
		{name: gateDown, broken: gateDown, failed: []string{"refuses a request with no token", "refuses a wrong token", "refuses a made-up ticket", "lets AppTown's own ticket in", "refuses that ticket a second time", "serves the app to that session"}, step: "asking the app through the gate"},
		{name: gateLetsAnyoneIn, broken: gateLetsAnyoneIn, failed: []string{"refuses a request with no token"}, step: gateCheckStep},
		// GC-01: the app's own 404 at / must not pass for the gate's refusal.
		{name: gateLetsAnyoneIn + " to an API whose root is 404", broken: gateLetsAnyoneIn, contract: api, answer: map[string]int{"/": 404}, failed: []string{"refuses a request with no token"}, step: gateCheckStep},
		{name: gateLetsAnyoneIn + " to an app that answers 404", broken: gateLetsAnyoneIn, answer: map[string]int{"*": 404}, failed: []string{"refuses a request with no token", "serves the app to that session"}, step: "asking the app through the gate"},
		{name: gateGuardsRoot, broken: gateGuardsRoot, contract: api, answer: map[string]int{"/": 404}, failed: []string{"refuses a request with no token"}, step: gateCheckStep},
		{name: gateSoftRefusal, broken: gateSoftRefusal, failed: []string{"refuses a request with no token", "refuses a wrong token", "refuses a made-up ticket", "refuses that ticket a second time"}, step: gateCheckStep},
		{name: gateForwardsAll + " (which refuses it)", broken: gateForwardsAll, answer: map[string]int{gate.EnterPath: 403}, failed: []string{"refuses a made-up ticket", "lets AppTown's own ticket in", "refuses that ticket a second time", "serves the app to that session"}, step: gateCheckStep},
		{name: gateCacheable, broken: gateCacheable, failed: []string{"refuses a request with no token", "refuses a wrong token"}, step: gateCheckStep},
		{name: gateTalks, broken: gateTalks, failed: []string{"refuses a request with no token", "refuses a wrong token"}, step: gateCheckStep},
		{name: gateAnyTicket, broken: gateAnyTicket, failed: []string{"refuses a made-up ticket", "refuses that ticket a second time", "serves the app to that session"}, step: gateCheckStep},
		{name: gateReplays, broken: gateReplays, failed: []string{"refuses that ticket a second time"}, step: gateCheckStep},
		{name: gateNoAppTown, broken: gateNoAppTown, failed: []string{"lets AppTown's own ticket in", "serves the app to that session"}, step: gateCheckStep},
		{name: gateForgets, broken: gateForgets, failed: []string{"serves the app to that session"}, step: gateCheckStep},
		{name: gateForwards, broken: gateForwards, failed: []string{"lets AppTown's own ticket in"}, step: gateCheckStep},
		{name: gateNoSession, broken: gateNoSession, failed: []string{"lets AppTown's own ticket in", "serves the app to that session"}, step: gateCheckStep},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRestoredWorld(t, tc.contract)
			w.platform.broken = tc.broken
			for path, status := range tc.answer {
				w.platform.answer[path] = status
			}
			op, c := w.start(RestoredCopyRequest{})
			if op.State != operations.Failed || c.Outcome != RehearsalFailed || c.FailedAt != tc.step || !c.EndedAt.Valid {
				t.Fatalf("a gate that %s: %s %s; %+v", tc.name, op.State, op.Detail, c)
			}
			var failed []string
			for _, ch := range c.Facts.Gate {
				if !ch.Passed {
					failed = append(failed, ch.Name)
				}
			}
			if len(c.Facts.Gate) != 6 || !slices.Equal(failed, tc.failed) || !strings.Contains(op.Detail, "the gate failed: "+tc.failed[0]) {
				t.Fatalf("a gate that %s: failed %v of %+v; %s", tc.name, failed, c.Facts.Gate, op.Detail)
			}
			if len(w.api.Snapshot().Apps) != 1 {
				t.Fatal("the copy behind a gate that does not hold was left up")
			}
			w.noSecretIn("the operation", op)
		})
	}
}

// The ticket AppTown issues itself lets in only the check of the start
// that issued it, while that start runs; a ticket naming no start lets
// nobody in.
//
// Kills: a check ticket accepted after its start ended, or from another
// copy's start; a ticket naming no start accepted.
func TestAppTownsOwnTicketLetsInOnlyItsRunningCheck(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	opened := w.standing()
	secretOf := w.platform.secretFor(w.appOf(opened.Deployment))
	if secretOf == "" {
		t.Fatalf("the standing copy has no gate secret: %+v", opened)
	}
	redeem := func(copyID ids.ID, holder string, checkOp ids.ID, secret string) error {
		ticket, err := w.s.issueTicket(ctx, copyID, holder, checkOp)
		if err != nil {
			t.Fatal(err)
		}
		_, err = w.s.RedeemGateTicket(ctx, secret, ticket)
		return err
	}
	// Its start has ended.
	if err := redeem(opened.ID, operations.SystemActor, opened.Operation, secretOf); !errors.Is(err, errTicket) {
		t.Fatalf("a check ticket of an ended start: %v", err)
	}
	// While a restore test of prod runs: a check ticket naming its start
	// does not open the other copy, and a ticket naming no start does not
	// open the test.
	w.platform.during = func(*http.Request) bool {
		copies, err := w.s.restoredCopiesOf(ctx, w.prod, 5)
		if err != nil || copies[0].ID == opened.ID {
			t.Errorf("the test's copy: %+v %v", copies, err)
			return true
		}
		test := copies[0]
		if err := redeem(opened.ID, operations.SystemActor, test.Operation, secretOf); !errors.Is(err, errTicket) {
			t.Errorf("a check ticket of another copy's running start: %v", err)
		}
		if err := redeem(test.ID, "alice", ids.Nil, w.platform.secretFor(w.appOf(test.Deployment))); !errors.Is(err, errTicket) {
			t.Errorf("a ticket naming no start: %v", err)
		}
		return true
	}
	if op, _ := w.start(RestoredCopyRequest{}); !endedAs(op, operationVerified) {
		t.Fatalf("the restore test %s: %s", op.State, op.Detail)
	}
}

// A copy left standing holds its source: it is on prod's card only, prod
// is not taken down while it stands, and it is not taken down as a
// deployment. The sweep takes it down (no final copy), and then prod is
// free.
func TestAStandingCopyHoldsItsSourceUntilTheSweepTakesItDown(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	c := w.standing()
	if listed, err := w.s.Deployments.ForProject(ctx, w.project); err != nil || len(listed) != 1 || listed[0].Name != "prod" {
		t.Fatalf("listed while the copy stands: %v %v", listed, err)
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"}); !errors.Is(err, errRestoredCopiesStand) {
		t.Fatalf("prod was taken down while a copy of it stands: %v", err)
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: c.Deployment}); !errors.Is(err, deployment.ErrRestoredCopyDoor) {
		t.Fatalf("the copy was taken down as a deployment: %v", err)
	}
	n, skipped, err := w.s.sweepRestoredCopies(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d %v %v", n, skipped, err)
	}
	ops, err := w.opsOn(c.Deployment)
	if err != nil || len(ops) != 1 || ops[0].Kind != OperationRestoredCopyEnd {
		t.Fatalf("the copy's take-down: %+v %v", ops, err)
	}
	if done := w.run(&ops[0]); !endedAs(done, operationDone) {
		t.Fatalf("take-down %s: %s", done.State, done.Detail)
	}
	copies, _ := w.s.restoredCopiesOf(ctx, w.prod, 5)
	if !copies[0].EndedAt.Valid {
		t.Fatalf("after the sweep: %+v", copies[0])
	}
	if state := w.api.Snapshot(); len(state.Apps) != 1 || len(state.DeletedClusters) != 1 || state.DeletedClusters[0] == w.source {
		t.Fatalf("after the sweep: apps %d, deleted %v", len(state.Apps), state.DeletedClusters)
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"}); errors.Is(err, errRestoredCopiesStand) {
		t.Fatal("prod is still held by a copy taken down")
	}
}

// A copy that fails is taken down and says where it failed in safe words:
// an import that fails names a row in its output, and that row reaches no
// operation, card, receipt or record; a copy whose app is not ready is
// taken down too.
func TestARestoredCopyThatFailsIsTakenDownAndSaysOnlySafeFacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("import", func(t *testing.T) {
		t.Parallel()
		w := newRestoredWorld(t, nil)
		w.clusters.importSays = `ERROR: duplicate key value violates unique constraint "users_email_key" DETAIL: Key (email)=(carol@example.com) already exists.`
		op, c := w.start(RestoredCopyRequest{})
		if op.State != operations.Failed || !strings.Contains(op.Detail, "filling postgres from the copy failed") || !strings.Contains(op.Detail, "exit status 1") {
			t.Fatalf("failed import %s: %s", op.State, op.Detail)
		}
		if !c.EndedAt.Valid {
			t.Fatalf("the failed copy stands: %+v", c)
		}
		if state := w.api.Snapshot(); len(state.DeletedClusters) != 1 || state.DeletedClusters[0] == w.source {
			t.Fatalf("the failed copy's cluster: %v", state.DeletedClusters)
		}
		collectProtectionForTest(t, w.s, w.project, "prod")
		prot, _ := w.s.DeploymentProtection(ctx, w.project, "prod")
		w.noSecretIn("the operation", op)
		w.noSecretIn("the card", prot)
		for _, kind := range []string{"deployment.operation-ended", "deployment.restored-copy-started", "deployment.torn-down"} {
			w.noSecretIn(kind, operationReceipts(t, w.db, kind, w.project))
		}
	})
	t.Run("not ready", func(t *testing.T) {
		t.Parallel()
		w := newRestoredWorld(t, nil)
		w.platform.answer["/"] = http.StatusServiceUnavailable
		op, c := w.start(RestoredCopyRequest{})
		if op.State != operations.Failed || !c.EndedAt.Valid || c.Outcome != RehearsalFailed {
			t.Fatalf("a copy not ready %s: %s; %+v", op.State, op.Detail, c)
		}
		if len(w.api.Snapshot().Apps) != 1 {
			t.Fatal("the copy that was not ready was left up")
		}
	})
	t.Run("data check", func(t *testing.T) {
		t.Parallel()
		w := newRestoredWorld(t, []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres:\n    version: \"16\"\nrehearsal:\n  checks: [\"GET /healthz/data\"]\n"))
		w.platform.answer["/healthz/data"] = http.StatusInternalServerError
		op, c := w.start(RestoredCopyRequest{})
		if op.State != operations.Failed || c.Outcome != RehearsalFailed || !c.EndedAt.Valid {
			t.Fatalf("a failed data check %s: %s; %+v", op.State, op.Detail, c)
		}
		var check *RehearsalCheck
		for i := range c.Facts.Checks {
			if c.Facts.Checks[i].Path == "/healthz/data" {
				check = &c.Facts.Checks[i]
			}
		}
		if check == nil || check.Status != 500 || check.Passed {
			t.Fatalf("the data check's record: %+v", c.Facts.Checks)
		}
	})
}

// Starting a restored copy spends money and puts production data
// somewhere new: it needs deployment:restore, a gate image, a production
// source on App Platform, and a rehearsal profile that does not inherit.
// A refused start makes nothing.
func TestARestoredCopyIsRefusedBeforeAnythingIsMade(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	apps := len(w.api.Snapshot().Apps)
	w.auth.set("deployer", "deployment:restore")
	if _, err := w.s.RequestRestoredCopy(ctx, w.project, "prod", RestoredCopyRequest{}); err == nil {
		t.Fatal("a caller without deployment:restore started a restored copy")
	}
	w.auth.set("alice")
	w.nameRunner("")
	if _, err := w.s.RequestRestoredCopy(ctx, w.project, "prod", RestoredCopyRequest{}); !errors.Is(err, deployment.ErrNoCopyRunner) {
		t.Fatalf("no copy runner: %v", err)
	}
	w.nameRunner("local")
	keep := w.s.restored
	w.s.restored.GateImage = ""
	if _, err := w.s.RequestRestoredCopy(ctx, w.project, "prod", RestoredCopyRequest{}); !errors.Is(err, errNoGate) {
		t.Fatalf("no gate image: %v", err)
	}
	w.s.restored = keep
	w.s.restored.Secrets = copySecrets{inherits: true}
	op, c := w.start(RestoredCopyRequest{})
	if op.State != operations.Failed || !strings.Contains(op.Detail, "inherits") || !c.EndedAt.Valid {
		t.Fatalf("an inheriting profile %s: %s; %+v", op.State, op.Detail, c)
	}
	if got := len(w.api.Snapshot().Apps); got != apps {
		t.Fatalf("a refused copy made an app (%d, was %d)", got, apps)
	}
	if _, err := w.ops.Cancel(ctx, op.ID); !errors.Is(err, errRestoreNotStopped) {
		t.Fatalf("a restore start was offered a stop: %v", err)
	}
}

// A restored copy's gate image has its own repository in the account's
// registry, so a registry with room for the project's repository but not
// the gate's refuses the copy at the room check, before its cluster or
// app is made.
//
// Kills: a room check that counts only the project's repository (the copy
// then fails copying the gate image, after its cluster was made and
// filled).
func TestARestoredCopyNeedsRoomForTheGatesRepository(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	w.api.SeedRegistry("forge-test", "basic", 2, "other", "ledger")
	before := w.api.Snapshot()
	op, c := w.start(RestoredCopyRequest{})
	if op.State != operations.Failed || c.FailedAt != "checking the target's room" || !strings.Contains(op.Detail, digitalocean.GateRepository) {
		t.Fatalf("the copy %s at %q: %s", op.State, c.FailedAt, op.Detail)
	}
	if after := w.api.Snapshot(); len(after.Apps) != len(before.Apps) || after.ClustersMade != before.ClustersMade {
		t.Fatalf("a refused copy made %d apps and %d clusters", len(after.Apps)-len(before.Apps), after.ClustersMade-before.ClustersMade)
	}
}

// A restored copy of a release whose parts and init steps are different
// images is refused when it is asked for, with App Platform's one image per
// repository and the remedy: no operation is queued and DigitalOcean is not
// called, as a refusal found later would call it through the copy's
// take-down.
//
// Kills: the bundle's rules left to the copy's start (the restored-copy
// admission skipping them).
func TestARestoredCopyOfAMultiImageReleaseIsRefusedWhenAskedFor(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	// sql-guarantee: no operation releases a multi-image bundle to App Platform any more; such a release exists only from before that refusal.
	if _, err := w.db.SQL().ExecContext(context.Background(), `UPDATE release_bundles SET init_json = jsonb_build_object('migrate', $1::text) WHERE id = $2`, releaseRepo+digestB, w.bundle.ID); err != nil {
		t.Fatal(err)
	}
	opsBefore, err := w.opsOn("prod")
	if err != nil {
		t.Fatal(err)
	}
	before := len(w.api.Snapshot().Requests)
	_, err = w.s.RequestRestoredCopy(context.Background(), w.project, "prod", RestoredCopyRequest{})
	if !errors.Is(err, deployment.ErrReleaseRefused) || !strings.HasSuffix(err.Error(), "App Platform runs one image per repository, and this release's parts and init steps use 2 different images in the project's one repository. Deploy it to a Docker host target instead.") {
		t.Fatalf("a restored copy of a multi-image release: %v", err)
	}
	if ops, err := w.opsOn("prod"); err != nil || len(ops) != len(opsBefore) {
		t.Fatalf("an operation was queued: %d, was %d (%v)", len(ops), len(opsBefore), err)
	}
	if after := w.api.Snapshot().Requests[before:]; len(after) != 0 {
		t.Fatalf("the refusal called DigitalOcean: %v", after)
	}
}

// AppTown stopping while a copy starts leaves it standing and billing,
// not forgotten: its start reads unknown, and the sweep takes it down.
func TestACopyAppTownStoppedStartingIsSweptAway(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	// AppTown stops at the start's last request (the gate check's read
	// with its session): the old start, which in life stopped with it, runs
	// on here, its end is not recorded, and (as in life) it does not take
	// the copy down: the platform does not list apps to it.
	w.platform.during = func(r *http.Request) bool {
		if r.Header.Get("Cookie") == "" {
			return false
		}
		if _, err := w.restart(); err != nil {
			t.Error(err)
		}
		w.api.Fail("GET", "/v2/apps", 500)
		return true
	}
	op, err := w.s.RequestRestoredCopy(ctx, w.project, "prod", RestoredCopyRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.ops.Run(ctx, op.ID) // its end is not recorded: the restart settled it
	w.api.Fail("GET", "/v2/apps", 0)
	got, err := w.ops.ByID(ctx, op.ID)
	if err != nil || got.State != operations.Unknown {
		t.Fatalf("the interrupted start: %+v %v", got, err)
	}
	if n, _, err := w.s.sweepRestoredCopies(ctx); err != nil || n != 1 {
		t.Fatalf("the sweep did not take the copy down: %d %v", n, err)
	}
	copies, _ := w.s.restoredCopiesOf(ctx, w.prod, 5)
	ops, _ := w.opsOn(copies[0].Deployment)
	if done := w.run(&ops[0]); !endedAs(done, operationDone) {
		t.Fatalf("take-down %s: %s", done.State, done.Detail)
	}
	if len(w.api.Snapshot().Apps) != 1 {
		t.Fatal("the interrupted copy's app stands")
	}
}

// A create the platform answered but AppTown never wrote down (the
// write failed, or AppTown stopped in between) is still taken down: the
// app and the cluster are found by the names and markers AppTown gives
// them and deleted, and only then is the copy ended. Nothing is left
// billing behind a copy shown as taken down (RTB-01).
func TestACreationAppTownDidNotRecordIsStillTakenDown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, statement string }{
		{"app identity", "UPDATE deployment_attempts SET observed_json"},
		{"cluster identity", "UPDATE need_instances SET handle_json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRestoredWorld(t, nil)
			dbtest.FailNextStatement(t, w.db, tc.statement)
			_, c := w.start(RestoredCopyRequest{})
			state := w.api.Snapshot()
			if len(state.Apps) != 1 || len(state.Databases) != 1 {
				t.Fatalf("left at the platform: %d apps, %d clusters (prod's are 1 each); copy %+v", len(state.Apps), len(state.Databases), c)
			}
			if !c.EndedAt.Valid || strings.Contains(c.EndDetail, "nothing") {
				t.Fatalf("the copy's end: %+v", c)
			}
			if got, _ := w.clusters.get(w.source+".fake", "app"); got != "ledger: 41, 42" {
				t.Fatalf("prod's data: %q", got)
			}
		})
	}
}

// When what a start made cannot be looked up, the copy is not called taken
// down: it stays standing on its card with what is unresolved, its source
// stays protected, and the sweep tries again (RTB-01).
func TestACopyWhoseLeftoversCannotBeLookedUpStaysStanding(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	c := w.standing()
	w.api.Fail("GET", "/v2/apps", 500)
	n, _, err := w.s.sweepRestoredCopies(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	ops, _ := w.opsOn(c.Deployment)
	if done := w.run(&ops[0]); done.State != operations.Failed || !strings.Contains(done.Detail, "whether anything stands is unknown") {
		t.Fatalf("take-down %s: %s", done.State, done.Detail)
	}
	copies, _ := w.s.restoredCopiesOf(ctx, w.prod, 5)
	if copies[0].EndedAt.Valid || !strings.HasPrefix(copies[0].EndDetail, "left standing") {
		t.Fatalf("a copy whose leftovers are unknown: %+v", copies[0])
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"}); !errors.Is(err, errRestoredCopiesStand) {
		t.Fatalf("prod lost its protection: %v", err)
	}
}

// Every standing copy stays on its source's card however many tests ended
// after it.
//
// The card reads stored copies, so the copies are stored records: one
// standing, checked and placed, then six ended tests, one more than the
// card's history shows. The restored-copy lifecycle tests prove the operations
// that make such records.
func TestAStandingCopyStaysOnTheCardPastTheHistory(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	pid := createTestProject(t, db, "ledger", "Ledger")
	s := assembled(db, testRuntime{}, deployment.Dependencies{})
	target, err := s.Targets.Put(ctx, &deployment.Target{Name: "local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	prod, err := s.Deployments.EnsureStanding(ctx, pid, "prod", target.ID, deployment.Attributes{DataClass: "production"}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	q := protectionsql.New(db.SQL())
	now := time.Now()
	store := func() ids.ID {
		row, err := q.InsertRestoredCopy(ctx, protectionsql.InsertRestoredCopyParams{ID: ids.New(now), ProjectID: pid, Source: "prod", SourceID: prod.ID, OperationID: idstest.N(1), BackupID: idstest.N(1), AskedBy: "alice", At: now})
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	standing := store()
	facts, err := json.Marshal(RehearsalFacts{Checks: []RehearsalCheck{{Name: "readiness", Passed: true}}, Gate: []RehearsalCheck{{Name: "refuses", Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, write := range []func() (int64, error){
		func() (int64, error) {
			return q.PlaceRestoredCopy(ctx, protectionsql.PlaceRestoredCopyParams{Deployment: "prod-copy-1", DeploymentID: idstest.N(999999), Target: "local", ID: standing})
		},
		func() (int64, error) {
			return q.RecordRestoredCopyURL(ctx, protectionsql.RecordRestoredCopyURLParams{GateUrl: "https://copy.example", ID: standing})
		},
		func() (int64, error) {
			return q.RecordRestoredCopyCheck(ctx, protectionsql.RecordRestoredCopyCheckParams{Outcome: "passed", FactsJson: facts, At: now, ID: standing})
		},
	} {
		if n, err := write(); err != nil || n != 1 {
			t.Fatalf("storing the standing copy: %d %v", n, err)
		}
	}
	for range 6 {
		if n, err := q.EndRestoredCopy(ctx, protectionsql.EndRestoredCopyParams{ID: store(), EndDetail: "taken down", At: now}); err != nil || n != 1 {
			t.Fatalf("ending a test: %d %v", n, err)
		}
	}
	collectProtectionForTest(t, s, pid, "prod")
	p, err := s.DeploymentProtection(ctx, pid, "prod")
	if err != nil {
		t.Fatal(err)
	}
	found, ended := false, 0
	for _, c := range p.RestoredCopies {
		if c.ID == standing {
			found = !c.EndedAt.Valid
		}
		if c.EndedAt.Valid {
			ended++
		}
	}
	if !found || ended != 5 {
		t.Fatalf("the standing copy on the card %v, ended shown %d of 6", found, ended)
	}
}

// appOf is the App Platform app the world's deployment name runs as: named
// by its key, read while its record stands (and remembered after).
func (w *restoredWorld) appOf(name string) string {
	w.t.Helper()
	return "apptown-" + deploy.HostKey(depOf(w.dataWorld, name).ID)
}

// An already released declaration may give a private service a grace that
// current App Platform releases refuse. The copy deliberately omits that
// service, so preflight must receive the same gated placement as rendering.
func TestARestoredCopyPreflightsOnlyTheServicesItWillStart(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	// Present retained source through the existing source-owner fake: the
	// current release entry cannot create this historical unsupported grace.
	source := w.s.sources.(dataSources)
	source.yaml = strings.Replace(source.yaml, "needs:", "  private:\n    build: .\n    ports: [\"8081\"]\n    health:\n      readiness: {http: /healthz, expect: 200, startupGrace: 3601s}\nneeds:", 1)
	source.yaml = strings.Replace(source.yaml, "public: true", "public: true\n    health: {readiness: {http: /healthz, expect: 200}}", 1)
	w.s.sources = source
	op, c := w.start(RestoredCopyRequest{})
	if !endedAs(op, operationVerified) {
		t.Fatalf("gated copy refused an omitted private service: %s at %q: %s", op.State, c.FailedAt, op.Detail)
	}
	if !slices.Contains(c.Facts.NotStarted, "private") {
		t.Fatalf("original declaration was lost: %+v", c.Facts.NotStarted)
	}
	for _, spec := range w.api.Snapshot().Specs {
		services, _ := spec["services"].([]any)
		for _, raw := range services {
			service, _ := raw.(map[string]any)
			if service["name"] == "private" {
				t.Fatal("omitted private service reached provider")
			}
		}
	}
}

func TestARestoredCopyRetainsItsDeclaredReadinessStatus(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, []byte(`app:
  web:
    build: .
    public: true
    health: {readiness: {http: /healthz, expect: 201}}
needs:
  postgres: {version: "16"}
`))
	w.platform.answer["/healthz"] = http.StatusOK
	op, c := w.start(RestoredCopyRequest{})
	if op.State != operations.Failed || c.FailedAt != "asking the app through the gate" || c.Outcome != RehearsalFailed || !c.EndedAt.Valid {
		t.Fatalf("wrong status accepted by restored owner: %s %+v", op.State, c)
	}
	if !slices.ContainsFunc(c.Facts.Checks, func(check RehearsalCheck) bool {
		return check.Name == "readiness" && check.Status == 200 && !check.Passed
	}) {
		t.Fatalf("missing failed exact-status fact: %+v", c.Facts.Checks)
	}
	w.noSecretIn("the failed-status operation", op)
}
