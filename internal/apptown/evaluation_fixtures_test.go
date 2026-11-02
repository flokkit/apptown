package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/inference"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
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
func (e checkOverrides) Ask(ctx context.Context, c modelcall.Call) (string, *modelcall.Usage, error) {
	if e.complete != nil {
		return e.complete(c.Model, c.System, c.User)
	}
	return e.Effects.Ask(ctx, c)
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

// setSecondaryFastSlot chooses the independent judge without configuring the
// primary fast model, so checks accidentally asking for fast fail.
func setSecondaryFastSlot(t *testing.T, a *App, pattern string) {
	t.Helper()
	if err := a.Models.Set(t.Context(), "secondary-fast", "standard", pattern, "fixture"); err != nil {
		t.Fatal(err)
	}
}
