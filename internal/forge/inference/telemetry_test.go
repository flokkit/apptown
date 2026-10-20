package inference

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/telemetry"
	"flokkit.io/forge/internal/telemetry/otlpfake"
)

// An instrumented gateway describes each request as a span and counts its
// tokens, and nothing it exports contains the provider credential, the
// session token or the request body.
func TestGatewayObservesRequestsWithoutCredentials(t *testing.T) {
	t.Parallel()
	receiver := otlpfake.New()
	defer receiver.Close()
	providers, err := telemetry.Start(t.Context(), telemetry.Config{Endpoint: receiver.Endpoint(), Insecure: true, ServiceName: "forge-test", MetricInterval: time.Hour, CloseTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := inferenceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"the secret prompt echoed"}}],"usage":{"prompt_tokens":8,"completion_tokens":3}}`)
	})
	linked := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{0xab, 0xcd, 1}, SpanID: trace.SpanID{0xef, 1}, TraceFlags: trace.FlagsSampled})
	g.Instrument(providers.Tracer.Tracer("test"), providers.Meter.Meter("test")).LinkAttempts(func(runID, stepID ids.ID, attempt int64) (trace.SpanContext, bool) {
		return linked, true
	})
	g.authenticate = func(context.Context, string) (identity.Workload, error) {
		return identity.Workload{Kind: "step", Actor: "alice", ProjectID: idstest.N(3), RunID: idstest.N(9), StepID: idstest.N(4), Attempt: 1}, nil
	}
	g.begin = func(context.Context, identity.Workload, string, string) (ids.ID, error) { return idstest.N(1), nil }
	w := httptest.NewRecorder()
	g.ServeHTTP(w, inferenceRequest("/providers/paid/chat/completions", `{"model":"allowed","messages":[{"role":"user","content":"prompt-canary"}]}`))
	if w.Code != 200 {
		t.Fatalf("gateway: %d %s", w.Code, w.Body.String())
	}
	if err := providers.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := providers.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := receiver.Spans()
	if len(spans) != 1 || spans[0].Name != "inference.request" {
		t.Fatalf("spans: %+v", spans)
	}
	if len(spans[0].Links) != 1 || spans[0].Links[0] != linked.TraceID().String() {
		t.Fatalf("inference span is not linked to its step attempt: %+v", spans[0].Links)
	}
	a := spans[0].Attributes
	if a["forge.inference.provider"] != "paid" || a["forge.inference.model"] != "allowed" || a["forge.workload.kind"] != "step" || a["forge.project"] != idstest.N(3).String() || a["forge.run"] != idstest.N(9).String() ||
		a["http.response.status_code"] != "200" || a["forge.inference.tokens.input"] != "8" || a["forge.inference.tokens.output"] != "3" {
		t.Fatalf("span attributes: %+v", a)
	}
	var requests, tokens bool
	for _, m := range receiver.Metrics() {
		switch m.Name {
		case "apptown_inference_requests_total":
			requests = m.Value == 1 && m.Attributes["provider"] == "paid" && m.Attributes["model"] == "allowed" && m.Attributes["outcome"] != ""
		case "apptown_inference_tokens_total":
			if m.Attributes["direction"] == "input" {
				tokens = m.Value == 8
			}
		}
	}
	if !requests || !tokens {
		t.Fatalf("metrics: %+v", receiver.Metrics())
	}
	for _, secret := range []string{"provider-canary", "session-secret", "prompt-canary", "secret prompt echoed"} {
		if receiver.Contains(secret) {
			t.Fatalf("%q reached the receiver", secret)
		}
	}
}
