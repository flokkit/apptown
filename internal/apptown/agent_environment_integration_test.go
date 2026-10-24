//go:build integration

package apptown

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	consolepi "github.com/flokkit/apptown/internal/console/pi"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// This launches the real image through AppTown's assembled environment, but sends
// no model prompt. The provider is a canary: it must never enter staged files.
func TestIntegrationAgentEnvironmentConsoleLaunch(t *testing.T) {
	requireDocker(t)
	// Packaging supplies a Linux artifact. Launch outside the source tree to
	// exercise the real mount without relying on runtime source compilation.
	cli := filepath.Join(t.TempDir(), "apptown-linux")
	if out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
		Argv: []string{"go", "build", "-o", cli, "../../cmd/apptown"}, Env: []string{"GOOS=linux", "CGO_ENABLED=0"}, Timeout: time.Minute,
	}); out.RunErr != nil {
		t.Fatalf("build console CLI: %v: %s", out.RunErr, out.Output)
	}
	t.Chdir(t.TempDir())

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer provider-canary" {
			t.Error("missing provider credential")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"smoke\",\"object\":\"chat.completion.chunk\",\"model\":\"probe\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Console smoke completed.\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"smoke\",\"object\":\"chat.completion.chunk\",\"model\":\"probe\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	runtimeURL := fmt.Sprintf("http://host.docker.internal:%d", listener.Addr().(*net.TCPAddr).Port)
	a := testApp(t, Config{ConsoleCLI: cli, AgentImage: "apptown-agent", InferenceURL: runtimeURL, InferenceNetworks: []string{"127.0.0.1/32", "192.168.0.0/16", "172.16.0.0/12", "10.0.0.0/8"}})
	proxy := &http.Server{Handler: a.InferenceHandler(), ReadHeaderTimeout: 5 * time.Second}
	go proxy.Serve(listener)
	defer proxy.Close()
	config := fmt.Sprintf(`{"model":"fixture/*","providerDef":{"baseUrl":%q,"api":"openai-completions","apiKey":"provider-canary"},"modelsJson":"[{\"id\":\"probe\",\"contextWindow\":32768,\"maxTokens\":1024}]"}`, upstream.URL+"/v1")
	if _, err := a.Integrations.Bindings.Save(context.Background(), "fixture", "model", "pi", config, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := a.Models.Set(context.Background(), "chat", ids.Nil, "", "standard", "fixture/probe", "fixture"); err != nil {
		t.Fatal(err)
	}
	// A console acts for a person, and its runtime credential opens the
	// inference door only while that person's account is enabled
	// (inference.Identity.admitAccount): the console's actor has an account,
	// as every person who opens a chat does.
	testAccounts(t, a.DB, "fixture")
	// The chat's project exists: its model is resolved against its profile.
	if _, err := database.Transact(context.Background(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: idstest.N(1), Slug: "smoke", Name: "Smoke"})
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	// A chat opened through the product records its opener's principal
	// (conversation.Open); the runtime door judges that person.
	s, err := a.Console.Open(ctx, console.OpenRequest{Scope: "environment-smoke", Labels: map[string]string{conversation.ProjectLabel: idstest.N(1).String(), conversation.OpenerLabel: testPrincipalOf(t, a.DB, "fixture").String()}, Workspace: console.Workspace{Path: t.TempDir()}, Actor: "fixture", Model: "fixture/probe"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := a.Console.End(context.Background(), s.ID); err != nil {
			t.Error(err)
		}
	})
	if out, err := (docker.CLI{}).Run(ctx, "", docker.Command{Args: []string{"exec", consolepi.ContainerName(s.ID), "apptown", "help"}}); err != nil || !strings.Contains(string(out), "projects") {
		t.Fatalf("CLI unavailable inside real console: %v: %s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, "pi-home", ".pi", "agent", "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), runtimeURL) || !strings.Contains(string(raw), "$APPTOWN_SESSION_TOKEN") || strings.Contains(string(raw), "provider-canary") {
		t.Fatal("generated inference boundary is wrong")
	}

	// Use the real harness both before and after the idle/reconnect boundary.
	turn := func(want int) {
		t.Helper()
		if err := a.Console.Send(ctx, s.ID, "Reply briefly without tools."); err != nil {
			t.Fatal(err)
		}
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			page, err := a.Console.Messages(ctx, s.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			completed := 0
			for _, m := range page.Messages {
				if m.Role == "helper" && strings.Contains(m.Text, "Console smoke completed.") {
					completed++
				}
			}
			if completed >= want && page.Turn == console.HandedBack {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("console turn did not finish: %+v", page)
			case <-ticker.C:
			}
		}
	}
	turn(1)
	if err := a.Console.Stop(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Console.Identity(s.Token); !ok {
		t.Fatal("idle stop revoked the session")
	}
	terminal, err := a.Console.Attach(ctx, s.ID, console.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Console.Identity(s.Token); !ok {
		t.Fatal("reconnection lost the session")
	}
	turn(2)
	var receipts int
	// sql-guarantee: inference_requests is written by the gateway and read back by no owner per request; each console turn's attribution and observed usage receipt is the guarantee
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM inference_requests WHERE kind='console' AND actor='fixture' AND session_id=? AND outcome='complete' AND usage_json->>'observed'='true'`, s.ID).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatalf("console attribution/receipts: %d %v", receipts, err)
	}

}

// Exercise the actual image, harness, proxy and Workflow settlement together.
// A local provider serves a deterministic completion; no paid API is contacted.
func TestIntegrationWorkflowAgentExecution(t *testing.T) {
	requireDocker(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer provider-canary" {
			t.Error("proxy did not supply the provider credential")
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"smoke\",\"object\":\"chat.completion.chunk\",\"model\":\"probe\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Smoke execution completed.\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"smoke\",\"object\":\"chat.completion.chunk\",\"model\":\"probe\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":3,\"total_tokens\":7}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	a := testApp(t, Config{AgentImage: "apptown-agent", AgentExecutor: ExecutorDocker, InferenceURL: fmt.Sprintf("http://host.docker.internal:%d", port), InferenceNetworks: []string{"127.0.0.1/32", "192.168.0.0/16", "172.16.0.0/12", "10.0.0.0/8"}})
	proxy := &http.Server{Handler: a.InferenceHandler(), ReadHeaderTimeout: 5 * time.Second}
	go proxy.Serve(listener)
	defer proxy.Close()
	config := fmt.Sprintf(`{"model":"fixture/*","providerDef":{"baseUrl":%q,"api":"openai-completions","apiKey":"provider-canary"},"modelsJson":"[{\"id\":\"probe\",\"name\":\"probe\",\"contextWindow\":32768,\"maxTokens\":1024}]"}`, upstream.URL+"/v1")
	if _, err := a.Integrations.Bindings.Save(context.Background(), "fixture", "model", "pi", config, "fixture"); err != nil {
		t.Fatal(err)
	}
	_, stepID := admittedAgentStep(t, a, `{"prompt":"Reply briefly. Do not use tools.","model":"fixture/probe","timeoutMinutes":1}`)
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	status, evidence := stepStatus(t, a, stepID)
	if status != workflow.StatusPass || calls.Load() == 0 {
		t.Fatalf("actual agent: status=%s calls=%d evidence=%+v", status, calls.Load(), evidence)
	}
	if !strings.Contains(fmt.Sprint(evidence["output"]), "Smoke execution completed") {
		t.Fatalf("missing actual response: %+v", evidence)
	}
}
