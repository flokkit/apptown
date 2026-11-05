package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/inference"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
)

type checkOverrides struct {
	verification.Effects
	run      func(execRequest) execOutcome
	complete func(string, string, string) (string, *modelcall.Usage, error)
	observe  func(id ids.ID, source, probe string) (conformance.Outcome, string)
}

func (e checkOverrides) Run(ctx context.Context, r execRequest) execOutcome {
	if e.run != nil {
		return e.run(r)
	}
	return e.Effects.Run(context.Background(), r)
}
func (e checkOverrides) Observe(ctx context.Context, id ids.ID, source, p string) (conformance.Outcome, string) {
	if e.observe != nil {
		return e.observe(id, source, p)
	}
	return e.Effects.Observe(ctx, id, source, p)
}

// Recompose stopped fixture owners through their normal dependencies; overrides
// are per instance and preserve the other effects, including earlier overrides.
func installCheckEffects(t *testing.T, a *App, o checkOverrides) {
	t.Helper()
	a.Work.Stop()
	if o.complete != nil {
		a.checkJudge = fixtureCheckInvoker{o.complete}
	}
	o.Effects = a.evaluator
	a.evaluator = o
	a.scanners = scannersFor(a)
	a.scanSteps = scanStepsFor(a)
	a.verification = changesFor(a)
	a.guidance = guidanceFor(a)
	rebuildWork(t, a, func(*workflow.Dependencies) {})
	a.Work.Stop()
}

// providerQuestion is one chat-completions request the fake provider got.
type providerQuestion struct {
	Model, System, User string
	// Request is the server request: its context ends when the gateway
	// gives up on the call.
	Request *http.Request
}

// fakeModelProvider binds a model provider named "fixture" whose address is
// a test server answering chat completions with answer, and returns the
// pattern of its one enabled model, "fixture/judge". AppTown's own model
// calls to it go through the real gateway: its admission, receipts and
// spend lines. A GET of /models is answered with nothing to discover.
func fakeModelProvider(t *testing.T, bindings *integration.Bindings, answer func(providerQuestion) (status int, body string)) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			http.NotFound(w, r)
			return
		}
		var in struct {
			Model    string
			Messages []struct{ Role, Content string }
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &in); err != nil || r.URL.Path != "/chat/completions" {
			t.Errorf("the fake provider got %s %s", r.URL.Path, raw)
			http.Error(w, "bad request", 400)
			return
		}
		q := providerQuestion{Model: in.Model, Request: r}
		for _, m := range in.Messages {
			switch m.Role {
			case "system":
				q.System = m.Content
			case "user":
				q.User = m.Content
			}
		}
		status, body := answer(q)
		if body == "" {
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	cfg, _ := json.Marshal(map[string]any{"baseUrl": server.URL, "api": "openai-completions", "models": []string{"judge"}})
	if _, err := bindings.Save(context.Background(), "fixture", "model", "fixture", string(cfg), "fixture"); err != nil {
		t.Fatal(err)
	}
	return "fixture/judge"
}

// chatAnswer is a chat-completions answer carrying text and the usage the
// provider reports; cost < 0 reports no price at all.
func chatAnswer(text string, prompt, completion int64, cost float64) string {
	usage := map[string]any{"prompt_tokens": prompt, "completion_tokens": completion}
	if cost >= 0 {
		usage["cost"] = cost
	}
	body, _ := json.Marshal(map[string]any{"model": "vendor/judge-2026", "choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": text}}}, "usage": usage})
	return string(body)
}

// gatewayOver is the inference gateway over db alone, for a test of an
// owner built without an App: AppTown's own calls need no runtime
// identity, network or listener.
func gatewayOver(t *testing.T, db *database.DB) *inference.Gateway {
	t.Helper()
	return inference.New(nil, inference.Identity{}, inference.NewCatalog(db, secret.NewService(db, secret.Config{DataDir: t.TempDir()})), usage.NewRequests(db))
}

// askFunc stands in for the gateway where a test is about the caller's own
// behaviour, not its spend.
type askFunc func(context.Context, modelcall.Call) (string, *modelcall.Usage, error)

func (f askFunc) Ask(ctx context.Context, c modelcall.Call) (string, *modelcall.Usage, error) {
	return f(ctx, c)
}

// setFastSlot points the instance's fast slot at pattern.
func setFastSlot(t *testing.T, a *App, pattern string) {
	t.Helper()
	if err := a.Models.Set(context.Background(), "fast", "standard", pattern, "fixture"); err != nil {
		t.Fatal(fmt.Errorf("setting the fast slot: %w", err))
	}
}

// fixtureCheckInvoker stands in for the isolated agent in owner assembly tests.
// Source is read from the exact commit, including baseline invocations; the
// checkout/task/gateway contracts have their own tests at those owners.
type fixtureCheckInvoker struct {
	complete func(string, string, string) (string, *modelcall.Usage, error)
}

func (j fixtureCheckInvoker) JudgeCheck(ctx context.Context, item check.Item, call modelcall.Call, diff string) verification.CheckResult {
	return invokeFixtureCheck(ctx, item, call, diff, func(call modelcall.Call) (string, *modelcall.Usage, error) {
		return j.complete(call.Model, call.System, call.User)
	})
}

type gatewayFixtureCheckInvoker struct{ gateway modelcall.Completer }

func (j gatewayFixtureCheckInvoker) JudgeCheck(ctx context.Context, item check.Item, call modelcall.Call, diff string) verification.CheckResult {
	return invokeFixtureCheck(ctx, item, call, diff, func(call modelcall.Call) (string, *modelcall.Usage, error) { return j.gateway.Ask(ctx, call) })
}

func invokeFixtureCheck(ctx context.Context, item check.Item, call modelcall.Call, diff string, complete func(modelcall.Call) (string, *modelcall.Usage, error)) verification.CheckResult {
	src, cleanup, err := scm.StageTreeAt(ctx, item.Repository, item.Commit)
	defer cleanup()
	if err != nil {
		return verification.CheckResult{Reason: err.Error()}
	}
	content, _ := os.ReadFile(filepath.Join(src, "README.md"))
	call.System = "one isolated check"
	call.User = item.Rubric + "\n" + string(content) + "\n" + diff
	text, u, err := complete(call)
	r := verification.CheckResult{}
	if u != nil {
		r.Usage = *u
		if u.Incomplete != "" {
			r.Reason = u.Incomplete
			return r
		}
	}
	if err != nil {
		r.Reason = err.Error()
		return r
	}
	var vs []check.Verdict
	if err := json.Unmarshal([]byte(text), &vs); err != nil || len(vs) == 0 {
		r.Reason = "fake agent left no verdict"
		return r
	}
	r.Verdict = vs[0]
	tree, _ := gitexec.Run(ctx, item.Repository, "rev-parse", item.Commit+"^{tree}")
	r.Evidence = check.AgentEvidence{Session: "fixture", Commit: item.Commit, Tree: strings.TrimSpace(tree), TemplateHash: "fixture-prompt", Runtime: "fixture-agent", RequestedModel: call.Model, ServedModel: r.Usage.Model}
	return r
}

func installGatewayChecks(t *testing.T, a *App) {
	t.Helper()
	a.checkJudge = gatewayFixtureCheckInvoker{a.inference}
	installCheckEffects(t, a, checkOverrides{})
}

func setChecksSlot(t *testing.T, a *App, pattern string) {
	t.Helper()
	if err := a.Models.Set(t.Context(), "checks", "standard", pattern, "fixture"); err != nil {
		t.Fatal(err)
	}
}
