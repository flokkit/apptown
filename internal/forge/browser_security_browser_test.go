//go:build browser

package forge

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/workflow"
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
			"FORGE_BROWSER_URL=https://" + a.cfg.publicHost,
			"FORGE_SIBLING_URL=https://evil.preview.example.test:" + port,
			"FORGE_OTHER_URL=https://evil.other.test:" + port,
			fmt.Sprintf("FORGE_BROWSER_CHANGE=%d", changeID),
			"FORGE_BROWSER_CONSOLE=" + session.ID,
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
