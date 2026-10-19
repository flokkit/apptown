package dataprotection

import (
	"context"
	"database/sql"
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
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/forge/dataprotection/internal/protectionsql"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/gate"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

const testGateImage = "registry.example/forge/gate@sha256:2222222222222222222222222222222222222222222222222222222222222222"

// gatedPlatform is the restored copy's app on App Platform as the forge
// meets it from outside: the real gate (internal/gate), run with the
// secret the forge rendered into the gate's spec, in front of the app; the
// gate redeems tickets with the forge's own RedeemGateTicket. An app with
// no gate secret in its spec answers 404. The broken-gate switches stand
// in for a gate that fails the forge's check of it.
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
	// broken is how the gate fails the forge's check of it ("" it works):
	// one of the gate... constants below.
	broken  string
	entered map[string]*httptest.ResponseRecorder
}

// The ways a gate can fail the forge's check of it.
const (
	gateDown         = "does not answer"
	gateLetsAnyoneIn = "lets in a request with no token"
	gateCacheable    = "lets its refusals be stored"
	gateAnyTicket    = "lets any ticket in"
	gateReplays      = "lets a ticket in twice"
	gateNoForge      = "cannot reach AppTown"
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
			// The body holds a row: the forge must never keep it.
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
	forge, _ := url.Parse("https://forge.example")
	h := gate.New(gate.Config{Upstream: upstream, Secret: secret, Forge: forge}, gate.Options{Client: &http.Client{Transport: forgeRedeemer{g}}})
	g.gates[key] = h
	return h
}

// forgeRedeemer is the forge's POST /gate/redeem as the gate reaches it:
// the gate's secret as a bearer credential, the ticket in the body, and
// the forge's RedeemGateTicket deciding.
type forgeRedeemer struct{ g *gatedPlatform }

func (f forgeRedeemer) RoundTrip(r *http.Request) (*http.Response, error) {
	f.g.mu.Lock()
	unreachable := f.g.broken == gateNoForge
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

// secretFor is the gate secret the forge put in app's spec, "" when its
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
// holding records, with a good copy in the store, and a forge that has a
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
	w := &restoredWorld{dataWorld: newDataWorldWith(t, dataWorldOptions{contract: contract})}
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
	w.s.restored = RestoredCopyDependencies{GateImage: testGateImage, Forge: "https://forge.example", Secrets: copySecrets{},
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
	copies, err := w.s.RestoredCopiesOf(context.Background(), w.prod, 5)
	if err != nil || len(copies) == 0 {
		w.t.Fatalf("restored copies: %v %v", copies, err)
	}
	return op, &copies[0]
}

// noSecretIn fails when a gate secret the forge rendered, or a row the
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
	op, c := w.start(RestoredCopyRequest{Test: true})
	if !endedAs(op, OperationVerified) {
		t.Fatalf("restore test %s: %s\n%+v", op.State, op.Detail, op.Steps)
	}
	if filledWithApps != 1 {
		t.Fatalf("the copy's app existed when its data was filled (apps then: %d, prod's is 1)", filledWithApps)
	}
	// The copy ran prod's release on prod's target, from the set asked.
	if c.Source != "prod" || c.Backup != w.set || c.Target != "do-syd" || !c.Test || c.Open || c.Outcome != RehearsalPassedNoData || !c.EndedAt.Valid {
		t.Fatalf("the copy: %+v", c)
	}
	if c.Facts.Withheld[0] != "SMTP_PASSWORD" || c.Facts.Egress != egressNotBlocked {
		t.Fatalf("what was withheld: %+v", c.Facts)
	}
	// The forge asked through the gate with the secret, and without it only
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
	// The forge's redemption is its own receipt, never a person's entry.
	checked := operationReceipts(t, w.db, "deployment.gate-checked", w.project)
	if len(checked) != 1 || checked[0].Actor != operations.SystemActor || !strings.Contains(string(checked[0].Payload), "nobody entered") {
		t.Fatalf("gate-checked receipts: %+v", checked)
	}
	if n := len(operationReceipts(t, w.db, "deployment.gate-entered", w.project)); n != 0 || len(c.Entries) != 0 {
		t.Fatalf("AppTown's check was recorded as an entry: %d receipts, entries %+v", n, c.Entries)
	}
	// Its app had the gate as its only public component.
	var spec map[string]any
	for _, s := range w.api.Snapshot().Specs {
		if s["name"] == "ledger-"+c.Deployment {
			spec = s
		}
	}
	raw, _ := json.Marshal(spec)
	if spec == nil || !strings.Contains(string(raw), `"ingress":{"rules":[{"component":{"name":"forge-gate"}`) || !strings.Contains(string(raw), `"internal_ports":[8080]`) {
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

// A gate ticket lasts its lifetime by the forge's clock, which also judges
// it at redemption: a ticket past its lifetime by that clock is refused and
// one within it is let in, wherever the machine's clock is.
func TestAGateTicketLastsByTheForgesClock(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	// The forge's clock, two days ahead of the machine's and moved by the test.
	var ahead atomic.Int64
	ahead.Store(int64(48 * time.Hour))
	w.s.clock = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
	op, c := w.start(RestoredCopyRequest{Minutes: 120})
	if !endedAs(op, OperationVerified) || !c.Open {
		t.Fatalf("opened copy %s: %s; %+v", op.State, op.Detail, c)
	}
	secretOf := w.platform.secretFor("ledger-" + c.Deployment)
	redeem := func(lasts time.Duration) error {
		t.Helper()
		w.auth.set("alice")
		w.auth.via = "session"
		w.s.restored.TicketFor = lasts
		to, err := w.s.OpenRestoredCopy(ctx, w.project, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		ticket := to[strings.LastIndex(to, "=")+1:]
		ahead.Add(int64(time.Minute)) // the gate redeems it a minute later, by the forge's clock
		_, err = w.s.RedeemGateTicket(ctx, secretOf, ticket)
		return err
	}
	if err := redeem(30 * time.Second); !errors.Is(err, ErrTicket) {
		t.Fatalf("a ticket a minute old that lasts 30 seconds: %v", err)
	}
	if err := redeem(2 * time.Minute); err != nil {
		t.Fatalf("a ticket a minute old that lasts two: %v", err)
	}
	w.s.restored.TicketFor = 0
}

// An opened copy stays up for its lifetime, people who may see production
// data enter it through the forge, and each entry is witnessed when the
// gate redeems its ticket: a ticket is single-use, of one copy, for a
// minute, and its holder's grant is asked again at redemption. An Auditor
// cannot open it, nor can a token, and opening is a browser's act.
func TestAnOpenedCopyAdmitsOnlyPeopleTheForgeLetsInAndWitnessesEachEntry(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	op, c := w.start(RestoredCopyRequest{Minutes: 120})
	if !endedAs(op, OperationVerified) || !strings.HasPrefix(op.Detail, "open until ") || !c.Open || c.EndedAt.Valid {
		t.Fatalf("opened copy %s: %s; %+v", op.State, op.Detail, c)
	}
	if len(w.api.Snapshot().Apps) != 2 {
		t.Fatal("the opened copy's app is not standing")
	}
	secretOf := w.platform.secretFor("ledger-" + c.Deployment)

	// A CLI token, or a caller with no browser session, gets no ticket.
	w.auth.via = "token"
	if _, err := w.s.OpenRestoredCopy(ctx, w.project, c.ID); !errors.Is(err, ErrBrowserOnly) {
		t.Fatalf("a token opened the copy: %v", err)
	}
	// An Auditor (reads everything, holds no open-restored) gets none.
	w.auth.set("auditor", "deployment:open-restored")
	w.auth.via = "session"
	if _, err := w.s.OpenRestoredCopy(ctx, w.project, c.ID); err == nil {
		t.Fatal("an Auditor opened the copy")
	}
	w.auth.set("alice")
	w.auth.via = "session"
	to, err := w.s.OpenRestoredCopy(ctx, w.project, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	ticket := strings.TrimPrefix(to, "https://ledger-"+c.Deployment+"-abc.ondigitalocean.app"+gate.EnterPath+"?ticket=")
	if ticket == to || len(ticket) != 64 {
		t.Fatalf("the address Open sends the browser to: %s", to)
	}
	// Issuing is not an entry.
	if n := len(operationReceipts(t, w.db, "deployment.gate-entered", w.project)); n != 0 {
		t.Fatalf("an entry was recorded before the gate redeemed the ticket (%d)", n)
	}
	if _, err := w.s.RedeemGateTicket(ctx, strings.Repeat("0", 64), ticket); !errors.Is(err, ErrGateCredential) {
		t.Fatalf("a ticket was redeemed by a gate without the copy's secret: %v", err)
	}
	// Two redemptions at once: one gets in.
	var wins, losses int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := w.s.RedeemGateTicket(ctx, secretOf, ticket)
			mu.Lock()
			defer mu.Unlock()
			if err == nil && g.Person == "alice" {
				wins++
			} else if errors.Is(err, ErrTicket) {
				losses++
			}
		}()
	}
	wg.Wait()
	if wins != 1 || losses != 3 {
		t.Fatalf("redemptions of one ticket: %d in, %d refused", wins, losses)
	}
	entered := operationReceipts(t, w.db, "deployment.gate-entered", w.project)
	if len(entered) != 1 || entered[0].Actor != "alice" || !strings.Contains(string(entered[0].Payload), "does not record signing in to the app") {
		t.Fatalf("entry receipts: %+v", entered)
	}
	// The grant lasts an hour at most, and never past the copy's time.
	w.auth.set("alice")
	w.auth.via = "session"
	to, _ = w.s.OpenRestoredCopy(ctx, w.project, c.ID)
	ticket = to[strings.LastIndex(to, "=")+1:]
	g, err := w.s.RedeemGateTicket(ctx, secretOf, ticket)
	if err != nil || time.Until(g.Until) > GateSessionMost || time.Until(g.Until) < 50*time.Minute {
		t.Fatalf("the grant: %+v %v", g, err)
	}
	// A grant removed between Open and the gate's redemption keeps her out.
	to, _ = w.s.OpenRestoredCopy(ctx, w.project, c.ID)
	ticket = to[strings.LastIndex(to, "=")+1:]
	w.auth.set("alice", "deployment:open-restored")
	if _, err := w.s.RedeemGateTicket(ctx, secretOf, ticket); err == nil {
		t.Fatal("a person whose grant was removed after Open was let in")
	}
	// A ticket past its minute is refused.
	w.auth.set("alice")
	w.auth.via = "session"
	var later atomic.Int64
	w.s.clock = func() time.Time { return time.Now().Add(time.Duration(later.Load())) }
	to, _ = w.s.OpenRestoredCopy(ctx, w.project, c.ID)
	ticket = to[strings.LastIndex(to, "=")+1:]
	later.Store(int64(2 * time.Minute)) // past the ticket's minute, by the forge's clock
	if _, err := w.s.RedeemGateTicket(ctx, secretOf, ticket); !errors.Is(err, ErrTicket) {
		t.Fatalf("an expired ticket: %v", err)
	}
	w.s.restored.TicketFor = 0
	copies, err := w.s.RestoredCopiesOf(ctx, w.prod, 5)
	if err != nil || len(copies[0].Entries) != 2 || copies[0].Entries[0].Person != "alice" {
		t.Fatalf("the card's entries: %+v %v", copies, err)
	}
	w.noSecretIn("the card", copies)
	w.noSecretIn("the entries", entered)
}

// The forge's check of a gate that does not hold fails the restore: the
// check that caught it is recorded failed with its status, the copy is
// taken down, and an opened copy is never left open behind such a gate.
//
// Kills, by the case that catches it: the refusal checks not demanding
// 404 or the entry checks 403 (soft refusal); not demanding the gate's own
// mark on a 404 (an app that answers 404) or on an entry refusal (passes
// every entry on); not demanding an empty body (talks) or no-store
// (cacheable); asking / and not the readiness path (guards only its front
// page); the forge's own ticket not required to redirect (forwards) or to
// give a session (no session, no forge); a second redemption not refused
// (replays); the session's read not judged (forgets); a gate failure that
// does not fail the copy, or leaves an opened copy open (every case).
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
		{name: gateNoForge, broken: gateNoForge, failed: []string{"lets AppTown's own ticket in", "serves the app to that session"}, step: gateCheckStep},
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
			// An opened copy, so a copy that answers is still taken down
			// when its gate does not hold.
			op, c := w.start(RestoredCopyRequest{Minutes: 60})
			if op.State != operations.Failed || c.Outcome != RehearsalFailed || c.FailedAt != tc.step || c.Open || !c.EndedAt.Valid {
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

// A copy whose gate failed its check is never open to people, even when
// its take-down could not remove it.
//
// Kills: Open (and so Open's tickets) that asks readiness but not the gate.
func TestACopyWhoseGateFailedIsNeverOpen(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	w.platform.broken = gateLetsAnyoneIn
	// Once the gate check begins, the platform cannot list apps, so the
	// take-down leaves the copy standing.
	w.platform.during = func(r *http.Request) bool {
		if r.Header.Get(deploy.GateProbeHeader) != "" {
			return false
		}
		w.api.Fail("GET", "/v2/apps", 500)
		return true
	}
	op, c := w.start(RestoredCopyRequest{Minutes: 60})
	if op.State != operations.Failed || c.EndedAt.Valid || !readinessPassed(c.Facts) {
		t.Fatalf("the copy %s: %s; %+v", op.State, op.Detail, c)
	}
	if c.Open || c.Protection != "" {
		t.Fatalf("a copy whose gate failed reads open: %+v", c)
	}
	w.auth.set("alice")
	w.auth.via = "session"
	if _, err := w.s.OpenRestoredCopy(ctx, w.project, c.ID); !errors.Is(err, ErrRestoredCopy) {
		t.Fatalf("a copy whose gate failed was opened: %v", err)
	}
}

// The ticket the forge issues itself lets in only the check of the start
// that issued it, while that start runs; and nobody's ticket opens a
// restore test.
//
// Kills: a check ticket accepted after its start ended, or from another
// copy's start; a person's ticket accepted on a copy that is not open.
func TestTheForgesOwnTicketLetsInOnlyItsRunningCheck(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	_, opened := w.start(RestoredCopyRequest{Minutes: 60})
	secretOf := w.platform.secretFor("ledger-" + opened.Deployment)
	if !opened.Open || len(opened.Entries) != 0 || secretOf == "" {
		t.Fatalf("the opened copy: %+v", opened)
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
	if err := redeem(opened.ID, operations.SystemActor, opened.Operation, secretOf); !errors.Is(err, ErrTicket) {
		t.Fatalf("a check ticket of an ended start: %v", err)
	}
	// While a restore test of prod runs: a check ticket naming its start
	// does not open the other copy, and alice's ticket does not open the
	// test.
	w.platform.during = func(*http.Request) bool {
		copies, err := w.s.RestoredCopiesOf(ctx, w.prod, 5)
		if err != nil || copies[0].ID == opened.ID {
			t.Errorf("the test's copy: %+v %v", copies, err)
			return true
		}
		test := copies[0]
		if err := redeem(opened.ID, operations.SystemActor, test.Operation, secretOf); !errors.Is(err, ErrTicket) {
			t.Errorf("a check ticket of another copy's running start: %v", err)
		}
		if err := redeem(test.ID, "alice", ids.Nil, w.platform.secretFor("ledger-"+test.Deployment)); !errors.Is(err, ErrTicket) {
			t.Errorf("a person's ticket on a restore test: %v", err)
		}
		return true
	}
	if op, _ := w.start(RestoredCopyRequest{Test: true}); !endedAs(op, OperationVerified) {
		t.Fatalf("the restore test %s: %s", op.State, op.Detail)
	}
	if n := len(operationReceipts(t, w.db, "deployment.gate-entered", w.project)); n != 0 {
		t.Fatalf("%d entries recorded", n)
	}
}

// An opened copy's time is kept: an extension moves it, with a receipt
// naming who; when it runs out the sweep takes the copy down (no final
// copy), and prod is not taken down while a copy of it stands.
func TestAnOpenedCopyIsTakenDownWhenItsTimeRunsOut(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	_, c := w.start(RestoredCopyRequest{Minutes: 30})
	// While it stands it is on prod's card only, never among the project's
	// deployments.
	if listed, err := w.s.Deployments.ForProject(ctx, w.project); err != nil || len(listed) != 1 || listed[0].Name != "prod" {
		t.Fatalf("listed while the copy stands: %v %v", listed, err)
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"}); !errors.Is(err, ErrRestoredCopiesStand) {
		t.Fatalf("prod was taken down while a copy of it stands: %v", err)
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: c.Deployment}); !errors.Is(err, deployment.ErrRestoredCopyDoor) {
		t.Fatalf("the copy was taken down as a deployment: %v", err)
	}
	extended, err := w.s.ExtendRestoredCopy(ctx, w.project, c.ID, 180)
	if err != nil || extended.ExpiresAt.Time.Sub(c.ExpiresAt.Time) < 2*time.Hour {
		t.Fatalf("extended: %+v %v", extended, err)
	}
	if got := operationReceipts(t, w.db, "deployment.restored-copy-extended", w.project); len(got) != 1 || got[0].Actor != "alice" {
		t.Fatalf("extension receipts: %+v", got)
	}
	// Not due yet.
	if n, _, err := w.s.sweepRestoredCopies(ctx, time.Now()); err != nil || n != 0 {
		t.Fatalf("swept before its time: %d %v", n, err)
	}
	n, skipped, err := w.s.sweepRestoredCopies(ctx, time.Now().Add(4*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d %v %v", n, skipped, err)
	}
	ops, err := w.opsOn(c.Deployment)
	if err != nil || len(ops) != 1 || ops[0].Kind != OperationRestoredCopyEnd {
		t.Fatalf("the copy's take-down: %+v %v", ops, err)
	}
	if done := w.run(&ops[0]); !endedAs(done, OperationDone) {
		t.Fatalf("take-down %s: %s", done.State, done.Detail)
	}
	copies, _ := w.s.RestoredCopiesOf(ctx, w.prod, 5)
	if !copies[0].EndedAt.Valid || copies[0].Open {
		t.Fatalf("after its time: %+v", copies[0])
	}
	if state := w.api.Snapshot(); len(state.Apps) != 1 || len(state.DeletedClusters) != 1 || state.DeletedClusters[0] == w.source {
		t.Fatalf("after the sweep: apps %d, deleted %v", len(state.Apps), state.DeletedClusters)
	}
	if _, err := w.s.ExtendRestoredCopy(ctx, w.project, c.ID, 60); err == nil {
		t.Fatal("a copy taken down was extended")
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"}); errors.Is(err, ErrRestoredCopiesStand) {
		t.Fatal("prod is still held by a copy taken down")
	}
}

// A copy that fails is taken down and says where it failed in safe words:
// an import that fails names a row in its output, and that row reaches no
// operation, card, receipt or record; a copy whose app is not ready is not
// left open.
func TestARestoredCopyThatFailsIsTakenDownAndSaysOnlySafeFacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("import", func(t *testing.T) {
		t.Parallel()
		w := newRestoredWorld(t, nil)
		w.clusters.importSays = `ERROR: duplicate key value violates unique constraint "users_email_key" DETAIL: Key (email)=(carol@example.com) already exists.`
		op, c := w.start(RestoredCopyRequest{Minutes: 60})
		if op.State != operations.Failed || !strings.Contains(op.Detail, "filling postgres from the copy failed") || !strings.Contains(op.Detail, "exit status 1") {
			t.Fatalf("failed import %s: %s", op.State, op.Detail)
		}
		if !c.EndedAt.Valid || c.Open {
			t.Fatalf("the failed copy stands: %+v", c)
		}
		if state := w.api.Snapshot(); len(state.DeletedClusters) != 1 || state.DeletedClusters[0] == w.source {
			t.Fatalf("the failed copy's cluster: %v", state.DeletedClusters)
		}
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
		op, c := w.start(RestoredCopyRequest{Minutes: 60})
		if op.State != operations.Failed || c.Open || !c.EndedAt.Valid || c.Outcome != RehearsalFailed {
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
		op, c := w.start(RestoredCopyRequest{Test: true})
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
	if _, err := w.s.RequestRestoredCopy(ctx, w.project, "prod", RestoredCopyRequest{Test: true}); err == nil {
		t.Fatal("a caller without deployment:restore started a restored copy")
	}
	w.auth.set("alice")
	if _, err := w.s.RequestRestoredCopy(ctx, w.project, "prod", RestoredCopyRequest{Minutes: 5}); !errors.Is(err, ErrRestoredCopy) {
		t.Fatalf("a five-minute copy: %v", err)
	}
	w.nameRunner("")
	if _, err := w.s.RequestRestoredCopy(ctx, w.project, "prod", RestoredCopyRequest{Test: true}); !errors.Is(err, deployment.ErrNoCopyRunner) {
		t.Fatalf("no copy runner: %v", err)
	}
	w.nameRunner("local")
	keep := w.s.restored
	w.s.restored.GateImage = ""
	if _, err := w.s.RequestRestoredCopy(ctx, w.project, "prod", RestoredCopyRequest{Test: true}); !errors.Is(err, ErrNoGate) {
		t.Fatalf("no gate image: %v", err)
	}
	w.s.restored = keep
	w.s.restored.Secrets = copySecrets{inherits: true}
	op, c := w.start(RestoredCopyRequest{Test: true})
	if op.State != operations.Failed || !strings.Contains(op.Detail, "inherits") || !c.EndedAt.Valid {
		t.Fatalf("an inheriting profile %s: %s; %+v", op.State, op.Detail, c)
	}
	if got := len(w.api.Snapshot().Apps); got != apps {
		t.Fatalf("a refused copy made an app (%d, was %d)", got, apps)
	}
	if _, err := w.ops.Cancel(ctx, op.ID); !errors.Is(err, ErrRestoreNotStopped) {
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
	op, c := w.start(RestoredCopyRequest{Test: true})
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
	if _, err := w.db.Exec(`UPDATE release_bundles SET init_json = jsonb_build_object('migrate', ?::text) WHERE id = ?`, releaseRepo+digestB, w.bundle.ID); err != nil {
		t.Fatal(err)
	}
	opsBefore, err := w.opsOn("prod")
	if err != nil {
		t.Fatal(err)
	}
	before := len(w.api.Snapshot().Requests)
	_, err = w.s.RequestRestoredCopy(context.Background(), w.project, "prod", RestoredCopyRequest{Test: true})
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

// The forge stopping while a copy starts leaves it standing and billing,
// not forgotten: its start reads unknown, and the sweep takes it down.
func TestACopyTheForgeStoppedStartingIsSweptAway(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	// The forge stops at the start's last request (the gate check's read
	// with its session): the old start, which in life stopped with it, runs
	// on here and leaves the copy open, as if it had ended unrecorded.
	w.platform.during = func(r *http.Request) bool {
		if r.Header.Get("Cookie") == "" {
			return false
		}
		if _, err := w.restart(); err != nil {
			t.Error(err)
		}
		return true
	}
	op, err := w.s.RequestRestoredCopy(ctx, w.project, "prod", RestoredCopyRequest{Minutes: 240})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.ops.Run(ctx, op.ID) // its end is not recorded: the restart settled it
	got, err := w.ops.ByID(ctx, op.ID)
	if err != nil || got.State != operations.Unknown {
		t.Fatalf("the interrupted start: %+v %v", got, err)
	}
	if n, _, err := w.s.sweepRestoredCopies(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("the sweep did not take the copy down: %d %v", n, err)
	}
	copies, _ := w.s.RestoredCopiesOf(ctx, w.prod, 5)
	ops, _ := w.opsOn(copies[0].Deployment)
	if done := w.run(&ops[0]); !endedAs(done, OperationDone) {
		t.Fatalf("take-down %s: %s", done.State, done.Detail)
	}
	if len(w.api.Snapshot().Apps) != 1 {
		t.Fatal("the interrupted copy's app stands")
	}
}

// A create the platform answered but the forge never wrote down (the
// write failed, or the forge stopped in between) is still taken down: the
// app and the cluster are found by the names and markers the forge gives
// them and deleted, and only then is the copy ended. Nothing is left
// billing behind a copy shown as taken down (RTB-01).
func TestACreationTheForgeDidNotRecordIsStillTakenDown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, statement string }{
		{"app identity", "UPDATE deployment_attempts SET observed_json"},
		{"cluster identity", "UPDATE need_instances SET handle_json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRestoredWorld(t, nil)
			dbtest.FailNextStatement(t, w.db, tc.statement)
			_, c := w.start(RestoredCopyRequest{Test: true})
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
	_, c := w.start(RestoredCopyRequest{Minutes: 30})
	w.api.Fail("GET", "/v2/apps", 500)
	n, _, err := w.s.sweepRestoredCopies(ctx, time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	ops, _ := w.opsOn(c.Deployment)
	if done := w.run(&ops[0]); done.State != operations.Failed || !strings.Contains(done.Detail, "whether anything stands is unknown") {
		t.Fatalf("take-down %s: %s", done.State, done.Detail)
	}
	copies, _ := w.s.RestoredCopiesOf(ctx, w.prod, 5)
	if copies[0].EndedAt.Valid || !strings.HasPrefix(copies[0].EndDetail, "left standing") {
		t.Fatalf("a copy whose leftovers are unknown: %+v", copies[0])
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"}); !errors.Is(err, ErrRestoredCopiesStand) {
		t.Fatalf("prod lost its protection: %v", err)
	}
}

// Every standing copy stays on its source's card, with its doors, however
// many tests ended after it.
//
// The card reads stored copies, so the copies are stored records: one
// open, checked and placed, then six ended tests, one more than the card's
// history shows. The restored-copy lifecycle tests prove the operations
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
	store := func(test bool) ids.ID {
		row, err := q.InsertRestoredCopy(ctx, protectionsql.InsertRestoredCopyParams{ID: ids.New(now), ProjectID: pid, Source: "prod", SourceID: prod.ID, OperationID: idstest.N(1), BackupID: idstest.N(1), IsTest: test, LifetimeMinutes: 240, AskedBy: "alice", At: now})
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	opened := store(false)
	facts, err := json.Marshal(RehearsalFacts{Checks: []RehearsalCheck{{Name: "readiness", Passed: true}}, Gate: []RehearsalCheck{{Name: "refuses", Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, write := range []func() (int64, error){
		func() (int64, error) {
			return q.PlaceRestoredCopy(ctx, protectionsql.PlaceRestoredCopyParams{Deployment: "prod-copy-1", DeploymentID: idstest.N(999999), Target: "local", ExpiresAt: sql.NullTime{Time: now.Add(240 * time.Minute), Valid: true}, ID: opened})
		},
		func() (int64, error) {
			return q.RecordRestoredCopyURL(ctx, protectionsql.RecordRestoredCopyURLParams{GateUrl: "https://copy.example", ID: opened})
		},
		func() (int64, error) {
			return q.RecordRestoredCopyCheck(ctx, protectionsql.RecordRestoredCopyCheckParams{Outcome: "passed", FactsJson: facts, At: now, ID: opened})
		},
	} {
		if n, err := write(); err != nil || n != 1 {
			t.Fatalf("storing the open copy: %d %v", n, err)
		}
	}
	for range 6 {
		if n, err := q.EndRestoredCopy(ctx, protectionsql.EndRestoredCopyParams{ID: store(true), EndDetail: "taken down", At: now}); err != nil || n != 1 {
			t.Fatalf("ending a test: %d %v", n, err)
		}
	}
	p, err := s.DeploymentProtection(ctx, pid, "prod")
	if err != nil {
		t.Fatal(err)
	}
	found, ended := false, 0
	for _, c := range p.RestoredCopies {
		if c.ID == opened {
			found = c.Open
		}
		if c.EndedAt.Valid {
			ended++
		}
	}
	if !found || ended != 5 {
		t.Fatalf("the open copy on the card %v, ended shown %d of 6", found, ended)
	}
}

// A restored copy's time is the forge's clock, written and judged: with
// that clock two days off the wall clock, a copy just opened for 30 minutes
// is open and not swept, and an hour later by the same clock it is closed
// and swept.
//
// Kills: the expiry written from the wall clock (the copy reads as closed
// at once), or judged by it (the copy stays open after its time).
func TestARestoredCopysTimeIsTheForgesClock(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	var ahead atomic.Int64
	ahead.Store(int64(48 * time.Hour))
	w.s.clock = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
	_, c := w.start(RestoredCopyRequest{Minutes: 30})
	if !c.Open {
		t.Fatalf("a copy just opened is not open by AppTown's clock: %+v", c)
	}
	if n, _, err := w.s.SweepRestoredCopies(ctx); err != nil || n != 0 {
		t.Fatalf("swept before its time: %d %v", n, err)
	}
	ahead.Add(int64(time.Hour))
	copies, err := w.s.RestoredCopiesOf(ctx, w.prod, 5)
	if err != nil || len(copies) == 0 || copies[0].Open {
		t.Fatalf("still open after its time by AppTown's clock: %+v %v", copies, err)
	}
	if n, _, err := w.s.SweepRestoredCopies(ctx); err != nil || n != 1 {
		t.Fatalf("not swept after its time: %d %v", n, err)
	}
}

// A ticket lets in the principal it was issued to and no one by name: one
// issued to a name the forge cannot tie to an account lets nobody in, so
// a later account given that name could not borrow it.
func TestAGateTicketNamingNoPrincipalLetsNobodyIn(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	op, c := w.start(RestoredCopyRequest{Minutes: 120})
	if !endedAs(op, OperationVerified) || !c.Open {
		t.Fatalf("opened copy %s: %s; %+v", op.State, op.Detail, c)
	}
	w.auth.set("zed")
	w.auth.via = "session"
	to, err := w.s.OpenRestoredCopy(ctx, w.project, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	ticket := to[strings.LastIndex(to, "=")+1:]
	if _, err := w.s.RedeemGateTicket(ctx, w.platform.secretFor("ledger-"+c.Deployment), ticket); !errors.Is(err, ErrTicket) {
		t.Fatalf("a ticket naming no principal: %v", err)
	}
}
