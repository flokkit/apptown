package inference

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func inferenceFixture(t *testing.T, upstream http.HandlerFunc) (*Gateway, *atomic.Bool) {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	live := &atomic.Bool{}
	live.Store(true)
	g := &Gateway{networks: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, timeout: time.Minute, client: server.Client(), active: map[string]int{}, slots: make(chan struct{}, 64)}
	g.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	g.authenticate = func(_ context.Context, token string) (identity.Workload, error) {
		if token != "session-secret" || !live.Load() {
			return identity.Workload{}, fmt.Errorf("invalid")
		}
		return identity.Workload{Kind: "console", Actor: "alice", ProjectID: idstest.N(3), SessionID: "chat-1"}, nil
	}
	g.providers = func(context.Context) (map[string]*inferenceProvider, error) {
		return map[string]*inferenceProvider{"paid": {name: "paid", baseURL: server.URL, api: "openai-completions", key: "provider-canary", patterns: []string{"allowed"}}}, nil
	}
	g.begin = func(_ context.Context, w identity.Workload, p, m string) (ids.ID, error) {
		if w.Actor != "alice" || w.SessionID != "chat-1" || p != "paid" || m != "allowed" {
			t.Errorf("wrong trusted attribution: %+v %s %s", w, p, m)
		}
		return idstest.N(1), nil
	}
	g.finish = func(context.Context, ids.ID, int, string, inferenceUsage) {}
	return g, live
}
func inferenceRequest(path, body string) *http.Request {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.10:45000"
	r.Header.Set("Authorization", "Bearer session-secret")
	return r
}
func TestInferenceAdmissionAndCredentialBoundary(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	g, live := inferenceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer provider-canary" {
			t.Error("upstream did not receive its server-side credential")
		}
		for _, k := range []string{"Cookie", "X-AppTown-Actor", "X-Forwarded-For", "X-Api-Key"} {
			if r.Header.Get(k) != "" {
				t.Errorf("forwarded %s", k)
			}
		}
		w.Header().Set("Set-Cookie", "key=provider-canary")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":4,"completion_tokens":2}}`)
	})
	for _, tc := range []struct {
		name, path, body, peer, token string
		status                        int
	}{
		{"ok", "/providers/paid/chat/completions", `{"model":"allowed"}`, "192.0.2.10:12", "session-secret", 200},
		{"stolen off network", "/providers/paid/chat/completions", `{"model":"allowed"}`, "203.0.113.10:12", "session-secret", 403},
		{"loopback not implicit", "/providers/paid/chat/completions", `{"model":"allowed"}`, "127.0.0.1:12", "session-secret", 403},
		{"unknown peer", "/providers/paid/chat/completions", `{"model":"allowed"}`, "", "session-secret", 403},
		{"network alone", "/providers/paid/chat/completions", `{"model":"allowed"}`, "192.0.2.10:12", "", 401},
		{"personal token", "/providers/paid/chat/completions", `{"model":"allowed"}`, "192.0.2.10:12", "pat-for-admin", 401},
		{"other model", "/providers/paid/chat/completions", `{"model":"expensive"}`, "192.0.2.10:12", "session-secret", 403},
		{"admin operation", "/providers/paid/files", `{"model":"allowed"}`, "192.0.2.10:12", "session-secret", 400},
		{"URL override", "/providers/paid/chat/completions?url=http://elsewhere", `{"model":"allowed"}`, "192.0.2.10:12", "session-secret", 400},
		{"fallback models", "/providers/paid/chat/completions", `{"model":"allowed","models":["expensive"]}`, "192.0.2.10:12", "session-secret", 400},
		{"background", "/providers/paid/chat/completions", `{"model":"allowed","background":true}`, "192.0.2.10:12", "session-secret", 400},
		{"invalid JSON", "/providers/paid/chat/completions", `null`, "192.0.2.10:12", "session-secret", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := inferenceRequest(tc.path, tc.body)
			r.RemoteAddr = tc.peer
			r.Header.Set("Authorization", "Bearer "+tc.token)
			r.Header.Set("X-Forwarded-For", "192.0.2.10")
			r.Header.Set("X-Real-IP", "192.0.2.10")
			r.Header.Set("X-AppTown-Actor", "admin")
			r.Header.Set("Cookie", "session=cookie")
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String()+fmt.Sprint(w.Header()), "provider-canary") {
				t.Fatal("provider credential escaped")
			}
		})
	}
	if calls.Load() != 1 {
		t.Fatalf("unauthorized upstream dispatch: %d", calls.Load())
	}
	live.Store(false)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, inferenceRequest("/providers/paid/chat/completions", `{"model":"allowed"}`))
	if w.Code != 401 {
		t.Fatal("revoked session admitted")
	}
}
func TestInferenceStreamingCancellationAndReceipt(t *testing.T) {
	t.Parallel()
	upstreamCancelled := make(chan struct{})
	g, _ := inferenceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(upstreamCancelled)
	})
	finished := make(chan string, 1)
	g.finish = func(_ context.Context, _ ids.ID, _ int, outcome string, _ inferenceUsage) { finished <- outcome }
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { r.RemoteAddr = "192.0.2.10:40"; g.ServeHTTP(w, r) }))
	defer proxy.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", proxy.URL+"/providers/paid/chat/completions", strings.NewReader(`{"model":"allowed","stream":true}`))
	req.Header.Set("Authorization", "Bearer session-secret")
	resp, err := proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), "hello") {
		t.Fatalf("stream was buffered or changed: %q %v", buf[:n], err)
	}
	cancel()
	select {
	case <-upstreamCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not cancel upstream")
	}
	select {
	case result := <-finished:
		if result == "complete" {
			t.Fatal("cancelled stream marked complete")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing terminal receipt")
	}
}
func TestInferenceUsageProtocols(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		sse        bool
		want       inferenceUsage
	}{
		{"completions", `{"usage":{"prompt_tokens":7,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":2}}}`, false, inferenceUsage{Observed: true, Input: 7, Output: 3, CacheRead: 2}},
		// A reported price is kept, and a reported 0 is a price, not none.
		{"priced", `{"usage":{"prompt_tokens":7,"completion_tokens":3,"cost":0.0125}}`, false, inferenceUsage{Observed: true, Input: 7, Output: 3, Cost: 0.0125, Priced: true}},
		{"free", `{"usage":{"prompt_tokens":7,"completion_tokens":3,"cost":0}}`, false, inferenceUsage{Observed: true, Input: 7, Output: 3, Priced: true}},
		{"responses", "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n", true, inferenceUsage{Observed: true, Input: 7, Output: 3}},
		{"anthropic", "data: {\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":1,\"cache_creation_input_tokens\":4}}}\n\ndata: {\"usage\":{\"output_tokens\":3}}\n\n", true, inferenceUsage{Observed: true, Input: 7, Output: 3, CacheWrite: 4}},
		{"google", `{"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"cachedContentTokenCount":2}}`, false, inferenceUsage{Observed: true, Input: 7, Output: 3, CacheRead: 2}},
		{"google thinking", `{"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"thoughtsTokenCount":5}}`, false, inferenceUsage{Observed: true, Input: 7, Output: 8}},
		{"missing", `{"choices":[]}`, false, inferenceUsage{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newInferenceUsageReader()
			u.sse = tc.sse
			for _, b := range []byte(tc.body) {
				_, _ = u.Write([]byte{b})
			}
			if got := u.result(); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}
func TestInferenceRejectsRedirectErrorsAndUnrecordedRequests(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	g, _ := inferenceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "http://attacker.invalid/?secret=provider-canary")
		w.WriteHeader(302)
		fmt.Fprint(w, "provider-canary")
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, inferenceRequest("/providers/paid/chat/completions", `{"model":"allowed"}`))
	if w.Code != 502 || strings.Contains(w.Body.String()+fmt.Sprint(w.Header()), "provider-canary") {
		t.Fatalf("redirect leaked or followed: %+v", w)
	}
	g.begin = func(context.Context, identity.Workload, string, string) (ids.ID, error) {
		return ids.Nil, fmt.Errorf("database down")
	}
	w = httptest.NewRecorder()
	g.ServeHTTP(w, inferenceRequest("/providers/paid/chat/completions", `{"model":"allowed"}`))
	if w.Code != 503 || calls.Load() != 1 {
		t.Fatal("unrecorded request dispatched")
	}
	for i := 0; i < 8; i++ {
		if !g.acquire("session-secret") {
			t.Fatal("early limit")
		}
	}
	defer func() {
		for i := 0; i < 8; i++ {
			g.release("session-secret")
		}
	}()
	w = httptest.NewRecorder()
	g.ServeHTTP(w, inferenceRequest("/providers/paid/chat/completions", `{"model":"allowed"}`))
	if w.Code != 429 {
		t.Fatal("concurrency limit bypassed")
	}
}
func TestInferenceNativeRoutes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ api, base, path, query, want, model string }{
		{"openai-completions", "https://api.example/v1", "chat/completions", "", "/chat/completions", "m"},
		{"openai-responses", "https://api.example/v1", "responses", "", "/responses", "m"},
		{"anthropic-messages", "https://api.example", "v1/messages", "", "/v1/messages", "m"},
		{"anthropic-messages", "https://api.example/v1", "v1/messages", "", "/messages", "m"},
		{"google-generative-ai", "https://api.example", "v1beta/models/gemini:streamGenerateContent", "alt=sse", "/v1beta/models/gemini:streamGenerateContent?alt=sse", "gemini"},
	} {
		p := &inferenceProvider{api: tc.api, baseURL: tc.base}
		q, _ := url.ParseQuery(tc.query)
		path, m, err := p.inferencePath(tc.path, q, "m")
		if err != nil || path != tc.want || m != tc.model {
			t.Errorf("%s: %s %s %v", tc.api, path, m, err)
		}
	}
}
func TestInferenceRevocationDuringPreparationAndStreaming(t *testing.T) {
	t.Parallel()
	t.Run("preparation", func(t *testing.T) {
		g, live := inferenceFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("revoked request reached upstream") })
		providers := g.providers
		g.providers = func(context.Context) (map[string]*inferenceProvider, error) {
			live.Store(false)
			return providers(context.Background())
		}
		g.begin = func(context.Context, identity.Workload, string, string) (ids.ID, error) {
			t.Error("revoked request admitted")
			return idstest.N(1), nil
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, inferenceRequest("/providers/paid/chat/completions", `{"model":"allowed"}`))
		if w.Code != 401 {
			t.Fatalf("revoked preparation: %d", w.Code)
		}
	})
	t.Run("streaming", func(t *testing.T) {
		started, cancelled, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		g, live := inferenceFixture(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {}\n\n")
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(cancelled)
		})
		g.finish = func(_ context.Context, _ ids.ID, _ int, outcome string, _ inferenceUsage) {
			if outcome != "cancelled" {
				t.Errorf("revocation outcome %s", outcome)
			}
		}
		go func() {
			defer close(done)
			g.ServeHTTP(httptest.NewRecorder(), inferenceRequest("/providers/paid/chat/completions", `{"model":"allowed","stream":true}`))
		}()
		<-started
		live.Store(false)
		select {
		case <-cancelled:
		case <-time.After(4 * time.Second):
			t.Fatal("revoked token kept upstream alive")
		}
		<-done
	})
}

// Rendering belongs to the harness, but inference must approve the final bytes:
// a credential hidden in an otherwise public model field must never be staged.

// The workload's project is asked the one model choice before anything is
// sent: an eval copy's run gets only its arm's models, refused otherwise
// with nothing dispatched and nothing admitted.
func TestInferenceAsksTheModelChoiceOfTheWorkloadsProject(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	g, _ := inferenceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":4,"completion_tokens":2}}`)
	})
	var asked []string
	g.ChooseModels(func(_ context.Context, projectID ids.ID, pattern string) error {
		asked = append(asked, fmt.Sprintf("%d %s", projectID, pattern))
		if pattern != "paid/allowed" || projectID != idstest.N(3) {
			return fmt.Errorf("could not assess: not this arm's model")
		}
		return nil
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, inferenceRequest("/providers/paid/chat/completions", `{"model":"allowed"}`))
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("the arm's own model: %d %s", w.Code, w.Body.String())
	}
	g.ChooseModels(func(context.Context, ids.ID, string) error {
		return fmt.Errorf("could not assess: not this arm's model")
	})
	w = httptest.NewRecorder()
	g.ServeHTTP(w, inferenceRequest("/providers/paid/chat/completions", `{"model":"allowed"}`))
	if w.Code != 403 || calls.Load() != 1 || !strings.Contains(w.Body.String(), "not this arm's model") {
		t.Fatalf("a model outside the arm: %d %s, %d calls", w.Code, w.Body.String(), calls.Load())
	}
	if len(asked) != 1 || asked[0] != idstest.N(3).String()+" paid/allowed" {
		t.Fatalf("asked %v", asked)
	}
}
