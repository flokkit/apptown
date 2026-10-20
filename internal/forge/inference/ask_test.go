package inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/modelcall"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/poll"
)

// askWorld is the real gateway over a test database, with one model
// binding, "native", whose provider is a test server.
type askWorld struct {
	db     *database.DB
	g      *Gateway
	ledger *usage.Ledger
	sent   atomic.Int32
}

func newAskWorld(t *testing.T, api string, upstream http.HandlerFunc) *askWorld {
	t.Helper()
	w := &askWorld{db: databaseSuite.New(t)}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" { // discovery
			http.NotFound(rw, r)
			return
		}
		w.sent.Add(1)
		upstream(rw, r)
	}))
	t.Cleanup(server.Close)
	cfg, _ := json.Marshal(map[string]any{"baseUrl": server.URL, "api": api, "models": []string{"judge"}, "providerDef": map[string]any{"apiKey": "provider-key"}})
	if _, err := integration.New(w.db).Save(context.Background(), "native", "model", "native", string(cfg), "fixture"); err != nil {
		t.Fatal(err)
	}
	w.g = New(nil, Identity{}, NewCatalog(w.db, secret.NewService(w.db, secret.Config{DataDir: t.TempDir()})), usage.NewRequests(w.db))
	w.ledger = usage.New(w.db)
	return w
}

// spent is project 7's spending, as the ledger reports it.
func (w *askWorld) spent(t *testing.T) usage.Totals {
	t.Helper()
	got, err := w.ledger.ProjectsTotal(context.Background(), nil, idstest.N(7))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func answering(body string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(rw, body) }
}

var narrative = modelcall.Call{Purpose: "narrative", ProjectID: idstest.N(7), RunID: idstest.N(3), Model: "native/judge", System: "s", User: "u"}

// One of AppTown's own calls is admitted and sent like a runtime's, and
// the gateway writes its one spend line: the call's purpose, project and
// run, the binding and model, and the price the provider reported. The
// caller gets the same usage back for its own budgets.
//
// Kills: spend left to callers (no line); a line per retry of settlement;
// the binding or purpose lost from attribution; a reported price dropped.
func TestAForgeCallWritesItsOneSpendLine(t *testing.T) {
	t.Parallel()
	w := newAskWorld(t, "openai-completions", answering(`{"model":"vendor/judge-1","choices":[{"message":{"content":"fine"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.002}}`))
	text, u, err := w.g.Ask(context.Background(), narrative)
	if err != nil || text != "fine" || u == nil || u.Prompt != 10 || u.Completion != 5 || u.Cost != 0.002 || u.Model != "vendor/judge-1" || u.Incomplete != "" {
		t.Fatalf("answer: %q %+v %v", text, u, err)
	}
	if got := w.spent(t); got.Input != 10 || got.Output != 5 || got.Total != 15 || got.CostMicros != 2000 || got.Incomplete != 0 {
		t.Fatalf("the spend line: %+v", got)
	}
	report, err := w.ledger.Summary(context.Background(), 1, idstest.N(7), "")
	if err != nil || len(report.ByKind) != 1 || report.ByKind[0].Key != "narrative" || len(report.ByModel) != 1 || report.ByModel[0].Key != "native/judge" {
		t.Fatalf("attribution: %+v %v", report, err)
	}
}

// Admission comes first, and a refusal sends nothing and writes nothing: a
// model its binding does not enable, a binding that does not exist, a
// refused eval model choice, and the judge's door for a call that is no
// eval comparison's. The judge's door asks the judge's choice instead of
// the arm's, so a judge outside the arm's models is still admitted there.
//
// Kills: the slot pattern trusted without the binding's eligibility; the
// eval choice skipped for AppTown calls; the judge's door open to any call,
// or asking the arm's choice as well.
func TestARefusedForgeCallIsNotSentAndOwesNothing(t *testing.T) {
	t.Parallel()
	w := newAskWorld(t, "openai-completions", answering(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":0.001}}`))
	refuse := errors.New("could not assess: not this arm's model")
	w.g.ChooseModels(func(_ context.Context, projectID ids.ID, pattern string) error {
		if pattern == "native/judge" {
			return refuse
		}
		return nil
	})
	w.g.ChooseJudge(func(_ context.Context, projectID ids.ID, pattern string) error { return nil })
	for name, call := range map[string]modelcall.Call{
		"not enabled":    {Purpose: "narrative", ProjectID: idstest.N(7), Model: "native/other"},
		"no binding":     {Purpose: "narrative", ProjectID: idstest.N(7), Model: "missing/judge"},
		"arm's choice":   narrative,
		"judge, no copy": {Purpose: "eval-score", Judge: true, Model: "native/judge"},
	} {
		if _, u, err := w.g.Ask(context.Background(), call); err == nil || u != nil {
			t.Fatalf("%s: admitted: %+v %v", name, u, err)
		}
	}
	if _, u, err := w.g.Ask(context.Background(), narrative); !errors.Is(err, refuse) || u != nil {
		t.Fatalf("the eval choice's own refusal: %v", err)
	}
	if w.sent.Load() != 0 || w.spent(t) != (usage.Totals{}) {
		t.Fatalf("refusals sent %d calls, spent %+v", w.sent.Load(), w.spent(t))
	}
	judged := modelcall.Call{Purpose: "eval-score", ProjectID: idstest.N(7), Judge: true, Model: "native/judge"}
	if _, _, err := w.g.Ask(context.Background(), judged); err != nil || w.sent.Load() != 1 {
		t.Fatalf("the judge's door asks only the judge's choice: %v", err)
	}
}

// A call the provider may have charged for is never recorded as free: an
// answer with no usage, tokens with no price, an unreadable answer and a
// call cut off after it was sent settle as incomplete, and the caller gets
// a Usage saying so. A price of 0 the provider reports is a price. A
// provider's refusal (non-2xx) returns no usage and counts nothing.
//
// Kills: unknown spend recorded as zero; a reported zero price read as
// none; settlement skipped when the caller's context ends.
func TestUnknownSpendSettlesIncomplete(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		upstream   http.HandlerFunc
		cancel     bool
		incomplete bool
		usage      bool
	}{
		{name: "whole", upstream: answering(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"cost":0.01}}`), usage: true},
		{name: "free", upstream: answering(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"cost":0}}`), usage: true},
		{name: "no usage", upstream: answering(`{"choices":[{"message":{"content":"ok"}}]}`), usage: true, incomplete: true},
		{name: "null price", upstream: answering(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"cost":null}}`), usage: true, incomplete: true},
		{name: "no price", upstream: answering(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`), usage: true, incomplete: true},
		{name: "unreadable", upstream: answering(`not json`), usage: true, incomplete: true},
		{name: "cancelled after sending", cancel: true, usage: true, incomplete: true, upstream: func(rw http.ResponseWriter, r *http.Request) {
			// The server notices the caller hanging up only once it has
			// read the request.
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
		}},
		{name: "rejected", upstream: func(rw http.ResponseWriter, r *http.Request) { http.Error(rw, "no", http.StatusTooManyRequests) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newAskWorld(t, "openai-completions", c.upstream)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.cancel {
				go func() {
					poll.Until(ctx, time.Minute, time.Millisecond, func() bool { return w.sent.Load() > 0 })
					cancel()
				}()
			}
			_, u, _ := w.g.Ask(ctx, narrative)
			if (u != nil) != c.usage || (u != nil && (u.Incomplete != "") != c.incomplete) {
				t.Fatalf("usage %+v, want usage=%v incomplete=%v", u, c.usage, c.incomplete)
			}
			if got := w.spent(t); (got.Incomplete == 1) != c.incomplete || got.Incomplete > 1 {
				t.Fatalf("the spend line: %+v, want incomplete=%v", got, c.incomplete)
			}
		})
	}
}

// AppTown's own calls are provider-neutral: a binding speaking
// Anthropic's dialect gets the request at its own route with its own
// credential header, and its answer and usage are read in that dialect.
// It reports no price, so its spend settles as unknown.
//
// Kills: an OpenRouter-only client; a credential sent in the wrong header.
func TestAForgeCallSpeaksTheBindingsDialect(t *testing.T) {
	t.Parallel()
	w := newAskWorld(t, "anthropic-messages", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "provider-key" || r.Header.Get("Authorization") != "" {
			http.Error(rw, "wrong route or credential", 400)
			return
		}
		_, _ = io.WriteString(rw, `{"model":"judge","content":[{"type":"text","text":"from anthropic"}],"usage":{"input_tokens":4,"output_tokens":2}}`)
	})
	text, u, err := w.g.Ask(context.Background(), narrative)
	if err != nil || text != "from anthropic" || u.Prompt != 4 || u.Completion != 2 || !strings.Contains(u.Incomplete, "no price") {
		t.Fatalf("anthropic dialect: %q %+v %v", text, u, err)
	}
	if got := w.spent(t); got.Total != 6 || got.Incomplete != 1 {
		t.Fatalf("the spend line: %+v", got)
	}
}
