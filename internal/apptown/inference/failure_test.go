package inference

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Both entry points describe the upstream status and keep raw provider text
// out of the headline, including when it claims a different cause.
func TestProviderFailureHeadlines(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		status int
		want   string
	}{
		{401, "OpenAI refused the API key (HTTP 401)"},
		{403, "OpenAI refused access to allowed (HTTP 403)"},
		{429, "OpenAI refused the call: rate limit or out of credit (HTTP 429)"},
		{500, "OpenAI had an internal error (HTTP 500)"},
		{503, "OpenAI had an internal error (HTTP 503)"},
	} {
		t.Run(fmt.Sprint(row.status), func(t *testing.T) {
			t.Parallel()
			g, _ := inferenceFixture(t, func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "raw canary: your API key is wrong", row.status)
			})
			ps, err := g.providers(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			p := ps["paid"]
			p.name = "openai"
			g.providers = func(context.Context) (map[string]*inferenceProvider, error) {
				return map[string]*inferenceProvider{"openai": p}, nil
			}
			got := g.send(t.Context(), p, "allowed", "/chat/completions", []byte(`{"model":"allowed"}`))
			if got.err == nil || got.err.Error() != row.want {
				t.Fatalf("AppTown call: %v; want %s", got.err, row.want)
			}
			w := httptest.NewRecorder()
			g.ServeHTTP(w, inferenceRequest("/providers/openai/chat/completions", `{"model":"allowed"}`))
			if w.Code != row.status || strings.TrimSpace(w.Body.String()) != row.want {
				t.Fatalf("runtime call: %d %s", w.Code, w.Body.String())
			}
			if got := Failure("openai", "allowed", 0, fmt.Sprintf("%d %s", row.status, row.want)); got != row.want {
				t.Fatalf("SDK: %s", got)
			}
		})
	}
}

type failureTransport struct{ err error }

func (f failureTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestUnreachableProviderHeadlines(t *testing.T) {
	t.Parallel()
	for _, err := range []error{errors.New("connection refused: private detail"), context.DeadlineExceeded} {
		t.Run(err.Error(), func(t *testing.T) {
			t.Parallel()
			g, _ := inferenceFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected call") })
			g.client = &http.Client{Transport: failureTransport{err}}
			ps, e := g.providers(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			p := ps["paid"]
			a := g.send(t.Context(), p, "allowed", "/chat/completions", nil)
			if a.err == nil || a.err.Error() != "AppTown couldn't reach paid" {
				t.Fatalf("AppTown: %v", a.err)
			}
			w := httptest.NewRecorder()
			g.ServeHTTP(w, inferenceRequest("/providers/paid/chat/completions", `{"model":"allowed"}`))
			if strings.TrimSpace(w.Body.String()) != "AppTown couldn't reach paid" {
				t.Fatalf("runtime: %s", w.Body.String())
			}
		})
	}
}

func TestEmptyModelAnswerNamesTheModel(t *testing.T) {
	t.Parallel()
	g, _ := inferenceFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"choices":[]}`)) })
	ps, err := g.providers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a := g.send(t.Context(), ps["paid"], "allowed", "/chat/completions", nil)
	if a.err == nil || a.err.Error() != "allowed returned nothing" {
		t.Fatalf("%v", a.err)
	}
	if got := Failure("openai", "gpt-test", 0, "502 AppTown couldn't reach OpenAI"); got != "AppTown couldn't reach OpenAI" {
		t.Fatal(got)
	}
}

func TestFailureUsesProviderTitles(t *testing.T) {
	t.Parallel()
	for _, row := range []struct{ name, title string }{{"openai", "OpenAI"}, {"anthropic", "Anthropic"}, {"openrouter", "OpenRouter"}, {"google", "Google"}, {"my-endpoint", "my-endpoint"}} {
		if got := Failure(row.name, "model", 401, ""); got != row.title+" refused the API key (HTTP 401)" {
			t.Fatal(got)
		}
	}
}
