//go:build integration

package apptown

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
)

// subagentsTestImage is the pinned Pi runtime with the pinned subagent
// extension, as deploy/apptown-agent/Dockerfile bakes it. Build it from
// deploy/apptown-agent, or with testdata/subagents.Dockerfile from a
// apptown-agent built before the extension was baked, tagged so.
const subagentsTestImage = "apptown-agent:s5-subagents-test"

// A delegated invocation on the worker (R10/R17): the packaged Pi runtime
// with its pinned subagent extension (pi 0.84.3, pi-subagents 0.61.0) runs
// an agent step whose model calls the subagent tool, and the delegate's own
// model call goes through the same controlled endpoint with its own,
// distinct usage (1000 in, 200 out; the parent's calls are single digits).
//
// What the runtime reports, observed here:
//   - a foreground delegation (async:false) reports the delegate's usage in
//     the parent's stream only inside the subagent tool's result:
//     tool_execution_end .result.details.results[].usage {input, output,
//     cacheRead, cacheWrite, cost (a number), turns}, repeated in the
//     toolResult's message_end, turn_end and agent_end, and cumulatively in
//     each tool_execution_update. No assistant message_end carries it.
//   - a background delegation (the tool's default) reports no usage in the
//     parent's stream at all: the tool returns at once, and the delegate's
//     completion, when the parent is still running, arrives only as a
//     "subagent-notify" custom message with its text and the path of its
//     own session file under /apptown/state/sessions/.
//
// So the invocation's recorded usage is the parent's assistant calls alone,
// and it is marked "delegated calls not included" in both cases: a
// background delegate's spend is not in what AppTown reads.
func TestIntegrationDelegatedUsageOnTheWorker(t *testing.T) {
	t.Parallel()
	requireDocker(t)
	label, err := dockertest.Output("image", "inspect", "--format", `{{index .Config.Labels "apptown.pi-subagents.version"}}`, subagentsTestImage)
	if err != nil || strings.TrimSpace(string(label)) == "" {
		t.Skipf("pending: the pinned subagent runtime (%s, labelled apptown.pi-subagents.version) is not on this machine's Docker; build it from deploy/apptown-agent or internal/apptown/testdata/subagents.Dockerfile", subagentsTestImage)
	}
	t.Run("foreground", func(t *testing.T) {
		t.Parallel()
		r := runDelegation(t, false)
		// The parent: 4+7 in, 3+5 out.
		if r.parentCalls != 2 || r.delegateCalls != 1 {
			t.Fatalf("model calls: parent %d, delegate %d", r.parentCalls, r.delegateCalls)
		}
		if got := subagentResultUsage(t, r.output); len(got) != 1 || got[0].Input != 1000 || got[0].Output != 200 {
			t.Fatalf("the delegate's usage in the subagent tool's result: %+v", got)
		}
		r.assertParentOnly(t, 11, 8)
	})
	t.Run("background", func(t *testing.T) {
		t.Parallel()
		r := runDelegation(t, true)
		// The parent: 4 in, 3 out, then 7 in, 5 out for each later call (it
		// may answer again after the delegate's completion notice).
		if r.parentCalls < 2 || r.delegateCalls != 1 {
			t.Fatalf("model calls: parent %d, delegate %d", r.parentCalls, r.delegateCalls)
		}
		if bytes.Contains(r.output, []byte(`"input":1000`)) || len(subagentResultUsage(t, r.output)) != 0 {
			t.Fatal("a background delegate's usage is in the parent's stream: account for it")
		}
		t.Logf("parent calls %d; the delegate's completion notice is in the stream: %v", r.parentCalls, bytes.Contains(r.output, []byte(`"customType":"subagent-notify"`)))
		later := int64(r.parentCalls - 1)
		r.assertParentOnly(t, 4+7*later, 3+5*later)
	})
}

type delegationRun struct {
	a                          *App
	w                          *agentWorkerWorld
	output                     []byte
	parentCalls, delegateCalls int
	status                     workflow.StepState
	evidence                   map[string]any
}

// assertParentOnly: the step passed, and its invocation's recorded usage is
// the parent's calls alone, marked as not including the delegated calls.
func (r delegationRun) assertParentOnly(t *testing.T, input, output int64) {
	t.Helper()
	if r.status != workflow.StatusPass {
		t.Fatalf("the step is %s: %v", r.status, r.evidence)
	}
	inv := r.w.invocations(t, 1)[0]
	spent, found, err := r.a.Usage.Ledger.Observed(context.Background(), inv.Operation)
	if err != nil || !found {
		t.Fatalf("the invocation's usage: %+v %v %v", spent, found, err)
	}
	if spent.Totals.Input != input || spent.Totals.Output != output {
		t.Fatalf("the recorded usage is not the parent's calls alone (%d in, %d out): %+v", input, output, spent.Totals)
	}
	if spent.Incomplete != "delegated calls not included" || !strings.Contains(fmt.Sprint(r.evidence["usageIncomplete"]), "delegated calls not included") {
		t.Fatalf("the delegated calls are not marked as missing: %+v %v", spent, r.evidence["usageIncomplete"])
	}
}

type delegateUsage struct{ Input, Output int64 }

// subagentResultUsage is the usage each finished subagent tool call's
// result reports (tool_execution_end only: the other events repeat it).
func subagentResultUsage(t *testing.T, out []byte) []delegateUsage {
	t.Helper()
	var got []delegateUsage
	for _, line := range bytes.Split(out, []byte("\n")) {
		var ev struct {
			Type     string `json:"type"`
			ToolName string `json:"toolName"`
			Result   struct {
				Details struct {
					Results []struct {
						Usage *delegateUsage `json:"usage"`
					} `json:"results"`
				} `json:"details"`
			} `json:"result"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Type != "tool_execution_end" || ev.ToolName != "subagent" {
			continue
		}
		for _, r := range ev.Result.Details.Results {
			if r.Usage != nil {
				got = append(got, *r.Usage)
			}
		}
	}
	return got
}

// runDelegation runs one agent step on a real worker with the subagent
// runtime, against a controlled model: the parent ("probe") first calls
// the subagent tool (in the background or not), then answers; the scout
// delegate ("delegate") answers at once. A background delegate is given
// time to run: the parent's answer waits for the delegate's call.
func runDelegation(t *testing.T, background bool) delegationRun {
	var mu sync.Mutex
	calls := map[string]int{}
	count := func(model string) int {
		mu.Lock()
		defer mu.Unlock()
		return calls[model]
	}
	sse := func(w http.ResponseWriter, model, delta, finish string, prompt, completion int) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":null}]}\n\n", model, delta)
		fmt.Fprintf(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d}}\n\ndata: [DONE]\n\n", model, finish, prompt, completion)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		calls[req.Model]++
		mu.Unlock()
		switch {
		case req.Model == "delegate":
			sse(w, "delegate", `{"role":"assistant","content":"Delegate finished the lookup."}`, "stop", 1000, 200)
		case bytes.Contains(body, []byte(`"role":"tool"`)):
			if background {
				poll.Until(r.Context(), 30*time.Second, 20*time.Millisecond, func() bool { return count("delegate") > 0 })
			}
			sse(w, "probe", `{"role":"assistant","content":"Step work completed."}`, "stop", 7, 5)
		default:
			args := map[string]any{"agent": "scout", "task": "Say that the lookup is finished."}
			if !background {
				args["async"] = false
			}
			raw, _ := json.Marshal(args)
			call, _ := json.Marshal(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "subagent", "arguments": string(raw)}}}})
			sse(w, "probe", string(call), "tool_calls", 4, 3)
		}
	}))
	t.Cleanup(upstream.Close)

	namespace := fmt.Sprintf("agentsub%d", time.Now().UnixNano()%1_000_000_000)
	removeTaskContainers(t, namespace)
	// The invocation's whole output, as AppTown reads it from the start
	// (one page holds it).
	var outMu sync.Mutex
	var output []byte
	capture := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/output") || r.URL.Query().Get("cursor") != "0" {
				h.ServeHTTP(rw, r)
				return
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			var page execution.OutputPage
			if rec.Code == http.StatusOK && json.Unmarshal(rec.Body.Bytes(), &page) == nil {
				outMu.Lock()
				if len(page.Data) > len(output) {
					output = append([]byte(nil), page.Data...)
				}
				outMu.Unlock()
			}
			for k, v := range rec.Header() {
				rw.Header()[k] = v
			}
			rw.WriteHeader(rec.Code)
			_, _ = rw.Write(rec.Body.Bytes())
		})
	}
	ca := workertest.NewCA(t)
	srv := workertest.StartServerWith(t, ca, &worker.Server{ToolVersions: stock.Versions(), State: t.TempDir(), Tasks: execution.DockerTasks{ToolVersions: stock.Versions(), Namespace: namespace},
		AgentImage: subagentsTestImage, ImageLabels: worker.DockerImages{Binary: "docker"}}, capture)
	var gateway http.Handler
	gatewayURL := serveOnHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gateway.ServeHTTP(w, r) }))
	cfg := Config{DataDir: t.TempDir(), AgentExecutor: ExecutorDocker, AgentImage: "apptown-agent", ConsoleCLI: fixtureCLI(t), Worker: workerClientConfig(t, ca, srv.URL),
		InferenceURL: gatewayURL, InferenceNetworks: []string{"127.0.0.1/32", "192.168.0.0/16", "172.16.0.0/12", "10.0.0.0/8"}}
	a := testApp(t, cfg)
	gateway = a.InferenceHandler()
	a.tasks.Poll = 100 * time.Millisecond
	binding := fmt.Sprintf(`{"model":"fixture/*","providerDef":{"baseUrl":%q,"api":"openai-completions","apiKey":"provider-canary"},"modelsJson":"[{\"id\":\"probe\",\"contextWindow\":32768,\"maxTokens\":1024},{\"id\":\"delegate\",\"contextWindow\":32768,\"maxTokens\":1024}]"}`, upstream.URL+"/v1")
	if _, err := a.Integrations.Bindings.Save(context.Background(), "fixture", "model", "pi", binding, "fixture"); err != nil {
		t.Fatal(err)
	}
	// The delegates (worker on the default slot, scout on the fast one) run
	// on the delegate model.
	if err := a.Models.SeedFallbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{"default", "fast"} {
		if err := a.Models.Set(context.Background(), slot, ids.Nil, "", "standard", "fixture/delegate", "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	run, steps := agentWorkerChange(t, a, []schema.Step{{Name: "build", Uses: "agent/pi", With: map[string]any{"prompt": "Ask the scout, then report.", "executor": "docker", "model": "fixture/probe", "timeoutMinutes": 4}}}, nil)
	onlyOnTheWorker(t, a)
	w := &agentWorkerWorld{a: a, cfg: cfg, namespace: namespace, image: subagentsTestImage, run: run, step: steps["build"], source: workflowSourceForTest(t, a, run)}
	w.execute(t, a)
	out := delegationRun{a: a, w: w, parentCalls: count("probe"), delegateCalls: count("delegate")}
	out.status, out.evidence = stepStatus(t, a, w.step.ID)
	outMu.Lock()
	out.output = append([]byte(nil), output...)
	outMu.Unlock()
	return out
}
