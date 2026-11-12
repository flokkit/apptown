//go:build browser

package apptown

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/evals"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/deploy/s3bucket/s3fake"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/execution"
	testinputs "github.com/flokkit/apptown/internal/execution/executiontest"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/openrouter/openrouterfake"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/version"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivertype"
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
	if err := a.Integrations.Attachments.Attach(t.Context(), pid, integration.CapErrors, "glitchtip", fmt.Sprintf(`{"dsn":"https://browserkey@%s/7"}`, reportingHost), "fixture", nil); err != nil {
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
	if err := a.DB.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM projects WHERE slug IN ('csrf-created','file-executed')`).Scan(&unexpected); err != nil || unexpected != 0 {
		t.Fatalf("browser attack mutated state: %d %v", unexpected, err)
	}
	if b := agent.Input(); b != "legitimate-terminal-input" {
		// Neither the key typed before Take control nor the other origin's
		// key reaches the agent.
		t.Fatalf("terminal input was not restricted to the controlling terminal on AppTown's origin: %q", b)
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
// the projects' own scripts, run for real on this host (tourScript, and
// the local shell for the conformance review's checks: the tour has no
// Docker; checks that need a tool's image or a model say they could not
// run). The chats are recorded transcripts on the console's fake agent,
// so no model is called; the commands they quote ran in the change's
// folder. The garage's extra fieldnotes case imports recorded outcomes,
// labelled as such, with a local preview stand-in. Not part of any gate:
// make tour runs it.
//
// The tour is the healthy AppTown, and tells one change's story: the neon
// makeover, checked, with its preview running and waiting for approval
// (tourScene.story); then Priya accepts it and its release puts it live,
// and then Sam deploys the earlier release again, the camera coming back
// after each. Its executor's image is present, its apps are
// running, every container the deployments recorded is on the host, and
// its disk, memory and CPU readings are fixed, roomy ones rather than this
// machine's. TestGarage is the same AppTown with one example of each
// trouble.
func TestTour(t *testing.T) {
	w := newTourWorld(t, false, 0)
	stops := readTourStops(t, filepath.Join(w.tourDir, "tour.yaml"))
	if w.hold() {
		return
	}
	w.server.Config.Handler = w.a.Handler()
	w.server.StartTLS()
	a, password, tourDir, made := w.a, w.password, w.tourDir, w.made

	// The stops, with the ids the scene made. The camera comes three times:
	// for the story up to Priya's decision; after she accepts the change
	// through the product and its release puts it live, for the stops
	// marked after: accept; and after the earlier release is deployed
	// again, for those marked after: rollback.
	vars := map[string]string{"{before}": made.before, "{after}": made.after, "{story}": made.story.String(), "{storySession}": made.storySession, "{preview}": made.preview, "{chat}": made.chat.String(), "{session}": made.session}
	t.Logf("the story's change: /changes/%s, its preview %s", made.story, made.preview)
	type shot struct {
		ID    string `json:"id"`
		Path  string `json:"path"`
		Wait  string `json:"wait,omitempty"`
		Click string `json:"click,omitempty"`
		Happy bool   `json:"happy,omitempty"`
	}
	module := os.Getenv("APPTOWN_PLAYWRIGHT_MODULE")
	if module == "" {
		home, _ := os.UserHomeDir()
		module = filepath.Join(home, ".local/share/forge-nightly/playwright/node_modules/playwright/index.mjs")
	}
	shoot := func(after string) {
		t.Helper()
		var shots []shot
		for _, s := range stops {
			if s.After != after {
				continue
			}
			p := s.Path
			for k, v := range vars {
				p = strings.ReplaceAll(p, k, v)
			}
			if strings.Contains(p, "{") {
				t.Fatalf("stop %s names a value the tour does not have: %s", s.ID, p)
			}
			shots = append(shots, shot{ID: s.ID, Path: p, Wait: s.Wait, Click: s.Click, Happy: s.Happy})
		}
		if len(shots) == 0 {
			return
		}
		shotsJSON, _ := json.Marshal(shots)
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
	}
	w.settle()
	shoot("")
	// Priya accepts the makeover as the Decide dialog does; its landing
	// merges it and main's release deploys it on the scene's host (approve).
	// The camera comes back within the deploy page's ten minutes
	// (DeploymentDeploys), so the release still shows its stages.
	w.scene.approve(made.storyRun, "priya")
	if ch, err := a.Work.Change(asPerson("sam"), made.story); err != nil || ch.State.String() != "shipped" {
		t.Fatalf("the makeover did not ship: %+v %v", ch, err)
	}
	vars["{live}"] = w.scene.mainURL("club-newsletter")
	// AppTown's rounds read the host again after the deploy, as they did
	// before the first visit.
	w.settle()
	shoot("accept")
	// Members find it a bit much: Sam, who may release (Priya approves
	// landings but may not), deploys the release before the makeover again,
	// as the deployment's Deploy this again does.
	vars["{rollback}"] = fmt.Sprint(w.scene.redeploy("sam", "club-newsletter", "main", 2))
	w.settle()
	shoot("rollback")
	writeTourPage(t, tourDir, stops)
}

// Both scenes run the host reader loops over their disposable daemon.
func TestHostSnapshotScenesReadInventory(t *testing.T) {
	for _, garage := range []bool{false, true} {
		t.Run(fmt.Sprint(garage), func(t *testing.T) {
			if !garage {
				t.Setenv("APPTOWN_DESIGN_HOLD", "127.0.0.1:0")
			}
			w := newTourWorld(t, garage, 10*time.Millisecond)
			if garage {
				assertFieldnotesDecision(t, w.scene)
				assertGarageProgress(t, w.scene)
				w.observe()
				assertGarageProgress(t, w.scene)
			}
			targets, err := w.a.targets.All(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range targets {
				if target.Kind != deployment.KindDockerHost || target.Status == deployment.TargetRetired {
					continue
				}
				inv, err := w.a.Deployment.InventoryFor(t.Context(), target)
				if target.Name == "tour-unreachable" {
					if err != nil || !inv.Unknown {
						t.Fatalf("offline scene host inventory: %+v %v", inv, err)
					}
					continue
				}
				if err != nil || inv.Unknown || inv.Invalidated {
					t.Fatalf("scene inventory unknown: %+v %v", inv, err)
				}
			}
			if !garage {
				target, err := w.a.targets.ByName(t.Context(), "built-in-docker")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.a.hostSnapshots.BeforeAct(t.Context(), target.ID); err != nil {
					t.Fatal(err)
				}
				var last string
				if !poll.Until(t.Context(), 25*time.Second, 10*time.Millisecond, func() bool {
					inv, err := w.a.Deployment.InventoryFor(t.Context(), target)
					last = fmt.Sprintf("%+v %v", inv, err)
					return err == nil && !inv.Invalidated && !inv.Unknown
				}) {
					t.Fatalf("held tour never completed a lap after an act: %s", last)
				}
			}
		})
	}
}

// TestGarage is the tour's AppTown with deliberate trouble, one example of
// each kind the UI has a screen for, so problem screens can be looked at
// (APPTOWN_DESIGN_HOLD, as for the tour): the executor's image is missing;
// a scanner Sam added names an image that does not exist; Lunch Orders'
// second landing's deploy failed (the host would not start its container),
// and it has a stopped, an unknown and an unreachable deployment beside
// main, and its contract does not say it is built with Python (a
// confirmed app-check problem); one container a deployment recorded is
// gone from the host; the host's disk and memory cannot be read (its
// capacity check fails); the data folder's disk is low; gitlab and
// infisical are not set up (as in the tour). Three additional changes
// hold a landing, agent work with a starting preview, and a release in
// progress. Their paths are logged at scene start. Not part of any gate.
func TestGarage(t *testing.T) {
	w := newTourWorld(t, true, 0)
	if w.hold() {
		return
	}
	// Each kind of trouble is on the attention list: the garage is only
	// useful while it shows them.
	var view struct {
		Items []struct {
			Key, Title string
			Unknown    bool
		}
	}
	if err := json.Unmarshal(w.scene.call("sam", "GET", "/api/v1/attention", nil), &view); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"integrations/docker": 0, "integrations/semgrep-strict": 0, "disks/": 0, "capacity/": 0, "checks/": 0, "inventory/": 0,
		"tour-stopped": 0, "tour-unknown": 0, "tour-unreachable": 0, "main: Release failed": 0}
	for _, item := range view.Items {
		// App checks that could not run (the scenes call no model) are not
		// known, not the garage's confirmed check problem; only that counts.
		if strings.HasPrefix(item.Key, "checks/") && item.Unknown {
			continue
		}
		for k := range want {
			if strings.HasPrefix(item.Key, k) || strings.Contains(item.Title, k) {
				want[k]++
			}
		}
	}
	for k, n := range want {
		expected := 1
		if k == "inventory/" {
			expected = 2 // main's refused replacement and tour-unknown's database
		}
		if n != expected {
			t.Errorf("the garage shows %d of %q on the attention list, want %d", n, k, expected)
		}
	}
	// The other runtime examples exist on the host. Only the refused
	// replacement and the deliberately removed database are missing.
	target, err := w.a.targets.ByName(t.Context(), "built-in-docker")
	if err != nil {
		t.Fatal(err)
	}
	reading, err := w.a.Deployment.InventoryFor(t.Context(), target)
	if err != nil || reading.Unknown || reading.Invalidated {
		t.Fatalf("garage inventory: %+v %v", reading, err)
	}
	var missing []string
	for _, resource := range reading.Missing {
		for _, host := range resource.Hosts {
			missing = append(missing, resource.Kind+"/"+host.Project+"/"+host.Deployment+"/"+host.Need)
		}
	}
	slices.Sort(missing)
	if !slices.Equal(missing, []string{"container/lunch-orders/main/", "container/lunch-orders/tour-unknown/postgres"}) {
		t.Fatalf("garage missing resources: %v", missing)
	}
}

// tourWorld is the tour's AppTown after its two weeks, observed and ready
// to photograph or hold open.
type tourWorld struct {
	t        *testing.T
	a        *App
	scene    *tourScene
	server   *httptest.Server
	made     tourIDs
	tourDir  string
	password string
	vite     string
	held     bool
}

// tourAgentImage is the executor's image: present on the tour's Docker,
// missing in the garage's.
const tourAgentImage = "apptown-agent"

// garageScannerImage is the garage's misconfigured scanner's image: a
// Semgrep version that does not exist.
const garageScannerImage = "semgrep/semgrep:1.999.0"

// A missing container or unreadable generation must leave its neighbours readable.
func TestSceneDockerInspectsEveryContainer(t *testing.T) {
	daemon, _, onHost, _ := newSceneDocker(t, true)
	resources := []deploy.Resource{}
	for _, state := range []string{"running", "stopped", "unknown"} {
		resources = append(resources, deploy.Resource{Kind: "container", Name: state, Hosts: []deploy.ResourceHost{{Deployment: "tour-" + state, Generation: 1}}})
	}
	onHost.Store(&resources)
	// Every command to the separate offline host fails, regardless of names.
	for _, args := range [][]string{{"info"}, {"inspect", "--format", docker.RuntimeFormat, "running"}} {
		_, err := daemon.Run(t.Context(), "ssh://apptown-target-tour-unreachable-test", docker.Command{Args: args})
		if !deploy.IsUnreachable(err) {
			t.Fatalf("offline host %v: %v", args, err)
		}
	}
	for _, names := range [][]string{{"running", "stopped", "unknown", "missing", "also-missing"}, {"missing", "also-missing", "unknown", "stopped", "running"}} {
		var stdout, stderr bytes.Buffer
		args := append([]string{"inspect", "--format", `{{json .Name}}` + "\t" + docker.RuntimeFormat}, names...)
		_, err := daemon.Run(t.Context(), "", docker.Command{Args: args, Stdout: &stdout, Stderr: &stderr})
		if err == nil || stderr.String() != "Error: No such object: missing\nError: No such object: also-missing\n" {
			t.Fatalf("missing container: stderr=%q err=%v", stderr.String(), err)
		}
		rows := map[string]docker.Runtime{}
		for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
			nameJSON, raw, ok := strings.Cut(line, "\t")
			var name string
			var runtime docker.Runtime
			if !ok || json.Unmarshal([]byte(nameJSON), &name) != nil || json.Unmarshal([]byte(raw), &runtime) != nil {
				t.Fatalf("invalid runtime row %q", line)
			}
			rows[name] = runtime
		}
		if len(rows) != 3 || !rows["/running"].Running || rows["/stopped"].Running || rows["/stopped"].ID == "" || rows["/running"].Labels[dockerlabel.Generation] != "1" || rows["/unknown"].Labels[dockerlabel.Generation] != "unreadable" {
			t.Fatalf("runtime rows: %+v", rows)
		}
	}
}

// The troubled tour deployments retain distinct states under host observations.
func TestSceneDockerObservesTourDeployments(t *testing.T) {
	daemon, _, onHost, _ := newSceneDocker(t, true)
	dataDir, err := os.MkdirTemp("", "s-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	a, h := internalApp(t, Config{DataDir: dataDir, DockerClient: daemon})
	a.SeedTargets(t.Context())
	boot := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "sam", "tour-password-not-secret"))
	if boot.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", boot.Code, boot.Body.String())
	}
	projectID, _, _ := fleetRepo(t, a, "lunch-orders", map[string]string{"index.html": "<h1>Lunch</h1>"})
	resources := tourRuntimeDeployments(t, a, []string{"stopped", "unknown", "unreachable"})
	onHost.Store(&resources)
	if err := a.Deployment.ObserveRuntime(asPerson("sam")); err != nil {
		t.Fatal(err)
	}
	rows, err := a.Deployment.List(asPerson("sam"), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("tour deployments: %+v", rows)
	}
	for _, row := range rows {
		if want := strings.TrimPrefix(row.Name, "tour-"); row.Observation.State.String() != want {
			t.Errorf("%s observation: %+v", row.Name, row.Observation)
		}
		if (row.Name == "tour-unreachable") != (row.Target.Name == "tour-unreachable") {
			t.Errorf("%s target: %s", row.Name, row.Target.Name)
		}
	}
}

// newSceneDocker supplies the tour and camera with the same reachable runtime.
func newSceneDocker(t *testing.T, garage bool) (*fakeDocker, int, *atomic.Pointer[[]deploy.Resource], *atomic.Bool) {
	t.Helper()
	// The tour's apps run on no real Docker: the fake answers AppTown's
	// commands as a working host would. (The landings' source scans, which
	// run where AppTown's checks run, use this machine's Docker for real.) It has the executor's image (not in the garage),
	// holds an image once a build or pull made it, and starts what it is
	// asked to run, except the run the garage refuses (refuseRun). Every
	// container it started answers on one front door: the scene's app
	// server, which is up and says the release a static part was told. A
	// main deployment of a static site answers there for its own address
	// (<project>.apps.localhost) with the files its release packed.
	// The garage's extra deployments, seeded below, are on the host as
	// recorded (onHost).
	onHost := new(atomic.Pointer[[]deploy.Resource])
	refuseRun := new(atomic.Bool)
	// listening is each started container's server on its published port.
	// packed is the files the last static site build packed (the folder
	// the file server's image copies to /srv), until a release runs it;
	// released is each release identity's files (part@commit), so a
	// release deployed again serves its own; front is each project's main
	// deployment that serves a site, by project.
	listening := struct {
		sync.Mutex
		on       map[string]*http.Server
		port     map[string]int
		packed   string
		released map[string]string
		front    map[string]http.Handler
	}{on: map[string]*http.Server{}, port: map[string]int{}, released: map[string]string{}, front: map[string]http.Handler{}}
	t.Cleanup(func() {
		listening.Lock()
		defer listening.Unlock()
		for _, srv := range listening.on {
			_ = srv.Close()
		}
	})
	images := map[string]bool{}
	var imagesMu sync.Mutex
	daemon := newFakeDocker()
	told := &atomic.Pointer[string]{}
	// answer is what a started container serves: a page saying what it is
	// (or, for a static site's mounted folder, site, that folder's files),
	// and the release a static part was told (release) at ReleasePath.
	answer := func(release func() string, site string) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if r.URL.Path == appcontract.ReleasePath {
				_, _ = rw.Write([]byte(release()))
				return
			}
			if site != "" {
				// AppTown's file server configuration answers the page's
				// browser values (docker.StaticServerCommand); this site is
				// given none.
				if r.URL.Path == "/_apptown/env.js" {
					rw.Header().Set("Content-Type", "application/javascript; charset=utf-8")
					_, _ = rw.Write([]byte(docker.BrowserEnvScript("{}")))
					return
				}
				http.FileServer(http.Dir(site)).ServeHTTP(rw, r)
				return
			}
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprintf(rw, "<!doctype html><title>%s</title><p>%s runs on the UI tour's stand-in Docker host, which answers for its container here.</p>", html.EscapeString(r.Host), html.EscapeString(r.Host))
		})
	}
	// The apps' front door (the main tier domain, below) is one server for
	// every app; it says the release a static part was told last.
	door := answer(func() string {
		if v := told.Load(); v != nil {
			return *v
		}
		return ""
	}, "")
	app := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		project, _, _ := strings.Cut(r.Host, ".") // <project>.apps.<domain>
		listening.Lock()
		h := listening.front[project]
		listening.Unlock()
		if h == nil {
			h = door
		}
		h.ServeHTTP(rw, r)
	}))
	t.Cleanup(app.Close)
	appPort := app.Listener.Addr().(*net.TCPAddr).Port
	// The apps' front door (the main tier domain, below) is that port on
	// *.localhost, which may resolve to ::1 first.
	if l, err := net.Listen("tcp", fmt.Sprintf("[::1]:%d", appPort)); err == nil {
		v6 := &http.Server{Handler: app.Config.Handler}
		go func() { _ = v6.Serve(l) }()
		t.Cleanup(func() { _ = v6.Close() })
	}
	daemon.port, daemon.told = appPort, told
	held := func(ref string) bool {
		imagesMu.Lock()
		defer imagesMu.Unlock()
		if images[ref] {
			return true
		}
		for image := range images {
			if ref == dockerfake.ImageID(image) { // the ID the fake gives the image
				return true
			}
		}
		return false
	}
	seededRuntime := func(ref string) string {
		if seeded := onHost.Load(); seeded != nil {
			for _, resource := range *seeded {
				if resource.Kind == "container" && resource.Name == ref {
					for _, host := range resource.Hosts {
						if host.Need == "" && strings.HasPrefix(host.Deployment, "tour-") {
							return strings.TrimPrefix(host.Deployment, "tour-")
						}
					}
				}
			}
		}
		return ""
	}
	// Stream runtime rows and inventory facts as a daemon would.
	daemon.command = func(_ context.Context, host string, req docker.Command) ([]byte, error) {
		if strings.HasPrefix(host, "ssh://apptown-target-tour-unreachable-") {
			return nil, deploy.Unreachable(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("tour host offline")})
		}
		if len(req.Args) >= 4 && req.Args[0] == "inspect" && req.Args[1] == "--format" && req.Args[2] == `{{json .Name}}`+"\t"+docker.RuntimeFormat {
			var result error
			for _, name := range req.Args[3:] {
				out, err := daemon.run("inspect", "--format", docker.RuntimeFormat, name)
				if err != nil {
					_, writeErr := fmt.Fprintln(req.Stderr, strings.TrimSpace(string(out)))
					result = errors.Join(result, err, writeErr)
					continue
				}
				_, err = fmt.Fprintf(req.Stdout, "%q\t%s\n", "/"+name, out)
				result = errors.Join(result, err)
			}
			return nil, result
		}
		const imageNames = "{{json .RepoTags}}\t{{json .RepoDigests}}\t{{json .Id}}"
		if len(req.Args) >= 6 && slices.Equal(req.Args[:5], []string{"inspect", "--type", "container", "--format", "{{json .}}"}) {
			for _, ref := range req.Args[5:] {
				out, err := daemon.run(append(req.Args[:5:5], ref)...)
				if err != nil {
					found := false
					if seeded := onHost.Load(); seeded != nil {
						for _, r := range *seeded {
							if r.Kind == "container" && r.Name == ref {
								c := docker.HostContainer{ID: dockerfake.ContainerID(ref), Name: "/" + ref}
								c.State.Running = seededRuntime(ref) != "stopped"
								c.State.Status = "running"
								if !c.State.Running {
									c.State.Status = "exited"
								}
								out, _ = json.Marshal(c)
								found = true
								break
							}
						}
					}
					if !found {
						return nil, err
					}
				}
				if _, err := fmt.Fprintln(req.Stdout, string(out)); err != nil {
					return nil, err
				}
			}
			return nil, nil
		}
		if req.Stdin == nil && req.Stdout == nil {
			return daemon.run(req.Args...)
		}
		args := req.Args
		// A deployment's backup streams each copy (pg_dump of a database, tar of
		// a volume's files) through a script that ends with the stream's size
		// and checksum. The stand-in host runs no processes, so it answers with
		// a stand-in copy and that line, and a release's backup before
		// deploying succeeds as it would.
		if len(args) > 2 && req.Stdin == nil && strings.Contains(args[len(args)-1], "APPTOWN-STREAM-END") {
			copied := []byte("-- the tour's stand-in database dump\n")
			if strings.Contains(args[len(args)-1], "'tar' 'cz'") {
				copied = standInArchive(t)
			}
			if _, err := fmt.Fprintf(req.Stdout, "%sAPPTOWN-STREAM-END %020d %x\n", copied, len(copied), sha256.Sum256(copied)); err != nil {
				return nil, err
			}
			return nil, nil
		}
		if len(args) < 5 || args[0] != "image" || args[1] != "inspect" || args[3] != imageNames || req.Stdin != nil {
			// What a container's processes print (a database's dump among
			// them) the stand-in host cannot give: it runs no processes.
			return nil, errors.New("the tour's stand-in Docker host runs no processes, so it has nothing of theirs to stream")
		}
		var missing error
		for _, ref := range args[4:] {
			if !held(ref) {
				if req.Stderr != nil {
					fmt.Fprintf(req.Stderr, "Error: No such image: %s\n", ref)
				}
				missing = errors.New("exit status 1")
				continue
			}
			fmt.Fprintf(req.Stdout, "[%q]\t[]\t%q\n", ref, dockerfake.ImageID(ref))
		}
		return nil, missing
	}
	tagOf := func(args []string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-t" || args[i] == "--tag" {
				return args[i+1]
			}
		}
		return ""
	}
	daemon.reply = func(args []string) (string, error, bool) {
		if slices.Equal(args, []string{"ps", "-aq", "--no-trunc"}) {
			names := map[string]bool{}
			daemon.mu.Lock()
			for name := range daemon.started {
				names[name] = true
			}
			daemon.mu.Unlock()
			if seeded := onHost.Load(); seeded != nil {
				for _, r := range *seeded {
					if r.Kind == "container" {
						names[r.Name] = true
					}
				}
			}
			var out []string
			for name := range names {
				out = append(out, name)
			}
			slices.Sort(out)
			return strings.Join(out, "\n"), nil, true
		}
		if len(args) > 0 && (args[0] == "build" || (args[0] == "buildx" && len(args) > 1 && args[1] == "build")) {
			if tag := tagOf(args); tag != "" {
				imagesMu.Lock()
				images[tag], images["sha256:built"] = true, true // the fake names every image it holds by this ID
				imagesMu.Unlock()
				// A static site packed into the file server's image
				// (docker.StaticSiteDockerfile) is its context's files: the
				// fake keeps a copy, as the image would. It names every
				// image by one ID, so the release that runs next is bound
				// to these files (released).
				for i := 0; i+1 < len(args); i++ {
					if args[i] != "-f" {
						continue
					}
					if file, err := os.ReadFile(args[i+1]); err == nil && strings.Contains(string(file), "\nCOPY . /srv\n") {
						site := t.TempDir()
						if err := os.CopyFS(site, os.DirFS(args[len(args)-1])); err != nil {
							return "copying the build context: " + err.Error(), err, true
						}
						listening.Lock()
						listening.packed = site
						listening.Unlock()
					}
				}
			}
			return "", nil, false
		}
		if len(args) == 2 && args[0] == "pull" && !(garage && args[1] == garageScannerImage) {
			imagesMu.Lock()
			images[args[1]], images["sha256:built"] = true, true
			imagesMu.Unlock()
			return "", nil, false
		}
		if len(args) > 0 && args[0] == "run" && refuseRun.Load() && slices.Contains(args, "-p") {
			// The app container is refused; the fake recorded it as
			// started before asking, so it forgets it as the daemon would
			// never have had it.
			name := containerNameOf(strings.Join(args, " "))
			daemon.mu.Lock()
			delete(daemon.started, name)
			daemon.mu.Unlock()
			return "docker: Error response from daemon: failed to create task for container: OCI runtime create failed: exec: \"gunicorn\": executable file not found in $PATH: unknown.", errors.New("exit status 127"), true
		}
		if len(args) > 0 && args[0] == "run" {
			// A container that publishes a port answers on it, as long as
			// it runs.
			name, published, release, project, main := containerNameOf(strings.Join(args, " ")), "", "", "", false
			for i := 0; i+1 < len(args); i++ {
				switch {
				case args[i] == "-p":
					published = args[i+1]
				case args[i] == "-e" && strings.HasPrefix(args[i+1], appcontract.ReleaseEnvVar+"="):
					release = strings.TrimPrefix(args[i+1], appcontract.ReleaseEnvVar+"=")
				case args[i] == "--label" && strings.HasPrefix(args[i+1], "apptown.info.project="):
					project = strings.TrimPrefix(args[i+1], "apptown.info.project=")
				case args[i] == "--label" && args[i+1] == "apptown.info.deployment=main":
					main = true
				}
			}
			if published != "" {
				parts := strings.Split(published, ":") // bind:host:container, host empty for any
				hostPort := "0"
				if len(parts) == 3 && parts[1] != "" {
					hostPort = parts[1]
				}
				l, err := net.Listen("tcp", "127.0.0.1:"+hostPort)
				if err != nil {
					return "docker: Error response from daemon: driver failed programming external connectivity: " + err.Error(), errors.New("exit status 125"), true
				}
				// A change's preview of a static site is the file server
				// with the change's folder mounted at /srv: the fake serves
				// that folder, as the file server would.
				site := ""
				for i := 0; i+1 < len(args); i++ {
					if args[i] == "-v" {
						if folder, ok := strings.CutSuffix(args[i+1], ":/srv:ro"); ok {
							site = folder
						}
					}
				}
				// A release of a static part serves the files its build
				// packed, and so does the same release run again.
				listening.Lock()
				if site == "" && release != "" {
					if listening.released[release] == "" && listening.packed != "" {
						listening.released[release], listening.packed = listening.packed, ""
					}
					site = listening.released[release]
				}
				listening.Unlock()
				srv := &http.Server{Handler: answer(func() string { return release }, site)}
				go func() { _ = srv.Serve(l) }()
				listening.Lock()
				if main && project != "" && site != "" {
					listening.front[project] = srv.Handler
				}
				if was := listening.on[name]; was != nil {
					_ = was.Close()
				}
				listening.on[name] = srv
				listening.port[name] = l.Addr().(*net.TCPAddr).Port
				listening.Unlock()
			}
			return "", nil, false
		}
		if len(args) > 1 && args[0] == "rm" {
			name := args[len(args)-1]
			listening.Lock()
			if was := listening.on[name]; was != nil {
				_ = was.Close()
			}
			delete(listening.on, name)
			delete(listening.port, name)
			listening.Unlock()
			return "", nil, false
		}
		if len(args) > 1 && args[0] == "port" {
			listening.Lock()
			port, ok := listening.port[args[1]]
			listening.Unlock()
			if ok {
				return fmt.Sprintf("127.0.0.1:%d\n", port), nil, true
			}
			return "", nil, false
		}
		if len(args) == 4 && args[0] == "inspect" && args[1] == "--format" && args[2] == "{{json .Config}}" && daemon.isStarted(args[3]) {
			// How a container was started: its image and the -e its run gave it.
			daemon.mu.Lock()
			record := daemon.storageContainers[args[3]]
			daemon.mu.Unlock()
			var run []string
			for _, c := range daemon.joined() {
				if strings.HasPrefix(c, "run ") && containerNameOf(c) == args[3] {
					run = strings.Fields(c)
				}
			}
			env := []string{}
			for i := 0; i+1 < len(run); i++ {
				if run[i] == "-e" && strings.Contains(run[i+1], "=") {
					env = append(env, run[i+1])
				}
			}
			image, _ := record["Config"].(map[string]string)
			out, err := json.Marshal(map[string]any{"Image": image["Image"], "Env": env})
			return string(out), err, true
		}
		if len(args) == 4 && args[0] == "inspect" && args[1] == "-f" && strings.HasPrefix(args[2], "{{range .Mounts}}") {
			// A container's mounts are the -v its run gave it.
			daemon.mu.Lock()
			record := daemon.storageContainers[args[3]]
			daemon.mu.Unlock()
			if !daemon.isStarted(args[3]) || record == nil {
				return "", nil, false
			}
			var lines []string
			for _, m := range record["Mounts"].([]map[string]string) {
				switch {
				case strings.Contains(args[2], "{{.Type}}"):
					lines = append(lines, m["Type"]+"\t"+m["Name"]+"\t\t"+m["Destination"])
				case m["Type"] == "volume":
					lines = append(lines, m["Name"]+"\t"+m["Destination"])
				}
			}
			return strings.Join(lines, "\n") + "\n", nil, true
		}
		if len(args) == 3 && args[0] == "ps" && args[1] == "-q" && args[2] == "--no-trunc" {
			// The running containers, by the IDs their runs printed.
			var ids []string
			daemon.mu.Lock()
			for name, up := range daemon.started {
				if up {
					ids = append(ids, dockerfake.ContainerID(name))
				}
			}
			daemon.mu.Unlock()
			slices.Sort(ids)
			return strings.Join(ids, "\n"), nil, true
		}
		if len(args) > 5 && args[0] == "inspect" && args[1] == "--type" && args[2] == "container" && strings.HasPrefix(args[4], "{{.Name}}\t{{.HostConfig.Memory}}") {
			// Each running container's limits, as its run set them, and the
			// labels it was made with.
			var lines []string
			daemon.mu.Lock()
			for _, ref := range args[5:] {
				for name, up := range daemon.started {
					if !up || (ref != name && ref != dockerfake.ContainerID(name)) {
						continue
					}
					var memory, nano int64
					fmt.Sscan(daemon.limits[name], &memory, &nano)
					labels := daemon.labels[name]
					value := func(k string) string {
						if v, ok := labels[k]; ok {
							return v
						}
						return "<no value>"
					}
					lines = append(lines, fmt.Sprintf("/%s\t%d\t%d\t0\t0\t\t%s\t%s\t<no value>", name, memory, nano, value(dockerlabel.Class), value(dockerlabel.Owner)))
				}
			}
			daemon.mu.Unlock()
			return strings.Join(lines, "\n") + "\n", nil, true
		}
		if slices.Equal(args, []string{"system", "df", "--format", "{{json .}}"}) {
			// What Docker's data holds, adding up to the disk reader's 10 GB.
			return `{"Type":"Images","Size":"6.6GB"}` + "\n" + `{"Type":"Containers","Size":"112MB"}` + "\n" +
				`{"Type":"Local Volumes","Size":"1.2GB"}` + "\n" + `{"Type":"Build Cache","Size":"2.1GB"}`, nil, true
		}
		if len(args) > 3 && args[0] == "stats" {
			// Each running container uses a steady, modest amount of
			// memory; with no names, every running container is reported.
			names := args[4:]
			if len(names) == 0 {
				daemon.mu.Lock()
				for name, up := range daemon.started {
					if up {
						names = append(names, name)
					}
				}
				daemon.mu.Unlock()
				slices.Sort(names)
			}
			var lines []string
			for _, name := range names {
				if daemon.isStarted(name) {
					lines = append(lines, fmt.Sprintf("%s\t48MiB / 160MiB\t%d.00%%", name, 2+7*(len(name)%5)))
				}
			}
			return strings.Join(lines, "\n"), nil, true
		}
		switch {
		case len(args) == 4 && args[0] == "inspect" && args[2] == docker.RuntimeFormat && seededRuntime(args[3]) != "":
			state := seededRuntime(args[3])
			generation := "1"
			if state == "unknown" {
				generation = "unreadable"
			}
			out, err := json.Marshal(docker.Runtime{ID: dockerfake.ContainerID(args[3]), Running: state != "stopped", Labels: map[string]string{dockerlabel.Generation: generation}})
			return string(out), err, true
		case len(args) > 1 && args[0] == "image" && args[1] == "inspect":
			ref := args[len(args)-1]
			if ref == tourAgentImage {
				if !garage {
					return `"sha256:` + strings.Repeat("a", 64) + `" {}`, nil, true
				}
			} else if held(ref) {
				if slices.Contains(args, "{{json .Id}} {{json (index .Config \"Labels\")}}") {
					return fmt.Sprintf("%q {}", dockerfake.ImageID(ref)), nil, true
				}
				return "", nil, false // the fake's own answer for an image it holds
			}
			return "Error: No such image: " + ref, errors.New("exit status 1"), true
		case garage && len(args) > 0 && args[0] == "pull" && args[len(args)-1] == garageScannerImage:
			return "Error response from daemon: manifest for " + garageScannerImage + " not found: manifest unknown", errors.New("exit status 1"), true
		case len(args) > 2 && args[0] == "ps" && args[1] == "-a" && strings.HasPrefix(args[len(args)-1], "{{.Names}}\t{{.Status}}"),
			len(args) > 1 && (args[0] == "volume" || args[0] == "network") && args[1] == "ls":
			// The host lists what it made and still has, with the labels
			// it was made with, and the garage's seeded leftovers (onHost).
			seen := map[string]bool{}
			var lines []string
			line := func(kind, name, status string, labels map[string]string) {
				if seen[kind+" "+name] {
					return
				}
				seen[kind+" "+name] = true
				owner, instance := labels[dockerlabel.Owner], labels[dockerlabel.Instance]
				if kind == "container" {
					lines = append(lines, name+"\t"+status+"\t\t"+owner+"\t\t"+instance)
				} else {
					lines = append(lines, name+"\t"+owner+"\t\t"+instance)
				}
			}
			daemon.mu.Lock()
			for key, made := range daemon.objects {
				kind, name, _ := strings.Cut(key, " ")
				if made && kind == args[0] {
					line(kind, name, "", nil)
				}
			}
			if args[0] == "ps" {
				for name, up := range daemon.started {
					if up {
						line("container", name, "Up 2 days", daemon.labels[name])
					}
				}
			}
			daemon.mu.Unlock()
			if seeded := onHost.Load(); seeded != nil {
				for _, r := range *seeded {
					switch {
					case args[0] == "ps" && r.Kind == "container":
						status := "Up 2 days"
						if seededRuntime(r.Name) == "stopped" {
							status = "Exited (0) 3 hours ago"
						}
						line("container", r.Name, status, nil)
					case args[0] == r.Kind:
						line(r.Kind, r.Name, "", nil)
					}
				}
			}
			slices.Sort(lines)
			return strings.Join(lines, "\n"), nil, true
		}
		return "", nil, false
	}
	return daemon, appPort, onHost, refuseRun
}

// A scene's cold-container check must reach the actual published port and
// read back the release passed to that container, rather than a canned probe.
func TestSceneDockerServesColdContainer(t *testing.T) {
	daemon, _, _, _ := newSceneDocker(t, false)
	if _, err := daemon.run("run", "--name", "camera-cold", "-p", "127.0.0.1::8080", "-e", appcontract.ReleaseEnvVar+"=camera-release", "camera-image"); err != nil {
		t.Fatal(err)
	}
	port, err := daemon.run("port", "camera-cold", "8080")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get("http://" + strings.TrimSpace(string(port)) + appcontract.ReleasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "camera-release" {
		t.Fatalf("release %q: %v", body, err)
	}
	if _, err := daemon.run("rm", "-f", "camera-cold"); err != nil {
		t.Fatal(err)
	}
	if response, err := http.Get("http://" + strings.TrimSpace(string(port)) + "/"); err == nil {
		response.Body.Close()
		t.Fatal("removed container still serves its published port")
	}
}

// newTourWorld builds the tour's AppTown; garage adds its trouble.
func newTourWorld(t *testing.T, garage bool, readerInterval time.Duration) *tourWorld {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tourDir := filepath.Join(root, "site", "tour")

	server := httptest.NewUnstartedServer(nil)
	t.Cleanup(server.Close)
	// APPTOWN_DESIGN_HOLD keeps the tour open on a fixed local address, over
	// plain HTTP, with web pages from the APPTOWN_DESIGN_VITE dev server, for
	// a person to look at instead of photographing it.
	hold, vite := os.Getenv("APPTOWN_DESIGN_HOLD"), os.Getenv("APPTOWN_DESIGN_VITE")
	if hold != "" {
		l, err := net.Listen("tcp", hold)
		if err != nil {
			t.Fatal(err)
		}
		server.Listener.Close()
		server.Listener = l
	}
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	runtime := consoletest.NewAgent()
	// The data folder, which the infrastructure page names. Each running
	// scene makes its own and removes only that one, so any number of tours
	// and garages can be open, or photographed, together. It is in TMPDIR:
	// the landings' source scans bind-mount folders under it into real
	// containers, and a daemon reads the path on its own host. ci-heavy.sh
	// runs the tour in a container beside the host's daemon and shares only
	// TMPDIR (and the checkout) at the same path; a folder in that
	// container's own /tmp reaches the scanners empty.
	scene := "tour"
	if garage {
		scene = "garage"
	}
	// Leave room for the attached host's SSH control socket on macOS.
	dataDir, err := os.MkdirTemp("", scene[:1]+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	daemon, appPort, onHost, refuseRun := newSceneDocker(t, garage)
	if garage {
		daemon.disk.Kernel = func(string) (int, []byte, string) { return 1, nil, "the host's /proc could not be read" }
	} else {
		// The whole host's memory and CPU, as /proc describes the machine the
		// daemon reports (8 GiB, 4 CPUs): steadily about 30% busy.
		var samples atomic.Int64
		daemon.disk.Kernel = func(command string) (int, []byte, string) {
			if command == "cat /proc/meminfo" {
				return 0, []byte("MemTotal: 8388608 kB\nMemAvailable: 5767168 kB\n"), ""
			}
			n := samples.Add(1)
			stat := fmt.Appendf(nil, "cpu %d 0 0 %d 0 0 0 0 0 0\n", n*120, n*280)
			for core := range 4 {
				stat = fmt.Appendf(stat, "cpu%d 0 0 0 0 0 0 0 0\n", core)
			}
			return 0, stat, ""
		}
	}
	// The garage has its own host name: browsers share cookies across ports
	// on one host, so a tour session would otherwise reach the garage.
	host := "apptown.localhost"
	if garage {
		host = "garage.localhost"
	}
	publicHost := host + ":" + port
	// Previews are served by AppTown itself, on its own port, as
	// change-….preview.localhost: the camera can open one.
	previewTier := "preview=preview.localhost:" + port
	if hold != "" {
		publicHost = "http://" + publicHost
		previewTier = "preview=http://preview.localhost:" + port
	}
	// The data folder reads the same on every machine: roomy in the tour
	// and low in the garage. Host use comes from the fake daemon's reader.
	dataDisk := func(string) (diskspace.Space, error) {
		return diskspace.Space{TotalBytes: 500 << 30, FreeBytes: 312 << 30}, nil
	}
	if garage {
		dataDisk = func(string) (diskspace.Space, error) {
			return diskspace.Space{TotalBytes: 500 << 30, FreeBytes: 30 << 30}, nil
		}
	}
	// The working preview waits before the fake host starts its container.
	// The daemon remains available for inventory and every other case.
	freeze := make(chan struct{})
	frozen := make(chan struct{}, 1)
	client := dockerClientFunc(func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
		args := req.Args
		if len(args) >= 2 && args[0] == "--config" {
			args = args[2:]
		}
		if garage && len(args) > 0 && args[0] == "run" && slices.Contains(args, "apptown.info.project=garage-work") {
			frozen <- struct{}{}
			<-freeze
			return nil, context.Canceled
		}
		return daemon.Run(ctx, host, req)
	})
	a, h := internalApp(t, Config{DataDir: dataDir, PublicHost: publicHost, TierDomains: []string{previewTier, fmt.Sprintf("main=http://apps.localhost:%d", appPort)},
		DockerClient: client, ConsoleAgent: runtime, ConsoleCLI: fixtureCLI(t), AgentImage: tourAgentImage,
		DataDiskSpace: dataDisk,
		InferenceURL:  "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
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
	setFastSlot(t, a, fakeModelProvider(t, a.Integrations.Bindings, func(q providerQuestion) (int, string) {
		phrase := "Project work"
		switch {
		case strings.Contains(q.User, "Lunch Orders"):
			phrase = "Daily lunch orders"
		case strings.Contains(q.User, "Club Newsletter"):
			phrase = "Cycling club news"
		case strings.Contains(q.User, "fieldnotes"):
			phrase = "A journal of everyday things"
		case strings.Contains(q.User, "Rocket Garden"):
			phrase = "Growing and launching rockets"
		}
		return 200, chatAnswer(phrase, 20, 5, 0)
	}))
	// The conformance review's checks run where the tour can run them; then
	// Work, stopped by that rewiring, is built again with the tour's
	// landing workflow.
	tourChecks(t, a)
	useScriptTasks(t, a, tourScript)
	defineWorkflows(t, a, tourWorkflow)

	const password = "tour-password-not-secret"
	boot := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "sam", password))
	if boot.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", boot.Code, boot.Body.String())
	}
	s := &tourScene{t: t, a: a, runtime: runtime, password: password, cookies: map[string]*http.Cookie{}, garage: garage, story: !garage, release: true, refuseRun: refuseRun}
	// configureConsoleModel supplied an explicit fixture binding; no provider is contacted.
	// The team: Sam runs AppTown; Priya and Tom make apps.
	for _, u := range []string{"priya", "tom"} {
		s.call("sam", "POST", "/api/v1/users", map[string]string{"username": u, "role": "member", "password": password})
	}
	made := s.play(start)
	if garage {
		// The recorded rejection is trouble: the tour's story leaves it out.
		s.fieldnotes()
		s.progress(freeze, frozen, daemon)
	}
	for _, job := range operationJobs(t, a, project.DescriptionJob) {
		runQueuedJobForTest(t, a, project.DescriptionJob, job.Key)
	}
	moveClock(a, -a.cfg.clock.ahead)
	if s.story {
		made.preview = s.openPreview("tom", made.story)
	}
	if garage {
		// Since the last landing the built-in host's disk cannot be read:
		// its capacity check fails, and it would refuse a new deploy.
		daemon.disk.Answer = func() (int, []byte, string) { return 125, nil, "df: /d: Input/output error" }
	}
	// Each app's main runs what its landings deployed; in the garage, three
	// more of Lunch Orders' deployments are stopped, unknown and
	// unreachable.
	var runtimeStates []string
	if garage {
		runtimeStates = []string{"stopped", "unknown", "unreachable"}
	}
	recorded := tourRuntimeDeployments(t, a, runtimeStates)
	// The host has what was recorded for the seeded deployments, except, in
	// the garage, the unknown deployment's database container. What the
	// landings deployed the fake made itself.
	var present []deploy.Resource
	removed := 0
	for _, r := range recorded {
		if len(r.Hosts) != 1 || !strings.HasPrefix(r.Hosts[0].Deployment, "tour-") {
			continue
		}
		if r.Kind == "container" && r.Hosts[0].Deployment == "tour-unknown" && r.Hosts[0].Need != "" {
			removed++
			continue
		}
		present = append(present, r)
	}
	if garage && removed != 1 {
		t.Fatalf("the garage found no database container of tour-unknown to remove: %+v", recorded)
	}
	onHost.Store(&present)
	lunch, err := a.Projects.BySlug(asPerson("sam"), "lunch-orders")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Deployment.ObserveRuntime(asPerson("sam")); err != nil {
		t.Fatal(err)
	}
	rows, err := a.Deployment.List(asPerson("sam"), lunch.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if strings.HasPrefix(row.Name, "tour-") {
			want := strings.TrimPrefix(row.Name, "tour-")
			if row.Observation.State.String() != want {
				t.Fatalf("tour %s: %+v", want, row.Observation)
			}
			seen[want] = true
		}
	}
	if len(seen) != len(runtimeStates) {
		t.Fatalf("tour runtime states: %v, want %v", seen, runtimeStates)
	}
	if garage {
		// Sam adds a Semgrep scanner with a mistyped version: its image
		// cannot be pulled.
		s.call("sam", "POST", "/api/v1/integrations", map[string]any{"name": "semgrep-strict", "capability": "scanner", "provider": "tool", "config": map[string]any{
			"description": "SAST via semgrep", "image": garageScannerImage, "command": "semgrep scan --config auto --sarif --output /work/.apptown/semgrep.sarif /work",
			"report": ".apptown/semgrep.sarif", "format": "sarif", "kind": "sast", "network": true, "appliesTo": []string{"repository"}}})
	}
	runtimeCtx, stopRuntime := context.WithCancel(context.Background())
	t.Cleanup(stopRuntime)
	go a.Deployment.RunObservations(runtimeCtx)
	w := &tourWorld{t: t, a: a, scene: s, server: server, made: made, tourDir: tourDir, password: password, vite: vite, held: hold != ""}
	w.observe()
	// Fixture jobs finish before the first lap; the held manager owns later laps.
	readHostsForTest(t, a)
	if w.held {
		readers := a.HostReaders
		readers.Interval = readerInterval
		done := make(chan struct{})
		go func() { defer close(done); readers.Run(runtimeCtx) }()
		t.Cleanup(func() { stopRuntime(); <-done })
	}
	return w
}

// settle brings the story's scene to what the camera photographs before it
// comes: AppTown's rounds and a host reader lap run until every deployment's
// host reading is newer than its last deploy. Work the scene started (the
// story's preview, a release) may settle after a lap, so the scene waits on
// that fact, never on a time; the camera then judges each stop once.
func (w *tourWorld) settle() {
	t, a := w.t, w.a
	var waiting []string
	if !poll.Until(context.Background(), 2*time.Minute, 100*time.Millisecond, func() bool {
		w.observe()
		readHostsForTest(t, a)
		waiting = waiting[:0]
		var projects []struct {
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal(w.scene.call("sam", "GET", "/api/v1/projects", nil), &projects); err != nil {
			t.Fatal(err)
		}
		for _, p := range projects {
			project, err := a.Projects.BySlug(asPerson("sam"), p.Slug)
			if err != nil {
				t.Fatal(err)
			}
			deps, err := a.Deployment.List(asPerson("sam"), project.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range deps {
				diag, err := a.Deployment.Diagnostics(asPerson("sam"), project.ID, d.Name)
				if err != nil {
					t.Fatal(err)
				}
				if diag.Reading.Unknown {
					waiting = append(waiting, fmt.Sprintf("%s %s: %s", p.Slug, d.Name, diag.Reading.Failure))
				}
			}
		}
		return len(waiting) == 0
	}) {
		t.Fatalf("the scene never settled: %s", strings.Join(waiting, "; "))
	}
}

// observe runs the normal scheduled pass and its observation jobs, as
// AppTown's job queue would: before photographing Home, and while the
// scene is held open, so its readings do not age into unknown.
func (w *tourWorld) observe() { observeScene(w.t, w.a, w.scene, w.held) }

// observeScene is the tour's and the camera's observation pass. held
// tolerates a job history past the list's cap, as a long-open scene's is.
func observeScene(t *testing.T, a *App, scene *tourScene, held bool) {
	if _, err := infrastructureChecksFor(a).Run(context.Background(), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := infrastructureChecksFor(a).RunHourly(context.Background(), "", nil); err != nil {
		t.Fatal(err)
	}
	scene.call("sam", "GET", "/api/v1/attention", nil)
	// Before the camera every job is accounted for (operationJobs refuses
	// a capped list). A held scene refreshes every few minutes and its job
	// history passes the list's cap within the hour: there the open jobs,
	// which the list gives first, are run and a capped list is only noted.
	queued := func(kind string) []jobs.Job { return operationJobs(t, a, kind) }
	if held {
		queued = func(kind string) []jobs.Job {
			const cap = 500
			all, err := jobs.List(context.Background(), a.DB, cap)
			if err != nil {
				t.Fatal(err)
			}
			if len(all) >= cap {
				t.Logf("the job list reached its cap of %d; running the open jobs it shows", cap)
			}
			var out []jobs.Job
			for _, j := range all {
				if j.Kind == kind {
					out = append(out, j)
				}
			}
			return out
		}
	}
	for _, kind := range []string{diskObservationJob, workerObservationJob, "capacity-observation", "inventory", "protection-observation", integration.HealthObservationJob, deployment.TargetObservationJob, deployment.DetailObservationJob, preview.ObservationJob, integration.ErrorObservationJob, dataprotection.StoreObservationJob} {
		for _, job := range queued(kind) {
			if job.State == "available" || job.State == "retryable" || job.State == "scheduled" {
				result := runJobForTest(t, a, job.ID)
				t.Logf("tour observation %s: %s", kind, result.State)
			}
		}
	}
	t.Logf("tour operational observations: %s", scene.call("sam", "GET", "/api/v1/attention", nil))
}

// hold serves the scene on APPTOWN_DESIGN_HOLD until interrupted, with web
// pages from the Vite dev server; false when no hold was asked for.
func (w *tourWorld) hold() bool {
	t := w.t
	if !w.held {
		return false
	}
	h := w.a.Handler()
	frontend, err := url.Parse(w.vite)
	if err != nil || frontend.Hostname() != "127.0.0.1" {
		t.Fatal("APPTOWN_DESIGN_VITE must be a local Vite server")
	}
	proxy := httputil.NewSingleHostReverseProxy(frontend)
	w.server.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.ServeHTTP(rw, r)
			return
		}
		proxy.ServeHTTP(rw, r)
	})
	w.server.Start()
	t.Logf("%s open at http://%s (sam, priya or tom / %s); interrupt to stop", t.Name(), w.a.cfg.publicHost, w.password)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	refresh := time.NewTicker(5 * time.Minute)
	defer refresh.Stop()
	for {
		select {
		case <-stop:
			return true
		case <-refresh.C:
			w.observe()
		}
	}
}

// tourRuntimeDeployments seeds Lunch Orders' troubled deployments, one in
// each runtime state named, and returns the built-in host's recorded inventory.
// The unreachable deployment has its own attached host; the unknown one's
// container has an unreadable generation label. No landing made these
// containers: seed the persisted pointer and generation, as
// no public operation imports an existing standing container without a
// release. The provider fake supplies their runtime facts; no release or
// check receipt is invented for them.
func tourRuntimeDeployments(t *testing.T, a *App, states []string) []deploy.Resource {
	t.Helper()
	ctx := asPerson("sam")
	p, err := a.Projects.BySlug(ctx, "lunch-orders")
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.Deployment.Targets.ByName(ctx, "built-in-docker")
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range states {
		deploymentTarget := target
		if word == "unreachable" {
			// Seed a previously attached host that is now offline; attaching it
			// through the connection wizard requires a responding daemon.
			if err := a.Secrets.SetInstance(ctx, "TOUR_OFFLINE_KEY", `-----BEGIN PRIVATE KEY-----\ndisposable test transport marker\n-----END PRIVATE KEY-----`, "sam"); err != nil {
				t.Fatal(err)
			}
			deploymentTarget, err = a.Deployment.Targets.Put(ctx, &deployment.Target{Name: "tour-unreachable", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetUnreachable, Address: map[string]any{"host": "ssh://deploy@offline.example.test", "keyFrom": "TOUR_OFFLINE_KEY"}})
			if err != nil {
				t.Fatal(err)
			}
		}
		d, err := a.Deployment.Deployments.EnsureStanding(ctx, p.ID, "tour-"+word, deploymentTarget.ID, deployment.Attributes{}.WithDefaults())
		if err != nil {
			t.Fatal(err)
		}
		names, err := d.HostNames()
		if err != nil {
			t.Fatal(err)
		}
		// Use the host naming protocol so inventory can confirm these containers.
		// sql-guarantee: seed an existing container and generation; no owner operation imports one without creating release evidence.
		if _, err := a.DB.SQL().ExecContext(ctx, `UPDATE deployments SET container=$1, desired_generation=1, observed_generation=1 WHERE id=$2`, names.App(), d.ID); err != nil {
			t.Fatal(err)
		}
	}
	recorded, err := a.Deployment.Inventory.Recorded(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	return recorded
}

// tourStop is one stop of site/tour/tour.yaml.
type tourStop struct {
	ID      string `yaml:"id"`
	Title   string `yaml:"title"`
	Caption string `yaml:"caption"`
	Path    string `yaml:"path"`
	Wait    string `yaml:"wait"`
	// Click is a button's text the camera clicks before the shot, for a
	// stop that shows a dialog; Happy has the camera refuse a failed or
	// unknown status on the stop (shoot.mjs).
	Click string `yaml:"click"`
	Happy bool   `yaml:"happy"`
	// After is "accept" for a stop photographed after Priya accepts the
	// story's change and its release has run, and "rollback" for one
	// photographed after the earlier release is deployed again (TestTour).
	After string `yaml:"after"`
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
  <p class="lead">This is the AppTown a small team shares. Sam set it up. Tom looks after a cycling club's newsletter and a rocket game he made for fun. Priya runs the team's lunch orders and signs off on newsletter changes. Below, Tom asks for a new look for the newsletter, Priya decides whether it goes out, and it goes live. Then the club changes its mind, and the old site goes back. After that there's a quick look around the rest of AppTown. The chats are scripted for the tour, but the checks run on real code. <a href="/">Back to apptown.dev</a>.</p>
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
	// garage keeps the trouble the tour leaves out: Lunch Orders' contract
	// does not say it is built with Python, and its second landing's deploy
	// fails.
	garage bool
	// story is the tour's happy path: its last act is the neon makeover,
	// checked, previewed and waiting for approval in place of the November
	// issue, and the history leaves out the change that failed (the order
	// total's style check) and the recorded fieldnotes rejection. The
	// garage and the camera keep the whole history, with the makeover
	// shipped.
	story bool
	// release runs each landing's release, as its follow-up and release
	// jobs would, so main runs what was landed. The tour and the garage
	// release; the camera records its releases' outcomes itself.
	release bool
	// refuseRun, while set, has the fake Docker host refuse to start an
	// app's container.
	refuseRun *atomic.Bool
}

// clubSignInKind is the shared service Sam defines: the cycling club's
// own sign-in, which the club's apps use instead of keeping passwords.
const clubSignInKind = `capability: club-sign-in
title: Club sign-in
description: Hilltop Cycling Club's own sign-in (OpenID Connect), so members use their club account in club apps.
connector:
  fields:
    - key: url
      label: Issuer base URL
      placeholder: https://sign-in.hilltop-cycling.example
resource:
  fields:
    - key: client_id
      label: Client ID for this app
    - key: client_secret_from
      label: Client secret (secret name)
      type: secretRef
env:
  - key: OIDC_ISSUER
    value: "{{connector.url}}"
  - key: OIDC_CLIENT_ID
    value: "{{resource.client_id}}"
  - key: OIDC_CLIENT_SECRET
    value: "{{secret:resource.client_secret_from}}"
steering: |
  Members sign in with their Hilltop Cycling Club account (OpenID Connect).
  The issuer, client ID and secret arrive as OIDC_ISSUER, OIDC_CLIENT_ID and
  OIDC_CLIENT_SECRET; read them from the environment, never hardcode them.
`

// clubSignIn sets up one shared service as Sam would: he defines the
// club's sign-in under Services › Shared services, connects it, and
// attaches it to the newsletter under its Settings › Services, with the
// newsletter's client secret held in the built-in secret store (a
// disposable value; the issuer is a reserved .example name nothing
// answers on).
func (s *tourScene) clubSignIn() {
	s.call("sam", "POST", "/api/v1/services", map[string]string{"yaml": clubSignInKind})
	s.call("sam", "POST", "/api/v1/integrations", map[string]any{"name": "hilltop-sign-in", "capability": "club-sign-in", "provider": "service",
		"config": map[string]any{"url": "https://sign-in.hilltop-cycling.example"}})
	s.call("sam", "PUT", "/api/v1/secrets/CLUB_NEWSLETTER_SIGN_IN_SECRET", map[string]string{"value": "disposable-club-sign-in-secret"})
	s.call("sam", "POST", "/api/v1/projects/club-newsletter/integrations", map[string]any{"capability": "club-sign-in", "connector": "hilltop-sign-in",
		"resource": map[string]string{"client_id": "club-newsletter", "client_secret_from": "CLUB_NEWSLETTER_SIGN_IN_SECRET"}})
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
		// A held design scene serves plain HTTP, which names the cookie
		// without the __Host- prefix.
		if (c.Name == secureSessionCookie || c.Name == "apptown_session") && c.Value != "" {
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
	if err := json.Unmarshal(s.call(who, "POST", fmt.Sprintf("/api/v1/changes/%s/runs", c.ID), map[string]string{"workflow": "publish"}), &started); err != nil {
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
	if d := s.steps(runID)["build"]; d.Status != "pass" {
		s.t.Fatalf("run %s: build is %q: %s (%+v)", runID, d.Status, d.Evidence, s.steps(runID))
	}
	// The landing's release follows, as its follow-up and release jobs run
	// it: main is deployed, unless the host was told to refuse it (the
	// garage's second Lunch Orders landing).
	if !s.release {
		return
	}
	ctx := s.t.Context()
	if _, err := workflowFollowUps(s.a).Complete(ctx, fmt.Sprintf("%s/release", runID), map[string]any{"approvedBy": who}); err != nil {
		s.t.Fatal(err)
	}
	run, err := s.a.Work.Run(s.a.asEngine(ctx), runID)
	if err != nil {
		s.t.Fatal(err)
	}
	rels, err := s.a.Deployment.Releases.LandingRequests(ctx, run.ProjectID, run.ChangeID)
	if err != nil {
		s.t.Fatal(err)
	}
	want := deployment.Released
	if s.refuseRun.Load() {
		want = deployment.ReleaseFailed
	}
	for _, r := range rels {
		_, _ = deploymentJobsForTest(s.a).Release(ctx, r.ID.String(), nil)
		got, err := s.a.Deployment.Releases.ByID(ctx, r.ID)
		if err != nil || got.State != want {
			s.t.Fatalf("run %s: release %d is %v, want %v (%v)", runID, r.Number, got, want, err)
		}
	}
}

// ship is a change with its checks passing, approved after wait and
// landed.
func (s *tourScene) ship(c *tourChange, asker, approver string, wait time.Duration) ids.ID {
	s.t.Helper()
	run := s.land(c, asker, map[string]string{"tests": "pass", "style": "pass"})
	s.later(wait)
	s.approve(run, approver)
	ch, err := s.a.Work.Change(asPerson("sam"), c.ID)
	if err != nil || ch.State.String() != "shipped" {
		s.t.Fatalf("change %s did not ship: %+v %v", c.ID, ch, err)
	}
	return run
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
	// cachedOnce: the first reply has written the conversation's start to
	// the prompt cache.
	cachedOnce bool
}

func newPiLog(at time.Time) *piLog {
	l := &piLog{started: at, at: at, context: 28000} // the agent's system prompt, tools and project guidance
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
	// A turn as a coding agent's are billed: the conversation so far is
	// read from the prompt cache, what is new to it (the agent's system
	// prompt, tools and the project's guidance on the first turn; the
	// files it read and the last command's output after that) is written
	// to the cache, and the reply is output.
	out := 900 + len(text)/2 + len(command)/2
	written := 6000 + len(text)
	cached := l.context
	if !l.cachedOnce {
		written += cached
		cached, l.cachedOnce = 0, true
	}
	fresh := 40
	// Claude Sonnet list prices per million tokens: $3 in, $15 out, $0.30 a
	// cache read, $3.75 a cache write.
	cost := (float64(fresh)*3 + float64(out)*15 + float64(cached)*0.3 + float64(written)*3.75) / 1e6
	l.add("assistant", 15*time.Second, map[string]any{
		"content": content, "api": "openai-completions", "provider": "openrouter", "model": "anthropic/claude-sonnet-5.5",
		"usage": map[string]any{"input": fresh, "output": out, "cacheRead": cached, "cacheWrite": written, "totalTokens": fresh + out + cached + written,
			"cost": map[string]any{"input": float64(fresh) * 3 / 1e6, "output": float64(out) * 15 / 1e6, "cacheRead": float64(cached) * 0.3 / 1e6, "cacheWrite": float64(written) * 3.75 / 1e6, "total": cost}},
		"stopReason": stop, "responseId": fmt.Sprintf("r%d", l.n+1)})
	l.context = cached + fresh + out + written
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
// approval; the merge follows the approval, and on entering ship the
// merged main is built and deployed to the project's main deployment, as
// the stock land workflow does, with its release record required.
var tourWorkflow = workflow.PreparedRun{
	Workflow: workflow.DefinitionRef{Name: "publish", Version: 1},
	Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{
		{Name: "tests", Uses: "script", With: map[string]any{"run": "sh checks/tests.sh", "env": map[string]any{tourCheckEnv: "checks/tests.sh"}}},
		{Name: "style", Uses: "script", With: map[string]any{"run": "sh checks/style.sh", "env": map[string]any{tourCheckEnv: "checks/style.sh"}}},
		{Name: "scan-source", Uses: "scan", With: map[string]any{"on": "source"}},
		{Name: "accept", Uses: "approval", With: map[string]any{"means": "I have read what changed and the checks, and it does what the change says."}},
	}}, {Name: "ship",
		OnEnter: []schema.Step{{Name: "build", Uses: "build"}},
		Steps:   []schema.Step{{Name: "build-exists", Uses: "artifact/require", With: map[string]any{"kind": "build"}}},
	}}},
}

// tourIDs are the ids the scene made that the stops name.
type tourIDs struct {
	review, chat  ids.ID
	session       string
	before, after string
	// story is the neon makeover, and storySession its chat, in the
	// tour's story (tourScene.story).
	story        ids.ID
	storyRun     ids.ID
	storySession string
	preview      string
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
	// The helper finds its way round first, as a coding agent does: what
	// the project holds and how its tests check it. Each look is a turn of
	// its own, billed like the rest, so a chat costs what such chats cost.
	look := func(text, command string) { log.say(text, command, s.sh(c, command)) }
	look("First, what the project holds.", "ls -R . | grep -v '^$' | head -40")
	look("What changed lately, so this fits.", "git log --oneline -8")
	look("And how its tests check it, so the change keeps them passing.", "cat checks/tests.sh")
	for _, name := range slices.Sorted(maps.Keys(h.files)) {
		if _, err := os.Stat(filepath.Join(c.folder, name)); err == nil {
			look("Reading "+name+" before changing it.", "sed -n 1,120p "+name)
		}
	}
	log.say(h.look, h.lookCommand, s.sh(c, h.lookCommand))
	s.write(c, h.files)
	look("Checking what the change touches before running the tests.", "git diff --stat")
	if _, err := os.Stat(filepath.Join(c.folder, "checks/style.sh")); err == nil {
		look("And the style check.", "sh checks/style.sh")
	}
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

// openPreview is who opening a change's preview, as Try it does, and
// answers its address. A preview nobody has looked at for eight hours is
// stopped, so the tour opens the story's when its clock reads now.
func (s *tourScene) openPreview(who string, change ids.ID) string {
	s.t.Helper()
	var opened struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(s.call(who, "POST", fmt.Sprintf("/api/v1/changes/%s/preview", change), map[string]any{}), &opened); err != nil || opened.URL == "" {
		s.t.Fatalf("change %s's preview: %+v %v", change, opened, err)
	}
	return opened.URL
}

// redeploy is who deploying a project's earlier release again on a
// deployment, as its Deploy this again button does, and the release job
// running it on the scene's host.
// It answers the new release's number.
func (s *tourScene) redeploy(who, slug, name string, number int64) int64 {
	s.t.Helper()
	var accepted struct {
		Number int64 `json:"number"`
	}
	if err := json.Unmarshal(s.call(who, "POST", "/api/v1/projects/"+slug+"/deployments/"+name+"/deploy", map[string]int64{"releaseNumber": number}), &accepted); err != nil {
		s.t.Fatal(err)
	}
	ctx := s.t.Context()
	p, err := s.a.Projects.BySlug(asPerson("sam"), slug)
	if err != nil {
		s.t.Fatal(err)
	}
	r, err := s.a.Deployment.Releases.ByNumber(ctx, p.ID, accepted.Number)
	if err != nil {
		s.t.Fatal(err)
	}
	_, _ = deploymentJobsForTest(s.a).Release(ctx, r.ID.String(), nil)
	if got, err := s.a.Deployment.Releases.ByID(ctx, r.ID); err != nil || got.State != deployment.Released {
		s.t.Fatalf("release %d (release %d again) is %+v (%v)", accepted.Number, number, got, err)
	}
	return accepted.Number
}

// mainURL is the address the project's main deployment serves on.
func (s *tourScene) mainURL(slug string) string {
	s.t.Helper()
	var rows []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(s.call("sam", "GET", "/api/v1/projects/"+slug+"/deployments", nil), &rows); err != nil {
		s.t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == "main" && r.URL != "" {
			return r.URL
		}
	}
	s.t.Fatalf("%s has no main deployment with an address: %+v", slug, rows)
	return ""
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
	// This fixture shows existing use across every area. Its administrator
	// enables beta through the same API an existing installation uses.
	for _, f := range identity.FeatureCatalogue() {
		if f.Tier == "beta" {
			s.call("sam", "PUT", "/api/v1/features/"+f.Area, map[string]bool{"expected": false, "enabled": true})
		}
	}
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
	type proj struct {
		slug, name, blurb string
		packs             []string
	}
	for _, p := range []proj{
		{"lunch-orders", "Lunch Orders", "Each morning people pick a lunch from a short menu; at 11am whoever is ordering sees the list with names and totals.", []string{"server-base", "django", "postgres", "server-rendered"}},
		{"club-newsletter", "Club Newsletter", "The cycling club's monthly newsletter, as a small static site.", []string{"conventions-only"}},
		{"rocket-garden", "Rocket Garden", "A little browser game: grow rockets, launch them, beat your best height.", []string{"html-game"}},
	} {
		s.call("sam", "POST", "/api/v1/projects", map[string]string{"slug": p.slug, "name": p.name, "blurb": p.blurb})
		s.call("sam", "POST", "/api/v1/projects/"+p.slug+"/repo", map[string]string{"defaultBranch": "main"})
		s.call("sam", "POST", "/api/v1/projects/"+p.slug+"/pack", map[string][]string{"names": p.packs})
		for _, who := range []string{"priya", "tom"} {
			s.call("sam", "POST", "/api/v1/projects/"+p.slug+"/members", map[string]string{"grantee": "user:" + who, "role": "project-contributor"})
		}
	}
	// Priya looks after the newsletter and approves its issues.
	s.call("sam", "POST", "/api/v1/projects/club-newsletter/members", map[string]string{"grantee": "user:priya", "role": "project-approver"})
	s.clubSignIn()
	// Adopting a way of working queues a review of the project; AppTown's
	// job queue runs it.
	for _, j := range operationJobs(t, s.a, verification.ReviewJob) {
		runQueuedJobForTest(t, s.a, verification.ReviewJob, j.Key)
	}

	at(0, "9:20")
	lunch1 := s.open("priya", "lunch-orders", "Start the lunch app", "A pick page with the four dishes we order most, and a list for whoever orders at 11am.")
	files := maps.Clone(lunchStart)
	// The app's dependencies are locked (uv lock, testdata/tour), so the
	// landing's dependency scan has what it reads.
	lock, err := os.ReadFile(filepath.Join("testdata", "tour", "lunch-orders-uv.lock"))
	if err != nil {
		t.Fatal(err)
	}
	files["uv.lock"] = string(lock)
	if !s.garage {
		// The app is Django: its contract says the web part is built with
		// Python, so the Python checks apply where its pyproject.toml is.
		files[".apptown/app.yaml"] = lunchContract
	}
	s.write(lunch1, files)
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
	// In the garage the host will not start the new version: the deploy
	// fails and the change does not ship.
	s.refuseRun.Store(s.garage)
	s.approve(run, "sam")
	s.refuseRun.Store(false)

	// Day 7: the ride calendar, approved by Priya.
	at(7, "20:10")
	news2 := s.open("tom", "club-newsletter", "Add the ride calendar", "The weekly rides with their start times and pace, linked from the front page.")
	s.write(news2, newsletterRides)
	s.commit(news2, "tom", "Ride calendar page")
	s.ship(news2, "tom", "priya", 25*time.Minute)

	// The neon makeover, a real before/after photographed from committed
	// example files. In the tour's story it is the last act, and waits for
	// Priya's approval with its checks passed and its preview running; in
	// the garage and the camera it ships on day 8.
	var made tourIDs
	makeover := func() {
		neon := s.open("tom", "club-newsletter", "Give the club a neon makeover", "Keep the rides and newsletters, but make it gloriously loud.")
		made.before = s.serveSite(neon)
		neonSession := s.helped(neon, "tom", tourAsk{
			ask:         "The site is sensible. Make it an exuberant neon cycling extravaganza. Keep it readable and keep every link working!",
			look:        "I'll keep the newsletter and ride pages and change the front page and its styling.",
			lookCommand: "cat public/index.html public/style.css",
			files:       newsletterNeon,
			did:         "The front page has a giant headline, neon colours, ride cards and a checkerboard border. No flashing or outside assets.",
			after:       "The links pass. The newsletters and ride calendar are still there. Try the new look before approving it.",
		})
		made.after = s.serveSite(neon)
		s.endChat("tom", neonSession)
		if !s.story {
			s.ship(neon, "tom", "priya", 25*time.Minute)
			return
		}
		// Tom closes the chat and asks for the landing; its checks pass
		// and it waits for Priya. Its preview is opened at the end (openPreview), so it is
		// running when the camera comes.
		s.later(10 * time.Minute)
		made.storyRun = s.land(neon, "tom", map[string]string{"tests": "pass", "style": "pass"})
		made.story, made.storySession = neon.ID, neonSession
	}
	// Day 8: the makeover, outside the story.
	if !s.story {
		at(8, "10:00")
		makeover()
	}

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
	// refuses. It waits for Sam to send it back or fix it. The story
	// leaves it out.
	if !s.story {
		at(11, "12:20")
		lunch3 := s.open("sam", "lunch-orders", "Show the order total at the top of the list", "Whoever orders needs the total to pay the café.")
		s.write(lunch3, lunchTotal)
		s.commit(lunch3, "sam", "Total price at the top of the 11am list")
		s.land(lunch3, "sam", map[string]string{"tests": "pass", "style": "fail"})
		made.review = lunch3.ID
	}

	// Day 12: a countdown that preserves the no-fuel rule.
	at(12, "16:45")
	rocket3 := s.open("tom", "rocket-garden", "Add a countdown before launch", "3, 2, 1, then up.")
	s.write(rocket3, rocketCountdown)
	s.commit(rocket3, "tom", "Countdown before launch")
	s.ship(rocket3, "tom", "sam", 20*time.Minute)

	// The night before the last day, the weekly review runs again.
	at(12, "23:00")
	s.review()

	// Day 13: the November issue waits for an approval (in the story,
	// the makeover does instead); Priya asks the helper to make the menu
	// editable.
	at(13, "7:30")
	if s.story {
		makeover()
	} else {
		news3 := s.open("tom", "club-newsletter", "Add the November newsletter", "Winter ride times and the end-of-season dinner.")
		s.write(news3, newsletterNovember)
		s.commit(news3, "tom", "November newsletter")
		s.land(news3, "tom", map[string]string{"tests": "pass", "style": "pass"})
	}

	at(13, "9:10")
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
	s.a.harvestAllConsoleUsage(context.Background())
	made.chat, made.session = lunch4.ID, session
	return made
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
		run: func(req execRequest) execOutcome {
			if req.Image != stock.Images().Sh {
				refusal := "the tour has no Docker to run " + req.Image
				return execOutcome{Output: []byte(refusal), RunErr: errors.New(refusal)}
			}
			return agentenv.Execute(a.processRunner, context.Background(), execution.Local, req, a.cfg.clock.Now())
		},
		complete: func(string, string, string) (string, *modelcall.Usage, error) {
			return "", nil, errors.New("the tour calls no model")
		},
	})
}

// tourCheckEnv names, in a landing script step's environment, the
// repository's own check script the tour runs for that step.
const tourCheckEnv = "TOUR_CHECK"

// tourScript runs a landing script step's check for the tour, which has no
// Docker: the step's check script (tourCheckEnv) in the task's workspace,
// the staged commit, with the local shell, as the conformance review's
// checks are run here.
func tourScript(spec execution.TaskSpec) (string, int) {
	script := ""
	for _, kv := range spec.Env {
		if v, found := strings.CutPrefix(kv, tourCheckEnv+"="); found {
			script = v
		}
	}
	out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{Workspace: spec.Workspace, Argv: []string{"sh", script}, Env: []string{"PATH=" + os.Getenv("PATH")}, Timeout: time.Minute})
	return string(out.Output), out.ExitCode
}

// The tour's three apps, as the files each change commits. Every check the
// tour shows runs these files' own scripts (checks/tests.sh and
// checks/style.sh) for real, so a pass or a failure on a page is what the
// code here earns.

// Lunch Orders, first version: pick a dish, see the 11am list.
// lunchContract is the Django starter's contract with the tool its web
// part is built with named.
const lunchContract = `version: 2
app:
  web:
    build: .
    tools: [python]
    ports: ["8080"]
    public: true
    resources: { memory: 160Mi }
    health:
      readiness: { http: /healthz }
needs:
  postgres:
    version: "16"
`

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
dependencies = ["django>=5.2,<5.3", "psycopg[binary]>=3.2"]
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
from urllib.parse import urlsplit

BASE_DIR = Path(__file__).resolve().parent.parent
SECRET_KEY = os.environ["DJANGO_SECRET_KEY"]
DEBUG = os.environ.get("DJANGO_DEBUG") == "1"

# AppTown's front door: its origins, wildcards in Django's leading-dot form.
APPTOWN_ORIGINS = [o for o in os.environ.get("APPTOWN_ORIGINS", "").split(",") if o]
ALLOWED_HOSTS = [
    "." + h[2:] if h.startswith("*.") else h
    for h in (urlsplit(o).hostname for o in APPTOWN_ORIGINS)
] or ["localhost", "127.0.0.1"]
CSRF_TRUSTED_ORIGINS = APPTOWN_ORIGINS
SECURE_PROXY_SSL_HEADER = ("HTTP_X_FORWARDED_PROTO", "https")
USE_X_FORWARDED_HOST = True

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
    "app.health.healthz",
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

// Redirects are covered at their destinations. The wildcard has no finite
// address set; a separate not-found capture exercises its rendered page.
var cameraRouteExclusions = map[string]string{
	"/invite/:token":              "consumed once and removed from the address bar; invite-pending captures acceptance at /invite",
	"/docs":                       "redirects to the first guide; docs captures /docs/guides/quickstart",
	"/projects/:slug/workflows/*": "redirects to /projects/:slug/settings/working",
	"*":                           "unbounded unknown addresses; camera-not-found captures the not-found page",
}

func cameraRouteCoverage(source string, paths []string, exclusions map[string]string) error {
	matches := regexp.MustCompile(`\bpath="([^"]+)"`).FindAllStringSubmatch(source, -1)
	if len(matches) != len(regexp.MustCompile(`\bpath\s*=`).FindAllString(source, -1)) {
		return fmt.Errorf("camera route inventory requires literal path= values")
	}
	routes := make([]string, 0, len(matches))
	for _, m := range matches {
		routes = append(routes, m[1])
	}
	for route, reason := range exclusions {
		if !slices.Contains(routes, route) || strings.TrimSpace(reason) == "" {
			return fmt.Errorf("invalid camera exclusion %q: %q", route, reason)
		}
	}
	// React Router prefers literal segments, then parameters, then splats.
	score := func(route string) int {
		n := 0
		for _, part := range strings.Split(route, "/") {
			switch {
			case part == "*":
				n -= 2
			case strings.HasPrefix(part, ":"):
				n += 4
			case part != "":
				n += 11
			default:
				n += 2
			}
		}
		return n
	}
	slices.SortStableFunc(routes, func(a, b string) int { return score(b) - score(a) })
	covered := map[string]bool{}
	for _, path := range paths {
		for _, route := range routes {
			parts := strings.Split(route, "/")
			for i, part := range parts {
				switch {
				case part == "*":
					parts[i] = ".*"
				case strings.HasPrefix(part, ":"):
					parts[i] = "[^/]+"
				default:
					parts[i] = regexp.QuoteMeta(part)
				}
			}
			if regexp.MustCompile("^" + strings.Join(parts, "/") + "/?$").MatchString(path) {
				covered[route] = true
				break
			}
		}
	}
	var missing []string
	for _, route := range routes {
		if !covered[route] && exclusions[route] == "" {
			missing = append(missing, route)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("routes without final-page captures or reasoned exclusions: %v", missing)
	}
	return nil
}

func TestWebUICameraRouteCoverage(t *testing.T) {
	if err := cameraRouteCoverage(`<Route path={dynamic} />`, nil, nil); err == nil {
		t.Fatal("dynamic route escaped inventory")
	}
	source := `<Route path="/"/><Route path="/integrations/:name"/><Route path="/integrations/profiles/:profile"/><Route path="/integrations/targets/new"/>`
	for _, paths := range [][]string{{"/", "/integrations/profiles/standard", "/integrations/targets/new"}, {"/", "/integrations/scm", "/integrations/profiles/standard"}} {
		if err := cameraRouteCoverage(source, paths, nil); err == nil {
			t.Fatal("missing route passed coverage")
		}
	}
	if err := cameraRouteCoverage(source, []string{"/", "/integrations/scm", "/integrations/profiles/standard", "/integrations/targets/new"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := cameraRouteCoverage(source, nil, map[string]string{"/gone": "removed"}); err == nil {
		t.Fatal("stale exclusion passed")
	}
	if err := cameraRouteCoverage(source, nil, map[string]string{"/": ""}); err == nil {
		t.Fatal("unexplained exclusion passed")
	}
}

func TestWebUICameraExclusionsNameCurrentRoutes(t *testing.T) {
	t.Parallel()
	source := cameraAppRoutes(t)
	for route := range cameraRouteExclusions {
		if !strings.Contains(source, `path="`+route+`"`) {
			t.Errorf("camera exclusion names a removed route: %s", route)
		}
	}
}

func TestWebUICameraRefusesTheScriptsOwnSettingNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"DESIGN_PAGES", "DESIGN_AUDIT", "DESIGN_BASELINE"} {
		env := map[string]string{name: "home"}
		err := cameraSettingsRefusal(func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), "APPTOWN_"+name) {
			t.Errorf("%s set: %v", name, err)
		}
	}
	env := map[string]string{"APPTOWN_DESIGN_PAGES": "home"}
	if err := cameraSettingsRefusal(func(k string) string { return env[k] }); err != nil {
		t.Errorf("the documented name refused: %v", err)
	}
}

func TestWebUICameraCoversDocsAtItsRedirectDestination(t *testing.T) {
	t.Parallel()
	source := `<Route path="/docs"/><Route path="/docs/:section/:slug"/>`
	if err := cameraRouteCoverage(source, []string{"/docs/guides/quickstart"}, map[string]string{"/docs": cameraRouteExclusions["/docs"]}); err != nil {
		t.Fatal(err)
	}
}

// cameraPhases records how long each part of the camera's scene took, in
// scene-timings.json beside the captures, rewritten after every phase so a
// failed run keeps what it measured.
type cameraPhases struct {
	t    *testing.T
	out  string
	last time.Time
	list []struct {
		Name string `json:"name"`
		Ms   int64  `json:"ms"`
	}
}

func (p *cameraPhases) mark(name string) {
	now := time.Now()
	took := now.Sub(p.last)
	p.last = now
	p.list = append(p.list, struct {
		Name string `json:"name"`
		Ms   int64  `json:"ms"`
	}{name, took.Milliseconds()})
	p.t.Logf("camera scene: %s took %s", name, took.Round(time.Millisecond))
	if err := os.MkdirAll(p.out, 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.out, "scene-timings.json"), mustJSONBytes(p.list), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// cameraSettingsRefusal stops a camera run before its scene is built when a
// caller set the browser script's own names (DESIGN_PAGES and friends, which
// web-ui.mjs reads). The test hands the script an explicit environment, so
// those values would be dropped silently and a run meant for a few pages
// would photograph every page.
func cameraSettingsRefusal(getenv func(string) string) error {
	for _, name := range []string{"DESIGN_PAGES", "DESIGN_AUDIT", "DESIGN_BASELINE"} {
		if getenv(name) != "" {
			return fmt.Errorf("%s is set, but the camera reads APPTOWN_%s; %s would be ignored", name, name, name)
		}
	}
	return nil
}

func cameraAppRoutes(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("../../web/src/App.tsx")
	if err != nil {
		t.Fatal(err)
	}
	return string(source)
}

// TestWebUICapture photographs every web page against
// real disposable tour records. No API responses are replaced by the camera.
// Run with APPTOWN_DESIGN_VITE=http://127.0.0.1:5180 and APPTOWN_DESIGN_OUT set.
// sceneModel gives the capture scene the model setup of a real install:
// an OpenRouter model provider whose enabled models are listed (so the
// catalog does not ask the provider), the standard profile on Claude Haiku
// and a second profile, frontier, on Claude Sonnet, so an eval compares two
// setups. It is bound to the fixture's unreachable provider address:
// nothing in the scene calls a model.
func sceneModel(t *testing.T, a *App) {
	t.Helper()
	const haiku, sonnet = "openrouter/anthropic/claude-haiku-5.5", "openrouter/anthropic/claude-sonnet-5.5"
	if _, err := a.Integrations.Bindings.Save(context.Background(), "openrouter", "model", "pi", `{"model":"openrouter/anthropic/claude-haiku-5.5","models":["anthropic/claude-haiku-5.5","anthropic/claude-sonnet-5.5"],"providerDef":{"baseUrl":"https://provider.invalid/v1","api":"openai-completions","apiKey":"provider-canary"},"modelsJson":"[{\"id\":\"anthropic/claude-haiku-5.5\",\"contextWindow\":200000,\"maxTokens\":8192},{\"id\":\"anthropic/claude-sonnet-5.5\",\"contextWindow\":200000,\"maxTokens\":8192}]"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := a.Models.SetDefault(context.Background(), haiku); err != nil {
		t.Fatal(err)
	}
	if err := a.Models.Set(context.Background(), "chat", "standard", haiku, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := a.Models.Set(context.Background(), "main-strong", "frontier", sonnet, "fixture"); err != nil {
		t.Fatal(err)
	}
}

func TestWebUICapture(t *testing.T) {
	if err := cameraSettingsRefusal(os.Getenv); err != nil {
		t.Fatal(err)
	}
	vite, out := os.Getenv("APPTOWN_DESIGN_VITE"), os.Getenv("APPTOWN_DESIGN_OUT")
	if vite == "" || out == "" {
		t.Skip("set APPTOWN_DESIGN_VITE and APPTOWN_DESIGN_OUT for the local UI capture")
	}
	// The household's network has names this machine cannot look up: its
	// NAS's S3 service, nasStoreHost, where the backup copies are kept, and
	// its apps under apps.<club domain>. AppTown connects to both (the
	// backup store's checks write and read a test object; the running-app
	// checks call an app's front door), so this process reaches them as a
	// client behind a forward proxy does: requests for those hosts go to a
	// proxy that hands them to the scene's S3 bucket and its apps' front
	// door (household, once each exists). Every other request is sent as
	// before. The transports AppTown's clients make are copies of Go's
	// default transport, taken at their first use, so it is set first.
	var household struct{ store, apps atomic.Pointer[url.URL] }
	appsSuffix := ".apps." + cameraClubDomain
	onHousehold := func(host string) bool { return host == nasStoreHost || strings.HasSuffix(host, appsSuffix) }
	// held is an upload the NAS has not answered yet: while the camera
	// photographs the jobs page, one backup's upload to the store waits
	// here (cameraJob), so its job is genuinely running.
	var held struct {
		mu   sync.Mutex
		path string        // the store path whose uploads wait
		gate chan struct{} // closed to let them through
	}
	releaseHeld := func() {
		held.mu.Lock()
		defer held.mu.Unlock()
		if held.gate != nil {
			close(held.gate)
			held.gate = nil
		}
	}
	t.Cleanup(releaseHeld)
	householdProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		held.mu.Lock()
		gate := held.gate
		if r.Host != nasStoreHost || r.Method != http.MethodPut && r.Method != http.MethodPost || held.path == "" || !strings.Contains(r.URL.Path, held.path) {
			gate = nil
		}
		held.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		target := household.store.Load()
		if strings.HasSuffix(r.Host, appsSuffix) {
			target = household.apps.Load()
		}
		if target == nil || !onHousehold(r.Host) {
			http.Error(w, "dial tcp: lookup "+r.Host+": no such host", http.StatusBadGateway)
			return
		}
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
	}))
	t.Cleanup(householdProxy.Close)
	householdProxyURL, err := url.Parse(householdProxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	defaults := http.DefaultTransport.(*http.Transport)
	usual := defaults.Proxy
	defaults.Proxy = func(r *http.Request) (*url.URL, error) {
		if onHousehold(r.URL.Host) {
			return householdProxyURL, nil
		}
		return usual(r)
	}
	t.Cleanup(func() { defaults.Proxy = usual })
	// Where the scene's time goes, written beside the captures (with the
	// browser script's timings.json) so a run's cost is measured, not guessed.
	phases := cameraPhases{t: t, out: out, last: time.Now()}
	frontend, err := url.Parse(vite)
	if err != nil || frontend.Hostname() != "127.0.0.1" {
		t.Fatal("design frontend must be a local Vite server")
	}
	server := httptest.NewUnstartedServer(nil)
	t.Cleanup(server.Close)
	// APPTOWN_DESIGN_HOLD keeps the scene open on a fixed local address for
	// a person to look at, over plain HTTP, instead of photographing it.
	hold := os.Getenv("APPTOWN_DESIGN_HOLD")
	if hold != "" {
		l, err := net.Listen("tcp", hold)
		if err != nil {
			t.Fatal(err)
		}
		server.Listener.Close()
		server.Listener = l
	}
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	runtime := consoletest.NewAgent()
	daemon, appPort, _, refuseRun := newSceneDocker(t, false)
	household.apps.Store(&url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", appPort)})
	// A built image's ID is its own digest, as a daemon gives it, not the
	// shared fake's one placeholder ID (the tour's deploys rely on that
	// placeholder; the camera records its releases' outcomes itself).
	sceneReply := daemon.reply
	daemon.reply = func(args []string) (string, error, bool) {
		if len(args) == 5 && args[0] == "image" && args[1] == "inspect" && args[2] == "--format" && args[3] == "{{.Id}}" {
			return dockerfake.ImageID(args[4]) + "\n", nil, true
		}
		return sceneReply(args)
	}
	cloud := &dofake.API{}
	cloudAPI := httptest.NewServer(cloud.Handler())
	t.Cleanup(cloudAPI.Close)

	// The household's AppTown has its own name, cameraPublicHost, with its
	// apps under apps.<club domain>. Nothing in AppTown dials either name
	// (app checks dial the published port and send the name as Host); the
	// camera's browser maps cameraPublicHost to this server's port. A held
	// scene, opened by a person's own browser, keeps a localhost address.
	publicHost, mainTier := cameraPublicHost, "main=http://apps."+cameraClubDomain
	if hold != "" {
		publicHost, mainTier = "http://apptown.localhost:"+port, fmt.Sprintf("main=http://apps.localhost:%d", appPort)
	}
	// The household runs a released AppTown, so its pages show a release's
	// version where a development build says it is one. This names the
	// build the scene runs as; it is not the published artifact.
	built := version.Release
	version.Release = "v1.0.0"
	t.Cleanup(func() { version.Release = built })
	// Keep bind mounts in TMPDIR, shared with the heavy runner's daemon,
	// and leave room for SSH's Unix socket suffix on attached targets.
	dataDir, err := os.MkdirTemp("", "c-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	a, h := internalApp(t, Config{DataDir: dataDir, PublicHost: publicHost,
		SSHConfigPath: filepath.Join(t.TempDir(), "ssh-config"),
		TierDomains:   []string{"preview=preview.localhost", mainTier}, DockerClient: daemon,
		DigitalOcean: digitalocean.Options{APIBase: cloudAPI.URL},
		ConsoleAgent: runtime, ConsoleCLI: fixtureCLI(t), InferenceURL: "http://worker.test:7779",
		InferenceNetworks: []string{"127.0.0.1/32"}})
	y, m, d := a.cfg.clock.Now().UTC().Date()
	start := time.Date(y, m, d-14, 0, 0, 0, 0, time.UTC)
	moveClock(a, start.Sub(a.cfg.clock.Now()))
	ctx := context.Background()
	a.SyncStock(ctx)
	a.SyncSeedIntegrations(ctx)
	a.SeedTargets(ctx)
	verification.SyncStockScanners(ctx, a.Integrations, stock.FS)
	a.SyncStockDependencies(ctx)
	sceneModel(t, a)
	tourChecks(t, a)
	// The camera's script checks really run, in milliseconds here; the
	// scene's clock then stands for the household's slower machine, as it
	// stands for the days between acts, so a check takes as long as it
	// would there and the timing pages have something to show.
	useScriptTasks(t, a, func(spec execution.TaskSpec) (string, int) {
		out, code := tourScript(spec)
		moveClock(a, cameraCheckTime(spec))
		return out, code
	})
	defineWorkflows(t, a, tourWorkflow)
	phases.mark("app and stock")
	const password = "design-local-password"
	boot := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "sam", password))
	if boot.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", boot.Code, boot.Body.String())
	}
	scene := &tourScene{t: t, a: a, runtime: runtime, password: password, cookies: map[string]*http.Cookie{}, refuseRun: refuseRun}
	for _, user := range []string{"priya", "tom"} {
		scene.call("sam", "POST", "/api/v1/users", map[string]string{"username": user, "role": "member", "password": password})
	}
	scene.play(start)
	phases.mark("story")
	// The camera needs a real, actionable finding for the remediation family.
	// The tour excepts the game's external font; revoke that exception here.
	var exceptions struct {
		Dispositions []struct {
			ID ids.ID `json:"id"`
		} `json:"dispositions"`
	}
	if err := json.Unmarshal(scene.call("sam", "GET", "/api/v1/projects/rocket-garden/dispositions", nil), &exceptions); err != nil {
		t.Fatal(err)
	}
	if len(exceptions.Dispositions) != 1 {
		t.Fatal("camera expects the tour's one font exception")
	}
	scene.call("sam", "DELETE", "/api/v1/dispositions/"+exceptions.Dispositions[0].ID.String(), nil)
	var pending struct {
		Active []struct {
			RunID ids.ID `json:"runId"`
		} `json:"active"`
	}
	if err := json.Unmarshal(scene.call("sam", "GET", "/api/v1/projects/club-newsletter/overview", nil), &pending); err != nil {
		t.Fatal(err)
	}
	for _, run := range pending.Active {
		scene.approve(run.RunID, "sam")
	}
	reviewWorkflow := tourWorkflow
	reviewWorkflow.Workflow.Name = "design-review"
	reviewWorkflow.Spec.Role = "working"
	// The evaluation replays an adopted working lane. Its local execution
	// uses the same script fixture; the project already adopts implement.
	evalWorkflow := reviewWorkflow
	evalWorkflow.Workflow.Name = "implement"
	defineWorkflows(t, a, tourWorkflow, reviewWorkflow, evalWorkflow)
	scene.call("sam", "POST", "/api/v1/workflows/library", map[string]string{"yaml": `apiVersion: apptown.dev/v1
kind: Workflow
metadata:
  name: design-review
  description: Checks a design change in a preview, then asks a person to accept it.
spec:
  kind: lifecycle
  role: working
  stages:
    - name: check
      steps:
        - name: tests
          uses: script
          with:
            run: sh checks/tests.sh
        - name: style
          uses: script
          with:
            run: sh checks/style.sh
        - name: accept
          uses: approval
          with:
            means: I have read what changed and the checks, and it does what the change says.
`})
	// Club Newsletter's main deployment runs each change soon after it
	// ships: its first release once the app declares its parts, then one
	// per change. Like the history below, the camera records each outcome
	// on its stand-in host.
	newsletter, err := a.Projects.BySlug(ctx, "club-newsletter")
	if err != nil {
		t.Fatal(err)
	}
	newsletterMain, err := a.Deployment.Deployments.ByName(ctx, newsletter.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	releaseShipped := func(i int, notes string) {
		t.Helper()
		head, err := gitexec.Run(ctx, a.projectRepoPath(newsletter.Slug), "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		revision := strings.TrimSpace(string(head))
		scene.later(12 * time.Minute)
		rel, err := a.Deployment.Releases.Start(ctx, deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: newsletter.ID, DeploymentID: newsletterMain.ID, Deployment: "main", Revision: revision, Actor: "priya"}, notes)
		if err != nil {
			t.Fatal(err)
		}
		at, _, err := a.Deployment.Outcomes.Admit(ctx, deployment.Start{DeploymentID: newsletterMain.ID, TargetID: newsletterMain.TargetID, Key: fmt.Sprintf("design-shipped-%d", i), Inputs: deployment.Inputs{ReleaseID: rel.ID, Revision: revision, Applied: cameraApplied}})
		if err != nil {
			t.Fatal(err)
		}
		scene.later(100 * time.Second)
		if _, err := a.Deployment.Outcomes.Complete(ctx, deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy",
			Handle: map[string]any{"container": docker.ByKey(newsletterMain.ID).App(), "hostPort": 43210}}}); err != nil {
			t.Fatal(err)
		}
	}
	// Extend one disposable project through public change operations. The camera uses these same records; script checks execute locally.
	for i, title := range []string{"Add club contact details", "Publish membership fees", "Update the committee list", "Add the Saturday meeting point", "Publish the annual meeting date"} {
		scene.later(20 * time.Minute)
		c := scene.open("tom", "club-newsletter", title, title+" on the club website.")
		scene.write(c, map[string]string{fmt.Sprintf("public/update-%d.html", i): "<!doctype html><title>" + title + "</title><h1>" + title + "</h1>"})
		if i == 0 {
			scene.write(c, map[string]string{".apptown/app.yaml": `version: 2
app:
  site:
    static: public
  archive:
    image: registry.hilltopcycling.club/club/newsletter-archive:1
    public: true
    ports: ["8080"]
    dataDir: /data
`})
		}
		scene.commit(c, "tom", title)
		if i == 0 {
			var started struct {
				Run struct {
					ID ids.ID `json:"id"`
				} `json:"run"`
			}
			if err := json.Unmarshal(scene.call("tom", "POST", "/api/v1/changes/"+c.ID.String()+"/runs", map[string]string{"workflow": "implement"}), &started); err != nil {
				t.Fatal(err)
			}
			a.Work.Wait()
			scene.approve(started.Run.ID, "priya")
		}
		scene.ship(c, "tom", "priya", 5*time.Minute)
		releaseShipped(i, title)
	}
	// A change stopped at once (already fixed) and an idle change stopped after
	// several days expose noise and misleading wall times on Home.
	duplicate := scene.open("sam", "club-newsletter", "Fix the broken ride calendar link", "The front page link to the ride calendar goes nowhere.")
	if _, err := a.Work.Abandon(asPerson("sam"), workflow.StopWork{ChangeID: duplicate.ID, Reason: "Already fixed by Fix the ride calendar links"}); err != nil {
		t.Fatal(err)
	}
	scene.later(-127*time.Hour - 24*time.Minute)
	idle := scene.open("tom", "club-newsletter", "Replace the old ride archive", "An idea left idle before it was stopped.")
	scene.call("tom", "POST", "/api/v1/changes/"+idle.ID.String()+"/runs", map[string]string{"workflow": "design-review"})
	a.Work.Wait()
	scene.later(127*time.Hour + 24*time.Minute)
	if _, err := a.Work.Abandon(asPerson("sam"), workflow.StopWork{ChangeID: idle.ID, Reason: "No longer needed"}); err != nil {
		t.Fatal(err)
	}
	scene.later(10 * time.Minute)
	broken := scene.open("tom", "club-newsletter", "Add photos from the spring ride", "Add ride photos with descriptions for people who cannot see them.")
	scene.write(broken, map[string]string{"public/photos.html": "<!doctype html><title>Spring ride</title><h1>Spring ride</h1><img src='ride.jpg'>", "public/ride.jpg": "local image fixture"})
	scene.commit(broken, "tom", "Spring ride photos")
	scene.call("tom", "POST", "/api/v1/changes/"+broken.ID.String()+"/runs", map[string]string{"workflow": "design-review"})
	a.Work.Wait()
	scene.later(10 * time.Minute)
	december := scene.open("tom", "club-newsletter", "Publish the December ride calendar", "Add the December ride calendar and meeting point.")
	scene.write(december, map[string]string{"public/december.html": `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>December rides</title>
  <link rel="stylesheet" href="style.css">
</head>
<body>
  <nav><a href="index.html">Club newsletter</a></nav>
  <h1>December rides</h1>
  <p>Saturday rides leave the station at 9 am.</p>
  <p>Bring lights for the return trip.</p>
  <h2>Meeting point</h2>
  <p>Meet outside the main station entrance.</p>
</body>
</html>
`})
	scene.commit(december, "tom", "December ride calendar")
	// Running production on a test daemon; the preview transport refuses SSH. ObserveRuntime records unreachable, never a stopped app.
	project, err := a.Projects.BySlug(ctx, "club-newsletter")
	if err != nil {
		t.Fatal(err)
	}
	productionHost, err := a.Deployment.Targets.ByName(ctx, deployment.BuiltInTargetName)
	if err != nil {
		t.Fatal(err)
	}
	scene.call("sam", "PUT", "/api/v1/secrets/DESIGN_PREVIEW_KEY", map[string]string{"value": `-----BEGIN PRIVATE KEY-----\ndisposable test transport marker\n-----END PRIVATE KEY-----`})
	previewHost, err := a.Deployment.Targets.Put(ctx, &deployment.Target{Name: "design-preview", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetUnreachable, Address: map[string]any{"host": "ssh://deploy@192.168.1.40", "keyFrom": "DESIGN_PREVIEW_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	prod, err := a.Deployment.Deployments.ByName(ctx, project.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.EditDeploymentLabel(asPerson("sam"), project.ID, prod.ID, prod.DisplayLabel, "Production"); err != nil {
		t.Fatal(err)
	}
	// The December change's preview runs on the household's own host. The
	// old laptop (design-preview) is off; the club's noticeboard copy,
	// recorded while it answered, is on it.
	if err := a.Deployment.Deployments.RecordPreview(ctx, project.ID, december.ID, productionHost.ID, cameraPreviewContainer, 43211, "https://preview.hilltopcycling.club", nil); err != nil {
		t.Fatal(err)
	}
	noticeboard, err := a.Deployment.Deployments.EnsureStanding(ctx, project.ID, "noticeboard", previewHost.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	// The garden shed's machine answers, but its Docker cannot read its
	// own disk: the club-rooms display runs there.
	gardenShed, err := a.Deployment.Targets.Put(ctx, &deployment.Target{Name: "garden-shed", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady, Address: map[string]any{"host": "ssh://deploy@" + gardenShedAddress, "keyFrom": "DESIGN_PREVIEW_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	clubRooms, err := a.Deployment.Deployments.EnsureStanding(ctx, project.ID, "club-rooms-display", gardenShed.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	previews, err := a.Deployment.Deployments.Previews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range previews {
		if dep.ChangeID == december.ID {
			daemon.markStartedWithLimits(cameraPreviewContainer, 0, 0, map[string]string{dockerlabel.Class: "preview", dockerlabel.Owner: dep.ID.String(), dockerlabel.Deployment: string(dep.Placement().Label)})
			if _, err := a.Deployment.EditDeploymentLabel(asPerson("sam"), project.ID, dep.ID, dep.DisplayLabel, "Preview"); err != nil {
				t.Fatal(err)
			}
		}
	}
	head, err := gitexec.Run(ctx, a.projectRepoPath(project.Slug), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range []*deployment.Deployment{prod, noticeboard, clubRooms} {
		at, _, err := a.Deployment.Outcomes.Admit(ctx, deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "design-" + dep.Name, Inputs: deployment.Inputs{Revision: strings.TrimSpace(string(head))}})
		if err != nil {
			t.Fatal(err)
		}
		observed := deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy", URL: "https://" + dep.Name + ".hilltopcycling.club"}
		if dep != prod {
			observed.Handle = map[string]any{"container": docker.ByKey(dep.ID).App(), "hostPort": 8080}
		}
		_, err = a.Deployment.Outcomes.Complete(ctx, deployment.Completion{Attempt: at, Observed: observed})
		if err != nil {
			t.Fatal(err)
		}
		if dep != prod {
			continue
		}
		if _, err := a.Deployment.Deployments.ReserveHostPort(ctx, project.ID, dep.Name, docker.ByKey(dep.ID).App(), 43210); err != nil {
			t.Fatal(err)
		}
	}
	// Export the archive volume through the test daemon's stream, then let
	// TakeBackup hash, store and record those bytes through the owner operation.
	if _, err := daemon.run("volume", "create", docker.ByKey(prod.ID).DataVolume()); err != nil {
		t.Fatal(err)
	}
	// Many deployments without limits reproduce a busy host. Capacity and
	// runtime still pass through their owners, using the disposable daemon.
	// They have the names a club gives its trial and staging copies.
	appContainer := func(dep *deployment.Deployment) {
		daemon.markStartedWithLimits(docker.ByKey(dep.ID).App(), 0, 0, map[string]string{dockerlabel.Class: "deployment", dockerlabel.Owner: dep.ID.String(), dockerlabel.Deployment: string(dep.Placement().Label)})
	}
	appContainer(prod)
	captureDeps := []*deployment.Deployment{prod}
	for i, name := range cameraDeployments {
		dep, err := a.Deployment.Deployments.EnsureStanding(ctx, project.ID, name, productionHost.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "non-production"})
		if err != nil {
			t.Fatal(err)
		}
		captureDeps = append(captureDeps, dep)
		appContainer(dep)
		if _, err := daemon.run("volume", "create", docker.ByKey(dep.ID).DataVolume()); err != nil {
			t.Fatal(err)
		}
		// Each copy has run a release for a day or more: its first, made
		// days apart, recorded on the stand-in host as Production's are.
		ago := time.Duration(30+i*17) * time.Hour
		scene.later(-ago)
		first, err := a.Deployment.Releases.Start(ctx, deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: project.ID, DeploymentID: dep.ID, Deployment: dep.Name, Revision: strings.TrimSpace(string(head)), Actor: "sam"}, cameraDeploymentNotes[name])
		if err != nil {
			t.Fatal(err)
		}
		at, _, err := a.Deployment.Outcomes.Admit(ctx, deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "camera-first-" + name, Inputs: deployment.Inputs{ReleaseID: first.ID, Revision: strings.TrimSpace(string(head))}})
		if err != nil {
			t.Fatal(err)
		}
		scene.later(95 * time.Second)
		if _, err := a.Deployment.Outcomes.Complete(ctx, deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy", Handle: map[string]any{"container": docker.ByKey(dep.ID).App(), "hostPort": 43221 + i}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Deployment.Deployments.ReserveHostPort(ctx, project.ID, dep.Name, docker.ByKey(dep.ID).App(), 43221+i); err != nil {
			t.Fatal(err)
		}
		daemon.markStartedWithLimits(docker.ByKey(dep.ID).App(), 0, 0, map[string]string{dockerlabel.Generation: fmt.Sprint(at.Generation)})
		scene.later(ago - 95*time.Second)
	}
	// The household's other apps run their last landing on the same host,
	// recorded on the stand-in host as Club Newsletter's are.
	for i, slug := range []string{"lunch-orders", "rocket-garden"} {
		other, err := a.Projects.BySlug(ctx, slug)
		if err != nil {
			t.Fatal(err)
		}
		dep, err := a.Deployment.Deployments.ByName(ctx, other.ID, "main")
		if err != nil {
			t.Fatal(err)
		}
		landed, err := gitexec.Run(ctx, a.projectRepoPath(slug), "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		at, _, err := a.Deployment.Outcomes.Admit(ctx, deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "camera-running-" + slug, Inputs: deployment.Inputs{Revision: strings.TrimSpace(string(landed))}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Deployment.Outcomes.Complete(ctx, deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy", Handle: map[string]any{"container": docker.ByKey(dep.ID).App(), "hostPort": 43240 + i}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Deployment.Deployments.ReserveHostPort(ctx, other.ID, dep.Name, docker.ByKey(dep.ID).App(), 43240+i); err != nil {
			t.Fatal(err)
		}
		daemon.markStartedWithLimits(docker.ByKey(dep.ID).App(), 0, 0, map[string]string{dockerlabel.Class: "deployment", dockerlabel.Owner: dep.ID.String(), dockerlabel.Generation: fmt.Sprint(at.Generation)})
		if slug == "lunch-orders" { // its database, which its contract needs
			names, err := dep.HostNames()
			if err != nil {
				t.Fatal(err)
			}
			daemon.markStartedWithLimits(names.Service("postgres"), 0, 0, map[string]string{dockerlabel.Class: "deployment", dockerlabel.Owner: dep.ID.String(), dockerlabel.Generation: fmt.Sprint(at.Generation)})
		}
	}
	// The household's other services share the host. AppTown did not start
	// them, and the photo library asks for more memory than the host has.
	for _, name := range cameraOtherContainers {
		daemon.markStarted(name)
	}
	daemon.markStartedWithLimits("photo-library", 4541927916, 0, nil) // 4.23 GiB asked for.
	// The kernel reader and daemon describe the same 3.8 GiB, two-core
	// machine (an old mini PC).
	kernelSamples := 0
	daemon.disk.Kernel = func(script string) (int, []byte, string) {
		if script == "cat /proc/meminfo" {
			return 0, []byte("MemTotal: 3984588 kB\nMemAvailable: 1677722 kB\n"), ""
		}
		kernelSamples++
		return 0, []byte(fmt.Sprintf("cpu %d 0 0 %d 0 0 0 0\ncpu0 0 0 0 0 0 0 0 0\ncpu1 0 0 0 0 0 0 0 0\n", kernelSamples*40, kernelSamples*60)), ""
	}
	// What each container uses: Production's parts as the demo's app uses,
	// the others fixed, distinct amounts by their sorted names.
	prodNames, err := prod.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	partUse := map[string]string{
		docker.ByKey(prod.ID).App():             "87.1MiB / 512MiB\t0.31%",
		prodNames.Service("newsletter-archive"): "41.6MiB / 256MiB\t0.02%",
		prodNames.Service("postgres"):           "62.9MiB / 256MiB\t0.00%",
	}
	var backupCopies atomic.Int64
	sceneDockerCommand := daemon.command
	daemon.command = func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
		if strings.Contains(host, "garden-shed") { // AppTown's SSH alias for the target names it
			// The garden shed's daemon answers what it is, then fails every
			// question about its containers, images and disk, as a daemon
			// with a failing disk does.
			if len(req.Args) == 3 && req.Args[0] == "info" && strings.Contains(req.Args[2], "MemTotal") {
				return []byte("2072321024\t2\t/var/lib/docker\n"), nil
			}
			if len(req.Args) == 3 && req.Args[0] == "info" && req.Args[2] == "{{json .}}" {
				return json.Marshal(map[string]any{"ID": "garden-shed", "MemTotal": 2072321024, "NCPU": 2, "DockerRootDir": "/var/lib/docker", "CgroupVersion": "2"})
			}
			return nil, errors.New("Error response from daemon: open /var/lib/docker/containers: input/output error")
		}
		if host != "" {
			return nil, deploy.Unreachable(errors.New("ssh: connect to host 192.168.1.40 port 22: No route to host"))
		}
		if len(req.Args) == 3 && req.Args[0] == "info" && strings.Contains(req.Args[2], "MemTotal") {
			return []byte("4080218112\t2\t/var/lib/docker\n"), nil // 3.8 GiB host, independent of container usage.
		}
		if len(req.Args) == 3 && req.Args[0] == "info" && req.Args[2] == "{{json .}}" {
			return json.Marshal(map[string]any{"ID": "fake-daemon", "MemTotal": 4080218112, "NCPU": 2, "DockerRootDir": "/var/lib/docker", "CgroupVersion": "2"})
		}
		if len(req.Args) > 3 && req.Args[0] == "stats" {
			var running []string
			daemon.mu.Lock()
			for name, up := range daemon.started {
				if up {
					running = append(running, name)
				}
			}
			daemon.mu.Unlock()
			slices.Sort(running)
			names := req.Args[4:]
			if len(names) == 0 {
				names = running
			}
			var rows []string
			for _, name := range names {
				i := slices.Index(running, name)
				if i < 0 {
					continue
				}
				use, ok := partUse[name]
				if !ok {
					use = fmt.Sprintf("%.1fMiB / 3.8GiB\t%.2f%%", 18+float64((i*37)%123)+float64(i%10)/10, float64((i*7)%40)/100)
				}
				rows = append(rows, name+"\t"+use)
			}
			return []byte(strings.Join(rows, "\n")), nil
		}
		if req.Stdin != nil && len(req.Args) > 0 && (req.Args[0] == "run" || req.Args[0] == "exec") {
			// A restore writing a copy back: the volume takes what it is sent.
			_, err := io.Copy(io.Discard, req.Stdin)
			return nil, err
		}
		if req.Stdout != nil && len(req.Args) > 0 && (req.Args[0] == "run" || req.Args[0] == "exec") {
			// A backup copy: a few hundred kilobytes, a different size each time.
			n := int(backupCopies.Add(1))
			_, err := containerresulttest.WriteString(req.Stdout, strings.Repeat("club newsletter archive\n", 7000+(n*3100)%26000))
			return nil, err
		}
		return sceneDockerCommand(ctx, host, req)
	}
	// Real local release receipts and exported backups reproduce the volume
	// of automated copies seen with five releases. The provider is the test daemon.
	var delivered *tourChange
	var deliveredRun ids.ID
	revisions, err := gitexec.Run(ctx, a.projectRepoPath(project.Slug), "rev-list", "--reverse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// Only commits that declare the app's runtime contract can be released.
	var history []string
	for _, rev := range strings.Fields(string(revisions)) {
		if _, err := gitexec.Run(ctx, a.projectRepoPath(project.Slug), "cat-file", "-e", rev+":.apptown/app.yaml"); err == nil {
			history = append(history, rev)
		}
	}
	releaseNotes := []string{"Weekly newsletter update", "Fix the ride calendar links", "New committee page", "Spring ride photos and captions", "Membership fees for next year", "Correct the meeting point map", "Larger text on the front page"}
	for i := 0; i < 15; i++ {
		if i == 14 {
			delivered = scene.open("tom", "club-newsletter", "Publish the winter ride times", "Publish the winter ride times on the club website.")
			// The ride pages format their dates with dayjs: the project
			// names it in package.json and npm's lock file, which the
			// source scan's bill of materials lists.
			scene.write(delivered, map[string]string{"public/winter.html": "<!doctype html><title>Winter rides</title><h1>Winter rides at 9 am</h1>",
				"package.json": cameraPackageJSON, "package-lock.json": cameraPackageLock})
			scene.commit(delivered, "tom", "Winter ride times")
			deliveredRun = scene.ship(delivered, "tom", "priya", 5*time.Minute)

			head, err = gitexec.Run(ctx, a.projectRepoPath(project.Slug), "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			continue // Keep the actual landing serving, including its release narrative.
		}
		scene.later(time.Minute)
		// Each release is a later commit of the project's history.
		revision := history[max(0, len(history)-14+i)]
		draft := deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: project.ID, DeploymentID: prod.ID,
			Deployment: "main", Revision: revision, Actor: "sam"}
		image := ""
		if i == 0 && os.Getenv("APPTOWN_DESIGN_AUDIT") != "" {
			// A disposable daemon-image bundle exercises long image identifiers
			// without claiming a registry publication or a portable build receipt.
			image = "sha256:" + strings.Repeat("a", 64)
			draft.BundleID, err = a.Deployment.Bundles.Cut(ctx, project.ID, draft.Revision,
				map[string]string{"web": image}, map[string]string{}, []byte("local camera contract"), deployment.BundleOrigin{TargetID: prod.TargetID})
			if err != nil {
				t.Fatal(err)
			}
			draft.Source = deployment.ReleaseFromBundle
		}
		rel, err := a.Deployment.Releases.Start(ctx, draft, releaseNotes[i%len(releaseNotes)])
		if err != nil {
			t.Fatal(err)
		}
		at, _, err := a.Deployment.Outcomes.Admit(ctx, deployment.Start{DeploymentID: prod.ID, TargetID: prod.TargetID,
			Key: fmt.Sprintf("design-release-%d", i), Inputs: deployment.Inputs{ReleaseID: rel.ID, BundleID: draft.BundleID, ImageDigest: image, Revision: revision, Applied: cameraApplied}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Deployment.Outcomes.Complete(ctx, deployment.Completion{Attempt: at, Observed: deploy.Observation{
			Generation: at.Generation, Rollout: "active", Health: "healthy",
			Handle: map[string]any{"container": docker.ByKey(prod.ID).App(), "hostPort": 43210},
		}}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.DataProtection.BeforeRelease(ctx, project.ID, "main", false); err != nil {
			t.Fatal(err)
		}
		for _, trigger := range []string{"release", "scheduled"} {
			if _, err := (dataprotection.JobWorker{Service: a.DataProtection, Projects: a.Projects}).Backup(ctx, project.ID.String()+"/main", map[string]any{"trigger": trigger}); err != nil {
				t.Fatal(err)
			}
		}
	}
	failedRelease, err := a.Deployment.Releases.Start(ctx, deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: project.ID, DeploymentID: prod.ID, Deployment: "main", Revision: strings.TrimSpace(string(head)), Actor: "sam"}, "local failed release")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Outcomes.FailUnstartedRelease(ctx, failedRelease.ID, errors.New("The app image could not be downloaded. Check the image store connection.")); err != nil {
		t.Fatal(err)
	}

	if _, err := workflowFollowUps(a).Complete(ctx, fmt.Sprintf("%s/release", deliveredRun), map[string]any{"approvedBy": "priya"}); err != nil {
		t.Fatal(err)
	}
	releases, err := a.Deployment.Releases.LandingRequests(ctx, project.ID, delivered.ID)
	if err != nil || len(releases) != 1 {
		t.Fatalf("camera serving release: %+v %v", releases, err)
	}
	// Like the historical releases above, this camera records a completed
	// outcome on its stand-in daemon. An empty URL leaves its notes available
	// for the change narrative published below. Production runs the way a
	// small real app does: a front door, a second part and a database, each
	// held to a limit.
	served := deployment.AppliedProfile{Profile: "default", How: "default",
		Parts: map[string]deployment.AppliedPart{
			"web":                {Public: true, Resources: appcontract.Resources{MemoryBytes: 512 << 20, CPUs: 0.5}},
			"newsletter-archive": {Resources: appcontract.Resources{MemoryBytes: 256 << 20, CPUs: 0.25}},
		},
		Services: map[string]deployment.AppliedPart{"postgres": {Need: true, Resources: appcontract.Resources{MemoryBytes: 256 << 20, CPUs: 0.5}}}}
	attempt, _, err := a.Deployment.Outcomes.Admit(ctx, deployment.Start{DeploymentID: prod.ID, TargetID: prod.TargetID, Key: "camera-serving", Inputs: deployment.Inputs{ReleaseID: releases[0].ID, BundleID: releases[0].BundleID, Revision: strings.TrimSpace(string(head)), Applied: served}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Outcomes.Complete(ctx, deployment.Completion{Attempt: attempt, Observed: deploy.Observation{Generation: attempt.Generation, Rollout: "active", Health: "healthy", Handle: map[string]any{"container": docker.ByKey(prod.ID).App(), "hostPort": 43210}}}); err != nil {
		t.Fatal(err)
	}

	// The camera completes this generation without a daemon run. Its
	// containers carry that same generation and their limits when the host
	// is observed, and use what the demo's app uses.
	generation := map[string]string{dockerlabel.Generation: fmt.Sprint(attempt.Generation)}
	for _, part := range []struct {
		container        string
		memory, nanoCPUs int64
	}{
		{docker.ByKey(prod.ID).App(), 512 << 20, 5e8},
		{prodNames.Service("newsletter-archive"), 256 << 20, 2.5e8},
		{prodNames.Service("postgres"), 256 << 20, 5e8},
	} {
		daemon.markStartedWithLimits(part.container, part.memory, part.nanoCPUs, generation)
	}

	// Publish a narrative in the shape the summary writer produces (title,
	// whether the request was met, what was made); it fills the empty release
	// notes and belongs in the release record, not the overview card.
	source, err := a.Work.NarrativeSource(asPerson("sam"), delivered.ID)
	if err != nil {
		t.Fatal(err)
	}

	draft, err := workflow.ParseNarrative("TITLE: Publish the winter ride times\nASKED: yes\n\nAdds the winter ride times page: each Saturday's 9 am start and route, linked from the home page.")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Work.PublishNarrative(asPerson("sam"), workflow.NarrativePublication{Source: source,
		Draft: draft,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := a.DataProtection.TakeBackup(ctx, project.ID, "main", "sam"); err != nil {
		t.Fatal(err)
	}

	scene.land(december, "tom", map[string]string{"tests": "pass", "style": "pass"})

	var overview struct {
		Active []struct {
			ChangeID string `json:"changeId"`
			Title    string `json:"title"`
		} `json:"active"`
	}
	if err := json.Unmarshal(scene.call("sam", "GET", "/api/v1/projects/club-newsletter/overview", nil), &overview); err != nil {
		t.Fatal(err)
	}
	var approvalChange string
	for _, run := range overview.Active {
		if run.Title == "Publish the December ride calendar" {
			approvalChange = run.ChangeID
		}
	}
	if approvalChange == "" {
		t.Fatal("the local scene has no November newsletter approval")
	}
	// A real retained-resource record reproduces the cloud target's warning.
	scene.call("sam", "PUT", "/api/v1/secrets/DESIGN_CLOUD_KEY", map[string]string{"value": "disposable-cloud-key"})
	if _, err := a.Integrations.Bindings.Save(ctx, "design-cloud", digitalocean.CapCloudAccount, "digitalocean", mustJSON(map[string]any{"tokenFrom": "DESIGN_CLOUD_KEY", "region": "syd", "registry": "design"}), "sam"); err != nil {
		t.Fatal(err)
	}
	a.SeedTargets(ctx)
	target, err := a.targets.ByName(ctx, "design-cloud")
	if err != nil {
		t.Fatal(err)
	}
	oldCloud, err := a.Deployment.Deployments.EnsureStanding(ctx, project.ID, "old-cloud", target.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	// The account's container registry is on the starter tier, whose one
	// repository the newsletter already fills: a reading with its reason.
	cloud.SeedRegistry("design", "starter", 1, "club-newsletter")
	cloud.SeedDatabase(map[string]any{"id": "old-club-db", "name": "Club database", "engine": "pg", "status": "online", "region": "syd", "version": "16"})
	if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
		_, err := deployment.RemoveRecordedIn(ctx, tx, oldCloud, false, deployment.RemovalSettlement{Target: target.Name, By: access.Principal{Name: "sam"}, At: a.cfg.clock.Now(), Taken: deploy.TakenDown{Left: []deploy.Left{{What: "postgres", Data: true, Handle: map[string]any{"id": "old-club-db", "name": "Club database"}, Why: "Kept for review"}}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := (inventoryRefresh{targets: a.targets, providers: a.targetProviders, inventory: a.Deployment.Inventory, cache: a.inventory}).Run(ctx, target.Name, nil); err != nil {
		t.Fatal(err)
	}

	// The household gives its apps AI models through an OpenRouter
	// provisioning key (Services › AI models), kept apart from AppTown's own
	// model provider. The key API is the OpenRouter fake.
	appModels := openrouterfake.New("design-provisioning-key")
	t.Cleanup(appModels.Close)
	scene.call("sam", "PUT", "/api/v1/secrets/OPENROUTER_PROVISIONING_KEY", map[string]string{"value": "design-provisioning-key"})
	if _, err := a.Integrations.Bindings.Save(ctx, "app-models", integration.CapLLM, "openrouter", mustJSON(map[string]any{"url": appModels.Base, "provisioningKeyFrom": []string{"OPENROUTER_PROVISIONING_KEY"}, "maxLimit": 20, "defaultLimit": 2, "previewLimit": 1}), "sam"); err != nil {
		t.Fatal(err)
	}

	scene.call("sam", "PUT", "/api/v1/secrets/DESIGN_REGISTRY_KEY", map[string]string{"value": "disposable-registry-key"})
	if _, err := a.Integrations.Bindings.Save(ctx, "design-registry", integration.CapContainerRegistry, "service", mustJSON(map[string]any{"url": "registry." + cameraClubDomain, "group": "club", "username": "sam", "tokenFrom": "DESIGN_REGISTRY_KEY"}), "sam"); err != nil {
		t.Fatal(err)
	}

	var deliveredView struct {
		Runs []struct {
			ID ids.ID `json:"id"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(scene.call("sam", "GET", "/api/v1/changes/"+delivered.ID.String(), nil), &deliveredView); err != nil {
		t.Fatal(err)
	}
	if len(deliveredView.Runs) == 0 {
		t.Fatal("capture has no completed run for old scan reports")
	}
	// Attach genuine report artifacts through the owner; their older date
	// reproduces clean reports whose code has changed since they ran.
	moveClock(a, -time.Hour)
	if _, err := a.Work.AttachArtifact(asPerson("sam"), deliveredView.Runs[0].ID, workflow.ArtifactDraft{Kind: "scan-report", Name: "gitleaks", Body: `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"gitleaks"}},"results":[]}]}`}); err != nil {
		t.Fatal(err)
	}
	moveClock(a, time.Hour)
	// The queue has no command to import completed history. River's test
	// driver records only these disposable completed rows; no AppTown work
	// is claimed to have run by a domain owner.
	for i := 0; i < 40; i++ {
		kind := []string{"protection-observation", deployment.RecoveryJob, deployment.MarkInstallationJob}[i%3]
		id, err := a.jobs.Enqueue(ctx, kind, fmt.Sprintf("design-history-%d", i%7), nil, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		at := a.cfg.clock.Now().Add(-time.Duration(40-i) * time.Minute)
		if _, err := riverdatabasesql.New(a.DB.Pool()).GetExecutor().JobUpdateFull(ctx, &riverdriver.JobUpdateFullParams{ID: id, StateDoUpdate: true, State: rivertype.JobStateCompleted, AttemptDoUpdate: true, Attempt: 1, FinalizedAtDoUpdate: true, FinalizedAt: &at}); err != nil {
			t.Fatal(err)
		}
	}

	// Several browser connections by one person must remain distinguishable,
	// with every individual disconnect action available after grouping.
	for _, name := range []string{"ChatGPT", "ChatGPT", "Claude", "Cursor", "Home Assistant"} {
		client, err := a.Access.RegisterOAuthClient(ctx, identity.Registration{Name: name, RedirectURIs: []string{"http://127.0.0.1:9999/callback"}})
		if err != nil {
			t.Fatal(err)
		}
		verifier, challenge := pkce()
		browser := identity.BrowserAuthorization{User: "sam", Binding: "camera", Request: identity.AuthorizationRequest{ClientID: client.ID, RedirectURI: client.RedirectURIs[0], Scope: "apptown:user:read apptown:change:create apptown:change:write", Challenge: challenge, Method: "S256", Resource: identity.AudienceUser}}
		prepared, err := a.Access.PrepareConsent(ctx, browser)
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.Access.DecideConsent(ctx, identity.ConsentDecision{Browser: browser, Token: prepared.Token, Choice: identity.ConsentAllow})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Access.ExchangeCode(ctx, identity.CodeExchange{Code: result.Code, Verifier: verifier, ClientID: client.ID, RedirectURI: browser.Request.RedirectURI, AuthMethod: "internal"}); err != nil {
			t.Fatal(err)
		}
		moveClock(a, time.Minute)
	}

	// The deployment camera shows a real request waiting for another person's
	// assent. Sam can approve Tom's request, and no job is created before that.
	for _, spec := range []identity.Spec{
		{SubjectType: "user", Subject: "sam", Role: access.RoleReleaseManager, Scope: access.ScopeProject, ScopeID: project.ID.String(), By: "camera"},
		{SubjectType: "user", Subject: "tom", Capability: string(access.CapDeploymentDeploy), Scope: access.ScopeProject, ScopeID: project.ID.String(), By: "camera"},
		{SubjectType: "user", Subject: "tom", Capability: string(access.CapReleasePromote), Scope: access.ScopeProject, ScopeID: project.ID.String(), By: "camera"},
	} {
		if _, err := a.Access.Grant(ctx, spec, ""); err != nil {
			t.Fatal(err)
		}
	}
	prod, err = a.Deployment.Deployments.ByID(ctx, prod.ID)
	if err != nil {
		t.Fatal(err)
	}
	sam := verifiedContext(ctx, access.Principal{ID: testPrincipalOf(t, a.DB, "sam"), Kind: "user", Name: "sam", Via: "header", Credential: identity.HeaderCredential("sam")})
	if _, err := a.Deployment.SetFeed(sam, project.ID, prod.Name, deployment.FeedSettings{Revision: prod.FeedRevision, Feed: prod.Feed, ApprovalRole: access.RoleReleaseManager}); err != nil {
		t.Fatal(err)
	}
	last, err := a.Deployment.Releases.LastReleasedForDeployment(ctx, prod.ID)
	if err != nil {
		t.Fatal(err)
	}
	tom := verifiedContext(ctx, access.Principal{ID: testPrincipalOf(t, a.DB, "tom"), Kind: "user", Name: "tom", Via: "header", Credential: identity.HeaderCredential("tom")})
	waitingRelease, err := a.Deployment.RequestRelease(tom, deployment.DeploySelection{ProjectID: project.ID, DeploymentName: prod.Name, BundleID: last.BundleID})
	if err != nil || waitingRelease.State.String() != "waiting for approval" {
		t.Fatalf("camera approval request: %+v %v", waitingRelease, err)
	}
	moveClock(a, -a.cfg.clock.ahead)
	// Three new releases: members-preview's is being deployed, held at a
	// provider milestone (the camera moves it from stage to stage, one
	// stage's pages at a time, through cameraStage below); staging's has
	// just gone live, after 1m 40s, and archive-migration's failed to start.
	// There is no owner operation to pause a real job at an arbitrary
	// provider milestone.
	// sql-guarantee: only this camera fixture supplies the persisted milestone;
	// all browser reads and outcome settlement use the production owners.
	var heldRelease ids.ID
	for i, stage := range []deploy.DeployStage{deploy.Building, deploy.BackingUp, deploy.Building, deploy.Starting, deploy.Checking, deploy.Live, deploy.Starting} {
		if i >= 1 && i <= 4 {
			continue // these copies keep running their first release
		}
		dep := captureDeps[i+1]
		began := 20*time.Second + time.Duration(i)*90*time.Second
		if stage == deploy.Live {
			began = 100 * time.Second
		}
		moveClock(a, -began)
		rel, err := a.Deployment.Releases.Start(ctx, deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, BundleID: last.BundleID, ProjectID: project.ID, DeploymentID: dep.ID, Deployment: dep.Name, Revision: strings.TrimSpace(string(head)), Actor: "sam"}, map[string]string{"members-preview": "Members' pages with the winter ride times", "staging": "Winter ride times", "archive-migration": "Archive moved to its own part"}[dep.Name])
		if err != nil {
			t.Fatal(err)
		}
		var attempt deployment.Attempt
		if stage == deploy.Live {
			if attempt, _, err = a.Deployment.Outcomes.Admit(ctx, deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "camera-progress-live", Inputs: deployment.Inputs{ReleaseID: rel.ID, Revision: strings.TrimSpace(string(head))}}); err != nil {
				t.Fatal(err)
			}
		}
		moveClock(a, began)
		// sql-guarantee: hold the release at a provider milestone; no owner operation pauses jobs.
		if _, err := a.DB.SQL().ExecContext(ctx, `UPDATE releases SET stage=$1, stage_entered_at=$2 WHERE id=$3`, stage.String(), a.cfg.clock.Now().Add(-15*time.Second), rel.ID); err != nil {
			t.Fatal(err)
		}
		if stage == deploy.Live {
			if _, err := a.Deployment.Outcomes.Complete(ctx, deployment.Completion{Attempt: attempt, Observed: deploy.Observation{Generation: attempt.Generation, Rollout: "active", Health: "healthy", Handle: map[string]any{"container": docker.ByKey(dep.ID).App(), "hostPort": 43221 + i}}}); err != nil {
				t.Fatal(err)
			}
			daemon.markStartedWithLimits(docker.ByKey(dep.ID).App(), 0, 0, map[string]string{dockerlabel.Generation: fmt.Sprint(attempt.Generation)})
		}
		if i == 6 {
			if _, err := a.Deployment.Outcomes.FailUnstartedRelease(ctx, rel.ID, errors.New("The app container exited during startup.")); err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			heldRelease = rel.ID
		}
	}
	if err := a.Deployment.ObserveRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	// Every copy has a backup but spring-edition, which never had one.
	// print-layout was stopped by Sam; ride-calendar-trial's app exited on
	// the host by itself.
	for _, dep := range captureDeps[1:] {
		if dep.Name == "spring-edition" {
			continue
		}
		if _, err := a.DataProtection.TakeBackup(ctx, project.ID, dep.Name, "sam"); err != nil {
			t.Fatal(err)
		}
	}
	scene.call("sam", "POST", "/api/v1/projects/club-newsletter/deployments/print-layout/stop", nil)
	for _, dep := range captureDeps {
		if dep.Name == "ride-calendar-trial" {
			daemon.mu.Lock()
			daemon.started[docker.ByKey(dep.ID).App()] = false
			daemon.mu.Unlock()
		}
	}
	if err := a.Deployment.ObserveRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("APPTOWN_DESIGN_AUDIT") != "" {
		// Deployment without a workflow run reproduces the activity/state
		// contradiction. The source commit and deployment are real local records.
		kitchen, revision, _ := fleetRepo(t, a, "kitchen-calendar", map[string]string{"index.html": "<h1>Kitchen calendar</h1>"})
		env, err := a.Deployment.Deployments.EnsureStanding(ctx, kitchen, "main", productionHost.ID, deployment.Attributes{Feed: "manual", Audience: "public", DataClass: "production"})
		if err != nil {
			t.Fatal(err)
		}
		at, _, err := a.Deployment.Outcomes.Admit(ctx, deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: "camera-no-runs", Inputs: deployment.Inputs{Revision: strings.TrimSpace(revision)}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Deployment.Outcomes.Complete(ctx, deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy", URL: "https://calendar.example.test"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Deployment.Deployments.ReserveHostPort(ctx, kitchen, env.Name, docker.ByKey(env.ID).App(), 43212); err != nil {
			t.Fatal(err)
		}
		daemon.markStarted(docker.ByKey(env.ID).App())
		if err := a.Deployment.ObserveRuntime(ctx); err != nil {
			t.Fatal(err)
		}
		// An older saved consent outlives its original workflow declarations.
		// Supply historical declarations to the real consent owner when creating
		// it; the page then reads it under today's ordinary eligibility rules.
		consent := workflow.NewStandingRules(a.DB, workflow.StandingDependencies{
			Authority: access.Checked{Authority: a.Access, Projects: a.Projects}, Identity: a.Access,
			Procedures: cameraStandingProcedure{}, Now: a.cfg.clock.Now,
		})
		in := workflow.StandingDefinition{Projects: []workflow.StandingProject{{ProjectID: project.ID, Workflow: "design-review"}}, Categories: workflow.StandingCategories{Dependencies: "patch", SecurityFixes: true}, Delivery: "land-only"}
		person := access.WithCaller(ctx, access.Caller{Verified: true, Principal: access.Principal{ID: testPrincipalOf(t, a.DB, "sam"), Kind: "user", Name: "sam", Via: "session"}})
		q, err := consent.Preview(person, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := consent.Enable(person, workflow.StandingCommand{Definition: in, ShownDigest: q.Digest}); err != nil {
			t.Fatal(err)
		}
		failed, err := a.conformance.BeginReview(testConformanceContext("sam"), conformance.ReviewRequest{Trigger: "manual"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = failed.Finish(ctx, conformance.Completion{Projects: 2, Errors: 1}, conformance.ReviewError); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.diskObserver.Run(ctx); err != nil {
		t.Logf("worker disks remain unknown: %v", err)
	}
	// Page reads use recorded background evidence. Collect the disposable
	// hosts before the camera checks their capacity and inventory.
	// Seeding the cloud connection also syncs the built-in target. Read its
	// current revision so the observer does not discard an obsolete request.
	productionHost, err = a.Deployment.Targets.ByID(ctx, productionHost.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.capacityObserver.Run(ctx, productionHost.ID, productionHost.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	if err := a.capacityObserver.Run(ctx, previewHost.ID, previewHost.SettingsRevision); err != nil {
		t.Logf("preview capacity remains unknown: %v", err)
	}
	if err := a.capacityObserver.Run(ctx, gardenShed.ID, gardenShed.SettingsRevision); err != nil {
		t.Logf("the garden shed's capacity remains unknown: %v", err)
	}
	// The local audit also covers installations that cannot run project chats.
	// Rebuild admission after the tour, whose recorded chats need a local runtime.
	if os.Getenv("APPTOWN_DESIGN_AUDIT") != "" {
		a.consoleAdmission = conversation.NewAdmission(a.DB, conversation.AdmissionDependencies{
			Work: a.Work, Sessions: a.Console, Models: a.modelSelection, Briefing: a.briefing,
			Harness: piHomeFor(a), Effects: a.changeConsole, Presence: holderPresenceFor(a),
			NoLocal: true, Now: a.cfg.clock.Now,
		}, a.cfg.dataDir)
		rebuildRoutes(a)
	}

	if os.Getenv("APPTOWN_DESIGN_AUDIT") != "" {
		// Imported backup metadata exercises retained final and partial sets.
		// The camera never executes a restore of these records.
		store := dataprotection.NewBackups(a.DB, a.cfg.clock.Now)
		copies, err := store.ForDeployment(ctx, project.ID, "main", 1)
		if err != nil || len(copies) == 0 {
			t.Fatalf("capture backup: %v", err)
		}
		latestCopyAt, err := time.Parse(time.RFC3339Nano, copies[0].TakenAt)
		if err != nil {
			t.Fatal(err)
		}
		for i, b := range []dataprotection.Backup{
			{Deployment: "main", Trigger: "take-down", Status: dataprotection.BackupOK},
			{Deployment: "main", Trigger: "manual", Status: dataprotection.BackupPartial},
			{Deployment: "intranet", Trigger: "manual", Status: dataprotection.BackupFailed},
		} {
			b.ProjectID, b.Actor = project.ID, "sam"
			b.TakenAt = latestCopyAt.Add(time.Duration(i-2) * time.Second).Format(time.RFC3339Nano)
			if _, err := store.Record(ctx, &b, "sam"); err != nil {
				t.Fatal(err)
			}
		}
		// A fake evaluator records an advisory flag through the real attempt
		// owner. It makes no model call and changes no gate outcome.
		rebuildWork(t, a, func(deps *workflow.Dependencies) {
			deps.Runners["guidance/conformance"] = func(e *workflow.Execution) {
				if _, err := e.Claim(); err != nil {
					t.Error(err)
					return
				}
				if err := e.Finish(workflow.StepOutcome{Status: workflow.StatusPass, Evidence: json.RawMessage(`{"checks":[{"check":"Clear page names","evaluator":"llm","verdict":"fail","gates":false,"note":"Consider a shorter page name."}]}`)}, nil); err != nil {
					t.Error(err)
				}
			}
		})
		scene.call("sam", "POST", "/api/v1/workflows/library", map[string]string{"yaml": `apiVersion: apptown.dev/v1
kind: Workflow
metadata:
  name: advisory-review
spec:
  kind: lifecycle
  role: working
  stages:
    - name: review
      steps:
        - name: rules
          uses: guidance/conformance
        - name: accept
          uses: approval
          with:
            means: I have read the checks.
`})
		defineWorkflows(t, a, tourWorkflow, reviewWorkflow, workflow.PreparedRun{
			Workflow: workflow.DefinitionRef{Name: "advisory-review", Version: 1},
			Spec: schema.Spec{Kind: "lifecycle", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{
				{Name: "rules", Uses: "guidance/conformance"},
				{Name: "accept", Uses: "approval", With: map[string]any{"means": "I have read the checks."}},
			}}}},
		})
		advisory := scene.open("tom", "club-newsletter", "Shorten the page names", "Review page names for the household.")
		scene.call("tom", "POST", "/api/v1/changes/"+advisory.ID.String()+"/runs", map[string]string{"workflow": "advisory-review"})
		a.Work.Wait()
	}
	// Attach a real local S3-compatible bucket after creating the historical
	// local copies, so both the store summary and its existing-copy caveat render.
	bucket := s3fake.New(t, "CAMERAKEY")
	bucket.AddBucket("household-copies")
	bucketURL, err := url.Parse(bucket.API.URL)
	if err != nil {
		t.Fatal(err)
	}
	household.store.Store(bucketURL)
	scene.call("sam", "PUT", "/api/v1/secrets/NAS_BACKUP_ACCESS_KEY", map[string]string{"value": "CAMERAKEY"})
	scene.call("sam", "PUT", "/api/v1/secrets/NAS_BACKUP_SECRET_KEY", map[string]string{"value": "local-camera-secret"})
	scene.call("sam", "PUT", "/api/v1/backup-store", map[string]string{"endpoint": "http://" + nasStoreHost, "region": "us-east-1", "bucket": "household-copies", "accessKeyFrom": "NAS_BACKUP_ACCESS_KEY", "secretKeyFrom": "NAS_BACKUP_SECRET_KEY"})

	// The camera's last review is the one its conformance pages show. It
	// runs the checks that need a tool's image for real, on this machine's
	// Docker through AppTown's own local check executor, as the landings'
	// source scans run; the scene's earlier reviews leave them unrun, so
	// the scanners do not run on every review. A check that asks a model
	// really asks a stand-in for the scene's model provider, which refuses
	// the scene's key as a provider refuses a revoked one: no model
	// answers, so no verdict is made up.
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid API key","code":401}}`)
	}))
	t.Cleanup(refusing.Close)
	localChecks := verification.LocalChecks(a.processRunner, a.cfg.clock.Now)
	installCheckEffects(t, a, checkOverrides{
		run: func(req execRequest) execOutcome {
			if req.Image != stock.Images().Sh {
				return localChecks(context.Background(), req)
			}
			return agentenv.Execute(a.processRunner, context.Background(), execution.Local, req, a.cfg.clock.Now())
		},
		complete: func(string, string, string) (string, *modelcall.Usage, error) {
			resp, err := http.Post(refusing.URL+"/v1/chat/completions", "application/json", strings.NewReader("{}"))
			if err != nil {
				return "", nil, fmt.Errorf("the model provider could not be reached: %w", err)
			}
			resp.Body.Close()
			return "", nil, fmt.Errorf("the model provider refused the request: %s", resp.Status)
		},
	})
	scene.review()

	// A project chat Sam had and ended: one question and the helper's
	// answer, recorded as every chat is (the past-chat pages show it).
	var projectChat struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(scene.call("sam", "POST", "/api/v1/projects/club-newsletter/console", map[string]any{}), &projectChat); err != nil {
		t.Fatal(err)
	}
	scene.call("sam", "POST", "/api/v1/console/"+projectChat.Session.ID+"/say", map[string]string{"text": "Which pages still show last year's membership fees?"})
	// The answer is billed as the helper's turns are: the project's guidance
	// written to the prompt cache, and the reply, at Claude Haiku's list
	// prices ($1 in, $5 out, $1.25 a cache write per million tokens).
	runtime.ReplyWith(projectChat.Session.ID, "Only the fees page did, and the change \"Publish membership fees\" has already updated it. The front page and the committee page link to it and show no prices themselves.", "openrouter",
		map[string]any{"input": 60, "output": 180, "cacheRead": 0, "cacheWrite": 26000, "totalTokens": 26240,
			"cost": map[string]any{"input": 60 * 1.0 / 1e6, "output": 180 * 5.0 / 1e6, "cacheRead": 0.0, "cacheWrite": 26000 * 1.25 / 1e6, "total": (60*1.0 + 180*5.0 + 26000*1.25) / 1e6}})
	scene.call("sam", "DELETE", "/api/v1/console/"+projectChat.Session.ID, nil)
	a.Work.Wait()
	cameraComplaints(scene)
	scene.call("sam", "GET", "/api/v1/packs", nil)
	// Hold one executor attempt for the camera, as the tour's progress scene
	// does. Completed script checks above it still execute the project's scripts.
	progressWorkflow := workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "camera-progress", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{
		{Name: "tests", Uses: "script", With: map[string]any{"run": "sh checks/tests.sh", "env": map[string]any{tourCheckEnv: "checks/tests.sh"}}},
		{Name: "style", Uses: "script", With: map[string]any{"run": "sh checks/style.sh", "env": map[string]any{tourCheckEnv: "checks/style.sh"}}},
		{Name: "check-layout", Uses: "script", With: map[string]any{"run": "sh checks/style.sh", "env": map[string]any{tourCheckEnv: "checks/style.sh"}}},
		{Name: "guidance-conformance", Uses: "guidance/conformance"},
	}}}}}
	defineWorkflows(t, a, tourWorkflow, reviewWorkflow, evalWorkflow, progressWorkflow)
	// The first run of the workflow finishes, so its steps have times on
	// the timing page; the second is held at check-layout.
	var layoutRuns atomic.Int32
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		original := deps.Runners["script"]
		deps.Runners["script"] = func(e *workflow.Execution) {
			if e.Step().Name != "check-layout" || layoutRuns.Add(1) == 1 {
				original(e)
				return
			}
			if _, err := e.Claim(); err != nil {
				t.Error(err)
			}
		}
	})
	springNotice := scene.open("sam", "club-newsletter", "Check the spring notice", "Check the spring notice against the club’s standards.")
	scene.call("sam", "POST", "/api/v1/changes/"+springNotice.ID.String()+"/runs", map[string]string{"workflow": "camera-progress"})
	a.Work.Wait()
	scene.later(25 * time.Minute)
	progressChange := scene.open("sam", "club-newsletter", "Check the club notice", "Check the notice against the club’s standards.")
	scene.call("sam", "POST", "/api/v1/changes/"+progressChange.ID.String()+"/runs", map[string]string{"workflow": "camera-progress"})
	a.Work.Wait()
	// Evals start their lanes on the workflow owner they were built with;
	// the scene rebuilt that owner above, so evals are built again on it,
	// through their normal dependencies.
	a.Evals = evals.New(a.DB, evalsDependencies(a))
	rebuildRoutes(a)
	api := a.Handler()
	proxy := httputil.NewSingleHostReverseProxy(frontend)
	// The page server (Vite in development) answers only its own host name,
	// not the scene's public name the browser used.
	direct := proxy.Director
	proxy.Director = func(r *http.Request) { direct(r); r.Host = frontend.Host }
	var prepareCamera sync.Once
	var prepareCameraErr error
	var startCameraReview sync.Once
	var cameraReviewErr error
	// seededWork is the browser's request to run the work it seeded; the
	// test goroutine runs it (runSeededWork) while it waits for the shots.
	// Nothing runs AppTown's job queue in the scene, so each job is run on
	// a consumer of its own, by its real handler: the what-if, the restore
	// and the eval's pass, then the noticeboard's backup, which fails. (One
	// backup is held running while the jobs page is shot, jobHold.)
	seededWork := make(chan chan struct{})
	// jobHold is the browser's request to hold one backup running: a
	// scheduled backup of club-newsletter's main, asked for as the sweep
	// asks, started on a consumer of its own; its upload to the NAS waits
	// (held) until the camera releases it, so the queue says it is running
	// because it is.
	jobHold := make(chan chan struct{})
	holdBackup := func() {
		t.Helper()
		key := project.ID.String() + "/main"
		held.mu.Lock()
		held.path, held.gate = "/household-copies/club-newsletter/main/", make(chan struct{})
		held.mu.Unlock()
		if err := (backupRequests{db: a.DB, life: ctx}).RequestBackup(ctx, key, "scheduled"); err != nil {
			t.Error(err)
			return
		}
		id := cameraQueuedJob(t, a, "backup", key)
		_, stop := startJobForTest(t, a, id)
		t.Cleanup(stop)
		if !poll.Until(ctx, 30*time.Second, 100*time.Millisecond, func() bool { return jobState(t, a, id) == string(rivertype.JobStateRunning) }) {
			t.Errorf("the held backup is %s, not running", jobState(t, a, id))
		}
	}
	runSeededWork := func() {
		t.Helper()
		// The camera's job history is longer than operationJobs reads.
		queued, err := jobs.List(ctx, a.DB, 5000)
		if err != nil {
			t.Fatal(err)
		}
		latest := func(kind string) int64 {
			var id int64
			for _, j := range queued {
				if j.Kind != kind {
					continue
				}
				if (j.State == "available" || j.State == "retryable" || j.State == "scheduled") && j.ID > id {
					id = j.ID
				}
			}
			return id
		}
		for _, kind := range []string{verification.WhatIfJob, operations.Job, evals.PassJob} {
			id := latest(kind)
			if id == 0 {
				t.Fatalf("the camera's seeded work has no queued %s job", kind)
			}
			done := runJobForTest(t, a, id)
			t.Logf("camera ran %s job %d: %s %v", kind, id, done.State, done.Errors)
		}
		// A scheduled backup of the noticeboard copy fails: the old laptop
		// it runs on is off. It is asked for as the sweep asks and run once
		// by its job's own handler, which ends it failed among the jobs'
		// latest.
		noticeboardBackup := project.ID.String() + "/noticeboard"
		if err := (backupRequests{db: a.DB, life: ctx}).RequestBackup(ctx, noticeboardBackup, "scheduled"); err != nil {
			t.Fatal(err)
		}
		if job := runJobForTest(t, a, cameraQueuedJob(t, a, "backup", noticeboardBackup)); job.State != rivertype.JobStateDiscarded {
			t.Fatalf("the noticeboard's backup ended %s, not failed: %v", job.State, job.Errors)
		}
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Finish the fixture's queued checks through their real worker path,
		// so idle tabs can show their Check/Fix actions before the running stop.
		if os.Getenv("APPTOWN_DESIGN_AUDIT") != "" && r.URL.Path == "/" && r.URL.Query().Get("cameraPrepare") == "1" {
			prepareCamera.Do(func() {
				var reviews []conformance.ReviewSummary
				reviews, prepareCameraErr = a.conformance.Reviews(testConformanceContext("sam"), 100)
				if prepareCameraErr != nil {
					return
				}
				for _, review := range reviews {
					if review.Status != conformance.ReviewQueued {
						continue
					}
					if _, prepareCameraErr = fleetReviewsFor(a).RunReviewJob(ctx, review.ID.String(), nil); prepareCameraErr != nil {
						return
					}
				}
			})
			if prepareCameraErr != nil {
				t.Errorf("camera queued reviews: %v", prepareCameraErr)
				http.Error(w, "camera preparation failed", 500)
				return
			}
		}
		// The final camera stop starts an actual review, after the other
		// tabs have been photographed with their available Check/Fix actions.
		if os.Getenv("APPTOWN_DESIGN_AUDIT") != "" && r.URL.Path == "/conformance/reviews" && r.URL.Query().Get("cameraRunning") == "1" {
			startCameraReview.Do(func() {
				var running *conformance.Execution
				running, cameraReviewErr = a.conformance.BeginReview(testConformanceContext("sam"), conformance.ReviewRequest{Trigger: "manual"})
				if cameraReviewErr == nil {
					cameraReviewErr = running.Progress(ctx, 1, "club-newsletter")
				}
			})
			if cameraReviewErr != nil {
				t.Errorf("camera running review: %v", cameraReviewErr)
				http.Error(w, "camera review failed", 500)
				return
			}
		}
		// The camera holds one backup running while it photographs the
		// jobs page (cameraJob=hold, run by the test goroutine) and lets its
		// upload through after (cameraJob=release, here: the test goroutine
		// may itself be waiting on that backup).
		if what := r.URL.Query().Get("cameraJob"); r.URL.Path == "/" && what != "" {
			switch what {
			case "hold":
				done := make(chan struct{})
				jobHold <- done
				<-done
			case "release":
				releaseHeld()
			default:
				http.Error(w, "hold or release", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// After the browser seeded its work (a what-if, a restore, an eval),
		// the test goroutine runs it through the jobs' own handlers.
		if r.URL.Path == "/" && r.URL.Query().Get("cameraSeeded") == "1" {
			done := make(chan struct{})
			seededWork <- done
			<-done
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// The camera moves members-preview's held release to the stage its
		// next pages show, before it photographs them (see heldRelease).
		if stage := r.URL.Query().Get("cameraStage"); r.URL.Path == "/" && stage != "" {
			if !slices.Contains([]string{deploy.Preparing.String(), deploy.BackingUp.String(), deploy.Building.String(), deploy.Starting.String(), deploy.Checking.String()}, stage) {
				http.Error(w, "no such stage", http.StatusBadRequest)
				return
			}
			// sql-guarantee: hold the release at a provider milestone; no owner operation pauses jobs.
			if _, err := a.DB.SQL().ExecContext(r.Context(), `UPDATE releases SET stage=$1, stage_entered_at=$2 WHERE id=$3`, stage, a.cfg.clock.Now().Add(-15*time.Second), heldRelease); err != nil {
				t.Errorf("camera stage %s: %v", stage, err)
				http.Error(w, "camera stage failed", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// This handler stands where a real install's reverse proxy
			// stands, on the same machine, and says who it forwards for.
			r.Header.Set("X-Real-IP", cameraClientAddress)
			api.ServeHTTP(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
	// One refresh is what AppTown's own loops do: the runtime observations
	// (which expire after 45 seconds), a host reader lap (diagnostics refuse
	// an old container section), then the observation jobs, run as a held
	// scene runs them: the camera's job history is past the list's cap
	// before it starts. The runtime loop and the reader keep going while
	// the jobs run, as they do on a host, so a long batch of jobs on a
	// loaded machine does not leave either older than the pages accept.
	refresh := func() {
		t.Helper()
		lap := func() {
			if err := a.Deployment.ObserveRuntime(ctx); err != nil {
				t.Fatal(err)
			}
			readHostsForTest(t, a)
		}
		lap()
		observeScene(t, a, scene, true)
		lap()
	}
	// The pages read only what the background checks recorded, so the camera
	// waits until the readings it means to show are known: Production's
	// resources, part by part, the built-in host's deployments' diagnostics
	// and its inventory. The preview host is unreachable on purpose and is
	// not waited for.
	known := func() string {
		res, err := a.Deployment.Resources(asPerson("sam"), project.ID, prod.Name)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Parts) != 3 {
			return fmt.Sprintf("Production has %d parts", len(res.Parts))
		}
		for _, p := range res.Parts {
			if !p.Observed.Known {
				return fmt.Sprintf("Production's %s: %s", p.Part, p.Observed.Reason)
			}
		}
		for _, dep := range captureDeps {
			diag, err := a.Deployment.Diagnostics(asPerson("sam"), project.ID, dep.Name)
			if err != nil {
				t.Fatal(err)
			}
			if diag.Reading.Unknown {
				return fmt.Sprintf("%s diagnostics: %s", dep.Name, diag.Reading.Failure)
			}
		}
		inv, err := a.Deployment.InventoryFor(ctx, productionHost)
		if err != nil {
			t.Fatal(err)
		}
		if inv.Unknown || inv.Invalidated {
			return "the built-in host's inventory is unknown"
		}
		return ""
	}
	var waiting string
	if !poll.Until(ctx, 3*time.Minute, 100*time.Millisecond, func() bool {
		refresh()
		waiting = known()
		return waiting == ""
	}) {
		t.Fatalf("the camera's scene never settled: %s", waiting)
	}
	// A refresh keeps the shoot's readings fresh: past the runtime
	// observations' 45 seconds, one refresh reads every one of them again.
	readAt := func() (runtime, resources, inventory time.Time) {
		rows, err := a.Deployment.List(asPerson("sam"), project.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Name == prod.Name {
				runtime = row.Observation.ObservedAt
			}
		}
		res, err := a.Deployment.Resources(asPerson("sam"), project.ID, prod.Name)
		if err != nil {
			t.Fatal(err)
		}
		inv, err := a.Deployment.InventoryFor(ctx, productionHost)
		if err != nil {
			t.Fatal(err)
		}
		return runtime, res.Reading.ObservedAt, inv.ObservedAt
	}
	phases.mark("records")
	runtimeBefore, resourcesBefore, inventoryBefore := readAt()
	moveClock(a, time.Minute)
	refresh()
	runtimeAfter, resourcesAfter, inventoryAfter := readAt()
	if !runtimeAfter.After(runtimeBefore) || !resourcesAfter.After(resourcesBefore) || !inventoryAfter.After(inventoryBefore) {
		t.Fatalf("a refresh did not read again: runtime %v -> %v, resources %v -> %v, inventory %v -> %v", runtimeBefore, runtimeAfter, resourcesBefore, resourcesAfter, inventoryBefore, inventoryAfter)
	}
	if why := known(); why != "" {
		t.Fatalf("after a refresh: %s", why)
	}
	// The scene is refreshed every 15 seconds while it is photographed or
	// held open, here on the test's own goroutine.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	if hold != "" {
		server.Start()
		t.Logf("design scene open at http://%s (sam / %s); interrupt to stop", a.cfg.publicHost, password)
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				refresh()
			}
		}
	}
	phases.mark("readings")
	server.StartTLS()
	module := os.Getenv("APPTOWN_PLAYWRIGHT_MODULE")
	if module == "" {
		home, _ := os.UserHomeDir()
		module = filepath.Join(home, ".local/share/forge-nightly/playwright/node_modules/playwright/index.mjs")
	}
	testinputs.Files(t, "testdata/web-ui.mjs", "../../web/row-action-lines.mjs")
	shot := make(chan execution.Outcome, 1)
	go func() {
		shot <- (execution.Runner{}).Run(ctx, execution.Local, execution.Request{
			Argv: []string{"node", "testdata/web-ui.mjs"},
			Env: []string{"APPTOWN_PLAYWRIGHT_MODULE=" + module, "DESIGN_URL=https://" + a.cfg.publicHost, "DESIGN_RESOLVE=" + a.cfg.publicHost + " 127.0.0.1:" + port,
				"DESIGN_AUDIT=" + os.Getenv("APPTOWN_DESIGN_AUDIT"), "DESIGN_PAGES=" + os.Getenv("APPTOWN_DESIGN_PAGES"), "DESIGN_BASELINE=" + os.Getenv("APPTOWN_DESIGN_BASELINE"), "DESIGN_PASSWORD=" + password, "DESIGN_PROGRESS=" + progressChange.ID.String(), "DESIGN_PROJECT_CHAT=" + projectChat.Session.ID, "DESIGN_CHANGE=" + approvalChange, "DESIGN_OUT=" + out,
				"TMPDIR=" + t.TempDir(), "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")},
			Timeout: 45 * time.Minute,
		})
	}()
	var run execution.Outcome
	for shooting := true; shooting; {
		select {
		case run = <-shot:
			shooting = false
		case <-ticker.C:
			refresh()
		case done := <-jobHold:
			func() {
				defer close(done)
				holdBackup()
			}()
		case done := <-seededWork:
			// done is closed even when the work fails the test, so the
			// browser's request returns and the run ends.
			// The readings are taken again after it, as the next lap would.
			func() {
				defer close(done)
				runSeededWork()
				refresh()
			}()
		}
	}
	t.Log(string(run.Output))
	phases.mark("browser script")
	if run.RunErr != nil {
		t.Fatal(fmt.Errorf("web UI camera: %w", run.RunErr))
	}
	if os.Getenv("APPTOWN_DESIGN_PAGES") == "" && os.Getenv("APPTOWN_DESIGN_AUDIT") == "" {
		data, err := os.ReadFile(filepath.Join(out, "final-paths.json"))
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		if err := json.Unmarshal(data, &paths); err != nil {
			t.Fatal(err)
		}
		if err := cameraRouteCoverage(cameraAppRoutes(t), paths, cameraRouteExclusions); err != nil {
			t.Fatal(err)
		}
	}
}

// markStartedWithLimits pretends a container is up as a run with --memory,
// --cpus and --label leaves it: held to memory bytes and nanoCPUs (0 is no
// limit), carrying labels. Docker reports them on inspect.
func (f *fakeDocker) markStartedWithLimits(name string, memory, nanoCPUs int64, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started[name] = true
	if f.limits == nil {
		f.limits = map[string]string{}
	}
	f.limits[name] = fmt.Sprintf("%d %d", memory, nanoCPUs)
	if f.labels[name] == nil {
		f.labels[name] = map[string]string{}
	}
	for k, v := range labels {
		f.labels[name][k] = v
	}
}

// cameraComplaints files the household's complaints of the past fortnight,
// each by the person who had the trouble, at the time they had it. Sam
// answers most a few hours later and archives some of those; the newest
// are still open. The clock ends where it started.
func cameraComplaints(scene *tourScene) {
	scene.t.Helper()
	type said struct{ who, text, why, wish, answer string }
	complaints := []said{
		{"tom", "Could not open the household calendar", "The link in Monday's email went to a page that said not found.", "Show a way back to the calendar.", "The calendar link has been repaired."},
		{"priya", "The newsletter page is blank on my phone", "I tapped the link in the email and saw a white page with nothing on it, twice.", "Open the page or say what went wrong.", "The phone page loads again; it was a slow image."},
		{"tom", "Approval button missing", "I was asked to approve the committee page but could not find where.", "Put the button where the message points.", "The approval is on the change page; the message now links there."},
		{"priya", "Backups say unknown", "The backups section says unknown and I do not know if the members list is safe.", "Tell me whether there is a copy.", "The check had not run yet; it now shows the last copy."},
		{"sam", "Wrong time on the ride calendar", "Saturday rides show 8 am but they leave at 9.", "Fix the time.", "Fixed in the latest release."},
		{"tom", "Too many emails", "I get a message for every small change, even ones I am not part of.", "Let me choose fewer messages.", "You can now choose a daily summary in your account."},
		{"priya", "Login keeps asking for my password", "Every morning it asks again even though I ticked remember me on this computer.", "Remember me for longer.", "Sign-ins now last two weeks on a remembered computer."},
		{"tom", "Photos upload sideways", "Ride photos taken on my phone show on their side on the spring ride page.", "Turn them the right way up.", "Photos are now turned using the camera's own setting."},
		{"sam", "Membership fees page shows last year's prices", "The fees page still says $45; the committee agreed $50 in October.", "Show this year's fees.", "Updated with the new fees and the date they start."},
		{"priya", "Map on the meeting point page does not load", "The map area stays grey on the library computer.", "Show the address even when the map cannot load.", ""},
		{"tom", "Cannot find last month's newsletter", "The archive only lists the current issue, so I cannot send the October one to a new member.", "List the older issues.", "Older issues are listed on the archive page again."},
		{"priya", "Text is too small on the ride calendar", "I have to zoom in to read the route names on my tablet, and then the table runs off the side.", "Make the text bigger.", ""},
		{"sam", "The committee page lists someone who left", "Alex stepped down in September but is still listed as treasurer.", "Remove Alex and show who took over.", "Alex has been removed and the new treasurer added."},
		{"tom", "Preview link asks me to sign in again", "I opened the preview Priya sent and had to sign in, then it lost the page I was on.", "Take me back to the preview after signing in.", ""},
		{"priya", "Event times shown in the wrong time zone", "The winter ride times show an hour later than the poster at the club rooms.", "Show our local time.", "Times are now shown in the club's own time zone."},
		{"tom", "Release waiting for days", "My change to the meeting point page has said waiting for approval since Tuesday.", "Tell me who needs to approve it.", ""},
		{"sam", "Email from the site goes to spam", "Two members said the reminder about the annual meeting arrived in their spam folder.", "Make the reminders arrive normally.", "The site's email now says who it is from properly; reminders arrive in the inbox."},
		{"priya", "Search does not find ride names", "Searching for Bayside Loop finds nothing although it is on the calendar.", "Find rides by their name.", ""},
		{"tom", "Printing the ride calendar cuts off the last column", "When I print the December calendar the distance column is missing.", "Fit the calendar on the page when printed.", ""},
	}
	total := time.Duration(0)
	move := func(d time.Duration) { scene.later(d); total += d }
	move(-14 * 24 * time.Hour)
	for i, c := range complaints {
		move(time.Duration(9+i%4*3) * time.Hour)
		var filed struct {
			ID ids.ID `json:"id"`
		}
		if err := json.Unmarshal(scene.call(c.who, "POST", "/api/v1/complaints", map[string]string{"text": c.text, "why": c.why, "wish": c.wish}), &filed); err != nil {
			scene.t.Fatal(err)
		}
		if c.answer == "" {
			continue
		}
		move(time.Duration(2+i%3) * time.Hour)
		scene.call("sam", "POST", "/api/v1/complaints/"+filed.ID.String()+"/answer", map[string]string{"answer": c.answer})
		if i < 5 {
			scene.call("sam", "POST", "/api/v1/complaints/"+filed.ID.String()+"/archive", nil)
		}
	}
	scene.later(-total)
}

// cameraCheckTime is how long a camera script check takes on the
// household's machine: the project's tests longest, its style check
// shorter, anything else a few seconds.
func cameraCheckTime(spec execution.TaskSpec) time.Duration {
	for _, kv := range spec.Env {
		switch kv {
		case tourCheckEnv + "=checks/tests.sh":
			return 41 * time.Second
		case tourCheckEnv + "=checks/style.sh":
			return 8 * time.Second
		}
	}
	return 3 * time.Second
}

// cameraApplied is how Club Newsletter's main deployment ran its earlier
// releases: its site, held to a memory limit, and its database.
var cameraApplied = deployment.AppliedProfile{Profile: "default", How: "default",
	Parts:    map[string]deployment.AppliedPart{"web": {Public: true, Resources: appcontract.Resources{MemoryBytes: 512 << 20}}},
	Services: map[string]deployment.AppliedPart{"postgres": {Need: true, Resources: appcontract.Resources{MemoryBytes: 256 << 20}}}}

// cameraPreviewContainer is the December change's preview container, named
// as AppTown names its previews' containers.
const cameraPreviewContainer = string(docker.Current) + "preview-december-ride-calendar"

// cameraClubDomain is the cycling club's own domain, under which the
// camera's AppTown and apps are named.
const cameraClubDomain = "hilltopcycling.club"

// cameraPublicHost is the camera's AppTown address (see TestWebUICapture).
const cameraPublicHost = "apptown." + cameraClubDomain

// cameraPackageJSON and cameraPackageLock are Club Newsletter's npm
// manifest and the lock file npm wrote for it.
const cameraPackageJSON = `{
  "name": "club-newsletter",
  "private": true,
  "description": "Hilltop Cycling Club newsletter: the ride pages format dates with dayjs.",
  "dependencies": {
    "dayjs": "1.11.13"
  }
}
`

const cameraPackageLock = `{
  "name": "club-newsletter",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "club-newsletter",
      "dependencies": {
        "dayjs": "1.11.13"
      }
    },
    "node_modules/dayjs": {
      "version": "1.11.13",
      "resolved": "https://registry.npmjs.org/dayjs/-/dayjs-1.11.13.tgz",
      "integrity": "sha512-oaMBel6gjolK862uaPQOVTA7q3TZhuSvuMQAAglQDOWYO9A91IrAOUJEyKVlqJlHE0vq5p5UXxzdPfMH/x6xNg==",
      "license": "MIT"
    }
  }
}
`

// gardenShedAddress is the garden shed's machine on the household's
// network (the garden-shed target).
const gardenShedAddress = "192.168.1.41"

// cameraClientAddress is the address the camera's browser connects from,
// a computer on the household's network, as the reverse proxy in front of
// the scene reports it.
const cameraClientAddress = "192.168.1.23"

// nasStoreHost is the household NAS's S3 service, where the camera's
// backup copies are kept (reached through the test's forward proxy).
const nasStoreHost = "nas.home.arpa:9000"

// cameraDeployments are Club Newsletter's other deployments on the camera's
// host, in the order of the deploy stages they are held at (web-ui.mjs
// photographs them under the same names).
var cameraDeployments = []string{"members-preview", "winter-timetable", "committee-review", "spring-edition", "ride-calendar-trial", "staging", "archive-migration", "print-layout"}

// cameraDeploymentNotes are the notes of each camera deployment's first
// release.
var cameraDeploymentNotes = map[string]string{
	"members-preview": "A copy for members to try the new pages", "winter-timetable": "Winter timetable draft",
	"committee-review": "Copy for the committee to review", "spring-edition": "Spring edition layout",
	"ride-calendar-trial": "Trying the new ride calendar", "staging": "Staging copy of the newsletter",
	"archive-migration": "Moving the old archive across", "print-layout": "Layout for the printed edition",
}

// cameraOtherContainers are the household's own services on the camera's
// host, which AppTown did not start.
var cameraOtherContainers = []string{"uptime-monitor", "backup-agent", "log-shipper", "photo-sync", "dns-filter", "home-automation", "media-server", "metrics-collector", "mqtt-broker", "image-updater"}

// The camera supplies historical declarations only. Consent identity,
// authority, immutable history and current eligibility still use their owners.
type cameraStandingProcedure struct{}

func (cameraStandingProcedure) StandingProcedure(context.Context, database.Queryer, ids.ID, string) (workflow.StandingProcedure, error) {
	return workflow.StandingProcedure{Acceptance: workflow.StandingPurpose{Workflow: "design-review", Version: 1, Step: "accept", Means: "accept small dependency fixes after checks pass"}, Landing: workflow.StandingPurpose{Workflow: "land", Version: 1, Step: "accept", Means: "land the checked update"}}, nil
}

// fieldnotesRecording contains only the supplied change view's request,
// report, procedure and verdict. It contains no invented commits, screenshots,
// execution receipts or deployment evidence.
type fieldnotesRecording struct {
	Title, Body, Report, Diffstat string
	Review                        map[string]any
	Steps                         []struct {
		Name, Type, Status string
		Config             map[string]any
	}
}

func readFieldnotesRecording(t *testing.T) fieldnotesRecording {
	t.Helper()
	b, err := os.ReadFile("testdata/tour/fieldnotes9.json")
	if err != nil {
		t.Fatal(err)
	}
	var r fieldnotesRecording
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// fieldnotes adds a recorded rejection after the tour's existing history.
// The runners import historical outcomes through Work's attempt owner; they
// explicitly label their provenance and claim no local agent or container run.
func (s *tourScene) fieldnotes() {
	t, a := s.t, s.a
	r := readFieldnotesRecording(t)
	s.call("sam", "POST", "/api/v1/projects", map[string]string{"slug": "fieldnotes", "name": "fieldnotes", "blurb": "A journal of everyday things"})
	s.call("sam", "POST", "/api/v1/projects/fieldnotes/repo", map[string]string{"defaultBranch": "main"})
	// Give main its own shipped app before opening the recorded change. This
	// is a local tour stand-in, not a reconstruction of the WordPress site.
	first := s.open("sam", "fieldnotes", "Start the journal", "A journal of everyday things, with a page for each entry.")
	s.write(first, map[string]string{
		".apptown/app.yaml": "version: 2\napp:\n  site:\n    static: public\n    public: true\n",
		"public/index.html": "<!doctype html><title>fieldnotes — local tour stand-in</title><h1>fieldnotes</h1><p>A journal of everyday things</p><p>Local tour stand-in for a WordPress journal.</p>",
		"checks/tests.sh":   "test -s public/index.html\n",
		"checks/style.sh":   "test -s .apptown/app.yaml\n",
	})
	s.commit(first, "sam", "Add local journal stand-in")
	s.ship(first, "sam", "sam", time.Minute)
	c := s.open("sam", "fieldnotes", r.Title, r.Body)
	// The source contains no preview image bytes or complete source tree. Serve
	// an explicit local stand-in, never the remote preview or a claimed rebuild.
	s.write(c, map[string]string{".apptown/app.yaml": "version: 2\napp:\n  site:\n    static: public\n    public: true\n", "public/index.html": "<!doctype html><title>fieldnotes — recorded change</title><h1>fieldnotes</h1><p>Local tour stand-in for the recorded adoption change. The original WordPress preview is not reproduced here.</p>"})
	s.commit(c, "sam", "Add local tour preview stand-in")
	previewURL := s.serveSite(c)
	steps := make([]schema.Step, 0, len(r.Steps))
	for _, step := range r.Steps {
		steps = append(steps, schema.Step{Name: step.Name, Uses: step.Type, With: step.Config})
	}
	a.Work.Wait()
	a.Work.Stop()
	defineWorkflows(t, a, tourWorkflow, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "quick-change", Version: 13}, Spec: schema.Spec{Kind: "lifecycle", Role: "working", Stages: []schema.Stage{{Name: "apply", OnEnter: steps[:1], Steps: steps[1:]}}}})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		for _, kind := range []string{"agent/pi", "preview", "agent/review"} {
			original := deps.Runners[kind]
			deps.Runners[kind] = func(e *workflow.Execution) {
				if e.Run().ChangeID != c.ID {
					original(e)
					return
				}
				if _, err := e.Claim(); err != nil {
					t.Error(err)
					return
				}
				evidence := map[string]any{"recording": "Imported from fieldnotes change #9; no local agent or container execution."}
				outcome := workflow.StepOutcome{Status: workflow.StatusPass}
				switch e.Step().Name {
				case "apply-change":
					evidence["output"] = r.Report
					outcome.Artifacts = []workflow.StepArtifact{{ArtifactDraft: workflow.ArtifactDraft{Kind: "change-report", Name: "change-report", Body: r.Report}}, {ArtifactDraft: workflow.ArtifactDraft{Kind: "recording", Name: "Recorded source", Body: "Imported fieldnotes change #9. The report and reviewer verdict describe that recorded change, not execution in this tour. The preview is a local stand-in. Original diffstat: " + r.Diffstat}}}
				case "boot":
					evidence["url"] = previewURL
					evidence["recording"] = "Local tour stand-in; the recorded demo boot passed. No WordPress rebuild or screenshot capture was performed."
					outcome.Artifacts = []workflow.StepArtifact{{ArtifactDraft: workflow.ArtifactDraft{Kind: "preview", Name: "preview", Ref: previewURL}}}
				case "review-change":
					for k, v := range r.Review {
						evidence[k] = v
					}
					outcome.Status = workflow.StatusFail
				default:
					t.Errorf("unexpected recorded step %s", e.Step().Name)
					return
				}
				var err error
				outcome.Evidence, err = json.Marshal(evidence)
				if err != nil {
					t.Error(err)
					return
				}
				if err := e.Finish(outcome, nil); err != nil {
					t.Error(err)
				}
			}
		}
	})
	var started struct{ Run struct{ ID string } }
	if err := json.Unmarshal(s.call("sam", "POST", fmt.Sprintf("/api/v1/changes/%s/runs", c.ID), map[string]string{"workflow": "quick-change"}), &started); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	t.Logf("fieldnotes recorded rejection: /changes/%s", c.ID)
}

// assertFieldnotesDecision protects the signed-in data used by Decide… and
// its decision screen, without opening or driving a browser.
func assertFieldnotesDecision(t *testing.T, s *tourScene) {
	t.Helper()
	assertFieldnotesProject(t, s)
	r := readFieldnotesRecording(t)
	var changes []struct {
		ID    string
		Title string
	}
	if err := json.Unmarshal(s.call("sam", "GET", "/api/v1/projects/fieldnotes/changes", nil), &changes); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("recorded change missing: %+v", changes)
	}
	var heldID string
	for _, c := range changes {
		if c.Title == r.Title {
			heldID = c.ID
		}
	}
	if heldID == "" {
		t.Fatal("recorded rejection missing")
	}
	var page struct {
		Change                     struct{ Title, Body string }
		State, StateDetail, Report string
		Detail                     struct {
			Steps []struct {
				Name, Type, Status string
				Evidence           map[string]any
			}
			Next      []struct{ Act string }
			Artifacts []struct{ Kind, Ref string }
		}
	}
	if err := json.Unmarshal(s.call("sam", "GET", "/api/v1/changes/"+heldID, nil), &page); err != nil {
		t.Fatal(err)
	}
	if page.Change.Title != r.Title || page.Change.Body != r.Body || page.Report != r.Report || page.State != "waiting on you" || page.StateDetail != "review-change (fail): "+r.Review["reason"].(string) {
		t.Fatalf("recorded rejection changed: %+v", page)
	}
	if len(page.Detail.Steps) != len(r.Steps) {
		t.Fatalf("steps: %+v", page.Detail.Steps)
	}
	for i, step := range page.Detail.Steps {
		want := r.Steps[i]
		if step.Name != want.Name || step.Type != want.Type || step.Status != want.Status {
			t.Errorf("step %d: %+v, want %+v", i, step, want)
		}
		if step.Name == "review-change" {
			for k, v := range r.Review {
				gotJSON, _ := json.Marshal(step.Evidence[k])
				wantJSON, _ := json.Marshal(v)
				if string(gotJSON) != string(wantJSON) {
					t.Errorf("review %s changed: %s", k, gotJSON)
				}
			}
		}
	}
	acts := map[string]bool{}
	for _, n := range page.Detail.Next {
		acts[n.Act] = true
	}
	if !acts["rework"] || !acts["override"] {
		t.Errorf("held decision not offered to Sam: %+v", page.Detail.Next)
	}
	for _, artifact := range page.Detail.Artifacts {
		if artifact.Kind == "preview" && artifact.Ref != "" {
			return
		}
	}
	t.Error("recorded change has no local preview")
}

// assertFieldnotesProject protects the project data used to offer Open app
// instead of Build the first version, without driving a browser.
func assertFieldnotesProject(t *testing.T, s *tourScene) {
	t.Helper()
	var p httpapi.ProjectDetail
	if err := json.Unmarshal(s.call("sam", "GET", "/api/v1/projects/fieldnotes", nil), &p); err != nil {
		t.Fatal(err)
	}
	if p.Blurb != "A journal of everyday things" || p.OriginalRequest != "A journal of everyday things" || p.RuntimeForm != "static site" || !p.IntranetReady {
		t.Errorf("fieldnotes project is not ready: %+v", p)
	}
	var deployments []httpapi.DeploymentView
	if err := json.Unmarshal(s.call("sam", "GET", "/api/v1/projects/fieldnotes/deployments", nil), &deployments); err != nil {
		t.Fatal(err)
	}
	for _, d := range deployments {
		if d.Name == "main" && d.URL != "" && d.Running != nil && *d.Running {
			return
		}
	}
	t.Errorf("fieldnotes has no running main with an address: %+v", deployments)
}

// The garage keeps progress visible across observation laps, using the same
// signed-in payloads as the change and deployment screens.
func assertGarageProgress(t *testing.T, s *tourScene) {
	t.Helper()
	for _, slug := range []string{"garage-landing", "garage-work", "garage-release"} {
		var changes []struct{ ID string }
		if err := json.Unmarshal(s.call("sam", "GET", "/api/v1/projects/"+slug+"/changes", nil), &changes); err != nil || len(changes) != 1 {
			t.Fatalf("%s progress change: %+v %v", slug, changes, err)
		}
		var page struct {
			State  string
			Detail struct {
				Steps []struct{ Name, Status string }
			}
		}
		if err := json.Unmarshal(s.call("sam", "GET", "/api/v1/changes/"+changes[0].ID, nil), &page); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"tests": "pass", "style": "pass", "accept": "pass", "build": "running", "build-exists": "pending"}
		if slug == "garage-work" {
			want = map[string]string{"apply-change": "running"}
			if page.State != "agent working" {
				t.Errorf("working change state: %q", page.State)
			}
			var status struct{ State string }
			if err := json.Unmarshal(s.call("sam", "GET", "/api/v1/changes/"+changes[0].ID+"/preview", nil), &status); err != nil || status.State != "starting" {
				t.Fatalf("working preview: %+v %v", status, err)
			}
		}
		if slug == "garage-release" {
			var deployments []httpapi.DeploymentView
			var releases []httpapi.ReleaseListed
			if err := json.Unmarshal(s.call("sam", "GET", "/api/v1/projects/"+slug+"/deployments", nil), &deployments); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(s.call("sam", "GET", "/api/v1/projects/"+slug+"/releases", nil), &releases); err != nil {
				t.Fatal(err)
			}
			if len(deployments) != 1 || len(releases) != 1 || releases[0].Deployment != deployments[0].Name || releases[0].State != deployment.Releasing || releases[0].Number < 1 || releases[0].StatusLabel.String() != "deploying" {
				t.Fatalf("%s deploying status inputs: deployments=%+v releases=%+v", slug, deployments, releases)
			}
		}
		for _, step := range page.Detail.Steps {
			if status, ok := want[step.Name]; ok {
				if step.Status != status {
					t.Errorf("%s %s: %s, want %s", slug, step.Name, step.Status, status)
				}
				delete(want, step.Name)
			}
		}
		if len(want) != 0 {
			t.Errorf("%s missing progress steps: %+v", slug, want)
		}
	}
}

// progress adds live owner transitions with deliberately unfinished external
// work. No receipts are asserted and no owner tables need raw writes.
func (s *tourScene) progress(freeze chan struct{}, frozen <-chan struct{}, daemon *fakeDocker) {
	t, a := s.t, s.a
	changes := map[string]*tourChange{}
	landingSlugs := []string{"garage-landing", "garage-release"}
	for _, item := range []struct{ slug, title string }{
		{"garage-landing", "Publish the club notice"},
		{"garage-work", "Write the next club notice"},
		{"garage-release", "Release the new club notice"},
	} {
		s.call("sam", "POST", "/api/v1/projects", map[string]string{"slug": item.slug, "name": item.slug})
		s.call("sam", "POST", "/api/v1/projects/"+item.slug+"/repo", map[string]string{"defaultBranch": "main"})
		c := s.open("sam", item.slug, item.title, "Local garage example with work held in progress.")
		s.write(c, newsletterStart)
		s.commit(c, "sam", item.title)
		changes[item.slug] = c
	}
	working := workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "garage-work", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "apply-change", Uses: "agent/pi", With: map[string]any{"prompt": "Write the next club notice."}}}}}}}
	defineWorkflows(t, a, tourWorkflow, working)
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		for _, kind := range []string{"agent/pi", "build"} {
			original := deps.Runners[kind]
			deps.Runners[kind] = func(e *workflow.Execution) {
				id := e.Run().ChangeID
				if id != changes["garage-work"].ID && id != changes["garage-landing"].ID && id != changes["garage-release"].ID {
					original(e)
					return
				}
				// The fake executor holds running examples at their reported stage,
				// without timers or background goroutines. Ending examples settle once.
				if _, err := e.Claim(); err != nil {
					t.Error(err)
					return
				}

			}
		}
	})
	var previewDone chan struct{}
	t.Cleanup(func() {
		a.Work.BeginShutdown()
		close(freeze)
		if previewDone != nil {
			<-previewDone
		}
		a.Work.Wait()
	})
	// Each landing runs the real check scripts before Sam approves it.
	runs := map[string]ids.ID{}
	for _, slug := range landingSlugs {
		runs[slug] = s.land(changes[slug], "sam", map[string]string{"tests": "pass", "style": "pass"})
	}
	s.call("sam", "POST", fmt.Sprintf("/api/v1/changes/%s/runs", changes["garage-work"].ID), map[string]string{"workflow": "garage-work"})
	a.Work.Wait()
	for _, slug := range landingSlugs {
		step := s.steps(runs[slug])["accept"]
		s.call("sam", "POST", fmt.Sprintf("/api/v1/steps/%s/approve", step.ID), map[string]string{"decision": "approve", "adds": shownAdds(t, a, asPerson("sam"), step.ID)})
	}
	a.Work.Wait()

	previewDone = make(chan struct{})
	go func() {
		defer close(previewDone)
		_, err := a.Preview.RequestChangePreview(asPerson("sam"), preview.Selection{ChangeID: changes["garage-work"].ID})
		if err == nil {
			t.Error("the frozen preview completed")
		}
	}()
	// Wait for the boot to reach its host, not just admission.
	if !poll.Until(t.Context(), 25*time.Second, 10*time.Millisecond, func() bool {
		select {
		case <-frozen:
			return true
		default:
			return false
		}
	}) {
		t.Fatal("garage preview never reached the frozen host")
	}

	// The release example admits an explicit branch release through its
	// owner. Its target has a container, supplied by the existing host fake;
	// neither the release nor the executor claims a completed deployment.
	c := changes["garage-release"]
	ch, err := a.Work.Change(asPerson("sam"), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.Deployment.Targets.ByName(t.Context(), "built-in-docker")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), ch.ProjectID, "main", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	names, err := dep.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	daemon.markStarted(names.App())
	if _, err := a.Deployment.Releases.Start(t.Context(), deployment.ReleaseDraft{
		Source: deployment.ReleaseFromBranch, ProjectID: ch.ProjectID, DeploymentID: dep.ID,
		RunID: runs["garage-release"], Deployment: "main", Revision: ch.LandedSHA, Actor: "sam",
	}, "Garage example: waiting for the local target"); err != nil {
		t.Fatal(err)
	}
	assertGarageProgress(t, s)
	for _, slug := range append(landingSlugs, "garage-work") {
		t.Logf("%s in progress: /changes/%s", slug, changes[slug].ID)
	}
}

func TestPolicyLibraryUpgradeBrowser(t *testing.T) {
	out := os.Getenv("APPTOWN_POLICY_UPGRADE_CAPTURE")
	if out == "" {
		t.Skip("set APPTOWN_POLICY_UPGRADE_CAPTURE to capture the migrated Policies page")
	}
	a := policyLibraryUpgradeFixture(t)
	app := a.Handler()
	dist, err := filepath.Abs("../../web/dist")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatal("build web before the browser capture: ", err)
	}
	files := http.FileServer(http.Dir(dist))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			r.Header.Set("X-AppTown-Actor", "boss")
			app.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/policies" {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	}))
	defer server.Close()
	module := os.Getenv("APPTOWN_PLAYWRIGHT_MODULE")
	if module == "" {
		home, _ := os.UserHomeDir()
		module = filepath.Join(home, ".local/share/forge-nightly/playwright/node_modules/playwright/index.mjs")
	}
	script := filepath.Join(t.TempDir(), "capture.mjs")
	if err := os.WriteFile(script, []byte(`
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE);
const browser = await chromium.launch({headless:true});
try {
 const page = await browser.newPage({viewport:{width:1440,height:1050}});
 const errors=[];
 page.on('pageerror',err=>errors.push(String(err)));
 const response=page.waitForResponse(r=>r.url().endsWith('/api/v1/policy-library'));
 await page.goto(process.env.CAPTURE_URL+'/policies');
 const policies=await response;
 if(policies.status()!==200) throw new Error('policy-library returned '+policies.status());
 await page.getByRole('link',{name:'New policy',exact:true}).waitFor();
 await page.getByText('approval',{exact:true}).waitFor();
 if(errors.length) throw new Error(errors.join('\n'));
 await page.screenshot({path:process.env.CAPTURE_OUT,fullPage:true});
 console.log('policy-library: 200; Policies rendered; screenshot: '+process.env.CAPTURE_OUT);
} finally {await browser.close();}
`), 0600); err != nil {
		t.Fatal(err)
	}
	run := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Argv: []string{"node", script}, Env: []string{"APPTOWN_PLAYWRIGHT_MODULE=" + module, "CAPTURE_URL=" + server.URL, "CAPTURE_OUT=" + out, "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, Timeout: time.Minute})
	t.Log(string(run.Output))
	if run.RunErr != nil {
		t.Fatal(run.RunErr)
	}
	if run.ExitCode != 0 {
		t.Fatalf("browser exit %d", run.ExitCode)
	}
}

// A real worker connection and the production page over disposable records.
// Its worker filesystem and older build are test service responses.
func TestExecutorUICapture(t *testing.T) {
	out := os.Getenv("APPTOWN_EXECUTOR_CAPTURE")
	if out == "" {
		t.Skip("set APPTOWN_EXECUTOR_CAPTURE for executor screenshots")
	}
	ca := workertest.NewCA(t)
	var unknown atomic.Bool
	srv := workertest.StartServerWith(t, ca, &worker.Server{State: t.TempDir(), Profile: execution.WorkerProfile{Name: execution.ProfileWorkerVM}, StateDisk: func(string) (diskspace.Space, error) {
		return diskspace.Space{TotalBytes: 100 << 30, FreeBytes: 10 << 30}, nil
	}}, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if unknown.Load() {
				http.Error(w, "worker unavailable", 503)
				return
			}
			if r.URL.Path == "/v1/version" {
				_ = json.NewEncoder(w).Encode(map[string]string{"version": "v1.0.0"})
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	a := authzActors(t, Config{NoLocalDocker: true, Worker: workerClientConfig(t, ca, srv.URL), DockerClient: newFakeDocker()})
	observe := func() error {
		snapshot, err := a.workerObserver.Snapshot(t.Context())
		if err != nil {
			return err
		}
		if err := a.workerObserver.Run(t.Context(), snapshot.Identity); err != nil {
			return err
		}
		return a.diskObserver.Run(t.Context())
	}
	if err := observe(); err != nil {
		t.Fatal(err)
	}

	app := a.Handler()
	dist, err := filepath.Abs("../../web/dist")
	if err != nil {
		t.Fatal(err)
	}
	files := http.FileServer(http.Dir(dist))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/camera/unknown" {
			unknown.Store(true)
			if err := observe(); err != nil {
				t.Error(err)
				http.Error(w, "observation failed", 500)
				return
			}
			w.WriteHeader(204)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			r.Header.Set("X-AppTown-Actor", "boss")
			app.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/assets/") {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	}))
	defer server.Close()
	script := filepath.Join(t.TempDir(), "executor.mjs")
	if err := os.MkdirAll(out, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(`
const {chromium}=await import(process.env.APPTOWN_PLAYWRIGHT_MODULE);
const browser=await chromium.launch({headless:true});
try {
 const page=await browser.newPage();
 const errors=[]; page.on('pageerror',err=>errors.push(String(err)));
 for(const state of ['reachable','unknown']) {
  if(state==='unknown') await page.request.get(process.env.CAPTURE_URL+'/camera/unknown');
  for(const [size,width,height] of [['desktop',1440,1000],['phone',390,844]]) for(const theme of ['light','dark']) {
   await page.setViewportSize({width,height});
   await page.goto(process.env.CAPTURE_URL+'/integrations/executor?theme='+theme,{waitUntil:'networkidle'});
   await page.getByText('Worker address',{exact:true}).waitFor();
   const facts=await page.locator('section[aria-label="Where work runs"]').innerText();
   const connection=await page.locator('dt').filter({hasText:'Connection'}).locator('xpath=following-sibling::dd[1]').innerText();
   if(!connection.includes(state)) throw new Error('worker state missing: '+facts);
   if(state==='reachable'&&!facts.includes('differs from AppTown')) throw new Error('version mismatch missing');
   if(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth)) throw new Error('executor overflows');
   await page.screenshot({path:process.env.CAPTURE_OUT+'/'+size+'-'+theme+'-'+state+'.png',fullPage:true});
  }
 }
 if(errors.length) throw new Error(errors.join('\n'));
} finally {await browser.close();}
`), 0600); err != nil {
		t.Fatal(err)
	}
	run := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Argv: []string{"node", script}, Env: []string{"APPTOWN_PLAYWRIGHT_MODULE=" + os.Getenv("APPTOWN_PLAYWRIGHT_MODULE"), "CAPTURE_URL=" + server.URL, "CAPTURE_OUT=" + out, "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, Timeout: 2 * time.Minute})
	t.Log(string(run.Output))
	if run.RunErr != nil {
		t.Fatal(run.RunErr)
	}
	if run.ExitCode != 0 {
		t.Fatalf("executor browser exited %d", run.ExitCode)
	}
}

// This capture uses the real fixing landing, with no seeded fix or verdict.
func TestLandingConformanceBrowser(t *testing.T) {
	out := os.Getenv("APPTOWN_LANDING_CAPTURE")
	if out == "" {
		t.Skip("set APPTOWN_LANDING_CAPTURE to capture the fixing landing")
	}
	a, run := fixingLandingFixture(t)
	// A real fleet report for two other projects leaves the fixing landing's
	// judgement as this project's newest evidence.
	var selected []ids.ID
	for _, slug := range []string{"library", "bookings"} {
		id, _, _ := fleetRepo(t, a, slug, map[string]string{"readme.txt": "clean"})
		adopt(t, a, id, "fleet-check", 1, checksJSON(shCheck("rule", "exit 0", "high")))
		selected = append(selected, id)
	}
	if _, _, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("reviewer"), "manual", conformance.ReviewOptions{Projects: selected}); err != nil {
		t.Fatal(err)
	}
	// Main moves after the fixing landing; the view must retain its evidence
	// and say that there is no judgement on the current commit.
	gittest.RunAsTester(t, a.projectRepoPath("review-landing"), "commit", "--allow-empty", "-m", "subsequent work")
	setTestFeatureAs(t, a, "conformance-estate", true, "reviewer")
	app := a.Handler()
	dist, err := filepath.Abs("../../web/dist")
	if err != nil {
		t.Fatal(err)
	}
	files := http.FileServer(http.Dir(dist))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			r.Header.Set("X-AppTown-Actor", "reviewer")
			app.ServeHTTP(w, r)
			return
		}
		if !strings.Contains(filepath.Base(r.URL.Path), ".") {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	}))
	defer server.Close()
	script := filepath.Join(t.TempDir(), "capture.mjs")
	if err := os.WriteFile(script, []byte(`
const {chromium}=await import(process.env.APPTOWN_PLAYWRIGHT_MODULE);
const browser=await chromium.launch({headless:true});
try {

 const errors=[];
 for(const width of [1440,390]) {
  const page=await browser.newPage({viewport:{width,height:1050}});
  page.on('pageerror',e=>errors.push(String(e)));
  await page.goto(process.env.CAPTURE_URL+'/conformance/projects/review-landing');
  await page.getByRole('heading',{name:'Fixed',exact:false}).waitFor();
  await page.locator('a[href="'+process.env.CHANGE_PATH+'"]').first().waitFor();
  await page.getByText('No judgement on the current commit',{exact:false}).last().waitFor();
  await page.getByText('via',{exact:false}).first().waitFor();
  await page.screenshot({path:process.env.CAPTURE_OUT+'/landing-conformance-'+width+'.png',fullPage:true});
  await page.goto(process.env.CAPTURE_URL+'/conformance/reviews');
  const report=page.getByRole('link',{name:/Fleet review ·/}).first();
  await report.waitFor();
  await page.screenshot({path:process.env.CAPTURE_OUT+'/conformance-reviews-'+width+'.png',fullPage:true});
  await report.click();
  await page.getByText(/Snapshot as of/).waitFor();
  await page.screenshot({path:process.env.CAPTURE_OUT+'/conformance-snapshot-'+width+'.png',fullPage:true});
  await page.goto(process.env.CAPTURE_URL+'/conformance');
  await page.getByText('judged on an older commit',{exact:false}).last().waitFor();
  await page.screenshot({path:process.env.CAPTURE_OUT+'/conformance-current-'+width+'.png',fullPage:true});
  await page.close();
 }
 if(errors.length)throw new Error(errors.join('\n'));
} finally {await browser.close();}
`), 0600); err != nil {
		t.Fatal(err)
	}
	result := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Argv: []string{"node", script}, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "APPTOWN_PLAYWRIGHT_MODULE=" + os.Getenv("APPTOWN_PLAYWRIGHT_MODULE"), "CAPTURE_URL=" + server.URL, "CAPTURE_OUT=" + out, "CHANGE_PATH=" + fmt.Sprintf("/changes/%s", run.ChangeID)}, Timeout: time.Minute})
	if result.RunErr != nil || result.ExitCode != 0 {
		t.Fatalf("capture: %v (%d) %s", result.RunErr, result.ExitCode, result.Output)
	}
}

// standInArchive is an empty gzipped tar: a volume's files as the tour's
// stand-in host copies them.
func standInArchive(t *testing.T) []byte {
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	if err := tar.NewWriter(z).Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// cameraQueuedJob is the newest queued job of kind with key. The camera's
// job history is longer than operationJobs reads.
func cameraQueuedJob(t *testing.T, a *App, kind, key string) int64 {
	t.Helper()
	all, err := jobs.List(context.Background(), a.DB, 5000)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for _, j := range all {
		if j.Kind == kind && j.Key == key && (j.State == "available" || j.State == "retryable" || j.State == "scheduled") && j.ID > id {
			id = j.ID
		}
	}
	if id == 0 {
		t.Fatalf("no queued %s job for %s", kind, key)
	}
	return id
}
