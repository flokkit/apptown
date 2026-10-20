package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"flokkit.io/forge/internal/execfile"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/workflowexec"
)

// Drafting is a model call through the inference owner, made in-process:
// it works on a binding that is not OpenRouter (here Anthropic's dialect),
// retries once with the validation error, and each attempt is its own
// admission attributed to the requesting person's assist workload (receipts
// and unknown usage: inference's TestComplete*). No container or process
// starts.
// A model the binding does not enable, or a disabled person, gets no
// upstream call.
func TestWorkflowDraftIsAModelCallThroughTheInferenceOwner(t *testing.T) {
	t.Parallel()
	alice := access.Principal{Kind: "user", Name: "alice", Via: "session"}
	valid := "apiVersion: forge.flokkit.io/v1\nkind: Workflow\nmetadata: {name: draft-fixture}\nspec:\n  kind: pipeline\n  stages:\n    - name: work\n      steps:\n        - name: done\n          uses: script\n          with: {run: \"true\"}"
	var calls atomic.Int32
	var prompts []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model, System string
			Messages      []struct{ Content string }
		}
		if r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "anthropic-key" || json.NewDecoder(r.Body).Decode(&body) != nil ||
			body.Model != "drafter" || !strings.Contains(body.System, "You write workflow YAML") || !strings.Contains(body.System, "a test project") {
			t.Errorf("upstream request %s %+v", r.URL.Path, body)
		}
		prompts = append(prompts, body.Messages[0].Content)
		text := "not a workflow"
		if calls.Add(1) == 2 {
			text = "```yaml\n" + valid + "\n```"
		}
		reply, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
	}))
	t.Cleanup(upstream.Close)
	processes := filepath.Join(t.TempDir(), "started")
	runner := filepath.Join(t.TempDir(), "docker")
	if err := execfile.Write(runner, []byte("#!/bin/sh\ntouch "+processes+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := testApp(t, Config{ProcessRunner: execution.Runner{DockerBinary: runner}, DockerClient: ownDockerTripwire{t}, InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	if _, err := a.Integrations.Bindings.Save(context.Background(), "anthropic", "model", "pi", mustJSON(map[string]any{"models": []string{"drafter"}, "providerDef": map[string]any{"baseUrl": upstream.URL, "api": "anthropic-messages", "apiKey": "anthropic-key"}}), "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := a.Models.Set(context.Background(), "draft", ids.Nil, "", "standard", "anthropic/drafter", "fixture"); err != nil {
		t.Fatal(err)
	}
	testAccounts(t, a.DB, "alice")
	alice.ID = testPrincipalOf(t, a.DB, "alice")

	// Each attempt is admitted as the workload its token names: the
	// requesting person's assist workload, the same token on the retry.
	var workloads []identity.Workload
	drafter := workflowexec.NewDrafter(&a.runTok, a.modelSelection, func(ctx context.Context, token, model, system, prompt string) (string, error) {
		who, _ := a.runTok.Lookup(token)
		workloads = append(workloads, who)
		return a.inference.Complete(ctx, token, model, system, prompt)
	})
	output, attempts, err := drafter.Draft(context.Background(), "a test project", "draft a tiny workflow", alice, "alice")
	if err != nil || attempts != 2 || output != valid {
		t.Fatalf("draft: %q attempts=%d %v", output, attempts, err)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "failed validation") {
		t.Fatalf("the retry did not carry the validation error: %q", prompts)
	}
	if len(workloads) != 2 || workloads[0] != workloads[1] || workloads[0].Kind != "assist" || workloads[0].Actor != "alice" || workloads[0].Person != "alice" {
		t.Fatalf("attempts attributed to %+v", workloads)
	}
	if _, err := os.Stat(processes); err == nil {
		t.Fatal("drafting started a process")
	}

	if err := a.Models.Set(context.Background(), "draft", ids.Nil, "", "standard", "anthropic/expensive", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := draftAgentFor(a).Draft(context.Background(), "", "draft", alice, "alice"); err == nil || calls.Load() != 2 {
		t.Fatalf("a model the binding does not enable: %v, upstream calls %d", err, calls.Load())
	}
	if err := a.Models.Set(context.Background(), "draft", ids.Nil, "", "standard", "anthropic/drafter", "fixture"); err != nil {
		t.Fatal(err)
	}
	testAdministrator(t, a, "admin")
	disabled := true
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "alice", Disabled: &disabled}, access.Principal{Kind: "user", Name: "admin", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := draftAgentFor(a).Draft(context.Background(), "", "draft", alice, "alice"); err == nil || calls.Load() != 2 {
		t.Fatalf("a disabled person: %v, upstream calls %d", err, calls.Load())
	}
}

// Drafting through the HTTP route acts as the authenticated person, never
// as the request's attribution. A person drafts directly (a personal
// token) or through their console; a console's draft is attributed to the
// console and lasts only while it does: one ended between attempts gets no
// second upstream call. (A disabled console owner: TestDraftingRefusesADisabledConsoleOwner.)
func TestWorkflowDraftActsAsTheAuthenticatedPersonWhileTheirConsoleLives(t *testing.T) {
	t.Parallel()
	valid := "apiVersion: forge.flokkit.io/v1\nkind: Workflow\nmetadata: {name: draft-fixture}\nspec:\n  kind: pipeline\n  stages:\n    - name: work\n      steps:\n        - name: done\n          uses: script\n          with: {run: \"true\"}"
	var calls atomic.Int32
	var onCall func(n int32) string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": onCall(calls.Add(1))}}})
		_, _ = w.Write(reply)
	}))
	t.Cleanup(upstream.Close)
	a := testApp(t, Config{AuthMode: "internal", ConsoleAgent: consoletest.NewAgent(), InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	if _, err := a.Integrations.Bindings.Save(context.Background(), "native", "model", "pi", mustJSON(map[string]any{"models": []string{"drafter"}, "providerDef": map[string]any{"baseUrl": upstream.URL, "api": "anthropic-messages", "apiKey": "fixture"}}), "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := a.Models.Set(context.Background(), "draft", ids.Nil, "", "standard", "native/drafter", "fixture"); err != nil {
		t.Fatal(err)
	}
	personal := mcp_token(t, a, "drafting", "alice", "member")
	m := consoleMetaFor("alice-console", ids.Nil, ids.Nil)
	m.Actor, m.Token = "alice", "alice-console-token"
	m.Labels[conversation.OpenerLabel] = testPrincipalOf(t, a.DB, "alice").String()
	s := a.Console.Restore(m)
	draft := func(header, value string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/assist/workflow", strings.NewReader(`{"description":"draft a workflow"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(header, value)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w
	}

	onCall = func(int32) string { return valid }
	if w := draft("Authorization", "Bearer "+personal); w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("a person drafting directly: %d %s", w.Code, w.Body.String())
	}
	if w := draft("X-AppTown-Session-Token", m.Token); w.Code != 200 || calls.Load() != 2 {
		t.Fatalf("a person drafting through their console: %d %s", w.Code, w.Body.String())
	}

	// The console's draft is attributed to the console and acts as alice.
	var workloads []identity.Workload
	drafter := workflowexec.NewDrafter(&a.runTok, a.modelSelection, func(ctx context.Context, token, model, system, prompt string) (string, error) {
		who, _ := a.runTok.Lookup(token)
		workloads = append(workloads, who)
		return a.inference.Complete(ctx, token, model, system, prompt)
	})
	if _, _, err := drafter.Draft(context.Background(), "", "draft", conversation.Principal(s), "alice via console/"+s.ID); err != nil {
		t.Fatal(err)
	}
	if len(workloads) != 1 || workloads[0] != (identity.Workload{Kind: "assist", Actor: "alice via console/" + s.ID, Person: "alice", Principal: testPrincipalOf(t, a.DB, "alice"), SessionID: s.ID}) {
		t.Fatalf("console draft workload: %+v", workloads)
	}

	// The console ends while its first attempt is in flight; the retry is
	// refused before any upstream call.
	onCall = func(int32) string {
		if _, err := a.Console.End(context.Background(), s.ID); err != nil {
			t.Error(err)
		}
		return "not a workflow"
	}
	before := calls.Load()
	if w := draft("X-AppTown-Session-Token", m.Token); w.Code == 200 || calls.Load() != before+1 {
		t.Fatalf("an ended console kept drafting: %d %s, upstream calls %d", w.Code, w.Body.String(), calls.Load()-before)
	}
}
