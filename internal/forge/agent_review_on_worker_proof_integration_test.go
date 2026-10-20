//go:build integration

package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/execfile"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/deploy/docker/dockertest"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// The composed proof of agent and review steps on a worker. What is real and what is controlled:
//   - real: apptown-worker's server in this process, over mutual TLS, running
//     its tasks on this machine's Docker in the packaged Pi runtime
//     (forge-agent:latest); the forge CLI built for Linux, as packaging
//     does; the forge's inference gateway and API, reached from inside the
//     task over the network, each checking the attempt's derived token; a
//     forge restart (a new App on the same database and data directory);
//     the dispatcher running the review after the agent settles;
//   - controlled: the model. An endpoint behind the gateway answers with a
//     fixed script of turns (tool calls the runtime executes for real);
//   - refused: this machine's Docker and process runner. Both record every
//     call; any call other than a container listing fails the test, and the
//     forge's own agent process is the tripwire (localTripwire).
//
// It shows, in one workflow of an agent step followed by a review step:
// the agent calls the model through the gateway with its derived token and
// commits in its own repository and runs `apptown check` through the packaged
// CLI in the step, which names the agent's commit and is refused as not on
// the forge yet, and checks the published tip when asked with --published;
// the worker collects the agent's work as one commit; the forge
// restarts while the invocation runs and the restarted forge settles it,
// once, with one invocation; the candidate is published, the run's folder
// brought forward; the agent's typed artifact exists when the review is
// running; the review runs on the worker and passes; usage is recorded once
// per invocation; nothing runs on this machine.
func TestIntegrationAnAgentAndAReviewRunOnTheWorkerAndNowhereElse(t *testing.T) {
	t.Parallel()
	requireDocker(t)
	const runtimeImage = "forge-agent:latest"
	if dockertest.Run("image", "inspect", runtimeImage) != nil {
		t.Skipf("pending: the packaged Pi runtime (%s) is not on this machine's Docker; it is not pulled here", runtimeImage)
	}
	const reviewMarker = "PROOF-REVIEW-PROMPT"

	// The forge after the restart, and a signal that it serves: the model
	// answers nothing until then, so every model call and CLI request of the
	// step reaches the restarted forge.
	var current atomic.Pointer[App]
	var gatewayHandler, apiHandler http.Handler // set before restarted closes
	restarted := make(chan struct{})

	// The controlled model: tool calls, then a closing text, for the agent
	// and for the reviewer. When the reviewer first calls, the agent's
	// typed artifact is read through its owner, as a witness that it exists
	// while the review runs.
	var mu sync.Mutex
	var calls []string
	var artifactDuringReview map[string]string
	var runRef workflow.RunRef
	sse := func(w http.ResponseWriter, delta, finish string) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"probe\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":null}]}\n\n", delta)
		fmt.Fprintf(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"probe\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n", finish)
	}
	bash := func(w http.ResponseWriter, command string) {
		args, _ := json.Marshal(map[string]string{"command": command})
		call, _ := json.Marshal(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "bash", "arguments": string(args)}}}})
		sse(w, string(call), "tool_calls")
	}
	// The agent's turn: it writes its change and commits it in its own
	// repository at /work/.git (worker/agent_git.go), reads its history and
	// its diff against the base branch, and runs `apptown check` twice
	// through the packaged CLI. The worker's collection squashes the agent's
	// commit, and what it left after, into one commit of its own.
	agentWork := strings.Join([]string{
		`echo "made on the worker" > made.txt`,
		`git add made.txt && git commit -q -m "the agent's own commit" > git-commit.txt 2>&1; echo "exit $?" >> git-commit.txt`,
		`git rev-parse HEAD > git-head.txt 2>&1`,
		`git rev-parse --abbrev-ref HEAD > git-branch.txt 2>&1`,
		`git diff --name-only main > git-diff.txt 2>&1; echo "exit $?" >> git-diff.txt`,
		`apptown check > check.txt 2>&1; echo "exit $?" >> check.txt`,
		`apptown check --published > published.txt 2>&1; echo "exit $?" >> published.txt`,
		`echo worked`,
	}, "; ")
	reviewWork := `mkdir -p .forge && printf '{"verdict":"pass","summary":"the change is what was asked","findings":[]}' > .forge/review.json && echo reviewed`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer provider-canary" {
			t.Error("the gateway did not present the provider's credential")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		review, answered := bytes.Contains(body, []byte(reviewMarker)), bytes.Contains(body, []byte(`"role":"tool"`))
		mu.Lock()
		defer mu.Unlock()
		switch {
		case review && !answered:
			calls = append(calls, "review:tool")
			if artifactDuringReview == nil {
				got, err := workflow.ReadRunArtifactsIn(context.Background(), current.Load().DB, runRef, workflow.ArtifactSelection{Kinds: []string{"report"}})
				artifactDuringReview = map[string]string{}
				if err != nil {
					t.Errorf("reading the agent's artifact while the review runs: %v", err)
				}
				for _, a := range got {
					artifactDuringReview[a.Name] = a.Body
				}
			}
			bash(w, reviewWork)
		case review:
			calls = append(calls, "review:text")
			sse(w, `{"role":"assistant","content":"Reviewed: the change is what was asked."}`, "stop")
		case !answered:
			calls = append(calls, "agent:tool")
			bash(w, agentWork)
		default:
			calls = append(calls, "agent:text")
			sse(w, `{"role":"assistant","content":"Step work completed."}`, "stop")
		}
	}))
	defer upstream.Close()

	// The gateway and the API, served on this machine where the task's
	// container reaches them; each records the credential it was shown.
	type seen struct{ path, token string }
	var gatewaySaw, apiSaw []seen
	var firstModelCall *http.Request
	var firstModelBody []byte
	gatewayURL := serveOnHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-restarted
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		mu.Lock()
		gatewaySaw = append(gatewaySaw, seen{r.URL.Path, r.Header.Get("Authorization")})
		if firstModelCall == nil && strings.HasSuffix(r.URL.Path, "/chat/completions") {
			firstModelCall, firstModelBody = r.Clone(context.Background()), body
		}
		mu.Unlock()
		gatewayHandler.ServeHTTP(w, r)
	}))
	apiURL := serveOnHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-restarted
		mu.Lock()
		// The API takes the step's token as its session header; the Git
		// endpoint, where `apptown check` sends the agent's commit to the
		// scratch ref, as a bearer credential git sends to it alone.
		token := r.Header.Get("X-AppTown-Session-Token")
		if strings.HasPrefix(r.URL.Path, "/git/") {
			token, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		apiSaw = append(apiSaw, seen{r.URL.Path, token})
		mu.Unlock()
		apiHandler.ServeHTTP(w, r)
	}))

	// This machine's Docker and process runner refuse and record.
	local := dockerfake.New()
	local.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "ps" {
			return nil, nil, true
		}
		return nil, os.ErrPermission, true
	})
	dir := t.TempDir()
	localCalls := filepath.Join(dir, "calls")
	localBinary := filepath.Join(dir, "local-docker")
	if err := execfile.Write(localBinary, []byte("#!/bin/sh\necho \"local $@\" >> "+localCalls+"\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	namespace := fmt.Sprintf("agentproof%d", time.Now().UnixNano()%1_000_000_000)
	removeTaskContainers(t, namespace)
	ca := workertest.NewCA(t)
	srv := workertest.StartServer(t, ca, &worker.Server{State: t.TempDir(), Tasks: execution.DockerTasks{Namespace: namespace},
		AgentImage: runtimeImage, ImageLabels: worker.DockerImages{Binary: "docker"}})
	cfg := Config{DataDir: t.TempDir(), AuthMode: "internal", ConsoleAPIURL: apiURL, AgentExecutor: ExecutorDocker, AgentImage: "forge-agent",
		ConsoleCLI: linuxForgeCLI(t), Worker: workerClientConfig(t, ca, srv.URL), DockerClient: local,
		ProcessRunner: execution.Runner{DockerBinary: localBinary},
		InferenceURL:  gatewayURL, InferenceNetworks: []string{"127.0.0.1/32", "192.168.0.0/16", "172.16.0.0/12", "10.0.0.0/8"}}
	a := testApp(t, cfg)
	cfg.DataDir = a.cfg.dataDir
	current.Store(a)
	a.tasks.Poll, a.tasks.Patience = 100*time.Millisecond, 2*time.Second
	binding := fmt.Sprintf(`{"model":"fixture/*","providerDef":{"baseUrl":%q,"api":"openai-completions","apiKey":"provider-canary"},"modelsJson":"[{\"id\":\"probe\",\"contextWindow\":32768,\"maxTokens\":1024}]"}`, upstream.URL+"/v1")
	if _, err := a.Integrations.Bindings.Save(t.Context(), "fixture", "model", "pi", binding, "fixture"); err != nil {
		t.Fatal(err)
	}
	run, steps := agentWorkerChange(t, a, []schema.Step{
		{Name: "build", Uses: "agent/pi", With: map[string]any{"prompt": "Make the change, then check it.", "executor": "docker", "model": "fixture/probe", "timeoutMinutes": 5,
			"output": map[string]any{"kind": "report", "name": "summary"}}},
		{Name: "review", Uses: "agent/review", With: map[string]any{"prompt": reviewMarker + ": check the change.", "model": "fixture/probe", "timeoutMinutes": 5}},
	}, nil)
	p, err := a.Projects.BySlug(context.Background(), "agents")
	if err != nil {
		t.Fatal(err)
	}
	a.addGrant("user", "alice", access.RoleProjectApprover, "", access.ScopeProject, fmt.Sprint(p.ID), "fixture", "alice", "")
	onlyOnTheWorker(t, a)
	build, review := steps["build"], steps["review"]
	source := workflowSourceForTest(t, a, run)
	hub, folder := source.Repository(a.cfg.dataDir), source.Workspace(a.cfg.dataDir)
	mu.Lock()
	runRef = workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID}
	mu.Unlock()
	invocations := func(step *workflow.Step) []tasks.Attempt {
		t.Helper()
		got, err := tasks.New(a.DB).Invocations(context.Background(), step.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// The agent's invocation starts on the worker; this forge then stops
	// (it abandons the step there, as a process that exits would).
	a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), build.ID); err != nil {
		t.Fatal(err)
	}
	if got := invocations(build); len(got) != 1 || got[0].Observed != tasks.Started {
		t.Fatalf("before the restart: %+v", got)
	}
	if status, _ := stepStatus(t, a, build.ID); status != workflow.StatusRunning {
		t.Fatalf("before the restart the agent step is %s", status)
	}

	// The restarted forge: a new App on the same database and data
	// directory, which runs startup repair and resumes recorded work, and
	// whose dispatcher runs the review once the agent settles.
	b := newApp(t, a.DB, cfg)
	b.tasks.Poll, b.tasks.Patience = 100*time.Millisecond, 2*time.Second
	b.agents = agentExecutionFor(b, localTripwire{t})
	rebuildWork(t, b, func(*workflow.Dependencies) {})
	if err := b.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	current.Store(b)
	gatewayHandler, apiHandler = b.InferenceHandler(), b.Handler()
	close(restarted)
	b.ResumePendingAutoSteps(t.Context())

	awaitStepFor(t, b, build.ID, workflow.StatusPass, 4*time.Minute)
	_, buildEv := stepStatus(t, b, build.ID)
	if buildEv["output"] != "Step work completed." || buildEv["worker"] != "worker at "+srv.URL || buildEv["image"] == "" {
		t.Fatalf("the agent step's evidence: %v", buildEv)
	}

	// The agent's work is its candidate, published: the branch is the
	// candidate's tip, the collection's commit of what the agent left on the
	// step's source, and the run's folder is brought forward to it.
	stepSource := fmt.Sprint(buildEv["source"])
	tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
	if candidate := gitexec.HubRef(context.Background(), hub, repositoryedit.CandidateRef(build.ID, 1)); candidate == "" || candidate != tip || tip == stepSource {
		t.Fatalf("the branch is at %s, the candidate at %q, the step's source %s", tip, candidate, stepSource)
	}
	if parent := strings.TrimSpace(gittest.Run(t, hub, "rev-parse", tip+"^")); parent != stepSource {
		t.Fatalf("the published tip %s builds on %s, not the step's source %s", tip, parent, stepSource)
	}
	if got := gittest.Run(t, hub, "show", tip+":made.txt"); got != "made on the worker" {
		t.Fatalf("the published branch holds %q", got)
	}
	// The agent committed in its own repository, on the change's branch,
	// and saw its change against the base branch. Its commit is not the
	// published one: the collection made one commit of what it left.
	if got := gittest.Run(t, hub, "show", tip+":git-commit.txt"); got != "exit 0" {
		t.Fatalf("the agent's commit: %s", got)
	}
	agentHead := strings.TrimSpace(gittest.Run(t, hub, "show", tip+":git-head.txt"))
	if len(agentHead) != 40 || agentHead == tip || agentHead == stepSource {
		t.Fatalf("the agent's HEAD %q (the published tip %s, the step's source %s)", agentHead, tip, stepSource)
	}
	if got := gittest.Run(t, hub, "show", tip+":git-branch.txt"); strings.TrimSpace(got) != source.Branch {
		t.Fatalf("the agent's branch %q, the change's %s", got, source.Branch)
	}
	if got := gittest.Run(t, hub, "show", tip+":git-diff.txt"); !strings.Contains(got, "made.txt") || !strings.HasSuffix(got, "exit 0") {
		t.Fatalf("the agent's diff against main: %s", got)
	}
	if subjects := gittest.Run(t, hub, "log", "--format=%s", stepSource+".."+tip); strings.Contains(subjects, "the agent's own commit") || strings.Count(strings.TrimSpace(subjects), "\n") != 0 {
		t.Fatalf("the published commits after the step's source: %q", subjects)
	}
	if at, err := gitexec.FolderTip(context.Background(), folder, source.Branch); err != nil || at != tip {
		t.Fatalf("the run's folder is at %q, the branch at %s (%v)", at, tip, err)
	}
	if behind, err := scm.Behind(context.Background(), folder, hub); err != nil || behind {
		t.Fatalf("the run's folder is behind: %v %v", behind, err)
	}
	if refused := gittest.Run(t, hub, "for-each-ref", "refs/forge/refused"); refused != "" {
		t.Fatalf("work was refused: %s", refused)
	}

	// `apptown check` ran through the packaged CLI inside the step, with the
	// attempt's token, against the restarted forge. It named the agent's
	// commit, this folder's HEAD, sent it to the attempt's scratch ref and
	// had it checked, as feedback only (workers.md, "apptown check inside a
	// step"); the scratch ref went when the attempt settled. --published
	// names the step's source as the published tip.
	check := gittest.Run(t, hub, "show", tip+":check.txt")
	if !strings.Contains(check, "checking commit "+agentHead+" (this folder's HEAD)") || !strings.Contains(check, "commit "+agentHead+" (your work in progress, sent for checking: this result is feedback only") || !strings.HasSuffix(check, "exit 0") {
		t.Fatalf("apptown check inside the step answered (the agent's HEAD %s):\n%s", agentHead, check)
	}
	// The ref goes once the outcome is recorded (the runner's deferred
	// drop), so a moment after the step reads as passed.
	var left string
	poll.Until(t.Context(), 30*time.Second, 100*time.Millisecond, func() bool {
		left = gittest.Run(t, hub, "for-each-ref", "refs/forge/scratch")
		return left == ""
	})
	if left != "" {
		t.Fatalf("the attempt's scratch ref outlived it: %s", left)
	}
	published := gittest.Run(t, hub, "show", tip+":published.txt")
	if !strings.Contains(published, "commit "+stepSource+" (the published tip") || !strings.HasSuffix(published, "exit 0") {
		t.Fatalf("apptown check --published inside the step answered (source %s):\n%s", stepSource, published)
	}

	// The review ran after the agent settled, on the worker, and passed.
	awaitStepFor(t, b, review.ID, workflow.StatusPass, 4*time.Minute)
	_, reviewEv := stepStatus(t, b, review.ID)
	if reviewEv["verdict"] != "pass" || reviewEv["worker"] != "worker at "+srv.URL {
		t.Fatalf("the review's evidence: %v", reviewEv)
	}
	if reviewEv["source"] != tip {
		t.Fatalf("the review read %v, not the published agent work %s", reviewEv["source"], tip)
	}
	mu.Lock()
	during, modelCalls := artifactDuringReview, strings.Join(calls, ",")
	mu.Unlock()
	if during["summary"] != "Step work completed." {
		t.Fatalf("the agent's artifact while the review ran: %v", during)
	}
	if modelCalls != "agent:tool,agent:text,review:tool,review:text" {
		t.Fatalf("the model was called: %s", modelCalls)
	}

	// One invocation per step, each settled and released; the agent's was
	// started by the first forge and settled by the second. Usage is
	// recorded once per invocation: the run's total is exactly their sum.
	// An image with pi-subagents promises the agent delegation, whose calls
	// its stream does not carry: the agent's usage then says it is
	// incomplete. A review is not offered delegation.
	incomplete := map[ids.ID]string{}
	if v, _ := buildEv["subagentsVersion"].(string); v != "" {
		incomplete[build.ID] = "delegated calls not included"
	}
	var sum int64
	for _, step := range []*workflow.Step{build, review} {
		awaitAttemptReleased(t, &agentWorkerWorld{a: b, step: step}, 1)
		got := invocations(step)
		want := map[ids.ID]string{build.ID: "agent", review.ID: "review"}[step.ID]
		if len(got) != 1 || got[0].Invocation != want || got[0].Observed != tasks.Settled || got[0].Backend != "worker at "+srv.URL {
			t.Fatalf("%s's invocations: %+v", step.Name, got)
		}
		spent, found, err := b.Usage.Ledger.Observed(context.Background(), got[0].Operation)
		if err != nil || !found || spent.Totals.Total == 0 || spent.Incomplete != incomplete[step.ID] {
			t.Fatalf("%s's usage: %+v %v %v", step.Name, spent, found, err)
		}
		sum += spent.Totals.Total
	}
	if total, _, err := b.Usage.Ledger.RunTotal(t.Context(), run.ID); err != nil || total != sum {
		t.Fatalf("the run's recorded usage is %d, its invocations' %d (%v)", total, sum, err)
	}

	// The gateway and the API were shown the attempts' derived tokens and
	// nothing else; a settled attempt's token no longer opens the gateway.
	agentToken, reviewToken := b.runTok.Step(build.ID, 1), b.runTok.Step(review.ID, 1)
	mu.Lock()
	gw, api, replay, replayBody := append([]seen(nil), gatewaySaw...), append([]seen(nil), apiSaw...), firstModelCall, firstModelBody
	mu.Unlock()
	var toAgent, toReview int
	for _, s := range gw {
		switch s.token {
		case "Bearer " + agentToken:
			toAgent++
		case "Bearer " + reviewToken:
			toReview++
		default:
			t.Fatalf("the gateway was shown %q for %s", s.token, s.path)
		}
	}
	if toAgent == 0 || toReview == 0 {
		t.Fatalf("model calls with the agent's token %d, the review's %d", toAgent, toReview)
	}
	var checks, pushes int
	for _, s := range api {
		if s.token != agentToken {
			t.Fatalf("the API was shown %q for %s", s.token, s.path)
		}
		if strings.HasSuffix(s.path, "/check") {
			checks++
		}
		if strings.HasSuffix(s.path, "/git-receive-pack") {
			pushes++
		}
	}
	if checks != 2 || pushes != 1 {
		t.Fatalf("apptown check reached the API %d times and pushed to the scratch ref %d times: %+v", checks, pushes, api)
	}
	replay.Body = io.NopCloser(bytes.NewReader(replayBody))
	replay.RemoteAddr = "127.0.0.1:40000"
	replay.RequestURI = ""
	rec := httptest.NewRecorder()
	gatewayHandler.ServeHTTP(rec, replay)
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("the settled agent attempt's token was answered %d: %s", rec.Code, rec.Body)
	}
	t.Logf("apptown check in the step:\n%s\nforge check --published:\n%s\nmodel calls %s through the gateway (%d with the agent's token, %d the review's); the settled attempt's token now: %d %s; this machine's Docker was asked %v",
		check, published, modelCalls, toAgent, toReview, rec.Code, strings.TrimSpace(rec.Body.String()), local.Commands())

	// Nothing ran on this machine: its Docker was asked only for listings,
	// its process runner never, and the tripwire never fired.
	for _, c := range local.Commands() {
		if len(c) == 0 || c[0] != "ps" {
			t.Errorf("this machine's Docker was asked to %v", c)
		}
	}
	if ran, err := os.ReadFile(localCalls); err == nil {
		t.Errorf("this machine's process runner ran: %s", ran)
	}
}

// awaitStepFor waits, up to within, for the step to reach want.
func awaitStepFor(t *testing.T, a *App, id ids.ID, want workflow.StepState, within time.Duration) {
	t.Helper()
	var step *workflow.Step
	var err error
	poll.Until(context.Background(), within, 50*time.Millisecond, func() bool {
		step, err = workflow.ReadStepIn(context.Background(), a.DB, id)
		return err != nil || step.Status == want || step.Status == workflow.StatusFail || step.Status == workflow.StatusError
	})
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != want {
		t.Fatalf("step %d is %s, want %s: %s", id, step.Status, want, step.Evidence)
	}
}
