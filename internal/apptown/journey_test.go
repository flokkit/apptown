//go:build journey

package apptown

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/strs"
)

// Journeys (design/architecture/testing-map.md): whole operational flows driven
// through the public API the way a person or agent uses AppTown. The
// fake agent (testdata/fake-pi) makes them deterministic and free;
// docker keeps containers real. Run with:
//
//	go test -tags=journey ./internal/apptown/ -run TestJourney -v

// jHarness boots an in-process AppTown with the fake agent in its Docker sandbox.
type jHarness struct {
	t   *testing.T
	app *App
	srv *httptest.Server
}

// removeMainAppAtCleanup removes the app container of the project's main
// deployment when the test ends, by the name AppTown gave it (the
// deployment's key), read from its record then: the deployment exists only
// once the journey has deployed.
func (h *jHarness) removeMainAppAtCleanup(slug string) {
	h.t.Cleanup(func() {
		ctx := context.Background()
		p, err := h.app.Projects.BySlug(ctx, slug)
		if err != nil {
			return
		}
		dep, err := h.app.Deployment.Deployments.ByName(ctx, p.ID, "main")
		if err != nil {
			return
		}
		if names, err := dep.HostNames(); err == nil {
			_ = dockertest.Run("rm", "-f", names.App())
		}
	})
}

func newJourney(t *testing.T, configs ...Config) *jHarness {
	t.Helper()
	if err := dockertest.Run("info"); err != nil {
		t.Fatalf("journeys require a running docker daemon: %v", err)
	}
	// Keep the supported credential boundary even for the deterministic harness.
	bin := t.TempDir()
	src, err := os.ReadFile("testdata/fake-pi")
	if err != nil {
		t.Fatal(err)
	}
	if err := execfile.Write(filepath.Join(bin, "pi"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "Dockerfile"), []byte("FROM apptown-agent\nCOPY pi /usr/local/bin/pi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	image := fmt.Sprintf("forge-journey:%d-%d", os.Getpid(), time.Now().UnixNano())
	if out, err := dockertest.CombinedOutput("build", "-q", "-t", image, bin); err != nil {
		t.Fatalf("build fake harness: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = dockertest.Run("image", "rm", image) })

	// Dedicated PostgreSQL database and disposable application workspaces.
	cfg := Config{}
	if len(configs) != 0 {
		cfg = configs[0]
	}
	cfg.DataDir, cfg.AgentExecutor, cfg.AgentImage = t.TempDir(), ExecutorDocker, image
	// The fake harness never calls inference. Environment preparation still
	// receives a gateway URL, as every Docker agent does.
	cfg.InferenceURL = "http://host.docker.internal:1"
	cfg.InferenceNetworks = []string{"127.0.0.1/32"}
	a := constructApp(t, testDB(t), cfg)
	// The journey creates projects, installs workflows and approves changes.
	// Its HTTP attribution is not an authorization grant. Under internal
	// auth the journey signs people in instead, and the first of them must
	// still be able to bootstrap the instance.
	if cfg.AuthMode != "internal" {
		testAdministrator(t, a, "journey")
	}
	if err := a.Models.SeedFallbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A model is set up, as on an AppTown with a key added: run admission
	// refuses model work on one with none.
	if err := a.Models.SetDefault(context.Background(), "fake/model"); err != nil {
		t.Fatal(err)
	}
	a.SyncStock(t.Context()) // the same boot list as apptownd: every stock kind synced
	a.SeedTargets(t.Context())
	// Completion and release are durable jobs; an HTTP server alone cannot
	// carry an accepted change through landing and deployment.
	if err := a.Jobs().Client.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.ShutdownBackground(ctx); err != nil {
			t.Error(err)
		}
		if err := a.Jobs().Client.StopAndCancel(ctx); err != nil {
			t.Error(err)
		}
	})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &jHarness{t: t, app: a, srv: srv}
}

// jCall drives the public surface; any 5xx fails the journey on the
// spot — "keep working" is an invariant, not a hope.
func (h *jHarness) jCall(method, path string, body any, out any) int {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+"/api/v1"+path, rd)
	req.Header.Set("X-AppTown-Actor", "journey")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 500 {
		h.t.Fatalf("%s %s: %d — a journey may fail its asserts, never 5xx: %s", method, path, res.StatusCode, raw)
	}
	if out != nil && res.StatusCode < 300 {
		_ = json.Unmarshal(raw, out)
	}
	if res.StatusCode >= 300 {
		h.t.Logf("%s %s -> %d %s", method, path, res.StatusCode, strs.Truncate(string(raw), 200))
	}
	return res.StatusCode
}

func (h *jHarness) mustCall(method, path string, body any, out any) {
	h.t.Helper()
	if code := h.jCall(method, path, body, out); code >= 300 {
		h.t.Fatalf("%s %s: %d", method, path, code)
	}
}

// until polls a condition with a deadline — journeys wait on real
// machinery (goroutines, containers), never sleep-and-hope.
func (h *jHarness) until(what string, timeout time.Duration, cond func() bool) {
	h.t.Helper()
	if !poll.Until(context.Background(), timeout, 100*time.Millisecond, cond) {
		h.t.Fatalf("timed out waiting for %s", what)
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// staticSiteTask is a change's sentence whose fake agent writes a static site
// and its runtime contract. The contract declares a readiness path, as the
// apptown pack's health rule requires, and the site serves it; stock land's
// conformance gate refuses a served part without one.
func staticSiteTask(text, page string) string {
	contract := "app:\n  web:\n    static: site\n    public: true\n    health: { readiness: { http: /healthz } }\n"
	return text + ". " +
		"[test:write-b64 site/index.html " + b64(page) + "] " +
		"[test:write-b64 site/healthz " + b64("ok\n") + "] " +
		"[test:write-b64 .apptown/app.yaml " + b64(contract) + "]"
}

// journeyWorkflows: a minimal working lane + landing lane. The agent
// step's prompt carries the change body, which is how tests hand the
// fake agent its directives.
const journeyQuickChange = `
apiVersion: apptown.dev/v1
kind: Workflow
metadata: { name: quick-change, category: change }
spec:
  kind: lifecycle
  role: working
  stages:
    - name: work
      onEnter:
        - name: do
          uses: agent/pi
          with: { prompt: "Do exactly this: ${{change.body}}" }
      steps:
        - name: accept
          uses: approval
          with: { approvers: [journey], means: "I looked at what the agent did" }
`

// Journeys land through stock land, the landing every real project runs;
// with no scanner attached its scan steps pass with a note. The one
// exception is unscannedLand, which stands in for stock land (as an
// instance's own landing does) in LawMidFlight only: that journey needs a
// landing with no scan before its deploy, so the law it adds has something
// to catch.
const unscannedLand = `
apiVersion: apptown.dev/v1
kind: Workflow
metadata: { name: unscanned-land, system: true, category: landing }
spec:
  kind: pipeline
  role: landing
  workspace: project
  stages:
    - name: gates
      steps:
        - name: approve-landing
          uses: approval
          with: { approvers: [journey], means: "this merges and deploys on my say-so" }
    - name: ship
      onEnter:
        - name: deploy
          uses: deploy
          with: { deployment: main }
`

type jChangeView struct {
	Change struct {
		ID     ids.ID `json:"id"`
		State  string `json:"state"`
		Branch string `json:"branch"`
	} `json:"change"`
	Detail *struct {
		Run struct {
			ID           ids.ID `json:"id"`
			Status       string `json:"status"`
			CurrentStage string `json:"currentStage"`
			WorkflowName string `json:"workflowName"`
		} `json:"run"`
		Steps []struct {
			ID       ids.ID          `json:"id"`
			Name     string          `json:"name"`
			Type     string          `json:"type"`
			Status   string          `json:"status"`
			Evidence json.RawMessage `json:"evidence"`
		} `json:"steps"`
	} `json:"detail"`
}

func (h *jHarness) changeView(id ids.ID) jChangeView {
	var v jChangeView
	h.mustCall("GET", fmt.Sprintf("/changes/%s", id), nil, &v)
	return v
}

// jQuestion is what a client reads before approving: the landing's
// sentences and what the approval's commit adds, whose digest it sends back.
type jQuestion struct {
	LandingMeans []string `json:"landingMeans"`
	Adds         *struct {
		Digest string `json:"digest"`
	} `json:"adds"`
}

func (q jQuestion) digest() string {
	if q.Adds == nil {
		return ""
	}
	return q.Adds.Digest
}

// approveStep approves as a client does: it reads what approving means,
// including what the landing would ask beside an acceptance, and sends back
// what it was shown.
func (h *jHarness) approveStep(stepID ids.ID) {
	var q jQuestion
	h.mustCall("GET", fmt.Sprintf("/steps/%s/question", stepID), nil, &q)
	h.mustCall("POST", fmt.Sprintf("/steps/%s/approve", stepID),
		map[string]any{"decision": "approve", "comment": "journey", "alsoApproves": q.LandingMeans, "adds": q.digest()}, nil)
}

func (h *jHarness) findStep(v jChangeView, name string) (ids.ID, string) {
	for _, s := range v.Detail.Steps {
		if s.Name == name {
			return s.ID, s.Status
		}
	}
	return ids.Nil, ""
}

func (h *jHarness) newProject(slug string, yamls ...string) {
	names := publishLanes(h.t, h.app, yamls...)
	h.mustCall("POST", "/projects", map[string]string{"slug": slug}, nil)
	h.mustCall("POST", "/projects/"+slug+"/repo", map[string]string{}, nil)
	selectLanes(h.t, h.app, slug, names...)
}

// publishLanes publishes a journey's shape of each workflow as the library's
// newest version — what an admin does under Ways of working — and answers
// the lanes among them. Per-project
// workflow copies are gone (the lanes design), so journeys stopped posting to
// /projects/{slug}/workflows. Publish before creating the project or pack that
// should run them: an adoption revision keeps the versions it already pinned,
// so a version published later never reaches the project by a lane edit and
// the journey would silently run stock land instead.
func publishLanes(t *testing.T, a *App, yamls ...string) []string {
	t.Helper()
	var names []string
	for _, yamlText := range yamls {
		if name := publishLane(t, a, yamlText); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// publishLane answers the lane's name, or "" for a system workflow, which
// every project runs without selecting it.

func publishLane(t *testing.T, a *App, yamlText string) string {
	t.Helper()
	doc, err := schema.ParseWorkflow([]byte(yamlText))
	if err != nil {
		t.Fatal(err)
	}
	name := doc.Metadata.Name
	if doc.Metadata.System {
		// A custom landing is published and bound in place of stock land; the
		// apptown pack keeps pinning land itself.
		if _, _, err := a.Ways.PublishWorkflow(t.Context(), yamlText, "fixture"); err != nil {
			t.Fatal(err)
		}
		if err := a.Ways.BindSystemWorkflow(t.Context(), "land", name, "fixture"); err != nil {
			t.Fatal(err)
		}
		return ""
	}
	if origin, lookupErr := a.Ways.WorkflowOrigin(t.Context(), name); lookupErr == nil && origin == "stock" {
		if _, err := wow.NewWorkflows(a.DB).StockUpdate(t.Context(), name, doc.Metadata.Description, yamlText); err != nil {
			t.Fatal(err)
		}
	} else if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		t.Fatal(lookupErr)
	} else {
		if published, _, err := a.Ways.PublishWorkflow(t.Context(), yamlText, "fixture"); err != nil {
			t.Fatal(err)
		} else if published != name {
			t.Fatalf("published workflow %q, want %q", published, name)
		}
	}
	return name
}

// selectLanes selects published workflows for a project.
func selectLanes(t *testing.T, a *App, slug string, names ...string) {
	t.Helper()
	p, err := a.Projects.BySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := a.lifecycle.SelectLane(t.Context(), p.ID, name, true, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
}

// TestJourneyKidStory — D1, the whole arc: one sentence in, a running
// app on the main tier out. The fake agent writes a static site plus
// its runtime contract; a person approves twice; the machinery does
// everything else for real: run, gates, auto-landing, merge, deploy,
// container, HTTP.
func TestJourneyKidStory(t *testing.T) {
	h := newJourney(t)
	slug := fmt.Sprintf("kid-%d", os.Getpid())
	h.newProject(slug, journeyQuickChange)
	h.removeMainAppAtCleanup(slug)

	page := `<!doctype html><html><body><h1>the cat is blue</h1><script>console.log("hi")</script></body></html>`
	sentence := staticSiteTask("make the cat blue", page)

	var created struct {
		Change struct{ ID ids.ID }
	}
	h.mustCall("POST", "/projects/"+slug+"/changes", map[string]string{"title": "make the cat blue", "body": sentence, "workflow": "quick-change"}, &created)
	chID := created.Change.ID
	if chID.IsZero() {
		t.Fatal("no change created")
	}

	// The fake agent runs; the accept gate lights up.
	var acceptID ids.ID
	var lastView jChangeView
	t.Cleanup(func() {
		if t.Failed() {
			data, _ := json.Marshal(lastView)
			t.Logf("last change view: %s", data)
		}
	})
	h.until("the agent to finish and the accept gate to wait", 60*time.Second, func() bool {
		v := h.changeView(chID)
		lastView = v
		if v.Detail == nil {
			return false
		}
		for _, step := range v.Detail.Steps {
			if step.Status == workflow.StatusError.String() || step.Status == workflow.StatusFail.String() {
				t.Fatalf("initial work failed at %s: %s", step.Name, step.Evidence)
			}
		}
		if id, status := h.findStep(v, "accept"); status == workflow.StatusPending.String() {
			if doID, doStatus := h.findStep(v, "do"); !doID.IsZero() && doStatus == workflow.StatusPass.String() {
				acceptID = id
				return true
			}
		}
		return false
	})

	// The change's branch holds the agent's real edits, shared, before
	// anyone approves.
	shared := filepath.Join(h.app.cfg.dataDir, "workspaces", "project-"+slug)
	if b, err := gitexec.Output(t.Context(), shared, "show", "refs/heads/"+h.changeView(chID).Change.Branch+":site/index.html"); err != nil || !strings.Contains(b, "the cat is blue") {
		t.Fatalf("the agent's edit must be real and shared: %v", err)
	}

	h.approveStep(acceptID)

	// Acceptance auto-starts the landing; its gates run; the acceptance
	// counts for its approval, because the accepter may approve it and was
	// shown its sentence; the merge lands the branch and the deploy boots a
	// real container. Nobody approves twice.
	h.until("the landing to finish and the change to ship", 120*time.Second, func() bool {
		v := h.changeView(chID)
		lastView = v
		if v.Detail != nil {
			for _, step := range v.Detail.Steps {
				if step.Name == "deploy" && (step.Status == workflow.StatusError.String() || step.Status == workflow.StatusFail.String()) {
					t.Fatalf("landing failed at %s: %s", step.Name, step.Evidence)
				}
			}
		}
		return v.Change.State == "shipped"
	})
	if _, status := h.findStep(lastView, "approve-landing"); status != workflow.StatusPass.String() {
		t.Fatalf("the landing approval must pass: %q", status)
	}
	for _, step := range lastView.Detail.Steps {
		if step.Name == "approve-landing" && !strings.Contains(string(step.Evidence), "accepted at the change gate") {
			t.Fatalf("the landing approval must be the carried acceptance: %s", step.Evidence)
		}
	}

	// The standing deployment serves the project's page over real HTTP.
	var envs []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	h.mustCall("GET", "/projects/"+slug+"/deployments", nil, &envs)
	var mainURL string
	for _, e := range envs {
		if e.Name == "main" {
			mainURL = e.URL
		}
	}
	if mainURL == "" {
		v := h.changeView(chID)
		for _, st := range v.Detail.Steps {
			t.Logf("land step %s (%s) %s: %s", st.Name, st.Type, st.Status, strs.Truncate(string(st.Evidence), 300))
		}
		t.Fatalf("no main deployment recorded: %+v (land run %s/%s)", envs, v.Detail.Run.WorkflowName, v.Detail.Run.Status)
	}
	h.until("the main deployment to serve the page", 60*time.Second, func() bool {
		res, err := http.Get(mainURL)
		if err != nil {
			return false
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode == 200 && strings.Contains(string(b), "the cat is blue")
	})

	// The record: landed branch, audited acts, evidence on every done step.
	repo := h.app.projectRepoPath(slug)
	if out, _ := gitexec.Run(t.Context(), repo, "log", "--oneline", "-5"); !strings.Contains(out, "apptown: complete") {
		t.Fatalf("the merge must be on main: %s", out)
	}
}

// TestJourneyPackEvolution — D4 + the Hello Spring story: a catalog
// change whose workflow the project lacks is offered grayed (never a
// foreign pack), the pack evolves, the project pulls it, the change
// becomes runnable and runs.
func TestJourneyPackEvolution(t *testing.T) {
	h := newJourney(t)
	h.app.Ways.SyncStockWorkflows(t.Context()) // the boot act: stock workflows into the library
	slug := fmt.Sprintf("evo-%d", os.Getpid())

	// A custom pack, v1 without dependency-uplift.
	h.mustCall("POST", "/packs", map[string]any{
		"name": "jp", "workflows": []string{"quick-change", "land"}}, nil)
	h.mustCall("POST", "/projects", map[string]string{"slug": slug}, nil)
	h.mustCall("POST", "/projects/"+slug+"/repo", map[string]string{}, nil)
	h.mustCall("POST", "/projects/"+slug+"/pack", map[string]string{"name": "jp"}, nil)

	// The shipped catalog's uplift/deps needs the missing workflow.

	type catEntry struct {
		Name            string `json:"name"`
		MissingWorkflow string `json:"missingWorkflow"`
		FromPack        string `json:"fromPack"`
	}
	find := func() catEntry {
		// Fresh struct per call: Unmarshal into a reused slice keeps old
		// field values when the new JSON omits a key — a stale
		// missingWorkflow haunted this journey for an hour.
		var cat struct {
			Catalog []catEntry `json:"catalog"`
		}
		h.mustCall("GET", "/catalog?project="+slug, nil, &cat)
		for _, e := range cat.Catalog {
			if e.Name == "uplift/deps" {
				return e
			}
		}
		t.Fatal("catalog entry vanished")
		return catEntry{}
	}
	e := find()
	if e.MissingWorkflow != "dependency-uplift" || e.FromPack != "" {
		t.Fatalf("must be grayed with NO foreign-pack offer (stock packs carry it): %+v", e)
	}

	// The pack evolves. Publishing never moves what projects are offered:
	// a person recommends the new version, then the project pulls and the
	// entry lights up.
	var saved struct {
		Version int64 `json:"version"`
	}
	h.mustCall("POST", "/packs", map[string]any{
		"name": "jp", "workflows": []string{"quick-change", "land", "dependency-uplift"}}, &saved)
	if e = find(); e.FromPack != "" {
		t.Fatalf("a published but unrecommended version must not be offered: %+v", e)
	}
	h.mustCall("POST", "/packs/jp/recommended", map[string]int64{"version": saved.Version}, nil)
	e = find()
	if e.FromPack != "jp" {
		t.Fatalf("the project's own pack must be offered: %+v", e)
	}
	var plan struct {
		Additions []string `json:"additions"`
		Removals  []string `json:"removals"`
	}
	h.mustCall("GET", "/projects/"+slug+"/pack/pull-plan", nil, &plan)
	if len(plan.Additions) != 1 || plan.Additions[0] != "dependency-uplift" || len(plan.Removals) != 0 {
		t.Fatalf("plan: %+v", plan)
	}
	h.mustCall("POST", "/projects/"+slug+"/pack/pull", map[string]string{}, nil)
	if e = find(); e.MissingWorkflow != "" {
		t.Fatalf("after the pull the change must be runnable: %+v", e)
	}

	// Run it: the prepared change starts a real run on the real workflow.
	var applied struct {
		Change struct{ ID ids.ID }
		Run    struct{ ID ids.ID }
	}
	h.mustCall("POST", "/catalog-apply/uplift/deps", map[string]any{"project": slug, "params": map[string]string{}}, &applied)
	if applied.Change.ID.IsZero() || applied.Run.ID.IsZero() {
		t.Fatalf("catalog apply must open a change with a run: %+v", applied)
	}
	h.mustCall("POST", fmt.Sprintf("/runs/%s/abandon", applied.Run.ID), map[string]string{"reason": "journey cleanup"}, nil)
	// Abandoning stops the running agent (Work cancels its step's context),
	// so its step settles promptly; the deadline is a hang guard.
	h.until("the abandoned run's steps to settle", 15*time.Second, func() bool {
		v := h.changeView(applied.Change.ID)
		if v.Detail == nil {
			return true
		}
		for _, st := range v.Detail.Steps {
			if st.Status == workflow.StatusRunning.String() {
				return false
			}
		}
		return true
	})
	// The steps settle a moment before their goroutines stop touching the
	// workspace: join the dispatched work before cleanup.
	drained(t, h.app)
}

// TestJourneyLawMidFlight — W2 through the public surface: a landing
// parks at its approval, the law changes underneath it, the approval is
// granted — and the merge pauses with the next action stated, restart
// starts a fresh landing, override is the recorded exit.
func TestJourneyLawMidFlight(t *testing.T) {
	h := newJourney(t)
	slug := fmt.Sprintf("law-%d", os.Getpid())
	h.newProject(slug, journeyQuickChange, unscannedLand)

	// A change goes straight to the landing lane and parks at approval.
	var created struct {
		Change struct{ ID ids.ID }
		Run    struct{ ID ids.ID }
	}
	h.mustCall("POST", "/projects/"+slug+"/changes",
		map[string]string{"title": "park me", "type": "chore", "workflow": "land"}, &created)
	chID := created.Change.ID
	var approveID ids.ID
	h.until("the landing to park at its approval", 30*time.Second, func() bool {
		v := h.changeView(chID)
		if v.Detail == nil {
			return false
		}
		id, status := h.findStep(v, "approve-landing")
		approveID = id
		return status == workflow.StatusPending.String()
	})

	// The amendment lands while the run waits.
	h.mustCall("POST", "/projects/"+slug+"/policies", map[string]any{
		"name": "scan-first", "rule": "requireStepBefore",
		"with": map[string]any{"uses": "deploy", "requires": "scan"}}, nil)

	// The parked approval is granted; the merge must pause, not slip.
	h.approveStep(approveID)
	var lawID ids.ID
	h.until("the current-rules pause with its next action", 30*time.Second, func() bool {
		v := h.changeView(chID)
		if v.Detail == nil {
			return false
		}
		for _, s := range v.Detail.Steps {
			if s.Type == "policy/current" && s.Status == workflow.StatusNeedsHuman.String() {
				if !strings.Contains(string(s.Evidence), "Restart landing") {
					t.Fatalf("the next action must be stated: %s", s.Evidence)
				}
				lawID = s.ID
				return true
			}
		}
		return false
	})

	// Restart is the usual answer to a law pause — but this lane cannot
	// satisfy the amended rule (it has no scan before its deploy), so the
	// current rules refuse the restart in a sentence and the parked landing
	// is left standing: nothing was abandoned for a run that could not
	// start. The recorded exception is the other door.
	if code := h.jCall("POST", fmt.Sprintf("/runs/%s/restart-landing", created.Run.ID), nil, nil); code != 422 {
		t.Fatalf("a restart the rules refuse must answer 422, got %d", code)
	}
	// The parked landing is left standing — nothing was abandoned for a run
	// that could not start.
	if v := h.changeView(chID); v.Change.State != "landing" || v.Detail == nil || v.Detail.Run.ID != created.Run.ID {
		t.Fatalf("a refused restart must leave the parked landing standing: state=%s run=%s", v.Change.State, v.Detail.Run.ID)
	}

	// Override: the recorded exception. The landing completes.
	h.mustCall("POST", fmt.Sprintf("/steps/%s/override", lawID),
		map[string]string{"reason": "journey: landing under the old rules on purpose"}, nil)
	h.until("the change to ship after the override", 60*time.Second, func() bool {
		return h.changeView(chID).Change.State == "shipped"
	})
}

// ---- The completeness ledger (design/architecture/testing-map.md) ----------------------
//
// The map is a TEST: both axes are enumerated here, every cell must carry
// a disposition, and vocabulary growth (a new door, noun, or step type)
// fails this test until the new cell is mapped.

var journeyDoors = []string{"simple", "chat", "detailed", "catalog", "import"}
var journeyNouns = []string{"guidance", "workflow", "policy", "pack", "catalog-entry",
	"integration", "people", "model-slot", "roster"}
var journeyBeats = []string{"create", "adopt", "amend", "deprecate", "remove"}

// dispositions: "journey:<TestName>" (a journey drives it),
// "unit:<TestName>" (pinned below the journey tier), "collapsed:<cell>"
// (same machinery as another cell), "backing:<what>" (needs a real
// external service — portainer, gitlab; integration tier's next target).
var journeyLedger = map[string]string{
	"door/simple":    "journey:TestJourneyKidStory",
	"door/chat":      "backing:console container + say/transcript — proven live 2026-08-31 (Prism); journey pending a containerized console harness",
	"door/detailed":  "journey:TestJourneyLawMidFlight",
	"door/catalog":   "journey:TestJourneyPackEvolution",
	"door/import":    "unit:TestImportBranch",
	"exit/land":      "journey:TestJourneyKidStory",
	"exit/release":   "backing:portainer — the lan-release half needs a throwaway portainer (design/architecture/testing.md)",
	"exit/packaging": "unit:TestSourcePackagedSignals — one rail: releasable = image OR repo Dockerfile (AppTown builds, never writes); the build+push leg shares exit/release's portainer gap; verified live 2026-08-31 (post-synthesis-deletion game)",

	"guidance/create":    "unit:guidance_conformance_test",
	"guidance/adopt":     "collapsed:pack/adopt",
	"guidance/amend":     "unit:guidance_conformance_test",
	"guidance/deprecate": "gap:guidance has no deprecation yet — vocabulary hole, tracked",
	"guidance/remove":    "collapsed:pack/remove",

	"workflow/create":    "journey:TestJourneyKidStory",
	"workflow/adopt":     "journey:TestJourneyPackEvolution",
	"workflow/amend":     "unit:TestCustomLibraryWorkflow",
	"workflow/deprecate": "unit:TestPackPull",
	"workflow/remove":    "unit:TestPackPull",

	"policy/create":    "journey:TestJourneyLawMidFlight",
	"policy/adopt":     "collapsed:policy/create",
	"policy/amend":     "unit:TestPolicyWordingFollowsLibrary",
	"policy/deprecate": "gap:policies have no deprecation — repeal is remove; collapsed:policy/remove",
	"policy/remove":    "unit:TestLawRelaxesClearsPause",

	"pack/create":    "journey:TestJourneyPackEvolution",
	"pack/adopt":     "journey:TestJourneyPackEvolution",
	"pack/amend":     "journey:TestJourneyPackEvolution",
	"pack/deprecate": "gap:packs have no deprecation — tracked",
	"pack/remove":    "journey:TestJourneyStackMigration",

	"catalog-entry/create":    "unit:TestTheShippedCatalogReadsAndEachEntryValidates (entries ship in stock/catalog.yaml)",
	"catalog-entry/adopt":     "collapsed:door/catalog",
	"catalog-entry/amend":     "collapsed:catalog-entry/create (a release changes the file)",
	"catalog-entry/deprecate": "collapsed:catalog-entry/remove",
	"catalog-entry/remove":    "collapsed:catalog-entry/create (a release changes the file)",

	"integration/create":    "unit:services_test + secrets_test",
	"integration/adopt":     "unit:TestProjectRosterGate (attach path) — journey needs backing services",
	"integration/amend":     "backing:gitlab/glitchtip fakes — integration tier's next target",
	"integration/deprecate": "collapsed:integration/remove",
	"integration/remove":    "backing:same",

	"people/create":    "journey:TestJourneyMultiplayer",
	"people/adopt":     "journey:TestJourneyMultiplayer (groups)",
	"people/amend":     "unit:TestSaveUserValidation + TestPrivileges",
	"people/deprecate": "collapsed:people/remove",
	"people/remove":    "unit:TestDeleteUserGuards",

	"model-slot/create":    "unit:TestSetupModelWritesSecrets",
	"model-slot/adopt":     "unit:TestResolveModelSlot",
	"model-slot/amend":     "unit:TestResolveModelSlot",
	"model-slot/deprecate": "collapsed:model-slot/remove",
	"model-slot/remove":    "gap:no slot removal surface — tracked",

	"roster/create":    "journey:TestJourneyMultiplayer",
	"roster/adopt":     "collapsed:roster/create",
	"roster/amend":     "journey:TestJourneyMultiplayer",
	"roster/deprecate": "collapsed:roster/remove",
	"roster/remove":    "unit:TestProjectRosterGate",
}

func TestJourneyLedgerComplete(t *testing.T) {
	cells := []string{"exit/land", "exit/release", "exit/packaging"}
	for _, d := range journeyDoors {
		cells = append(cells, "door/"+d)
	}
	for _, n := range journeyNouns {
		for _, b := range journeyBeats {
			cells = append(cells, n+"/"+b)
		}
	}
	for _, c := range cells {
		if _, ok := journeyLedger[c]; !ok {
			t.Errorf("unmapped cell %q — the vocabulary grew; give it a disposition", c)
		}
	}
	known := map[string]bool{}
	for _, c := range cells {
		known[c] = true
	}
	for c := range journeyLedger {
		if !known[c] {
			t.Errorf("ledger entry %q matches no cell — typo or removed vocabulary", c)
		}
	}
	// Step-type coverage: every step the engine supports must be named by
	// SOME test tier; a new step type fails here until it is.
	stepTier := map[string]string{
		"script": "unit:core_test gates", "approval": "journey:all three",
		"artifact/require": "collapsed:stock land (release-exists) — unit gate tests",
		"adr/require":      "unit:TestADRTransitions + gate machinery",
		"agent/pi":         "journey:TestJourneyKidStory (fake pi)",
		"scm/mr":           "unit:TestTheMergeRequestStepSaysWhoOpenedTheChange (fake GitLab)", "preview": "integration:TestIntegrationStaticPreviewBoot",
		"deploy": "journey:TestJourneyKidStory", "workflow/call": "unit:TestPackPullKeepsCalledWorkflows (reference scan); run-path gap tracked",
		"scan": "backing:scanner binaries", "agent/review": "unit:autoRework path w/ fake verdicts; fake-pi review supported",
		"guidance/conformance": "unit:guidance_conformance_test",
		"tests":                "unit:TestDetectTestCommand; proven live both directions (pass receipt + sabotaged tree blocked landing)",
		"reconcile":            "unit:TestReconcilePreflight (up-to-date, catch-up, conflict); land preflight",
	}
	for st := range schema.SupportedSteps {
		if _, ok := stepTier[st]; !ok {
			t.Errorf("step type %q has no test disposition", st)
		}
	}
}

// jUser: a signed-in person (internal auth) driving the same surface.
type jUser struct {
	h      *jHarness
	client *http.Client
	name   string
}

func (h *jHarness) signup(admin *jUser, name, password string) *jUser {
	h.t.Helper()
	if admin != nil {
		admin.mustCall("POST", "/users", map[string]string{"username": name, "password": password}, nil)
	}
	jar, _ := cookiejar.New(nil)
	u := &jUser{h: h, client: &http.Client{Jar: jar}, name: name}
	path, body := "/auth/login", map[string]string{"username": name, "password": password}
	if admin == nil {
		path, body = "/auth/bootstrap", firstAccount(h.t, h.app, name, password)
	}
	u.mustCall("POST", path, body, nil)
	return u
}

func (u *jUser) call(method, path string, body any, out any) int {
	u.h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, u.h.srv.URL+"/api/v1"+path, rd)
	res, err := u.client.Do(req)
	if err != nil {
		u.h.t.Fatalf("%s %s as %s: %v", method, path, u.name, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 500 {
		u.h.t.Fatalf("%s %s as %s: %d — never 5xx: %s", method, path, u.name, res.StatusCode, raw)
	}
	if out != nil && res.StatusCode < 300 {
		_ = json.Unmarshal(raw, out)
	}
	return res.StatusCode
}

// approve approves as a client does, sending back what the question showed
// of the landing (see jHarness.approveStep).
func (u *jUser) approve(stepID ids.ID) {
	u.h.t.Helper()
	var q jQuestion
	u.mustCall("GET", fmt.Sprintf("/steps/%s/question", stepID), nil, &q)
	u.mustCall("POST", fmt.Sprintf("/steps/%s/approve", stepID), map[string]any{"decision": "approve", "alsoApproves": q.LandingMeans, "adds": q.digest()}, nil)
}

func (u *jUser) mustCall(method, path string, body any, out any) {
	u.h.t.Helper()
	if code := u.call(method, path, body, out); code >= 300 {
		u.h.t.Fatalf("%s %s as %s: %d", method, path, u.name, code)
	}
}

// TestJourneyMultiplayer — W4: internal auth, accounts, a group, a
// roster, and a two-person quorum, driven end to end by three signed-in
// people. "kid" is refused twice (roster, approver list); the panel's
// two OKs land the change; every act lands under its true name.
func TestJourneyMultiplayer(t *testing.T) {
	h := newJourney(t, Config{AuthMode: "internal"})
	slug := fmt.Sprintf("multi-%d", os.Getpid())
	h.removeMainAppAtCleanup(slug)

	boss := h.signup(nil, "boss", "boss-password")
	ana := h.signup(boss, "ana", "ana-password")
	kid := h.signup(boss, "kid", "kid-password")
	boss.mustCall("POST", "/groups", map[string]any{"name": "reviewers", "members": []string{"ana"}}, nil)

	quorumLane := strings.Replace(journeyQuickChange,
		"with: { approvers: [journey], means: \"I looked at what the agent did\" }",
		"with: { approvers: [boss, \"group:reviewers\"], quorum: 2, means: \"both of us read it\" }", 1)
	lanes := publishLanes(t, h.app, quorumLane)
	boss.mustCall("POST", "/projects", map[string]string{"slug": slug}, nil)
	boss.mustCall("POST", "/projects/"+slug+"/repo", map[string]string{}, nil)
	selectLanes(t, h.app, slug, lanes...)
	// Roles are explicit. The panel both starts work and approves it, and no
	// single project role does both.
	boss.mustCall("POST", "/projects/"+slug+"/members", map[string]string{"grantee": "boss", "role": access.RoleProjectOwner}, nil)
	for _, role := range []string{access.RoleProjectContrib, access.RoleProjectApprover} {
		boss.mustCall("POST", "/projects/"+slug+"/members", map[string]string{"grantee": "group:reviewers", "role": role}, nil)
	}

	// The kid is outside the roster: starting work is refused plainly.
	if code := kid.call("POST", "/projects/"+slug+"/changes", map[string]string{"title": "let me in"}, nil); code != 403 {
		t.Fatalf("non-member starting work must 403, got %d", code)
	}

	page := `<!doctype html><html><body><h1>made together</h1><script>1</script></body></html>`
	sentence := staticSiteTask("team page", page)
	var created struct {
		Change struct{ ID ids.ID }
	}
	ana.mustCall("POST", "/projects/"+slug+"/changes", map[string]string{"title": "team page", "body": sentence, "workflow": "quick-change"}, &created)
	chID := created.Change.ID

	var acceptID ids.ID
	h.until("the quorum gate", 60*time.Second, func() bool {
		var v jChangeView
		ana.mustCall("GET", fmt.Sprintf("/changes/%s", chID), nil, &v)
		if v.Detail == nil {
			return false
		}
		// A person accepts what the agent did, so the panel waits for it.
		id, status := h.findStep(v, "accept")
		_, doStatus := h.findStep(v, "do")
		acceptID = id
		return status == workflow.StatusPending.String() && doStatus == workflow.StatusPass.String()
	})

	// The kid is not an approver: refused with the list named.
	if code := kid.call("POST", fmt.Sprintf("/steps/%s/approve", acceptID),
		map[string]string{"decision": "approve"}, nil); code != 403 {
		t.Fatalf("non-approver must be refused, got %d", code)
	}
	// One of two: the panel is not satisfied by a single voice.
	ana.approve(acceptID)
	var v jChangeView
	ana.mustCall("GET", fmt.Sprintf("/changes/%s", chID), nil, &v)
	if _, status := h.findStep(v, "accept"); status != workflow.StatusPending.String() {
		t.Fatalf("one of two approvals must keep the panel waiting: %q", status)
	}
	boss.approve(acceptID)

	h.until("the team's change to ship", 120*time.Second, func() bool {
		var v jChangeView
		boss.mustCall("GET", fmt.Sprintf("/changes/%s", chID), nil, &v)
		return v.Change.State == "shipped"
	})

	// The ledger holds both voices under their true names.
	accepted, err := workflow.ReadStepIn(context.Background(), h.app.DB, acceptID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(accepted.Evidence), `"ana"`) || !strings.Contains(string(accepted.Evidence), `"boss"`) {
		t.Fatalf("both votes must be in evidence: %s", accepted.Evidence)
	}
}

// TestJourneyStackMigration — W1: a project re-packs to a different way
// of working while its standing deployment keeps serving. The probe
// polls the app THROUGH the migration; only the deploy's own container
// swap may blip, and briefly.
func TestJourneyStackMigration(t *testing.T) {
	h := newJourney(t)
	h.app.Ways.SyncStockWorkflows(t.Context())
	slug := fmt.Sprintf("mig-%d", os.Getpid())
	h.removeMainAppAtCleanup(slug)

	// The working lane is customized (the journey shape) as the library's
	// newest version before either pack pins it.
	publishLanes(t, h.app, journeyQuickChange)
	// Stack A carries a lane stack B will drop.
	h.mustCall("POST", "/packs", map[string]any{"name": "stack-a", "workflows": []string{"quick-change", "land", "research"}}, nil)
	h.mustCall("POST", "/packs", map[string]any{"name": "stack-b", "workflows": []string{"quick-change", "land"}}, nil)
	h.mustCall("POST", "/projects", map[string]string{"slug": slug}, nil)
	h.mustCall("POST", "/projects/"+slug+"/repo", map[string]string{}, nil)
	h.mustCall("POST", "/projects/"+slug+"/pack", map[string]string{"name": "stack-a"}, nil)

	ship := func(marker string) {
		page := `<!doctype html><html><body><h1>` + marker + `</h1><script>1</script></body></html>`
		sentence := staticSiteTask(marker, page)
		var created struct {
			Change struct{ ID ids.ID }
		}
		h.mustCall("POST", "/projects/"+slug+"/changes", map[string]string{"title": marker, "body": sentence, "workflow": "quick-change"}, &created)
		var acceptID ids.ID
		h.until("the gate for "+marker, 60*time.Second, func() bool {
			v := h.changeView(created.Change.ID)
			if v.Detail == nil {
				return false
			}
			// Accept what the agent did, once it has done it.
			id, status := h.findStep(v, "accept")
			_, doStatus := h.findStep(v, "do")
			acceptID = id
			return status == workflow.StatusPending.String() && doStatus == workflow.StatusPass.String()
		})
		h.approveStep(acceptID)
		var last jChangeView
		defer func() {
			if t.Failed() {
				data, _ := json.Marshal(last)
				t.Logf("last change view for %s: %s", marker, data)
			}
		}()
		h.until(marker+" to ship", 120*time.Second, func() bool {
			v := h.changeView(created.Change.ID)
			last = v
			return v.Change.State == "shipped"
		})
	}
	ship("stack a page")

	var envs []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	h.mustCall("GET", "/projects/"+slug+"/deployments", nil, &envs)
	var url string
	for _, e := range envs {
		if e.Name == "main" {
			url = e.URL
		}
	}
	if url == "" {
		t.Fatal("no main env")
	}

	// The standing probe: the app must serve throughout the migration,
	// allowing only a bounded blip for the deploy's own container swap.
	stop := make(chan struct{})
	var probeFails, maxStreak, streak int
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		// A probe every 150 ms, the first at once, until told to stop.
		tick := time.NewTicker(150 * time.Millisecond)
		defer tick.Stop()
		for first := true; ; first = false {
			if !first {
				select {
				case <-stop:
					return
				case <-tick.C:
				}
			}
			res, err := http.Get(url)
			ok := err == nil && res != nil && res.StatusCode == 200
			if res != nil {
				res.Body.Close()
			}
			if ok {
				streak = 0
			} else {
				probeFails++
				streak++
				if streak > maxStreak {
					maxStreak = streak
				}
			}
		}
	}()

	// The migration: re-pack to stack B (research removed, modified lanes
	// kept), then the porting change lands the new stack's page.
	var plan struct {
		Removals []string `json:"removals"`
	}
	h.mustCall("GET", "/projects/"+slug+"/pack/pull-plan?pack=stack-b", nil, &plan)
	if len(plan.Removals) != 1 || plan.Removals[0] != "research" {
		t.Fatalf("re-pack plan removals: %+v", plan)
	}
	h.mustCall("POST", "/projects/"+slug+"/pack/pull", map[string]string{"pack": "stack-b"}, nil)
	// The customized lanes are in the new pack too — the pull re-selects
	// lanes; it never rewinds the library's newest version or drops the
	// selection.
	_, qcYAML, err := h.app.Ways.WorkflowVersion(t.Context(), "quick-change", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(qcYAML, "I looked at what the agent did") {
		t.Fatal("re-pack clobbered a customized lane")
	}
	repacked, err := h.app.Projects.BySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	if !laneOn(t, h.app.Adoption, repacked.ID, "quick-change") {
		t.Fatal("re-pack dropped the quick-change selection")
	}
	ship("stack b page")

	close(stop)
	<-probeDone
	// Serving throughout: the only tolerated gap is the container swap.
	if maxStreak > 40 { // 40 × 150ms = 6s of continuous downtime is a failure
		t.Fatalf("the environment went down for too long during migration: %d consecutive failed probes", maxStreak)
	}
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), "stack b page") {
		t.Fatalf("the migrated app must serve the new stack's content: %s", strs.Truncate(string(b), 120))
	}
	t.Logf("probe: %d total failures, worst streak %d (container swap)", probeFails, maxStreak)
}

// ---- The aged-instance corpus (design/architecture/testing-map.md §5) -----------------

func TestJourneyPackDeliversItsPromises(t *testing.T) {
	h := newJourney(t)
	slug := fmt.Sprintf("promise-%d", os.Getpid())
	h.mustCall("POST", "/projects", map[string]string{"slug": slug}, nil)
	h.mustCall("POST", "/projects/"+slug+"/repo", map[string]string{}, nil)

	var res struct {
		Toolchain string   `json:"toolchain"`
		Seeded    []string `json:"seeded"`
	}
	h.mustCall("POST", "/projects/"+slug+"/pack", map[string]string{"name": "java-spring"}, &res)
	if res.Toolchain != "java" {
		t.Fatalf("java-spring pack must seed its toolchain declaration, got %q", res.Toolchain)
	}
	repo := h.app.projectRepoPath(slug)
	for _, f := range []string{"mise.toml", "Dockerfile"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err != nil {
			t.Fatalf("pack apply must deliver %s: %v", f, err)
		}
	}
	// The app itself comes from the ecosystem's generator on the first
	// change — a hand-kept skeleton is never seeded.
	if _, err := os.Stat(filepath.Join(repo, "pom.xml")); err == nil {
		t.Fatalf("java-spring must not seed a skeleton; Initializr generates the app")
	}
	// The committed state, not just the working tree: seeds and toolchain
	// both land as commits.
	if out, _ := gitexec.Run(t.Context(), repo, "log", "--oneline", "-3"); !strings.Contains(out, "toolchain") || !strings.Contains(out, "seed java-spring") {
		t.Fatalf("seed and toolchain must be committed: %s", out)
	}
	// The recipe references no wrapper scripts — the guidance forbids
	// vendoring them, so a recipe expecting them can never build.
	df, _ := os.ReadFile(filepath.Join(repo, "Dockerfile"))
	for _, line := range strings.Split(string(df), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.Contains(line, "mvnw") || strings.Contains(line, "gradlew") {
			t.Fatalf("packaging recipe expects wrapper scripts the seed does not ship: %s", line)
		}
	}
	// Applying twice never clobbers: the project's own files win.
	var again struct {
		Toolchain string `json:"toolchain"`
	}
	h.mustCall("POST", "/projects/"+slug+"/pack", map[string]string{"name": "java-spring"}, &again)
	if again.Toolchain != "" {
		t.Fatalf("second apply must not re-seed the toolchain, got %q", again.Toolchain)
	}
}

// Server packs whose ecosystem has a generator (rails new, dotnet new, npm
// create) seed the toolchain, the packaging recipe and the runtime contract,
// never the app: the guidance tells the agent to generate the app on the
// first change.
func TestJourneyServerPacksDeliver(t *testing.T) {
	h := newJourney(t)
	h.app.Ways.SyncStockWorkflows(t.Context())
	h.app.Ways.SyncStockPacks(t.Context())
	cases := []struct {
		pack, toolchain string
		files           []string
		generated       string // what the ecosystem's generator writes
	}{
		{"dotnet", "dotnet", []string{"mise.toml", "Dockerfile"}, "App/App.csproj"},
		{"rails", "ruby", []string{"mise.toml", "Dockerfile"}, "Gemfile"},
		{"node-fullstack", "node", []string{"mise.toml", "Dockerfile"}, "package.json"},
	}
	for _, c := range cases {
		slug := fmt.Sprintf("%s-%d", c.pack, os.Getpid())
		h.mustCall("POST", "/projects", map[string]string{"slug": slug}, nil)
		h.mustCall("POST", "/projects/"+slug+"/repo", map[string]string{}, nil)
		var res struct {
			Toolchain string   `json:"toolchain"`
			Seeded    []string `json:"seeded"`
			Missing   []string `json:"missing"`
		}
		h.mustCall("POST", "/projects/"+slug+"/pack", map[string]string{"name": c.pack}, &res)
		if res.Toolchain != c.toolchain {
			t.Fatalf("%s: toolchain %q, want %q", c.pack, res.Toolchain, c.toolchain)
		}
		if len(res.Missing) > 0 {
			t.Fatalf("%s: pack promises library content that does not exist: %v", c.pack, res.Missing)
		}
		repo := h.app.projectRepoPath(slug)
		for _, f := range c.files {
			if _, err := os.Stat(filepath.Join(repo, f)); err != nil {
				t.Fatalf("%s: pack apply must deliver %s: %v", c.pack, f, err)
			}
		}
		df, _ := os.ReadFile(filepath.Join(repo, "Dockerfile"))
		if strings.Contains(string(df), "\nENTRYPOINT") {
			t.Fatalf("%s: the recipe must end in CMD so the init step can reuse the image", c.pack)
		}
		// The seed declares the runtime contract from the start (its parts
		// and needs), so the first change's agent gets its database.
		if _, err := docker.ReadWorkspaceContract(repo); err != nil {
			t.Fatalf("%s: the seed must declare a readable runtime contract: %v", c.pack, err)
		}
		if !slices.Contains(res.Seeded, ".apptown/app.yaml") || !slices.Contains(res.Seeded, "Dockerfile") {
			t.Fatalf("%s: the recipe and the contract are seeded, got %v", c.pack, res.Seeded)
		}
		// Nothing of the app is seeded: the ecosystem's generator makes it
		// on the first change, and the selection note says exactly how.
		if _, err := os.Stat(filepath.Join(repo, c.generated)); err == nil {
			t.Fatalf("%s: the pack must not seed %s; the generator writes it", c.pack, c.generated)
		}
	}
}
