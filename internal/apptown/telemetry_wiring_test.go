package apptown

import (
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/telemetry"
	"github.com/flokkit/apptown/internal/telemetry/otlpfake"
)

// With the endpoint set, an API request becomes a span named by its route
// pattern, its duration lands in the histogram, and the standard log lines
// arrive as log records. The SPA is not traced. Not parallel: the log
// bridge is process-global for the test's lifetime.
func TestApptowndExportsRequestSpansAndLogsWhenConfigured(t *testing.T) {
	receiver := otlpfake.New()
	defer receiver.Close()
	a := authzFixtureWithApproval(t, workflow.ApprovalConfig{}, Config{OTLPEndpoint: receiver.Endpoint()})
	t.Cleanup(func() { _ = a.telemetry.close(context.Background()) })
	t.Cleanup(BridgeLogs(a))
	if a.telemetry.providers == nil {
		t.Fatal("endpoint set but nothing started")
	}
	h := a.Handler()
	if w := authzCall(h, "owner", "GET", "/api/v1/projects/game", ""); w.Code != 200 {
		t.Fatalf("project read: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "GET", "/", ""); w.Code >= 500 {
		t.Fatalf("spa: %d", w.Code)
	}
	log.Printf("telemetry bridge canary %d", 42)
	if err := a.telemetry.providers.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	var routed bool
	for _, s := range receiver.Spans() {
		if s.Attributes["url.path"] == "/" {
			t.Fatalf("the SPA was traced: %+v", s)
		}
		if s.Name == "GET /api/v1/projects/{slug}" && s.Attributes["http.route"] == s.Name && s.Attributes["apptown.project"] == "game" &&
			s.Attributes["http.response.status_code"] == "200" && s.Attributes["apptown.principal.kind"] == "user" {
			routed = true
		}
	}
	if !routed {
		t.Fatalf("no route-named span: %+v", receiver.Spans())
	}
	var histogram bool
	for _, m := range receiver.Metrics() {
		if m.Name == "apptown_http_request_duration" && m.Attributes["http.route"] == "GET /api/v1/projects/{slug}" && m.Attributes["status"] == "2xx" {
			histogram = true
		}
	}
	if !histogram {
		t.Fatalf("no duration sample: %+v", receiver.Metrics())
	}
	var bridged bool
	for _, l := range receiver.Logs() {
		if strings.Contains(l.Body, "telemetry bridge canary 42") && !strings.HasPrefix(l.Body, "20") && l.Resource["service.name"] == "apptownd" {
			bridged = true
		}
	}
	if !bridged {
		t.Fatalf("log line not bridged: %+v", receiver.Logs())
	}
}

// Without the endpoint nothing is started, and the daemon behaves as before.
func TestApptowndSendsNothingWhenUnconfigured(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	if a.telemetry == nil || a.telemetry.providers != nil {
		t.Fatalf("unconfigured telemetry started something: %+v", a.telemetry)
	}
	if err := a.telemetry.close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The bridge emits every complete line of a write, strips the logger's
// timestamp, keeps a partial tail for the next write, and so a panic with its
// stack arrives whole rather than as its last line.
func TestLogBridgeEmitsEveryCompleteLine(t *testing.T) {
	t.Parallel()
	receiver := otlpfake.New()
	defer receiver.Close()
	providers, err := telemetry.Start(t.Context(), telemetry.Config{Endpoint: receiver.Endpoint(), Insecure: true, ServiceName: "apptownd", MetricInterval: time.Hour, CloseTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	b := &logLineBridge{ctx: context.Background(), logger: otelslog.NewLogger("test", otelslog.WithLoggerProvider(providers.Logger))}
	if _, err := b.Write([]byte("2026/09/18 10:00:00 panic: boom\ngoroutine 1 [running]:\nmain.main()\n\tpartial")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte(" line\n")); err != nil {
		t.Fatal(err)
	}
	if err := providers.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = providers.Close(context.Background())
	var bodies []string
	for _, l := range receiver.Logs() {
		bodies = append(bodies, l.Body)
	}
	want := []string{"panic: boom", "goroutine 1 [running]:", "main.main()", "\tpartial line"}
	if len(bodies) != len(want) {
		t.Fatalf("bodies: %q", bodies)
	}
	for i := range want {
		if bodies[i] != want[i] {
			t.Fatalf("line %d: %q want %q", i, bodies[i], want[i])
		}
	}
}
