package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// completeFixture is a gateway whose one binding, "native", speaks api to
// upstream; begin and finish record the receipts it writes.
type completeFixture struct {
	g        *Gateway
	upstream atomic.Int32
	begun    []identity.Workload
	finished []inferenceUsage
	outcomes []string
}

func newCompleteFixture(t *testing.T, api string, upstream http.HandlerFunc) *completeFixture {
	t.Helper()
	f := &completeFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.upstream.Add(1)
		upstream(w, r)
	}))
	t.Cleanup(server.Close)
	f.g = &Gateway{timeout: time.Minute, client: server.Client(), active: map[string]int{}, slots: make(chan struct{}, 64)}
	f.g.authenticate = func(_ context.Context, token string) (identity.Workload, error) {
		if token != "assist-token" {
			return identity.Workload{}, errDenied
		}
		return identity.Workload{Kind: "assist", Actor: "alice"}, nil
	}
	f.g.providers = func(context.Context) (map[string]*inferenceProvider, error) {
		return map[string]*inferenceProvider{"native": {name: "native", baseURL: server.URL, api: api, key: "provider-key", patterns: []string{"drafter"}}}, nil
	}
	f.g.begin = func(_ context.Context, w identity.Workload, p, m string) (ids.ID, error) {
		f.begun = append(f.begun, w)
		return idstest.N(uint64(len(f.begun))), nil
	}
	f.g.finish = func(_ context.Context, _ ids.ID, _ int, outcome string, u inferenceUsage) {
		f.finished = append(f.finished, u)
		f.outcomes = append(f.outcomes, outcome)
	}
	return f
}

// A trusted in-process call works against every dialect a binding can
// speak, not only OpenRouter's: it goes to the binding's own route with
// the binding's credential, is admitted once as the token's workload, and
// gives back the model's text.
func TestCompleteSpeaksEachProvidersDialect(t *testing.T) {
	t.Parallel()
	for api, c := range map[string]struct{ path, reply string }{
		"openai-completions":   {"/chat/completions", `{"choices":[{"message":{"content":"drafted"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`},
		"openai-responses":     {"/responses", `{"output":[{"content":[{"type":"output_text","text":"drafted"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`},
		"anthropic-messages":   {"/v1/messages", `{"content":[{"type":"text","text":"drafted"}],"usage":{"input_tokens":3,"output_tokens":1}}`},
		"google-generative-ai": {"/v1beta/models/drafter:generateContent", `{"candidates":[{"content":{"parts":[{"text":"drafted"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`},
	} {
		t.Run(api, func(t *testing.T) {
			t.Parallel()
			f := newCompleteFixture(t, api, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != c.path {
					t.Errorf("route %s", r.URL.Path)
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || !strings.Contains(fmt.Sprint(body), "write YAML") || body["tools"] != nil {
					t.Errorf("body %v", body)
				}
				if !strings.Contains(r.Header.Get("Authorization")+r.Header.Get("X-Api-Key")+r.Header.Get("X-Goog-Api-Key"), "provider-key") {
					t.Error("the binding's credential was not sent")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, c.reply)
			})
			text, err := f.g.Complete(context.Background(), "assist-token", "native/drafter", "write YAML", "a workflow")
			if err != nil || text != "drafted" {
				t.Fatalf("%q %v", text, err)
			}
			if len(f.begun) != 1 || f.begun[0].Kind != "assist" || f.begun[0].Actor != "alice" || len(f.finished) != 1 || !f.finished[0].Observed || f.finished[0].Input != 3 {
				t.Fatalf("receipts %+v %+v", f.begun, f.finished)
			}
		})
	}
}

// What admission refuses never reaches the provider: a model the binding
// does not enable, a credential that is no longer live, and a receipt that
// cannot be written.
func TestCompleteRefusesBeforeTheUpstreamCall(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		token, model string
		begin        error
	}{
		"model not enabled":  {"assist-token", "native/expensive", nil},
		"actor not admitted": {"revoked", "native/drafter", nil},
		"receipt failed":     {"assist-token", "native/drafter", errors.New("database down")},
		"unknown provider":   {"assist-token", "elsewhere/drafter", nil},
		"no single model":    {"assist-token", "native/*", nil},
	} {
		f := newCompleteFixture(t, "anthropic-messages", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
		if c.begin != nil {
			f.g.begin = func(context.Context, identity.Workload, string, string) (ids.ID, error) { return ids.Nil, c.begin }
		}
		if _, err := f.g.Complete(context.Background(), c.token, c.model, "s", "p"); err == nil || f.upstream.Load() != 0 {
			t.Fatalf("%s: err %v, upstream calls %d", name, err, f.upstream.Load())
		}
	}
}

// A provider that reports no usage leaves the receipt's usage unknown, not
// zero; a call cancelled by its caller is recorded as cancelled.
func TestCompleteRecordsUnknownUsageAndCancellation(t *testing.T) {
	t.Parallel()
	f := newCompleteFixture(t, "anthropic-messages", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"content":[{"type":"text","text":"drafted"}]}`)
	})
	if _, err := f.g.Complete(context.Background(), "assist-token", "native/drafter", "s", "p"); err != nil {
		t.Fatal(err)
	}
	if len(f.finished) != 1 || f.finished[0].Observed {
		t.Fatalf("usage %+v", f.finished)
	}

	ctx, cancel := context.WithCancel(context.Background())
	blocked := newCompleteFixture(t, "anthropic-messages", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body) // the server notices the caller leave only after the body is read
		cancel()
		<-r.Context().Done()
	})
	blocked.g.timeout = time.Hour // only the caller's cancellation can end the call
	done := make(chan error, 1)
	go func() { _, err := blocked.g.Complete(ctx, "assist-token", "native/drafter", "s", "p"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled call succeeded")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the caller's cancellation did not reach the provider call")
	}
	if len(blocked.outcomes) != 1 || blocked.outcomes[0] != "cancelled" {
		t.Fatalf("outcomes %v", blocked.outcomes)
	}
}
