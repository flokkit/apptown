//go:build integration

package apptown

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/testinputs"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
)

// linuxAppTownCLI builds AppTown CLI an agent is given, for Linux, as
// packaging does.
func linuxAppTownCLI(t *testing.T) string {
	t.Helper()
	cli := filepath.Join(t.TempDir(), "apptown-linux")
	testinputs.GoBuild(t, "", []string{"GOOS=linux", "CGO_ENABLED=0"}, "../../cmd/apptown")
	if out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
		Argv: []string{"go", "build", "-o", cli, "../../cmd/apptown"}, Env: []string{"GOOS=linux", "CGO_ENABLED=0"}, Timeout: 2 * time.Minute,
	}); out.RunErr != nil {
		t.Fatalf("building the agents' CLI: %v: %s", out.RunErr, out.Output)
	}
	return cli
}

// serveOnHost serves h on every interface of this machine and returns the
// address a container reaches it at.
func serveOnHost(t *testing.T, h http.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(listener)
	t.Cleanup(func() { srv.Close() })
	return fmt.Sprintf("http://%s:%d", containerHost(t), listener.Addr().(*net.TCPAddr).Port)
}

// containerHost is this machine's name inside the containers its Docker
// starts. Docker Desktop resolves host.docker.internal in every container; a
// Linux daemon resolves it only where it is added as a host entry, and a
// worker's task containers get none, so there it is the default bridge's
// gateway, an address of this machine.
func containerHost(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		return "host.docker.internal"
	}
	out, err := dockertest.CombinedOutput("network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}")
	gateway := strings.TrimSpace(string(out))
	if err != nil || net.ParseIP(gateway) == nil {
		t.Fatalf("the default bridge's gateway: %v: %s", err, out)
	}
	return gateway
}

// The packaged apptown CLI runs inside an agent step on the worker, from its
// /apptown/in path, and AppTown's API admits it with the attempt's derived
// token alone (on an AppTown that admits no one without credentials); the
// same request with another token is refused.
func TestIntegrationThePackagedCLIAnswersInsideAStepWithTheDerivedToken(t *testing.T) {
	t.Parallel()
	requireDocker(t)
	image := fakePiImage(t)
	namespace := fmt.Sprintf("agentcli%d", time.Now().UnixNano()%1_000_000_000)
	removeTaskContainers(t, namespace)
	ca := workertest.NewCA(t)
	srv := workertest.StartServer(t, ca, &worker.Server{ToolVersions: stock.Versions(), State: t.TempDir(), Tasks: execution.DockerTasks{ToolVersions: stock.Versions(), Namespace: namespace},
		AgentImage: image, ImageLabels: worker.DockerImages{Binary: "docker"}})
	// The API's address is known only once it listens: the App is served
	// behind a handler that is set once it exists.
	var api http.Handler
	apiURL := serveOnHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { api.ServeHTTP(w, r) }))
	a := testApp(t, Config{AuthMode: "internal", ConsoleAPIURL: apiURL, AgentExecutor: ExecutorDocker, AgentImage: "apptown-agent",
		ConsoleCLI: linuxAppTownCLI(t), Worker: workerClientConfig(t, ca, srv.URL)})
	api = a.Handler()
	a.tasks.Poll = 100 * time.Millisecond
	run, steps := agentWorkerChange(t, a, []schema.Step{{Name: "build", Uses: "agent/pi", With: map[string]any{"prompt": "ask AppTown", "executor": "docker"}}},
		map[string]string{"fake-pi.sh": `
apptown status "$APPTOWN_RUN_ID" > cli.txt 2>&1; echo "exit $?" >> cli.txt
APPTOWN_SESSION_TOKEN=rt_1_1_bogus apptown status "$APPTOWN_RUN_ID" > denied.txt 2>&1; echo "exit $?" >> denied.txt
say "asked AppTown"`})
	p, err := a.Projects.BySlug(context.Background(), "agents")
	if err != nil {
		t.Fatal(err)
	}
	a.addGrant("user", "alice", access.RoleProjectApprover, "", access.ScopeProject, fmt.Sprint(p.ID), "fixture", "alice", "")
	onlyOnTheWorker(t, a)
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), steps["build"].ID); err != nil {
		t.Fatal(err)
	}
	if status, ev := stepStatus(t, a, steps["build"].ID); status != workflow.StatusPass {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	source := workflowSourceForTest(t, a, run)
	hub := requireLocation(t)(source.Repository(a.cfg.dataDir))
	cli := gittest.Run(t, hub, "show", "refs/heads/"+source.Branch+":cli.txt")
	if !strings.HasSuffix(cli, "exit 0") || !strings.Contains(cli, "build") {
		t.Fatalf("the CLI inside the step answered:\n%s", cli)
	}
	if denied := gittest.Run(t, hub, "show", "refs/heads/"+source.Branch+":denied.txt"); strings.HasSuffix(denied, "exit 0") {
		t.Fatalf("another token was admitted:\n%s", denied)
	}
}

// `apptown check` inside an agent step on the worker checks the agent's own
// commit, which AppTown has not collected yet: the packaged CLI pushes it
// to the attempt's scratch ref through the read-only listener, with the
// attempt's token, and prints that the result is feedback only. Once the
// attempt has settled, its scratch ref is gone.
func TestIntegrationAppTownCheckOnTheWorkerChecksTheAgentsCommitAsFeedback(t *testing.T) {
	t.Parallel()
	requireDocker(t)
	image := fakePiGitImage(t)
	namespace := fmt.Sprintf("agentscratch%d", time.Now().UnixNano()%1_000_000_000)
	removeTaskContainers(t, namespace)
	ca := workertest.NewCA(t)
	srv := workertest.StartServer(t, ca, &worker.Server{ToolVersions: stock.Versions(), State: t.TempDir(), Tasks: execution.DockerTasks{ToolVersions: stock.Versions(), Namespace: namespace},
		AgentImage: image, ImageLabels: worker.DockerImages{Binary: "docker"}})
	var api http.Handler
	apiURL := serveOnHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { api.ServeHTTP(w, r) }))
	a := testApp(t, Config{AuthMode: "internal", ConsoleAPIURL: apiURL, AgentExecutor: ExecutorDocker, AgentImage: "apptown-agent",
		ConsoleCLI: linuxAppTownCLI(t), Worker: workerClientConfig(t, ca, srv.URL)})
	api = GETOnly(a.Handler()) // the agent's $APPTOWN_URL is the read-only listener
	a.tasks.Poll = 100 * time.Millisecond
	run, steps := agentWorkerChange(t, a, []schema.Step{{Name: "build", Uses: "agent/pi", With: map[string]any{"prompt": "check your work", "executor": "docker"}}},
		map[string]string{"fake-pi.sh": `
echo "work in progress" > wip.txt
{ git add wip.txt && git -c user.name=agent -c user.email=agent@example.invalid commit -qm "work in progress"; } > committing.txt 2>&1
git rev-parse HEAD > checked.txt 2>> committing.txt
apptown check > check.txt 2>&1; echo "exit $?" >> check.txt
say "checked my work"`})
	p, err := a.Projects.BySlug(context.Background(), "agents")
	if err != nil {
		t.Fatal(err)
	}
	a.addGrant("user", "alice", access.RoleProjectApprover, "", access.ScopeProject, fmt.Sprint(p.ID), "fixture", "alice", "")
	onlyOnTheWorker(t, a)
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), steps["build"].ID); err != nil {
		t.Fatal(err)
	}
	if status, ev := stepStatus(t, a, steps["build"].ID); status != workflow.StatusPass {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	source := workflowSourceForTest(t, a, run)
	hub := requireLocation(t)(source.Repository(a.cfg.dataDir))
	// The commit the agent checked, and what apptown check printed, as the
	// step's collected work brought them back.
	committed := gittest.Run(t, hub, "show", "refs/heads/"+source.Branch+":checked.txt")
	if !gitexec.IsObjectID(committed) || gittest.Run(t, hub, "log", "-1", "--format=%s", committed) != "work in progress" {
		t.Fatalf("the checked commit is not the agent's: %q\n%s", committed, gittest.Run(t, hub, "show", "refs/heads/"+source.Branch+":committing.txt"))
	}
	check := gittest.Run(t, hub, "show", "refs/heads/"+source.Branch+":check.txt")
	if !strings.HasSuffix(strings.TrimSpace(check), "exit 0") || !strings.Contains(check, "commit "+committed+" (your work in progress, sent for checking: this result is feedback only") {
		t.Fatalf("apptown check inside the step on the worker answered:\n%s", check)
	}
	refs, err := scm.ScratchRefs(context.Background(), hub, scm.ScratchPrefix)
	if err != nil || len(refs) != 0 {
		t.Fatalf("scratch refs after the attempt settled: %v %v", refs, err)
	}
}

// The packaged Pi runtime (AppTown-agent image on this machine's Docker,
// as the worker's agent image) runs an agent step on the worker against a
// controlled model endpoint behind AppTown's gateway, which checks the
// attempt's derived token: the transcript Pi appends to is the file the
// AppTown named, captured when Pi exits and kept by digest; a continue retry
// after the workspace's release and an AppTown restart resumes exactly those
// bytes, and each receipt still reads its own.
func TestIntegrationThePackagedPiRuntimeRunsAnAgentStepOnTheWorker(t *testing.T) {
	t.Parallel()
	requireDocker(t)
	const runtimeImage = "apptown-agent:latest"
	if dockertest.Run("image", "inspect", runtimeImage) != nil {
		t.Skipf("pending: the packaged Pi runtime (%s) is not on this machine's Docker; it is not pulled here", runtimeImage)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer provider-canary" {
			t.Error("the gateway did not present the provider's credential")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"probe\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Step work completed.\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"probe\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	namespace := fmt.Sprintf("agentpi%d", time.Now().UnixNano()%1_000_000_000)
	removeTaskContainers(t, namespace)
	ca := workertest.NewCA(t)
	srv := workertest.StartServer(t, ca, &worker.Server{ToolVersions: stock.Versions(), State: t.TempDir(), Tasks: execution.DockerTasks{ToolVersions: stock.Versions(), Namespace: namespace},
		AgentImage: runtimeImage, ImageLabels: worker.DockerImages{Binary: "docker"}})
	var gateway http.Handler
	gatewayURL := serveOnHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gateway.ServeHTTP(w, r) }))
	cfg := Config{DataDir: t.TempDir(), AgentExecutor: ExecutorDocker, AgentImage: "apptown-agent", ConsoleCLI: fixtureCLI(t), Worker: workerClientConfig(t, ca, srv.URL),
		InferenceURL: gatewayURL, InferenceNetworks: []string{"127.0.0.1/32", "192.168.0.0/16", "172.16.0.0/12", "10.0.0.0/8"}}
	a := testApp(t, cfg)
	gateway = a.InferenceHandler()
	a.tasks.Poll = 100 * time.Millisecond
	binding := fmt.Sprintf(`{"model":"fixture/*","providerDef":{"baseUrl":%q,"api":"openai-completions","apiKey":"provider-canary"},"modelsJson":"[{\"id\":\"probe\",\"contextWindow\":32768,\"maxTokens\":1024}]"}`, upstream.URL+"/v1")
	if _, err := a.Integrations.Bindings.Save(context.Background(), "fixture", "model", "pi", binding, "fixture"); err != nil {
		t.Fatal(err)
	}
	run, steps := agentWorkerChange(t, a, []schema.Step{{Name: "build", Uses: "agent/pi", With: map[string]any{"prompt": "Reply briefly without tools.", "executor": "docker", "model": "fixture/probe", "timeoutMinutes": 3}}}, nil)
	onlyOnTheWorker(t, a)
	w := &agentWorkerWorld{a: a, cfg: cfg, namespace: namespace, image: runtimeImage, run: run, step: steps["build"], source: workflowSourceForTest(t, a, run)}
	w.cfg.DataDir = a.cfg.dataDir

	w.execute(t, a)
	if status, ev := stepStatus(t, a, w.step.ID); status != workflow.StatusPass || ev["output"] != "Step work completed." || ev["piVersion"] == "" {
		t.Fatalf("the first attempt is %s: %v", status, ev)
	}
	awaitAttemptReleased(t, w, 1)
	first, firstRaw := w.transcript(t, 1)
	if first.Session != fmt.Sprintf("step-%d-a1", w.step.ID) || !bytes.Contains(firstRaw, []byte("Step work completed.")) {
		t.Fatalf("the first transcript: %+v\n%s", first, firstRaw)
	}
	inv := w.invocations(t, 1)[0]
	if spent, found, err := a.Usage.Ledger.Observed(context.Background(), inv.Operation); err != nil || !found || spent.Totals.Total == 0 {
		t.Fatalf("the invocation's usage: %+v %v %v", spent, found, err)
	}

	b := w.restartForRetry(t)
	gateway = b.InferenceHandler()
	if _, err := b.Work.RetryStep(asPerson("alice"), workflow.RetryCommand{StepID: w.step.ID, Mode: workflow.RetryContinue, Feedback: "one more"}); err != nil {
		t.Fatal(err)
	}
	w.execute(t, b)
	if status, ev := stepStatus(t, b, w.step.ID); status != workflow.StatusPass {
		t.Fatalf("the continued attempt is %s: %v", status, ev)
	}
	second, secondRaw := w.transcript(t, 2)
	if second.Session != first.Session || !bytes.HasPrefix(secondRaw, firstRaw) || len(secondRaw) <= len(firstRaw) {
		t.Fatalf("the continued transcript does not extend the first:\n%s\n---\n%s", firstRaw, secondRaw)
	}
	if _, raw := w.transcript(t, 1); !bytes.Equal(raw, firstRaw) {
		t.Fatal("the first receipt reads other bytes")
	}
}

// The packaged Pi runtime resumes exactly the transcript file AppTown
// points it at (--session), never another file in the session folder whose
// header names the same session (R15). The agent itself plants two such
// files, one older and one newer by name, then leaves an empty turn; the empty-turn retry starts Pi
// again in the same workspace, and the history it sends the model is the
// named file's, not a planted one's. The captured transcript is the named
// file, recorded with its digest.
func TestIntegrationThePackagedPiRuntimeResumesOnlyTheNamedTranscript(t *testing.T) {
	t.Parallel()
	requireDocker(t)
	const runtimeImage = "apptown-agent:latest"
	if dockertest.Run("image", "inspect", runtimeImage) != nil {
		t.Skipf("pending: the packaged Pi runtime (%s) is not on this machine's Docker; it is not pulled here", runtimeImage)
	}
	var mu sync.Mutex
	var bodies [][]byte
	var session string // the attempt's session id, set before the step runs
	sse := func(w http.ResponseWriter, delta, finish string) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"probe\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":null}]}\n\n", delta)
		fmt.Fprintf(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"probe\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n", finish)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		n, sid := len(bodies), session
		mu.Unlock()
		switch n {
		case 1:
			// Files naming the same session, older and newer by name, with
			// a turn of their own (sent encoded, so only a file resumed from it can
			// carry its words to the model).
			// Its header's folder is Pi's own and its turn is the latest by
			// time, so Pi's own lookup of the session by id would choose it.
			planted := fmt.Sprintf("{\"type\":\"session\",\"version\":3,\"id\":%q,\"timestamp\":\"9999-12-31T00:00:00.000Z\",\"cwd\":\"/work\"}\n", sid) +
				"{\"type\":\"message\",\"id\":\"c0000001\",\"parentId\":null,\"timestamp\":\"9999-12-31T00:00:00.000Z\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"COMPETING HISTORY\"}],\"timestamp\":253402214400000}}\n"
			encoded := base64.StdEncoding.EncodeToString([]byte(planted))
			command := "for at in 0000-01-01T00-00-00-000Z 9999-12-31T00-00-00-000Z; do echo " + encoded + " | base64 -d > /apptown/state/sessions/${at}_" + sid + ".jsonl; done && echo planted"
			args, _ := json.Marshal(map[string]string{"command": command})
			call, _ := json.Marshal(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "bash", "arguments": string(args)}}}})
			sse(w, string(call), "tool_calls")
		case 2:
			sse(w, `{"role":"assistant","content":""}`, "stop") // an empty turn
		default:
			sse(w, `{"role":"assistant","content":"Step work completed."}`, "stop")
		}
	}))
	defer upstream.Close()
	namespace := fmt.Sprintf("agentpin%d", time.Now().UnixNano()%1_000_000_000)
	removeTaskContainers(t, namespace)
	ca := workertest.NewCA(t)
	srv := workertest.StartServer(t, ca, &worker.Server{ToolVersions: stock.Versions(), State: t.TempDir(), Tasks: execution.DockerTasks{ToolVersions: stock.Versions(), Namespace: namespace},
		AgentImage: runtimeImage, ImageLabels: worker.DockerImages{Binary: "docker"}})
	var gateway http.Handler
	gatewayURL := serveOnHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gateway.ServeHTTP(w, r) }))
	cfg := Config{DataDir: t.TempDir(), AgentExecutor: ExecutorDocker, AgentImage: "apptown-agent", ConsoleCLI: fixtureCLI(t), Worker: workerClientConfig(t, ca, srv.URL),
		InferenceURL: gatewayURL, InferenceNetworks: []string{"127.0.0.1/32", "192.168.0.0/16", "172.16.0.0/12", "10.0.0.0/8"}}
	a := testApp(t, cfg)
	gateway = a.InferenceHandler()
	a.tasks.Poll = 100 * time.Millisecond
	binding := fmt.Sprintf(`{"model":"fixture/*","providerDef":{"baseUrl":%q,"api":"openai-completions","apiKey":"provider-canary"},"modelsJson":"[{\"id\":\"probe\",\"contextWindow\":32768,\"maxTokens\":1024}]"}`, upstream.URL+"/v1")
	if _, err := a.Integrations.Bindings.Save(context.Background(), "fixture", "model", "pi", binding, "fixture"); err != nil {
		t.Fatal(err)
	}
	run, steps := agentWorkerChange(t, a, []schema.Step{{Name: "build", Uses: "agent/pi", With: map[string]any{"prompt": "Plant the file, then finish.", "executor": "docker", "model": "fixture/probe", "timeoutMinutes": 3}}}, nil)
	onlyOnTheWorker(t, a)
	w := &agentWorkerWorld{a: a, cfg: cfg, namespace: namespace, image: runtimeImage, run: run, step: steps["build"], source: workflowSourceForTest(t, a, run)}
	w.cfg.DataDir = a.cfg.dataDir
	mu.Lock()
	session = fmt.Sprintf("step-%d-a1", w.step.ID)
	mu.Unlock()

	w.execute(t, a)
	if status, ev := stepStatus(t, a, w.step.ID); status != workflow.StatusPass || ev["output"] != "Step work completed." {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	if inv := w.invocations(t, 1); len(inv) != 2 {
		t.Fatalf("the empty turn was not retried once: %d invocations", len(inv))
	}
	mu.Lock()
	sent := append([][]byte(nil), bodies...)
	mu.Unlock()
	if len(sent) != 3 || !bytes.Contains(sent[1], []byte("planted")) {
		t.Fatalf("the agent did not plant the competing file: %d model calls\n%s", len(sent), sent[len(sent)-1])
	}
	// The retry's history is the named file's: the first invocation's
	// prompt and tool call, and none of the planted file's turn.
	if resumed := sent[2]; bytes.Contains(resumed, []byte("COMPETING HISTORY")) || !bytes.Contains(resumed, []byte("Plant the file, then finish.")) || !bytes.Contains(resumed, []byte("planted")) {
		t.Fatalf("the retry resumed other history:\n%s", resumed)
	}
	receipt, raw := w.transcript(t, 1)
	if receipt.Unavailable != "" || receipt.Digest == "" || bytes.Contains(raw, []byte("COMPETING HISTORY")) || !bytes.Contains(raw, []byte("Step work completed.")) {
		t.Fatalf("the captured transcript: %+v\n%s", receipt, raw)
	}
}
