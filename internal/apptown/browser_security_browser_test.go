//go:build browser

package apptown

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/testinputs"
	"github.com/flokkit/apptown/stock"
)

// A real Chromium browser against disposable AppTown state and three TLS
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
	// Configure the same attachment the browser configuration endpoint owns.
	pid, _, _ := fleetRepo(t, a, selfErrorsProject, map[string]string{"README.md": "# AppTown"})
	reportingHost := "reports.other.test:" + port
	if err := a.Integrations.Attachments.Attach(t.Context(), pid, integration.CapErrors, "glitchtip", fmt.Sprintf(`{"dsn":"https://browserkey@%s/7"}`, reportingHost), integration.Provisioned, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	a.selfErr.checked = time.Time{}
	var envelopes, blockedRequests atomic.Int32
	h = a.Handler()
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == a.cfg.publicHost {
			h.ServeHTTP(w, r)
			return
		}
		if r.Host == reportingHost {
			w.Header().Set("Access-Control-Allow-Origin", "https://"+a.cfg.publicHost)
			w.Header().Set("Access-Control-Allow-Headers", "content-type")
			if r.Method == "POST" && r.URL.Path == "/api/7/envelope/" {
				body, _ := io.ReadAll(r.Body)
				if bytes.Contains(body, []byte("resource-policy-browser-proof")) {
					envelopes.Add(1)
				}
			}
			w.WriteHeader(200)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/blocked/") {
			blockedRequests.Add(1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<!doctype html><title>Untrusted site fixture</title><body>untrusted site</body>")
	})
	server.StartTLS()
	testinputs.Files(t, "testdata/browser_security.mjs")
	run := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
		Argv: []string{"node", "testdata/browser_security.mjs"},
		Env: []string{
			"APPTOWN_BROWSER_URL=https://" + a.cfg.publicHost,
			"APPTOWN_SIBLING_URL=https://evil.preview.example.test:" + port,
			"APPTOWN_OTHER_URL=https://evil.other.test:" + port,
			fmt.Sprintf("APPTOWN_BROWSER_CHANGE=%d", changeID),
			"APPTOWN_BROWSER_CONSOLE=" + session.ID,
			"APPTOWN_REPORTING_URL=https://" + reportingHost,
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
	if envelopes.Load() < 1 {
		t.Fatal("configured reporting endpoint received no browser envelope")
	}
	if blockedRequests.Load() != 0 {
		t.Fatalf("disallowed resource server received %d requests", blockedRequests.Load())
	}
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

// The UI tour (site/tour, `make tour`): one small team's AppTown after two
// weeks of use, built through its HTTP API and owners (tourScene, below)
// and photographed by Playwright in both themes. The example uses records the owners wrote; the camera substitutes a
// plausible disk reading and the conversations are recorded examples. The checks are
// the projects' own scripts, run for real on this host (HostScripts, and
// the local shell for the conformance review's checks: the tour has no
// Docker; checks that need a tool's image or a model say they could not
// run). The chats are recorded transcripts on the console's fake agent,
// so no model is called; the commands they quote ran in the change's
// folder. Not part of any gate: make tour runs it.
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
	// The tour has no Docker: the fake answers AppTown's questions about
	// its (empty) host, has no images and builds none.
	daemon := newFakeDocker()
	daemon.reply = func(args []string) (string, error, bool) {
		switch {
		case len(args) > 1 && args[0] == "image" && args[1] == "inspect":
			return "Error: No such image", errors.New("exit status 1"), true
		case len(args) > 0 && args[0] == "build":
			return "", errors.New("the tour has no Docker to build an image"), true
		}
		return "", nil, false
	}
	a, h := internalApp(t, Config{DataDir: dataDir, PublicHost: "apptown.localhost:" + port, TierDomains: []string{"preview=preview.localhost"},
		DockerClient: daemon, ConsoleAgent: runtime, ConsoleCLI: fixtureCLI(t), HostScripts: true,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	// The scene's two weeks end yesterday: AppTown's clock starts at
	// midnight UTC fourteen days back, the scene moves it forward act by
	// act, and it is set right before the camera comes.
	y, m, d := a.cfg.clock.Now().UTC().Date()
	start := time.Date(y, m, d-14, 0, 0, 0, 0, time.UTC)
	moveClock(a, start.Sub(a.cfg.clock.Now()))
	// What apptownd seeds at start: stock content, integrations, targets.
	ctx := context.Background()
	a.SyncStock(ctx)
	a.SyncSeedIntegrations(ctx)
	a.SeedTargets(ctx)
	verification.SyncStockScanners(ctx, a.Integrations, stock.FS)
	a.SyncStockDependencies(ctx)
	configureConsoleModel(t, a)
	// The conformance review's checks run where the tour can run them; then
	// Work, stopped by that rewiring, is built again with the tour's
	// landing workflow.
	tourChecks(t, a)
	defineWorkflows(t, a, tourWorkflow)

	const password = "tour-password-not-secret"
	boot := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "sam", password))
	if boot.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", boot.Code, boot.Body.String())
	}
	s := &tourScene{t: t, a: a, runtime: runtime, password: password, cookies: map[string]*http.Cookie{}}
	// configureConsoleModel supplied an explicit fixture binding; no provider is contacted.
	// The team: Sam runs AppTown; Priya and Tom make apps.
	for _, u := range []string{"priya", "tom"} {
		s.call("sam", "POST", "/api/v1/users", map[string]string{"username": u, "role": "member", "password": password})
	}
	made := s.play(start)
	moveClock(a, -a.cfg.clock.ahead)
	// Let the normal observation owners collect facts before photographing Home.
	s.call("sam", "GET", "/api/v1/attention", nil)
	for _, kind := range []string{diskObservationJob, workerObservationJob, "capacity-observation", "inventory", "protection-observation"} {
		for _, job := range operationJobs(t, a, kind) {
			if job.State == "available" || job.State == "retryable" || job.State == "scheduled" {
				result := runQueuedJobForTest(t, a, kind, job.Key)
				t.Logf("tour observation %s: %s", kind, result.State)
			}
		}
	}
	t.Logf("tour operational observations: %s", s.call("sam", "GET", "/api/v1/attention", nil))

	h = a.Handler()
	server.Config.Handler = h
	server.StartTLS()

	// The stops, with the ids the scene made.
	vars := map[string]string{"{before}": made.before, "{after}": made.after, "{review}": made.review.String(), "{chat}": made.chat.String(), "{session}": made.session}
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
	identity := tourIdentity(t)
	fmt.Fprintf(&b, "  <p class=\"lead\" data-tour-identity>%s · source <code>%s</code> · <a href=\"/tour/\">All versions</a></p>\n", html.EscapeString(identity.Version), html.EscapeString(identity.Source))
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
  <p class="lead">Follow a small team's apps from a request to a checked change. Conversations are recorded examples; the checks run against real example code. <a href="/">Back to apptown.dev</a>.</p>
`

const tourPageTail = `</main>
</body>
</html>
`

// tourScene builds the tour's AppTown the way its people would: through
// the HTTP routes with each person's own session, and through the owners'
// operations where a test stands in for an agent (committing in a change's
// folder) or for AppTown's own rounds (the usage harvest). Times come from
// AppTown's clock, which the scene moves between acts; nothing is written
// with a time of its own.
type tourScene struct {
	t        *testing.T
	a        *App
	runtime  *consoletest.Agent
	password string
	cookies  map[string]*http.Cookie
}

func (s *tourScene) later(d time.Duration) { moveClock(s.a, d) }

// until moves the clock forward to at, if it is not there yet.
func (s *tourScene) until(at time.Time) {
	if d := at.Sub(s.now()); d > 0 {
		s.later(d)
	}
}
func (s *tourScene) now() time.Time { return s.a.cfg.clock.Now() }

// session is who's signed-in cookie, signing in once.
func (s *tourScene) session(who string) *http.Cookie {
	s.t.Helper()
	if c := s.cookies[who]; c != nil {
		return c
	}
	w := postJSON(s.a.Handler(), "/api/v1/auth/login", map[string]string{"username": who, "password": s.password})
	if w.Code != http.StatusOK {
		s.t.Fatalf("%s signs in: %d %s", who, w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == secureSessionCookie && c.Value != "" {
			s.cookies[who] = c
			return c
		}
	}
	s.t.Fatalf("%s's sign-in issued no session", who)
	return nil
}

// call is one request as who, which must succeed.
func (s *tourScene) call(who, method, path string, body any) []byte {
	s.t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s.session(who))
	w := httptest.NewRecorder()
	s.a.Handler().ServeHTTP(w, req)
	if w.Code >= 300 {
		s.t.Fatalf("%s: %s %s: %d %s", who, method, path, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

// tourChange is a change the scene opened, and the folder its work is
// committed in.
type tourChange struct {
	ID     ids.ID
	slug   string
	title  string
	folder string
}

// open is who asking for a change on a project.
func (s *tourScene) open(who, slug, title, body string) *tourChange {
	s.t.Helper()
	var created struct {
		Change struct {
			ID ids.ID `json:"id"`
		} `json:"change"`
	}
	if err := json.Unmarshal(s.call(who, "POST", "/api/v1/projects/"+slug+"/changes", map[string]string{"title": title, "body": body}), &created); err != nil {
		s.t.Fatal(err)
	}
	id := created.Change.ID
	folder := filepath.Join(s.a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", id))
	if _, err := os.Stat(filepath.Join(folder, ".git")); err != nil {
		s.t.Fatalf("change %q has no folder: %v", title, err)
	}
	return &tourChange{ID: id, slug: slug, title: title, folder: folder}
}

// write puts files in the change's folder.
func (s *tourScene) write(c *tourChange, files map[string]string) {
	s.t.Helper()
	for name, content := range files {
		path := filepath.Join(c.folder, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			s.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.TrimLeft(content, "\n")), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
}

// sh runs a command in the change's folder, as the helper does in a chat,
// and returns what it printed: a chat transcript quotes real output.
func (s *tourScene) sh(c *tourChange, command string) string {
	s.t.Helper()
	out, _ := s.run(c, command)
	return out
}

// run is sh with the command's exit code.
func (s *tourScene) run(c *tourChange, command string) (string, int) {
	s.t.Helper()
	out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
		Workspace: c.folder, Argv: []string{"sh", "-c", command},
		Env:     []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")},
		Timeout: time.Minute,
	})
	if !out.Answered() {
		s.t.Fatalf("%s: %v", command, out.RunErr)
	}
	return string(out.Output), out.ExitCode
}

// commit saves the folder's work on the change's branch, dated by
// AppTown's clock: as who, or as the folder's own identity (the helper's
// commits in a chat) when who is "".
func (s *tourScene) commit(c *tourChange, who, message string) {
	s.t.Helper()
	gittest.Run(s.t, c.folder, "add", "-A")
	when := s.now().Format(time.RFC3339)
	args := []string{"commit", "-qm", message}
	if who != "" {
		args = append([]string{"-c", "user.name=" + who, "-c", "user.email=" + who + "@example.test"}, args...)
	}
	cmd := gittest.Command(args...)
	cmd.Dir = c.folder
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_DATE="+when, "GIT_COMMITTER_DATE="+when)
	if out, err := cmd.CombinedOutput(); err != nil {
		s.t.Fatalf("commit %q: %v\n%s\n%s", message, err, out, gittest.Run(s.t, c.folder, "status"))
	}
}

// committedAt is when the change's latest commit says it was made.
func (s *tourScene) committedAt(c *tourChange) time.Time {
	s.t.Helper()
	at, err := time.Parse(time.RFC3339, gittest.Run(s.t, c.folder, "show", "-s", "--format=%cI", "HEAD"))
	if err != nil {
		s.t.Fatal(err)
	}
	return at
}

// tourStep is one step of a run as the change's page reads it.
type tourStep struct {
	ID       ids.ID
	Name     string
	Status   string
	Evidence string
}

func (s *tourScene) steps(runID ids.ID) map[string]tourStep {
	s.t.Helper()
	run, err := s.a.Work.Snapshot(asPerson("sam"), runID, workflow.DetailView)
	if err != nil {
		s.t.Fatal(err)
	}
	steps := map[string]tourStep{}
	for _, st := range run.Steps {
		steps[st.Name] = tourStep{ID: st.ID, Name: st.Name, Status: st.Status.String(), Evidence: string(st.Evidence)}
	}
	return steps
}

// land starts the change's landing as who asked for it and waits for its
// checks; want is what each check must have come to.
func (s *tourScene) land(c *tourChange, who string, want map[string]string) ids.ID {
	s.t.Helper()
	var started struct {
		Run struct {
			ID ids.ID `json:"id"`
		} `json:"run"`
	}
	if err := json.Unmarshal(s.call(who, "POST", fmt.Sprintf("/api/v1/changes/%s/runs", c.ID), map[string]string{"workflow": "tour-land"}), &started); err != nil {
		s.t.Fatal(err)
	}
	run := started.Run
	s.a.Work.Wait()
	steps := s.steps(run.ID)
	for name, status := range want {
		if steps[name].Status != status {
			s.t.Fatalf("change %s: %s is %q, want %q (%+v)", c.ID, name, steps[name].Status, status, steps)
		}
	}
	return run.ID
}

// approve is who giving the landing's approval, agreeing to the commit
// the approval question shows, as the change page does.
func (s *tourScene) approve(runID ids.ID, who string) {
	s.t.Helper()
	step := s.steps(runID)["accept"]
	adds := shownAdds(s.t, s.a, asPerson(who), step.ID)
	s.call(who, "POST", fmt.Sprintf("/api/v1/steps/%s/approve", step.ID), map[string]string{"decision": "approve", "adds": adds})
	s.a.Work.Wait()
}

// ship is a change with its checks passing, approved after wait and
// landed.
func (s *tourScene) ship(c *tourChange, asker, approver string, wait time.Duration) {
	s.t.Helper()
	run := s.land(c, asker, map[string]string{"tests": "pass", "style": "pass"})
	s.later(wait)
	s.approve(run, approver)
	ch, err := s.a.Work.Change(asPerson("sam"), c.ID)
	if err != nil || ch.State.String() != "shipped" {
		s.t.Fatalf("change %s did not ship: %+v %v", c.ID, ch, err)
	}
}

// chat opens a chat on a change as who.
func (s *tourScene) chat(c *tourChange, who string) string {
	s.t.Helper()
	var opened struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(s.call(who, "POST", "/api/v1/projects/"+c.slug+"/console", map[string]any{"changeId": c.ID}), &opened); err != nil {
		s.t.Fatal(err)
	}
	s.runtime.Screen(opened.Session.ID, []byte("ready"))
	return opened.Session.ID
}

// transcript gives a chat what was said in it: the recording the console's
// fake agent plays (no model is called).
func (s *tourScene) transcript(sessionID string, log *piLog) {
	s.t.Helper()
	session, err := s.a.Console.Get(context.Background(), sessionID)
	if err != nil {
		s.t.Fatal(err)
	}
	dir := filepath.Join(session.Dir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, log.started.UTC().Format("2006-01-02T15-04-05")+"_console.jsonl"), log.bytes(), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// piLog is a pi session transcript, built turn by turn. Each assistant
// message carries the token counts a recording carries, priced as the
// model's list price; the usage harvest reads them as it reads any chat.
type piLog struct {
	started time.Time
	at      time.Time
	lines   []map[string]any
	prev    any
	n       int
	context int // tokens of conversation so far, read again each turn
}

func newPiLog(at time.Time) *piLog {
	l := &piLog{started: at, at: at, context: 9000}
	l.lines = append(l.lines, map[string]any{"type": "session", "version": 3, "id": "console", "timestamp": l.stamp(0), "cwd": "/work"})
	return l
}

func (l *piLog) stamp(d time.Duration) string {
	l.at = l.at.Add(d)
	return l.at.UTC().Format("2006-01-02T15:04:05.000Z")
}

func (l *piLog) add(role string, d time.Duration, message map[string]any) {
	l.n++
	id := fmt.Sprintf("m%d", l.n)
	message["role"] = role
	message["timestamp"] = 0
	l.lines = append(l.lines, map[string]any{"type": "message", "id": id, "parentId": l.prev, "timestamp": l.stamp(d), "message": message})
	l.prev = id
}

func (l *piLog) user(text string) {
	l.add("user", 20*time.Second, map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
	l.context += len(text) / 4
}

// say is the helper's message; command, when set, is the command it runs
// next, and output what that command printed.
func (l *piLog) say(text, command, output string) {
	content := []any{map[string]any{"type": "text", "text": text}}
	stop := "stop"
	call := ""
	if command != "" {
		call = fmt.Sprintf("c%d", l.n+1)
		content = append(content, map[string]any{"type": "toolCall", "id": call, "name": "bash", "arguments": map[string]any{"command": command}})
		stop = "toolUse"
	}
	out := 60 + len(text)/4 + len(command)/4
	fresh := 400 + len(text)/2
	cached := l.context
	// Claude Sonnet list prices per million tokens: $3 in, $15 out, $0.30 a
	// cache read.
	cost := (float64(fresh)*3 + float64(out)*15 + float64(cached)*0.3) / 1e6
	l.add("assistant", 15*time.Second, map[string]any{
		"content": content, "api": "openai-completions", "provider": "openrouter", "model": "anthropic/claude-sonnet-5.5",
		"usage": map[string]any{"input": fresh, "output": out, "cacheRead": cached, "cacheWrite": 0, "totalTokens": fresh + out + cached,
			"cost": map[string]any{"input": float64(fresh) * 3 / 1e6, "output": float64(out) * 15 / 1e6, "cacheRead": float64(cached) * 0.3 / 1e6, "cacheWrite": 0, "total": cost}},
		"stopReason": stop, "responseId": fmt.Sprintf("r%d", l.n+1)})
	l.context += fresh + out
	if command != "" {
		l.add("toolResult", 4*time.Second, map[string]any{"toolCallId": call, "toolName": "bash",
			"content": []any{map[string]any{"type": "text", "text": output}}, "isError": false})
		l.context += len(output) / 4
	}
}

func (l *piLog) bytes() []byte {
	var b bytes.Buffer
	for _, line := range l.lines {
		j, _ := json.Marshal(line)
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// tourWorkflow is how the tour's projects land a change: the repository's
// own checks, run on the landing's staged commit, then a person's
// approval; the merge follows the approval.
var tourWorkflow = workflow.PreparedRun{
	Workflow: workflow.DefinitionRef{Name: "tour-land", Version: 1},
	Spec: schema.Spec{Kind: "lifecycle", Role: "landing", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{
		{Name: "tests", Uses: "script", With: map[string]any{"run": "sh checks/tests.sh", "executor": "local"}},
		{Name: "style", Uses: "script", With: map[string]any{"run": "sh checks/style.sh", "executor": "local"}},
		{Name: "accept", Uses: "approval", With: map[string]any{"means": "I have read what changed and the checks, and it does what the change says."}},
	}}}},
}

// tourIDs are the ids the scene made that the stops name.
type tourIDs struct {
	review, chat  ids.ID
	session       string
	before, after string
}

// tourAsk is a change a person asks the helper for in a chat: what they
// said, what the helper looked at first, the files it wrote, and what it
// said about them. The helper's commands run for real in the change's
// folder, and its transcript quotes their output.
type tourAsk struct {
	ask, look, lookCommand string
	files                  map[string]string
	did, after             string
}

// helped is a chat in which the helper makes the change; the work is saved
// on the change's branch. The chat stays open; end it with endChat.
func (s *tourScene) helped(c *tourChange, who string, h tourAsk) string {
	s.t.Helper()
	session := s.chat(c, who)
	log := newPiLog(s.now())
	log.user(h.ask)
	asked := log.at
	log.say(h.look, h.lookCommand, s.sh(c, h.lookCommand))
	s.write(c, h.files)
	// The helper's last word says the tests pass; they must.
	tests, code := s.run(c, "sh checks/tests.sh")
	if code != 0 {
		s.t.Fatalf("the helper's tests for %q fail:\n%s", c.title, tests)
	}
	log.say(h.did+" Running the tests:", "sh checks/tests.sh", tests)
	// The work is saved when the tests have passed, before the helper says
	// so: the clock moves to that point of the conversation first.
	s.until(log.at)
	s.commit(c, "", c.title)
	if committed := s.committedAt(c); committed.Before(asked.Truncate(time.Second)) {
		s.t.Fatalf("the helper saved %q at %s, before it was asked at %s", c.title, committed, asked)
	}
	log.say(h.after, "", "")
	s.transcript(session, log)
	s.until(log.at)
	return session
}

// endChat is who closing a chat, which harvests its usage.
func (s *tourScene) endChat(who, session string) {
	s.t.Helper()
	s.call(who, "DELETE", "/api/v1/console/"+session, nil)
}

// play is two weeks of a small team's AppTown, in order. Day 0 is midnight
// UTC two weeks back, so the last acts are yesterday's; each act happens
// when AppTown's clock reads day d at hh:mm UTC (the camera shoots in UTC),
// and the clock only moves forward.
func (s *tourScene) play(start time.Time) tourIDs {
	t := s.t
	at := func(day int, clock string) {
		t.Helper()
		var h, m int
		if _, err := fmt.Sscanf(clock, "%d:%d", &h, &m); err != nil {
			t.Fatal(err)
		}
		y, mo, d := start.Date()
		target := time.Date(y, mo, d+day, h, m, 0, 0, time.UTC)
		if d := target.Sub(s.now()); d > 0 {
			s.later(d)
		} else {
			t.Fatalf("the scene's acts are out of order: day %d %s is %s behind the clock", day, clock, -d)
		}
	}

	// Day 0: Sam sets up the three apps; Priya and Tom work on them.
	at(0, "9:00")
	type proj struct{ slug, name, blurb, pack string }
	for _, p := range []proj{
		{"lunch-orders", "Lunch Orders", "Each morning people pick a lunch from a short menu; at 11am whoever is ordering sees the list with names and totals.", "django-htmx"},
		{"club-newsletter", "Club Newsletter", "The cycling club's monthly newsletter, as a small static site.", "conventions-only"},
		{"rocket-garden", "Rocket Garden", "A little browser game: grow rockets, launch them, beat your best height.", "html-game"},
	} {
		s.call("sam", "POST", "/api/v1/projects", map[string]string{"slug": p.slug, "name": p.name, "blurb": p.blurb})
		s.call("sam", "POST", "/api/v1/projects/"+p.slug+"/repo", map[string]string{"defaultBranch": "main"})
		s.call("sam", "POST", "/api/v1/projects/"+p.slug+"/pack", map[string]string{"name": p.pack})
		for _, who := range []string{"priya", "tom"} {
			s.call("sam", "POST", "/api/v1/projects/"+p.slug+"/members", map[string]string{"grantee": "user:" + who, "role": "project-contributor"})
		}
	}
	// Priya looks after the newsletter and approves its issues.
	s.call("sam", "POST", "/api/v1/projects/club-newsletter/members", map[string]string{"grantee": "user:priya", "role": "project-approver"})
	// Adopting a way of working queues a review of the project; AppTown's
	// job queue runs it.
	for _, j := range operationJobs(t, s.a, verification.ReviewJob) {
		runQueuedJobForTest(t, s.a, verification.ReviewJob, j.Key)
	}

	at(0, "9:20")
	lunch1 := s.open("priya", "lunch-orders", "Start the lunch app", "A pick page with the four dishes we order most, and a list for whoever orders at 11am.")
	s.write(lunch1, lunchStart)
	s.commit(lunch1, "priya", "Start the lunch app: pick a dish, see today's orders")
	at(0, "10:05")
	run := s.land(lunch1, "priya", map[string]string{"tests": "pass", "style": "pass"})
	at(0, "11:40")
	s.approve(run, "sam")

	// Day 1: the game and the newsletter start, with the helper.
	at(1, "16:10")
	rocket1 := s.open("tom", "rocket-garden", "Start Rocket Garden", "Grow a rocket, launch it, see how high it goes.")
	session := s.helped(rocket1, "tom", tourAsk{
		ask:         "I want a game where you plant a rocket, it grows fuel while you wait, and when you press Launch it flies. Higher with more fuel. Can you start it?",
		look:        "Sure. The project is empty apart from AppTown's own files, so I'll start from scratch with plain HTML and JavaScript, as the way of working asks.",
		lookCommand: "git ls-files",
		files:       rocketStart,
		did:         "I've written index.html with a fuel counter and a Launch button, game.js to grow one fuel a second and show the height, and rules.js with the rules on their own: grow up to 10 fuel, and a launch height of 120 m per fuel plus 40 m per fin, nothing without fuel. There are tests for the three rules.",
		after:       "All three tests pass. Open the preview and wait a few seconds before you launch: more fuel goes higher. The title uses the Fredoka font from Google Fonts; say if you'd rather keep everything in the project.",
	})
	at(1, "16:40")
	s.endChat("tom", session)
	s.ship(rocket1, "tom", "sam", 40*time.Minute)

	at(1, "19:30")
	news1 := s.open("priya", "club-newsletter", "Put up the October newsletter", "The hill climb results and the lights-on reminder.")
	session = s.helped(news1, "priya", tourAsk{
		ask:         "Please make the club newsletter site: a front page that lists the issues, and the October issue. Hill climb on the 4th, 31 riders, Mei set a club record of 6:42 on Ridge Road. And a reminder that lights go on from the 26th when the clocks change.",
		look:        "I'll see what's in the project first.",
		lookCommand: "git ls-files",
		files:       newsletterStart,
		did:         "I've made public/index.html listing the issues, public/2026-10.html with the hill climb and the lights reminder, a small stylesheet and the club logo as an SVG. checks/links.py follows every link between pages, and checks/alt.py wants alt text on every image.",
		after:       "Both pages link up and the logo has alt text. It's saved on the change; land it when it reads right to you.",
	})
	at(1, "19:55")
	s.endChat("priya", session)
	s.ship(news1, "priya", "sam", time.Hour+5*time.Minute)

	// Day 4: Tom asks the helper for names on the 11am list.
	at(4, "10:15")
	lunch2 := s.open("tom", "lunch-orders", "Show each person's name next to their order on the 11am list", "")
	session = s.helped(lunch2, "tom", tourAsk{
		ask:         "On the 11am list I can see 3 × Veggie wrap but not who ordered them. Can it show the names next to each dish?",
		look:        "I'll look at how the list is built first.",
		lookCommand: "cat orders/views.py orders/templates/orders/list.html",
		files:       lunchNames,
		did:         "The list only gets a count per dish. I've added people_by_dish to orders/totals.py (names in alphabetical order), passed rows of dish, count and names to the template, and the list now reads \"3 × Veggie wrap: Mei, Priya, Tom\". There is a test for the name order.",
		after:       "All three tests pass. I've saved the work on this change; land it when you're happy with it.",
	})
	at(4, "10:40")
	s.endChat("tom", session)
	run = s.land(lunch2, "tom", map[string]string{"tests": "pass", "style": "pass"})
	at(4, "13:55")
	s.approve(run, "sam")

	// Day 7: the ride calendar, approved by Priya.
	at(7, "20:10")
	news2 := s.open("tom", "club-newsletter", "Add the ride calendar", "The weekly rides with their start times and pace, linked from the front page.")
	s.write(news2, newsletterRides)
	s.commit(news2, "tom", "Ride calendar page")
	s.ship(news2, "tom", "priya", 25*time.Minute)

	// Day 8: a real before/after, photographed from committed example files.
	at(8, "10:00")
	neon := s.open("tom", "club-newsletter", "Give the club a neon makeover", "Keep the rides and newsletters, but make it gloriously loud.")
	before := s.serveSite(neon)
	neonSession := s.helped(neon, "tom", tourAsk{
		ask:         "The site is sensible. Make it an exuberant neon cycling extravaganza. Keep it readable and keep every link working!",
		look:        "I'll keep the newsletter and ride pages and change the front page and its styling.",
		lookCommand: "cat public/index.html public/style.css",
		files:       newsletterNeon,
		did:         "The front page has a giant headline, neon colours, ride cards and a checkerboard border. No flashing or outside assets.",
		after:       "The links pass. The newsletters and ride calendar are still there. Try the new look before approving it.",
	})
	after := s.serveSite(neon)
	s.endChat("tom", neonSession)
	s.ship(neon, "tom", "priya", 25*time.Minute)

	// Day 9: the best height survives a reload.
	at(9, "17:00")
	rocket2 := s.open("tom", "rocket-garden", "Remember the best height between visits", "")
	session = s.helped(rocket2, "tom", tourAsk{
		ask:         "When I reload the page my best height is gone. Can the game remember it?",
		look:        "I'll check how the launch is handled now.",
		lookCommand: "cat game.js",
		files:       rocketBest,
		did:         "The game keeps nothing between visits. I've added best() to rules.js, and game.js now keeps the best height in the browser's localStorage and shows it under the launch. Two new tests cover best().",
		after:       "All five tests pass. Launch twice, reload, and the best height is still there.",
	})
	at(9, "17:25")
	s.endChat("tom", session)
	s.ship(rocket2, "tom", "sam", 55*time.Minute)

	// Day 10: Sam reviews the fleet against its ways of working.
	at(10, "8:30")
	s.review()
	// The game's title font comes from Google Fonts, which a client-only
	// game should not load; Sam accepts that for now, until the art pass.
	at(10, "9:10")
	s.call("sam", "POST", "/api/v1/projects/rocket-garden/dispositions", map[string]string{
		"fingerprint": "guidance|game/client-only@1|no-external-urls|", "status": "accepted",
		"justification": "The title font comes from Google Fonts; offline the game falls back to the system font and plays the same. Bundle the font in the art pass.",
		"expiresAt":     s.now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)})

	// Day 11: the order total, with a debugging print the style check
	// refuses. It waits for Sam to send it back or fix it.
	at(11, "12:20")
	lunch3 := s.open("sam", "lunch-orders", "Show the order total at the top of the list", "Whoever orders needs the total to pay the café.")
	s.write(lunch3, lunchTotal)
	s.commit(lunch3, "sam", "Total price at the top of the 11am list")
	s.land(lunch3, "sam", map[string]string{"tests": "pass", "style": "fail"})

	// Day 12: a countdown that preserves the no-fuel rule.
	at(12, "16:45")
	rocket3 := s.open("tom", "rocket-garden", "Add a countdown before launch", "3, 2, 1, then up.")
	s.write(rocket3, rocketCountdown)
	s.commit(rocket3, "tom", "Countdown before launch")
	s.ship(rocket3, "tom", "sam", 20*time.Minute)

	// The night before the last day, the weekly review runs again.
	at(12, "23:00")
	s.review()

	// Day 13: the November issue waits for an approval; Priya asks the
	// helper to make the menu editable.
	at(13, "7:30")
	news3 := s.open("tom", "club-newsletter", "Add the November newsletter", "Winter ride times and the end-of-season dinner.")
	s.write(news3, newsletterNovember)
	s.commit(news3, "tom", "November newsletter")
	s.land(news3, "tom", map[string]string{"tests": "pass", "style": "pass"})

	at(13, "8:50")
	lunch4 := s.open("priya", "lunch-orders", "Let an admin edit the menu", "")
	session = s.helped(lunch4, "priya", tourAsk{
		ask:         "Can an admin edit the menu? Right now the dishes are hard-coded. Sam should be able to add, rename and hide dishes without asking me.",
		look:        "Yes. I'll move the dishes into the database and use the Django admin for editing, so Sam gets add, rename and hide with no new pages to build. First I'll look at how the menu is defined now.",
		lookCommand: "grep -rn MENU orders/",
		files:       lunchMenuModel,
		did:         "The menu is a list in orders/views.py. I've added a Dish model with a name and a shown flag, a migration that copies the four current dishes in, and registered it in the admin (the shown box can be ticked straight from the list). The pick page now offers shown dishes only, by name.",
		after:       "All five tests pass, including two new ones: a hidden dish is not offered, and the menu is listed by name. Orders keep the dish name they were placed with, so renaming a dish leaves earlier orders as they were. Sam can sign in to /admin and add, rename or untick a dish. It's saved on this change; land it when you've tried it.",
	})
	// AppTown's own round reads the open chat's usage, as it does at start
	// and every few minutes.
	s.a.HarvestAllConsoleUsage(context.Background())
	return tourIDs{review: lunch3.ID, chat: lunch4.ID, session: session, before: before, after: after}
}

// review is Sam running a conformance review of every project's committed
// main: the stock checks each project adopted.
func (s *tourScene) review() {
	s.t.Helper()
	rev, verdicts, err := fleetReviewsFor(s.a).RunFleetReviewWith(testConformanceContext("sam"), "manual", conformance.ReviewOptions{})
	if err != nil {
		s.t.Fatal(err)
	}
	for _, v := range verdicts {
		s.t.Logf("review %s: %s %s@%d/%s %s %s %q", rev.ID, v.ProjectSlug, v.Block, v.Version, v.Check, v.Evaluator, v.State, v.Detail)
	}
}

// tourChecks runs the conformance review's checks where the tour can: a
// check script (an sh check, in its pinned shell image) runs in the local shell against
// the staged tree; a check that needs a tool's own image cannot run (the
// tour has no Docker) and says so; a check that asks a model is answered
// that no model is called. Each lands as the review records any check that
// could not run.
func tourChecks(t *testing.T, a *App) {
	t.Helper()
	installCheckEffects(t, a, checkOverrides{
		run: func(req ExecRequest) ExecOutcome {
			if req.Image != stock.Images().Sh {
				refusal := "the tour has no Docker to run " + req.Image
				return ExecOutcome{Output: []byte(refusal), RunErr: errors.New(refusal)}
			}
			return agentenv.Execute(a.processRunner, context.Background(), ExecutorLocal, req, a.cfg.clock.Now())
		},
		complete: func(string, string, string) (string, *modelcall.Usage, error) {
			return "", nil, errors.New("the tour calls no model")
		},
	})
}

// The tour's three apps, as the files each change commits. Every check the
// tour shows runs these files' own scripts (checks/tests.sh and
// checks/style.sh) for real, so a pass or a failure on a page is what the
// code here earns.

// Lunch Orders, first version: pick a dish, see the 11am list.
var lunchStart = map[string]string{
	"README.md": `
# Lunch Orders

Each morning people pick a lunch from a short menu. At 11am whoever is
ordering opens the list and sees every order, with a count per dish.

Run it: ` + "`uv run python manage.py migrate && uv run python manage.py runserver`" + `

Checks: ` + "`sh checks/tests.sh`" + ` runs the unit tests; ` + "`sh checks/style.sh`" + ` refuses
stray print() calls.
`,
	"pyproject.toml": `
[project]
name = "lunch-orders"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = ["django>=5.1,<5.2", "psycopg[binary]>=3.2"]
`,
	"manage.py": `
#!/usr/bin/env python
import os
import sys

if __name__ == "__main__":
    os.environ.setdefault("DJANGO_SETTINGS_MODULE", "lunch.settings")
    from django.core.management import execute_from_command_line

    execute_from_command_line(sys.argv)
`,
	"lunch/__init__.py": "",
	"lunch/settings.py": `
import os
from pathlib import Path

BASE_DIR = Path(__file__).resolve().parent.parent
SECRET_KEY = os.environ["DJANGO_SECRET_KEY"]
DEBUG = os.environ.get("DJANGO_DEBUG") == "1"
ALLOWED_HOSTS = os.environ.get("ALLOWED_HOSTS", "localhost").split(",")

INSTALLED_APPS = [
    "django.contrib.admin",
    "django.contrib.auth",
    "django.contrib.contenttypes",
    "django.contrib.sessions",
    "django.contrib.messages",
    "django.contrib.staticfiles",
    "orders",
]
MIDDLEWARE = [
    "django.middleware.security.SecurityMiddleware",
    "django.contrib.sessions.middleware.SessionMiddleware",
    "django.middleware.common.CommonMiddleware",
    "django.middleware.csrf.CsrfViewMiddleware",
    "django.contrib.auth.middleware.AuthenticationMiddleware",
    "django.contrib.messages.middleware.MessageMiddleware",
]
ROOT_URLCONF = "lunch.urls"
TEMPLATES = [
    {
        "BACKEND": "django.template.backends.django.DjangoTemplates",
        "APP_DIRS": True,
        "OPTIONS": {
            "context_processors": [
                "django.template.context_processors.request",
                "django.contrib.auth.context_processors.auth",
                "django.contrib.messages.context_processors.messages",
            ]
        },
    }
]
DATABASES = {"default": {"ENGINE": "django.db.backends.postgresql", "NAME": os.environ.get("PGDATABASE", "lunch")}}
STATIC_URL = "static/"
DEFAULT_AUTO_FIELD = "django.db.models.BigAutoField"
`,
	"lunch/urls.py": `
from django.contrib import admin
from django.urls import path

from orders import views

urlpatterns = [
    path("", views.pick, name="pick"),
    path("list/", views.order_list, name="list"),
    path("admin/", admin.site.urls),
]
`,
	"orders/__init__.py": "",
	"orders/models.py": `
from django.db import models


class Order(models.Model):
    person = models.CharField(max_length=80)
    dish = models.CharField(max_length=80)
    day = models.DateField(auto_now_add=True)

    def __str__(self):
        return f"{self.person}: {self.dish}"
`,
	"orders/migrations/__init__.py": "",
	"orders/migrations/0001_initial.py": `
from django.db import migrations, models


class Migration(migrations.Migration):
    initial = True
    dependencies = []
    operations = [
        migrations.CreateModel(
            name="Order",
            fields=[
                ("id", models.BigAutoField(primary_key=True, serialize=False)),
                ("person", models.CharField(max_length=80)),
                ("dish", models.CharField(max_length=80)),
                ("day", models.DateField(auto_now_add=True)),
            ],
        ),
    ]
`,
	"orders/totals.py": `
"""Counting today's orders, kept free of Django so it is tested on its own."""

from collections import Counter


def count_by_dish(orders):
    """How many of each dish, most ordered first."""
    counts = Counter(o.dish for o in orders)
    return sorted(counts.items(), key=lambda item: (-item[1], item[0]))
`,
	"orders/views.py": `
import datetime

from django.shortcuts import redirect, render

from .models import Order
from .totals import count_by_dish

MENU = ["Chicken salad", "Veggie wrap", "Soup of the day", "Tuna sandwich"]


def pick(request):
    if request.method == "POST":
        Order.objects.create(person=request.POST["person"], dish=request.POST["dish"])
        return redirect("list")
    return render(request, "orders/pick.html", {"menu": MENU})


def order_list(request):
    orders = Order.objects.filter(day=datetime.date.today())
    return render(request, "orders/list.html", {"counts": count_by_dish(orders)})
`,
	"orders/templates/orders/pick.html": `
<!doctype html>
<title>Lunch</title>
<h1>What would you like today?</h1>
<form method="post">
  {% csrf_token %}
  <input name="person" placeholder="Your name" required>
  {% for dish in menu %}
  <label><input type="radio" name="dish" value="{{ dish }}" required> {{ dish }}</label>
  {% endfor %}
  <button>Order</button>
</form>
`,
	"orders/templates/orders/list.html": `
<!doctype html>
<title>Today's orders</title>
<script src="https://unpkg.com/htmx.org@2.0.3"></script>
<h1>Today's orders</h1>
<ul hx-get="" hx-trigger="every 30s" hx-select="ul" hx-swap="outerHTML">
  {% for dish, count in counts %}
  <li>{{ count }} &times; {{ dish }}</li>
  {% empty %}
  <li>No orders yet.</li>
  {% endfor %}
</ul>
`,
	"orders/tests/__init__.py": "",
	"orders/tests/test_totals.py": `
import unittest
from types import SimpleNamespace as Order

from orders.totals import count_by_dish


class CountByDish(unittest.TestCase):
    def test_most_ordered_first(self):
        orders = [Order(dish="Soup of the day"), Order(dish="Veggie wrap"), Order(dish="Veggie wrap")]
        self.assertEqual(count_by_dish(orders), [("Veggie wrap", 2), ("Soup of the day", 1)])

    def test_no_orders(self):
        self.assertEqual(count_by_dish([]), [])
`,
	"checks/tests.sh": `
#!/bin/sh
# The unit tests that need no database.
exec python3 -m unittest discover -s orders/tests -t . -v
`,
	"checks/style.sh": `
#!/bin/sh
# Debugging output does not ship: no print() in the app's code.
if grep -rn --include='*.py' 'print(' lunch orders; then
  echo "style: remove the print() calls above (use logging if it should stay)"
  exit 1
fi
echo "style: no print() calls"
`,
}

// Lunch Orders, change 2: the names beside each dish on the 11am list.
var lunchNames = map[string]string{
	"orders/totals.py": `
"""Counting today's orders, kept free of Django so it is tested on its own."""

from collections import Counter, defaultdict


def count_by_dish(orders):
    """How many of each dish, most ordered first."""
    counts = Counter(o.dish for o in orders)
    return sorted(counts.items(), key=lambda item: (-item[1], item[0]))


def people_by_dish(orders):
    """Who ordered each dish, names in alphabetical order."""
    people = defaultdict(list)
    for o in orders:
        people[o.dish].append(o.person)
    return {dish: sorted(names) for dish, names in people.items()}
`,
	"orders/views.py": `
import datetime

from django.shortcuts import redirect, render

from .models import Order
from .totals import count_by_dish, people_by_dish

MENU = ["Chicken salad", "Veggie wrap", "Soup of the day", "Tuna sandwich"]


def pick(request):
    if request.method == "POST":
        Order.objects.create(person=request.POST["person"], dish=request.POST["dish"])
        return redirect("list")
    return render(request, "orders/pick.html", {"menu": MENU})


def order_list(request):
    orders = Order.objects.filter(day=datetime.date.today())
    people = people_by_dish(orders)
    rows = [(dish, count, people[dish]) for dish, count in count_by_dish(orders)]
    return render(request, "orders/list.html", {"rows": rows})
`,
	"orders/templates/orders/list.html": `
<!doctype html>
<title>Today's orders</title>
<script src="https://unpkg.com/htmx.org@2.0.3"></script>
<h1>Today's orders</h1>
<ul hx-get="" hx-trigger="every 30s" hx-select="ul" hx-swap="outerHTML">
  {% for dish, count, names in rows %}
  <li>{{ count }} &times; {{ dish }}: {{ names|join:", " }}</li>
  {% empty %}
  <li>No orders yet.</li>
  {% endfor %}
</ul>
`,
	"orders/tests/test_totals.py": `
import unittest
from types import SimpleNamespace as Order

from orders.totals import count_by_dish, people_by_dish


class CountByDish(unittest.TestCase):
    def test_most_ordered_first(self):
        orders = [Order(dish="Soup of the day"), Order(dish="Veggie wrap"), Order(dish="Veggie wrap")]
        self.assertEqual(count_by_dish(orders), [("Veggie wrap", 2), ("Soup of the day", 1)])

    def test_no_orders(self):
        self.assertEqual(count_by_dish([]), [])


class PeopleByDish(unittest.TestCase):
    def test_names_in_alphabetical_order(self):
        orders = [Order(dish="Veggie wrap", person="Tom"), Order(dish="Veggie wrap", person="Priya")]
        self.assertEqual(people_by_dish(orders), {"Veggie wrap": ["Priya", "Tom"]})
`,
}

// Lunch Orders, change 3: the total at the top of the list, with a
// debugging print left in, which the style check refuses.
var lunchTotal = map[string]string{
	"orders/totals.py": lunchNames["orders/totals.py"] + `

def total_price(orders, prices):
    """What today's orders cost together, in cents."""
    return sum(prices[o.dish] for o in orders)
`,
	"orders/views.py": `
import datetime

from django.shortcuts import redirect, render

from .models import Order
from .totals import count_by_dish, people_by_dish, total_price

MENU = ["Chicken salad", "Veggie wrap", "Soup of the day", "Tuna sandwich"]
PRICES = {"Chicken salad": 1250, "Veggie wrap": 1100, "Soup of the day": 900, "Tuna sandwich": 1050}


def pick(request):
    if request.method == "POST":
        Order.objects.create(person=request.POST["person"], dish=request.POST["dish"])
        return redirect("list")
    return render(request, "orders/pick.html", {"menu": MENU})


def order_list(request):
    orders = Order.objects.filter(day=datetime.date.today())
    people = people_by_dish(orders)
    rows = [(dish, count, people[dish]) for dish, count in count_by_dish(orders)]
    total = total_price(orders, PRICES)
    print("order total", total)
    return render(request, "orders/list.html", {"rows": rows, "total": total / 100})
`,
	"orders/templates/orders/list.html": `
<!doctype html>
<title>Today's orders</title>
<script src="https://unpkg.com/htmx.org@2.0.3"></script>
<h1>Today's orders</h1>
<p class="total">Total: ${{ total|floatformat:2 }}</p>
<ul hx-get="" hx-trigger="every 30s" hx-select="ul" hx-swap="outerHTML">
  {% for dish, count, names in rows %}
  <li>{{ count }} &times; {{ dish }}: {{ names|join:", " }}</li>
  {% empty %}
  <li>No orders yet.</li>
  {% endfor %}
</ul>
`,
	"orders/tests/test_total.py": `
import unittest
from types import SimpleNamespace as Order

from orders.totals import total_price


class TotalPrice(unittest.TestCase):
    def test_adds_every_order(self):
        prices = {"Veggie wrap": 1100, "Soup of the day": 900}
        orders = [Order(dish="Veggie wrap"), Order(dish="Veggie wrap"), Order(dish="Soup of the day")]
        self.assertEqual(total_price(orders, prices), 3100)

    def test_nothing_ordered_costs_nothing(self):
        self.assertEqual(total_price([], {}), 0)
`,
}

// Lunch Orders, change 4 (the chat): dishes move into the database, the
// Django admin edits them, the pick page offers the shown ones.
var lunchMenuModel = map[string]string{
	"orders/models.py": `
from django.db import models


class Dish(models.Model):
    name = models.CharField(max_length=80, unique=True)
    shown = models.BooleanField(default=True, help_text="Untick to take it off the menu without deleting it.")

    class Meta:
        ordering = ["name"]
        verbose_name_plural = "dishes"

    def __str__(self):
        return self.name


class Order(models.Model):
    person = models.CharField(max_length=80)
    dish = models.CharField(max_length=80)
    day = models.DateField(auto_now_add=True)

    def __str__(self):
        return f"{self.person}: {self.dish}"
`,
	"orders/migrations/0002_dish.py": `
from django.db import migrations, models

FIRST_MENU = ["Chicken salad", "Veggie wrap", "Soup of the day", "Tuna sandwich"]


def copy_menu(apps, schema_editor):
    Dish = apps.get_model("orders", "Dish")
    for name in FIRST_MENU:
        Dish.objects.create(name=name)


class Migration(migrations.Migration):
    dependencies = [("orders", "0001_initial")]
    operations = [
        migrations.CreateModel(
            name="Dish",
            fields=[
                ("id", models.BigAutoField(primary_key=True, serialize=False)),
                ("name", models.CharField(max_length=80, unique=True)),
                ("shown", models.BooleanField(default=True)),
            ],
            options={"ordering": ["name"], "verbose_name_plural": "dishes"},
        ),
        migrations.RunPython(copy_menu, migrations.RunPython.noop),
    ]
`,
	"orders/admin.py": `
from django.contrib import admin

from .models import Dish


@admin.register(Dish)
class DishAdmin(admin.ModelAdmin):
    list_display = ["name", "shown"]
    list_editable = ["shown"]
`,
	"orders/menu.py": `
"""What the pick page offers, kept free of Django so it is tested on its own."""


def offered(dishes):
    """The dishes shown on the menu, by name."""
    return sorted((d for d in dishes if d.shown), key=lambda d: d.name)
`,
	"orders/views.py": `
import datetime

from django.shortcuts import redirect, render

from .menu import offered
from .models import Dish, Order
from .totals import count_by_dish, people_by_dish


def pick(request):
    if request.method == "POST":
        Order.objects.create(person=request.POST["person"], dish=request.POST["dish"])
        return redirect("list")
    menu = [d.name for d in offered(Dish.objects.all())]
    return render(request, "orders/pick.html", {"menu": menu})


def order_list(request):
    orders = Order.objects.filter(day=datetime.date.today())
    people = people_by_dish(orders)
    rows = [(dish, count, people[dish]) for dish, count in count_by_dish(orders)]
    return render(request, "orders/list.html", {"rows": rows})
`,
	"orders/tests/test_menu.py": `
import unittest
from types import SimpleNamespace as Dish

from orders.menu import offered


class Offered(unittest.TestCase):
    def test_a_hidden_dish_is_not_offered(self):
        dishes = [Dish(name="Veggie wrap", shown=True), Dish(name="Tuna sandwich", shown=False)]
        self.assertEqual([d.name for d in offered(dishes)], ["Veggie wrap"])

    def test_the_menu_is_listed_by_name(self):
        dishes = [Dish(name="Soup of the day", shown=True), Dish(name="Chicken salad", shown=True)]
        self.assertEqual([d.name for d in offered(dishes)], ["Chicken salad", "Soup of the day"])
`,
}

// Club Newsletter, first version: the October issue.
var newsletterStart = map[string]string{
	"README.md": `
# Club Newsletter

The cycling club's monthly newsletter: plain HTML in public/, served as it
is. A new issue is a new page linked from the front page.

Checks: ` + "`sh checks/tests.sh`" + ` follows every link between pages; ` + "`sh checks/style.sh`" + `
wants alt text on every image.
`,
	"public/index.html": `
<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Hilltop Cycling Club</title>
<link rel="stylesheet" href="style.css">
<header><img src="images/club-logo.svg" alt="Hilltop Cycling Club"><h1>Hilltop Cycling Club</h1></header>
<main>
  <h2>Newsletters</h2>
  <ul>
    <li><a href="2026-10.html">October 2026</a></li>
  </ul>
</main>
</html>
`,
	"public/2026-10.html": `
<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>October 2026 | Hilltop Cycling Club</title>
<link rel="stylesheet" href="style.css">
<p><a href="index.html">All newsletters</a></p>
<h1>October 2026</h1>
<h2>Autumn hill climb</h2>
<p>Thirty-one riders took on Ridge Road on the 4th. Mei set a new club record of 6:42.</p>
<h2>Lights on</h2>
<p>The clocks change on the 26th. Front and rear lights on every evening ride from then on, please.</p>
</html>
`,
	"public/style.css": `
body { font: 18px/1.5 Georgia, serif; max-width: 40rem; margin: 2rem auto; padding: 0 1rem; color: #222; }
header { display: flex; align-items: center; gap: 1rem; }
header img { width: 64px; height: 64px; }
a { color: #0b5d3b; }
`,
	"public/images/club-logo.svg": `
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><circle cx="18" cy="44" r="12" fill="none" stroke="#0b5d3b" stroke-width="4"/><circle cx="46" cy="44" r="12" fill="none" stroke="#0b5d3b" stroke-width="4"/><path d="M18 44 L30 22 L46 44 M30 22 L40 22" fill="none" stroke="#0b5d3b" stroke-width="4"/></svg>
`,
	".apptown/app.yaml": `
version: 2
app:
  site:
    static: public
    public: true
`,
	"checks/tests.sh": `
#!/bin/sh
# Every link from one page to another lands on a page that exists.
exec python3 checks/links.py public
`,
	"checks/links.py": `
"""Follow every local link and image in the site; name the broken ones."""

import sys
from html.parser import HTMLParser
from pathlib import Path


class Links(HTMLParser):
    def __init__(self):
        super().__init__()
        self.targets = []

    def handle_starttag(self, tag, attrs):
        for name, value in attrs:
            if name in ("href", "src") and value and "://" not in value and not value.startswith("#"):
                self.targets.append(value)


root = Path(sys.argv[1])
pages = sorted(root.glob("*.html"))
broken = []
for page in pages:
    parser = Links()
    parser.feed(page.read_text())
    for target in parser.targets:
        if not (root / target).exists():
            broken.append(f"{page.name}: {target}")
for line in broken:
    print("broken link", line)
print(f"{len(pages)} pages, {len(broken)} broken links")
sys.exit(1 if broken else 0)
`,
	"checks/style.sh": `
#!/bin/sh
# Every image says what it shows, for readers who cannot see it.
exec python3 checks/alt.py public
`,
	"checks/alt.py": `
"""Every <img> has alt text."""

import sys
from html.parser import HTMLParser
from pathlib import Path


class Images(HTMLParser):
    def __init__(self):
        super().__init__()
        self.missing = 0
        self.images = 0

    def handle_starttag(self, tag, attrs):
        if tag == "img":
            self.images += 1
            if not dict(attrs).get("alt"):
                self.missing += 1


images = missing = 0
for page in sorted(Path(sys.argv[1]).glob("*.html")):
    parser = Images()
    parser.feed(page.read_text())
    if parser.missing:
        print(f"{page.name}: {parser.missing} image(s) without alt text")
    images += parser.images
    missing += parser.missing
print(f"{images} images, {missing} without alt text")
sys.exit(1 if missing else 0)
`,
}

// Club Newsletter, change 2: the ride calendar.
var newsletterRides = map[string]string{
	"public/index.html": `
<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Hilltop Cycling Club</title>
<link rel="stylesheet" href="style.css">
<header><img src="images/club-logo.svg" alt="Hilltop Cycling Club"><h1>Hilltop Cycling Club</h1></header>
<main>
  <p><a href="rides.html">Ride calendar</a></p>
  <h2>Newsletters</h2>
  <ul>
    <li><a href="2026-10.html">October 2026</a></li>
  </ul>
</main>
</html>
`,
	"public/rides.html": `
<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Ride calendar | Hilltop Cycling Club</title>
<link rel="stylesheet" href="style.css">
<p><a href="index.html">Home</a></p>
<h1>Ride calendar</h1>
<table>
  <tr><th>When</th><th>Ride</th><th>Pace</th></tr>
  <tr><td>Saturdays 8:00</td><td>Reservoir loop, 45 km</td><td>Steady</td></tr>
  <tr><td>Sundays 9:00</td><td>Café ride, 30 km</td><td>Easy, no one dropped</td></tr>
  <tr><td>Wednesdays 18:00</td><td>Ridge Road repeats</td><td>Hard</td></tr>
</table>
</html>
`,
}

// Club Newsletter, change 3: the November issue, waiting for approval.
var newsletterNovember = map[string]string{
	"public/index.html": `
<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Hilltop Cycling Club</title>
<link rel="stylesheet" href="style.css">
<header><img src="images/club-logo.svg" alt="Hilltop Cycling Club"><h1>Hilltop Cycling Club</h1></header>
<main>
  <p><a href="rides.html">Ride calendar</a></p>
  <h2>Newsletters</h2>
  <ul>
    <li><a href="2026-11.html">November 2026</a></li>
    <li><a href="2026-10.html">October 2026</a></li>
  </ul>
</main>
</html>
`,
	"public/2026-11.html": `
<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>November 2026 | Hilltop Cycling Club</title>
<link rel="stylesheet" href="style.css">
<p><a href="index.html">All newsletters</a></p>
<h1>November 2026</h1>
<h2>Winter rides</h2>
<p>From the 1st, Saturday rides start at 9:00 instead of 8:00. Mudguards, please: the riders behind you will thank you.</p>
<h2>End-of-season dinner</h2>
<p>Friday the 28th at the Red Lion. Tell Tom by the 21st if you are coming.</p>
</html>
`,
}

// Rocket Garden, first version: grow rockets, launch them.
var rocketStart = map[string]string{
	"README.md": `
# Rocket Garden

A little browser game: plant a rocket, let it grow fuel, launch it, and see
how high it goes. Plain HTML, CSS and JavaScript; open index.html.

Checks: ` + "`sh checks/tests.sh`" + ` runs the rules' tests with node; ` + "`sh checks/style.sh`" + `
refuses console.log left in the game.
`,
	"index.html": `
<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Rocket Garden</title>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Fredoka:wght@600&display=swap">
<link rel="stylesheet" href="style.css">
<h1>Rocket Garden</h1>
<p id="fuel">Fuel: 0</p>
<button id="launch">Launch!</button>
<p id="height"></p>
<script type="module" src="game.js"></script>
</html>
`,
	"style.css": `
body { font: 20px system-ui, sans-serif; text-align: center; background: #0e1630; color: #f4f1e8; }
h1 { font-family: Fredoka, system-ui, sans-serif; }
button { font-size: 1.5rem; padding: .5rem 1.5rem; border-radius: 999px; }
`,
	"rules.js": `
// The rules of Rocket Garden, kept apart from the drawing so they can be tested.
export const MAX_FUEL = 10;

export function grow(rocket) {
  return { ...rocket, fuel: Math.min(rocket.fuel + 1, MAX_FUEL) };
}

export function launchHeight(rocket) {
  if (rocket.fuel <= 0) return 0;
  return rocket.fuel * 120 + rocket.fins * 40;
}
`,
	"game.js": `
import { grow, launchHeight } from "./rules.js";

let rocket = { fuel: 0, fins: 3 };
const fuel = document.getElementById("fuel");
const height = document.getElementById("height");

setInterval(() => {
  rocket = grow(rocket);
  fuel.textContent = "Fuel: " + rocket.fuel;
}, 1000);

document.getElementById("launch").addEventListener("click", () => {
  height.textContent = "Up " + launchHeight(rocket) + " m!";
  rocket = { ...rocket, fuel: 0 };
});
`,
	"tests/rules.test.js": `
import { test } from "node:test";
import assert from "node:assert/strict";
import { grow, launchHeight, MAX_FUEL } from "../rules.js";

test("a rocket grows one fuel a tick, up to the most it holds", () => {
  assert.equal(grow({ fuel: 0, fins: 3 }).fuel, 1);
  assert.equal(grow({ fuel: MAX_FUEL, fins: 3 }).fuel, MAX_FUEL);
});

test("a rocket with no fuel does not leave the ground", () => {
  assert.equal(launchHeight({ fuel: 0, fins: 3 }), 0);
});

test("more fuel goes higher", () => {
  assert.ok(launchHeight({ fuel: 5, fins: 3 }) > launchHeight({ fuel: 2, fins: 3 }));
});
`,
	".apptown/app.yaml": `
version: 2
app:
  game:
    static: .
    public: true
`,
	"checks/tests.sh": `
#!/bin/sh
# The rules' tests, with node's own test runner.
exec node --test "tests/*.test.js"
`,
	"checks/style.sh": `
#!/bin/sh
# Debugging output does not ship: no console.log in the game.
if grep -n 'console.log' *.js; then
  echo "style: remove the console.log calls above"
  exit 1
fi
echo "style: no console.log calls"
`,
}

// Rocket Garden, change 2: the best height survives a reload.
var rocketBest = map[string]string{
	"rules.js": rocketStart["rules.js"] + `
export function best(previous, height) {
  return Math.max(previous || 0, height);
}
`,
	"index.html": `
<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Rocket Garden</title>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Fredoka:wght@600&display=swap">
<link rel="stylesheet" href="style.css">
<h1>Rocket Garden</h1>
<p id="fuel">Fuel: 0</p>
<button id="launch">Launch!</button>
<p id="height"></p>
<p id="best"></p>
<script type="module" src="game.js"></script>
</html>
`,
	"game.js": `
import { best, grow, launchHeight } from "./rules.js";

let rocket = { fuel: 0, fins: 3 };
let record = Number(localStorage.getItem("best")) || 0;
const fuel = document.getElementById("fuel");
const height = document.getElementById("height");
const shownBest = document.getElementById("best");
shownBest.textContent = record ? "Best: " + record + " m" : "";

setInterval(() => {
  rocket = grow(rocket);
  fuel.textContent = "Fuel: " + rocket.fuel;
}, 1000);

document.getElementById("launch").addEventListener("click", () => {
  const up = launchHeight(rocket);
  height.textContent = "Up " + up + " m!";
  record = best(record, up);
  localStorage.setItem("best", String(record));
  shownBest.textContent = "Best: " + record + " m";
  rocket = { ...rocket, fuel: 0 };
});
`,
	"tests/best.test.js": `
import { test } from "node:test";
import assert from "node:assert/strict";
import { best } from "../rules.js";

test("the best height is the higher of the old best and this launch", () => {
  assert.equal(best(800, 1200), 1200);
  assert.equal(best(1200, 800), 1200);
});

test("a first launch is the best so far", () => {
  assert.equal(best(null, 640), 640);
});
`,
}

// Rocket Garden, change 3: a countdown that retains the no-fuel rule.
var rocketCountdown = map[string]string{
	"rules.js": `
// The rules of Rocket Garden, kept apart from the drawing so they can be tested.
export const MAX_FUEL = 10;

export function grow(rocket) {
  return { ...rocket, fuel: Math.min(rocket.fuel + 1, MAX_FUEL) };
}

export function launchHeight({ fuel, fins }) {
  return fuel === 0 ? 0 : fuel * 120 + fins * 40;
}

export function best(previous, height) {
  return Math.max(previous || 0, height);
}

export function countdown(from) {
  return Array.from({ length: from }, (_, i) => from - i);
}
`,
	"tests/countdown.test.js": `
import { test } from "node:test";
import assert from "node:assert/strict";
import { countdown } from "../rules.js";

test("the countdown goes 3, 2, 1", () => {
  assert.deepEqual(countdown(3), [3, 2, 1]);
});
`,
}

// serveSite photographs committed example files, without claiming a deployment.
func (s *tourScene) serveSite(c *tourChange) string {
	s.t.Helper()
	dest := s.t.TempDir()
	err := filepath.WalkDir(filepath.Join(c.folder, "public"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(c.folder, path)
		if err != nil {
			return err
		}
		// These are fixed fixture filenames, never user-controlled shell arguments.
		if !regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`).MatchString(rel) {
			s.t.Fatalf("unsafe fixture path %q", rel)
		}
		content, code := s.run(c, "git show HEAD:"+rel)
		if code != 0 {
			s.t.Fatalf("read committed example %s: %s", rel, content)
		}
		target := filepath.Join(dest, strings.TrimPrefix(rel, "public/"))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, []byte(content), 0644)
	})
	if err != nil {
		s.t.Fatal(err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(dest)))
	s.t.Cleanup(server.Close)
	return server.URL + "/"
}

type tourMetadata struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
	Source  string `json:"source"`
}

func tourIdentity(t *testing.T) tourMetadata {
	t.Helper()
	m := tourMetadata{Version: os.Getenv("TOUR_VERSION"), Source: os.Getenv("TOUR_SOURCE")}
	if m.Source != "" {
		root, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"git", "rev-parse", "HEAD"}, {"git", "ls-files", "--others", "--exclude-standard"}, {"git", "diff", "--quiet", "HEAD", "--", ".", ":(exclude)site/tour/index.html"}} {
			out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{Workspace: "../..", Argv: tourSourceCommand(root, args), Env: []string{"PATH=" + os.Getenv("PATH")}, Timeout: 10 * time.Second})
			if !out.Exited0() || (args[1] == "rev-parse" && strings.TrimSpace(string(out.Output)) != m.Source) || (args[1] == "ls-files" && len(out.Output) != 0) {
				t.Fatalf("published tour requires the exact clean source %s: %v %s", m.Source, out.RunErr, out.Output)
			}
		}
	}
	if m.Version == "" {
		m.Version = "Unreleased preview"
	}
	m.Kind = "release"
	if m.Version == "Unreleased preview" {
		m.Kind = "preview"
	}
	if m.Source == "" {
		m.Source = "local working tree"
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("../../site/tour/metadata.json", append(b, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	return m
}

// The release container runs as root against the runner-owned checkout.
// Trust only this absolute checkout for these read-only source checks.
func tourSourceCommand(root string, args []string) []string {
	return append([]string{"git", "-c", "safe.directory=" + root}, args[1:]...)
}

func TestTourSourceChecksTrustOnlyTheirCheckout(t *testing.T) {
	root := t.TempDir()
	gittest.Run(t, root, "init")
	gittest.RunAsTester(t, root, "commit", "--allow-empty", "-m", "fixture")
	for _, args := range [][]string{{"git", "rev-parse", "HEAD"}, {"git", "ls-files", "--others", "--exclude-standard"}, {"git", "diff", "--quiet", "HEAD", "--", ".", ":(exclude)site/tour/index.html"}} {
		run := func(argv []string) execution.Outcome {
			return (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Workspace: root, Argv: argv, Env: []string{"PATH=" + os.Getenv("PATH"), "GIT_TEST_ASSUME_DIFFERENT_OWNER=1"}, Timeout: 10 * time.Second})
		}
		refused := run(args)
		if refused.Exited0() || (args[1] == "rev-parse" && !strings.Contains(string(refused.Output), "dubious ownership")) {
			t.Fatalf("ownership probe did not refuse %v: %s", args, refused.Output)
		}
		allowed := run(tourSourceCommand(root, args))
		if !allowed.Exited0() {
			t.Fatalf("explicit checkout trust refused %v: %s", args, allowed.Output)
		}
	}
}

var newsletterNeon = map[string]string{
	"public/index.html": `<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Hilltop Cycling Club — Full colour. Full pedal.</title><link rel="stylesheet" href="style.css">
<body><div class="ticker">HILLTOP CYCLING CLUB ★ EVERY BIKE WELCOME ★ NO ONE LEFT BEHIND ★</div>
<header><img src="images/club-logo.svg" alt="Hilltop Cycling Club"><span>EST. 1998<br>STILL GOING UPHILL</span><a href="rides.html">Find your next ride ↗</a></header>
<main><p class="eyebrow">YOUR LEGS CALLED. THEY WANT A WEEKEND.</p><h1>LESS SCROLL.<br><span>MORE ROLL.</span></h1>
<div class="badge">100%<br>POWERED<br>BY SNACKS</div>
<p class="intro">Good people. Questionable hills. Excellent cake.<br>Come for a ride. Stay for the café stop.</p>
<div class="cards"><a class="ride" href="rides.html"><small>MEET YOUR PEOPLE</small><h2>LET'S<br>RIDE ↗</h2><p>Times, routes &amp; a pace that suits you.</p></a>
<a class="news" href="2026-10.html"><small>HOT OFF THE HANDLEBARS</small><h2>CLUB<br>NEWS ↗</h2><p>October's hill climb. Lights on. All the gossip.</p></a></div>
</main><footer>BIG COLOURS. SMALL CLUB. REALLY GOOD RIDES. ★</footer></body></html>`,
	"public/style.css": `* { box-sizing:border-box } body { margin:0; background:#171039; color:#fff; font:18px/1.5 Arial,sans-serif }
 a { color:inherit } .ticker { background:#c6ff00; color:#171039; text-align:center; padding:10px; font-weight:900; letter-spacing:.15em }
 header { display:flex; align-items:center; gap:18px; padding:22px 7%; border-bottom:2px solid #ffffff33; font-weight:800; font-size:14px } header img { background:#c6ff00; border-radius:50%; padding:5px; width:58px; height:58px } header a { margin-left:auto; font-size:17px }
 main { max-width:1160px; margin:auto; padding:30px 28px; position:relative } .eyebrow { color:#ff88e8; font-size:14px; font-weight:900; letter-spacing:.15em }
 h1 { font-size:clamp(52px,7.2vw,104px); line-height:.94; letter-spacing:-.055em; margin:20px 0; font-weight:1000 } h1 span { color:#c6ff00; text-shadow:5px 5px 0 #a526db }
 .intro { color:#ece7ff; font-size:21px } .badge { position:absolute; right:45px; top:65px; transform:rotate(12deg); border-radius:50%; width:164px; height:164px; padding:35px 15px; text-align:center; background:#ff80df; color:#241140; font-weight:900; line-height:1.3; border:7px double #241140 }
 .cards { display:grid; grid-template-columns:1fr 1fr; gap:20px; margin-top:25px } .cards a { text-decoration:none; color:#171039; padding:22px 26px; border:3px solid #fff; box-shadow:7px 7px 0 #000; } .ride { background:#c6ff00; transform:rotate(-1deg) } .news { background:#ff80df; transform:rotate(1deg) }
 h2 { font-size:36px; line-height:1; margin:12px 0 } small { font-weight:900; letter-spacing:.08em } .cards p { margin-bottom:0 } footer { text-align:center; font-weight:900; padding:24px; margin-top:20px; border-top:12px solid transparent; border-image:repeating-linear-gradient(90deg,#fff 0 16px,#171039 16px 32px) 12 }
 a:focus-visible { outline:4px solid #00dfff; outline-offset:6px } body>h1,body>h2,body>p,body>ul { max-width:50rem; margin:24px auto; padding:0 20px }
 @media(max-width:700px) { .badge{position:static; width:auto;height:auto;border-radius:0;transform:none;padding:12px}.cards{grid-template-columns:1fr}header{padding:14px} }`,
}
