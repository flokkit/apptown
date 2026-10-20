//go:build browser

package forge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/stock"
)

// A real Chromium browser against disposable Forge state and three TLS
// origins. No model, Docker workload or production service is invoked.
// See design/subsystems/browser-security.md for the optional browser tier.
func TestBrowserSecurityChromium(t *testing.T) {
	server := httptest.NewUnstartedServer(nil)
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	runtime := consoletest.NewAgent()
	agent := &browserAgent{Agent: runtime}
	a, h := internalApp(t, Config{PublicHost: "forge.example.test:" + port, TierDomains: []string{"preview=preview.example.test"},
		DockerClient: newFakeDocker(), ConsoleAgent: agent, ConsoleCLI: fixtureCLI(t),
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	bootstrap := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "boss", "boss-password"))
	if bootstrap.Code != http.StatusOK {
		t.Fatalf("production bootstrap: %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	var admin *http.Cookie
	for _, cookie := range bootstrap.Result().Cookies() {
		if cookie.Name == secureSessionCookie && cookie.Value != "" {
			admin = cookie
		}
	}
	if admin == nil {
		t.Fatalf("production bootstrap did not issue its secure cookie: %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	if created := postJSON(h, "/api/v1/projects", map[string]string{"slug": "p", "name": "P"}, admin); created.Code != http.StatusCreated {
		t.Fatalf("create browser project: %d %s", created.Code, created.Body.String())
	}
	project, err := a.Projects.BySlug(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	change, err := a.Work.Create(asPerson("boss"), workflow.ChangeDraft{ProjectID: project.ID, Title: "browser security fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if change.State != workflow.ChangeInProgress {
		t.Fatalf("browser fixture change is not active: %+v", change)
	}
	changeID := change.ID
	dir := filepath.Join(a.cfg.dataDir, "browser-security-checkout")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A real Console session backed by the fake agent exercises the WebSocket
	// handler without provisioning a container.
	labels := withLabel(consoleMetaFor("", project.ID, ids.Nil), conversation.SlugLabel, project.Slug).Labels
	session, err := a.Console.Open(context.Background(), console.OpenRequest{
		Scope: conversation.Key(project.ID, ids.Nil), Labels: labels,
		Workspace: console.Workspace{Path: dir}, Actor: "boss",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Screen(session.ID, []byte("terminal-ready"))
	png, err := base64.StdEncoding.DecodeString(browserTestPNG)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"picture-1.png": []byte(`<script>fetch('/api/v1/projects',{method:'POST',body:JSON.stringify({slug:'file-executed',name:'bad'})})</script>`),
		"picture-2.png": png,
	} {
		if err := os.WriteFile(filepath.Join(session.Dir, "uploads", name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h = a.Handler()
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == a.cfg.publicHost {
			h.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<!doctype html><title>Untrusted site fixture</title><body>untrusted site</body>")
	})
	server.StartTLS()
	run := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
		Argv: []string{"node", "testdata/browser_security.mjs"},
		Env: []string{
			"APPTOWN_BROWSER_URL=https://" + a.cfg.publicHost,
			"APPTOWN_SIBLING_URL=https://evil.preview.example.test:" + port,
			"APPTOWN_OTHER_URL=https://evil.other.test:" + port,
			fmt.Sprintf("APPTOWN_BROWSER_CHANGE=%d", changeID),
			"APPTOWN_BROWSER_CONSOLE=" + session.ID,
			// Chromium keeps its shared memory in a folder of its own: its
			// child processes drop root's override of folder permissions, so
			// on CI they cannot write the job's temporary folder, which is
			// the runner user's, and a page never renders.
			"TMPDIR=" + t.TempDir(),
		},
		Timeout: 90 * time.Second,
	})
	if run.RunErr != nil {
		t.Fatalf("browser security: %v\n%s", run.RunErr, run.Output)
	}
	t.Log(string(run.Output))
	var unexpected int
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM projects WHERE slug IN ('csrf-created','file-executed')`).Scan(&unexpected); err != nil || unexpected != 0 {
		t.Fatalf("browser attack mutated state: %d %v", unexpected, err)
	}
	if b := agent.Input(); b != "legitimate-terminal-input" {
		t.Fatalf("terminal input was not restricted to legitimate origin: %q", b)
	}
}

// browserAgent records browser keystrokes apart from screen output. The fake
// terminal waits for output as a real PTY does, so the first keystroke is
// acknowledged by putting a line on the session's screen.
type browserAgent struct {
	*consoletest.Agent
	mu    sync.Mutex
	input bytes.Buffer
}

func (a *browserAgent) Attach(ctx context.Context, id string, size console.Size) (console.Terminal, error) {
	term, err := a.Agent.Attach(ctx, id, size)
	if err != nil {
		return nil, err
	}
	return &browserTerminal{Terminal: term, agent: a, id: id}, nil
}

func (a *browserAgent) Input() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.input.String()
}

type browserTerminal struct {
	console.Terminal
	agent   *browserAgent
	id      string
	ackOnce sync.Once
}

func (t *browserTerminal) Write(p []byte) (int, error) {
	t.agent.mu.Lock()
	n, err := t.agent.input.Write(p)
	t.agent.mu.Unlock()
	if err == nil {
		t.ackOnce.Do(func() { t.agent.Screen(t.id, []byte("terminal-input-recorded")) })
	}
	return n, err
}

// The UI tour (site/tour, `make tour`): one believable small team's AppTown,
// built through its owners and HTTP API, photographed by Playwright in both
// themes. Nothing on a page is faked: every value comes from records the
// owners wrote. The step outcomes are a fake runner's and the chat is a
// recorded transcript on the console's fake agent, so no model, Docker
// workload or real check runs. Not part of any gate: make tour runs it.
func TestTour(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tourDir := filepath.Join(root, "site", "tour")
	stops := readTourStops(t, filepath.Join(tourDir, "tour.yaml"))

	server := httptest.NewUnstartedServer(nil)
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	runtime := consoletest.NewAgent()
	// A readable data folder: the infrastructure page names it.
	dataDir := "/tmp/apptown-tour"
	_ = os.RemoveAll(dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	a, h := internalApp(t, Config{DataDir: dataDir, PublicHost: "apptown.localhost:" + port, TierDomains: []string{"preview=preview.localhost"},
		DockerClient: newFakeDocker(), ConsoleAgent: runtime, ConsoleCLI: fixtureCLI(t),
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	// What apptownd seeds at start: stock content, integrations, targets.
	ctx := context.Background()
	a.SyncStock(ctx)
	a.SyncSeedIntegrations(ctx)
	a.SeedTargets(ctx)
	verification.SyncStockScanners(ctx, a.Integrations, stock.FS)
	a.SyncStockDependencies(ctx)
	configureConsoleModel(t, a)

	const password = "tour-password-not-secret"
	boot := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "sam", password))
	if boot.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", boot.Code, boot.Body.String())
	}
	var admin *http.Cookie
	for _, c := range boot.Result().Cookies() {
		if c.Name == secureSessionCookie && c.Value != "" {
			admin = c
		}
	}
	if admin == nil {
		t.Fatal("bootstrap issued no session")
	}
	call := func(method, path string, body any) []byte {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(admin)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		if w.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}

	// A model key, set up as a person does on the home page: a placeholder
	// that no model ever sees (nothing in the tour calls a model).
	call("POST", "/api/v1/setup/model", map[string]string{"provider": "anthropic", "key": "tour-placeholder-key"})

	// The team: Sam runs AppTown; Priya and Tom make apps.
	for _, u := range []string{"priya", "tom"} {
		call("POST", "/api/v1/users", map[string]string{"username": u, "role": "member", "password": password})
	}

	// Three projects, each with its stock way of working.
	type proj struct{ slug, name, blurb, pack string }
	projects := []proj{
		{"lunch-orders", "Lunch Orders", "Each morning people pick a lunch from a short menu; at 11am whoever is ordering sees the list with names and totals.", "django-htmx"},
		{"club-newsletter", "Club Newsletter", "The cycling club's monthly newsletter, as a small static site.", "frontend-only"},
		{"rocket-garden", "Rocket Garden", "A little browser game: grow rockets, launch them, beat your best height.", "html-game"},
	}
	for _, p := range projects {
		call("POST", "/api/v1/projects", map[string]string{"slug": p.slug, "name": p.name, "blurb": p.blurb})
		call("POST", "/api/v1/projects/"+p.slug+"/repo", map[string]string{"defaultBranch": "main"})
		call("POST", "/api/v1/projects/"+p.slug+"/pack", map[string]string{"name": p.pack})
	}
	lunch, err := a.Projects.BySlug(context.Background(), "lunch-orders")
	if err != nil {
		t.Fatal(err)
	}

	// A change whose checks have run: the tests passed, the style check
	// failed, and it waits for a person's approval with its stated meaning.
	defineWorkflows(t, a, workflow.PreparedRun{
		Workflow: workflow.DefinitionRef{Name: "tour-review", Version: 1},
		Spec: schema.Spec{Kind: "lifecycle", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{
			{Name: "tests", Uses: "script"},
			{Name: "style", Uses: "script"},
			{Name: "accept", Uses: "approval", With: map[string]any{"means": "I have tried the change in its preview and it does what the change says."}},
		}}}},
	})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Git = fakeGit{}
		deps.Runners = map[string]workflow.Runner{"script": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			status := workflow.StatusPass
			if task.Step().Name == "style" {
				status = workflow.StatusFail
			}
			if err := task.Finish(workflow.StepOutcome{Status: status}, nil); err != nil {
				t.Error(err)
			}
		}}
	})
	review, err := a.Work.Create(asPerson("sam"), workflow.ChangeDraft{ProjectID: lunch.ID, Type: "change", Title: "Show the order total at the top of the list"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.StartRun(asPerson("sam"), workflow.StartChangeRun{ChangeID: review.ID, Workflow: "tour-review"}); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()

	// A change with a chat: the helper's work so far is a recorded
	// transcript (site/tour/chat.jsonl) on the console's fake agent.
	chatChange, err := a.Work.Create(asPerson("sam"), workflow.ChangeDraft{ProjectID: lunch.ID, Title: "Let an admin edit the menu"})
	if err != nil {
		t.Fatal(err)
	}
	var opened struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(call("POST", "/api/v1/projects/lunch-orders/console", map[string]any{"changeId": chatChange.ID}), &opened); err != nil {
		t.Fatal(err)
	}
	session, err := a.Console.Get(context.Background(), opened.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := os.ReadFile(filepath.Join(tourDir, "chat.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(session.Dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session.Dir, "sessions", "2026-10-20T00-00-00_console.jsonl"), transcript, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Screen(session.ID, []byte("ready"))

	h = a.Handler()
	server.Config.Handler = h
	server.StartTLS()

	// The stops, with the ids the scene made.
	vars := map[string]string{"{review}": review.ID.String(), "{chat}": chatChange.ID.String(), "{session}": session.ID}
	type shot struct {
		ID   string `json:"id"`
		Path string `json:"path"`
		Wait string `json:"wait,omitempty"`
	}
	var shots []shot
	for _, s := range stops {
		p := s.Path
		for k, v := range vars {
			p = strings.ReplaceAll(p, k, v)
		}
		shots = append(shots, shot{ID: s.ID, Path: p, Wait: s.Wait})
	}
	shotsJSON, _ := json.Marshal(shots)
	module := os.Getenv("APPTOWN_PLAYWRIGHT_MODULE")
	if module == "" {
		home, _ := os.UserHomeDir()
		module = filepath.Join(home, ".local/share/forge-nightly/playwright/node_modules/playwright/index.mjs")
	}
	run := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
		Argv: []string{"node", filepath.Join(tourDir, "shoot.mjs")},
		Env: []string{
			"APPTOWN_PLAYWRIGHT_MODULE=" + module,
			"TOUR_URL=https://" + a.cfg.publicHost,
			"TOUR_USER=sam", "TOUR_PASSWORD=" + password,
			"TOUR_SHOTS=" + string(shotsJSON),
			"TOUR_OUT=" + tourDir,
			"TMPDIR=" + t.TempDir(),
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
		},
		Timeout: 10 * time.Minute,
	})
	t.Log(string(run.Output))
	if run.RunErr != nil {
		t.Fatalf("the camera failed: %v", run.RunErr)
	}
	writeTourPage(t, tourDir, stops)
}

// tourStop is one stop of site/tour/tour.yaml.
type tourStop struct {
	ID      string `yaml:"id"`
	Title   string `yaml:"title"`
	Caption string `yaml:"caption"`
	Path    string `yaml:"path"`
	Wait    string `yaml:"wait"`
}

func readTourStops(t *testing.T, path string) []tourStop {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Stops []tourStop `yaml:"stops"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Stops
}

// writeTourPage writes site/tour/index.html from the stops: each stop's
// title, caption and shot, the dark shot under a dark system theme.
func writeTourPage(t *testing.T, dir string, stops []tourStop) {
	t.Helper()
	var b strings.Builder
	b.WriteString(tourPageHead)
	for _, s := range stops {
		fmt.Fprintf(&b, `  <section id="%s">
    <h2>%s</h2>
    <p>%s</p>
    <picture>
      <source srcset="%s-dark.png" media="(prefers-color-scheme: dark)">
      <img src="%s-light.png" width="1440" height="900" alt="%s" loading="lazy">
    </picture>
  </section>
`, html.EscapeString(s.ID), html.EscapeString(s.Title), html.EscapeString(s.Caption), html.EscapeString(s.ID), html.EscapeString(s.ID), html.EscapeString(s.Title))
	}
	b.WriteString(tourPageTail)
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

const tourPageHead = `<!doctype html>
<!-- Generated by make tour from site/tour/tour.yaml; edit that, not this. -->
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AppTown tour</title>
<meta name="description" content="What AppTown looks like: its main pages, in order, with what each is for.">
<style>
  :root { --bg: #fbfaf7; --fg: #1d1d1b; --muted: #6b6a66; --line: #e4e1da; }
  @media (prefers-color-scheme: dark) {
    :root { --bg: #1b1a18; --fg: #ecebe6; --muted: #a3a19a; --line: #34322e; }
  }
  body { margin: 0; background: var(--bg); color: var(--fg); font: 17px/1.55 system-ui, -apple-system, "Segoe UI", sans-serif; }
  main { max-width: 1100px; margin: 0 auto; padding: 56px 16px; }
  h1 { font-size: 36px; margin: 0 0 4px; }
  .lead { color: var(--muted); margin: 0 0 40px; }
  section { border-top: 1px solid var(--line); padding: 28px 0 36px; }
  h2 { font-size: 22px; margin: 0 0 6px; }
  section p { margin: 0 0 16px; max-width: 70ch; }
  img { display: block; width: 100%; height: auto; border: 1px solid var(--line); border-radius: 6px; }
  a { color: inherit; }
</style>
</head>
<body>
<main>
  <h1>AppTown tour</h1>
  <p class="lead">What AppTown looks like, page by page. The pictures are made from a small example team's AppTown each release; <a href="/">back to apptown.dev</a>.</p>
`

const tourPageTail = `</main>
</body>
</html>
`
