package apptown

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	nooptrace "go.opentelemetry.io/otel/trace/noop"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/telemetry"
)

// AppTown's own telemetry, beside the self-error reporter: one setting, the
// OTLP endpoint, turns it on. Unset, every tracer and meter here is a no-op
// and nothing is created, connected or sent. What is exported, and its
// limits, is in design/subsystems/telemetry.md.
type instrumentation struct {
	providers    *telemetry.Providers // nil when unconfigured
	tracer       trace.Tracer
	meter        metric.Meter
	httpDuration metric.Float64Histogram
	links        *attemptLinks
	now          func() time.Time // AppTown's clock, which times a request
}

func startInstrumentation(ctx context.Context, cfg configuration) (*instrumentation, error) {
	in := &instrumentation{tracer: nooptrace.NewTracerProvider().Tracer("apptownd"), meter: noopmetric.NewMeterProvider().Meter("apptownd"), links: &attemptLinks{m: map[attemptKey]trace.SpanContext{}}, now: cfg.clock.Now}
	if cfg.otlpEndpoint == "" {
		in.httpDuration, _ = in.meter.Float64Histogram("apptown_http_request_duration")
		return in, nil
	}
	endpoint, insecure := splitOTLPEndpoint(cfg.otlpEndpoint)
	attrs := map[string]string{"service.version": buildRevision()}
	if cfg.publicHost != "" {
		attrs["apptown.instance"] = cfg.publicHost
	}
	p, err := telemetry.Start(ctx, telemetry.Config{
		Endpoint: endpoint, Protocol: cfg.otlpProtocol, Insecure: insecure,
		ServiceName: "apptownd", ServiceNamespace: "apptown", Attributes: attrs,
	})
	if err != nil {
		return nil, err
	}
	in.providers = p
	in.tracer = p.Tracer.Tracer("apptownd")
	in.meter = p.Meter.Meter("apptownd")
	in.httpDuration, _ = in.meter.Float64Histogram("apptown_http_request_duration", metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000),
		metric.WithDescription("HTTP request duration by route pattern, method and status class"))
	log.Printf("telemetry: exporting to %s over %s", endpoint, orDefault(cfg.otlpProtocol, "http"))
	return in, nil
}

// close flushes and stops the exporters within the library's close bound;
// a no-op when nothing was configured.
func (in *instrumentation) close(ctx context.Context) error {
	if in == nil || in.providers == nil {
		return nil
	}
	return in.providers.Close(ctx)
}

// splitOTLPEndpoint accepts host:port or a URL. A URL's scheme decides TLS;
// a bare host:port is the LAN case and is sent in the clear.
func splitOTLPEndpoint(raw string) (endpoint string, insecure bool) {
	switch {
	case strings.HasPrefix(raw, "https://"):
		return strings.TrimSuffix(strings.TrimPrefix(raw, "https://"), "/"), false
	case strings.HasPrefix(raw, "http://"):
		return strings.TrimSuffix(strings.TrimPrefix(raw, "http://"), "/"), true
	}
	return raw, true
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 8 {
			return s.Value[:8]
		}
	}
	return "unknown"
}

// logLineBridge turns each line the standard logger writes into a log
// record. The logger's own timestamp prefix is dropped; the record has its
// own. There is no request context on this path, so no trace id; new code
// writes slog with a context and gets one.
type logLineBridge struct {
	// ctx is the logs' context: the App's, detached, so a line written as
	// AppTown stops is still sent.
	ctx    context.Context
	logger *slog.Logger
	rest   []byte
}

var logPrefix = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)? `)

func (b *logLineBridge) Write(p []byte) (int, error) {
	buf := append(b.rest, p...)
	b.rest = nil
	for {
		nl := bytes.IndexByte(buf, '\n')
		if nl < 0 {
			break
		}
		b.emit(string(buf[:nl]))
		buf = buf[nl+1:]
	}
	if len(buf) > 0 {
		b.rest = append([]byte(nil), buf...)
	}
	return len(p), nil
}

func (b *logLineBridge) emit(line string) {
	line = logPrefix.ReplaceAllString(line, "")
	if line == "" {
		return
	}
	level := slog.LevelInfo
	lower := strings.ToLower(line)
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "panic") {
		level = slog.LevelError
	}
	b.logger.Log(b.ctx, level, line)
}

// tracedHandler is the span per HTTP request. It sits inside the auth layer,
// so the caller is known, and outside the mux, so the matched pattern is
// known once the mux returns. Spans are named by pattern, never by path.
func tracedHandler(in *instrumentation, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !instrumentedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		now := clock.Or(in.now)
		started := now()
		ctx, span := in.tracer.Start(r.Context(), "http.request", trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("url.path", r.URL.Path)))
		rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		routed := r.WithContext(ctx)
		next.ServeHTTP(rec, routed)
		pattern := routed.Pattern
		if pattern == "" {
			pattern = "unmatched"
		}
		span.SetName(pattern)
		attrs := []attribute.KeyValue{attribute.String("http.route", pattern), attribute.Int("http.response.status_code", rec.status)}
		if slug := routed.PathValue("slug"); slug != "" {
			attrs = append(attrs, attribute.String("apptown.project", slug))
		}
		// The auth layer above us attached the identity; the caller the route
		// layer builds from it lives on a copy below us.
		if id := identityFrom(routed); id != nil {
			pr := principalOf(id, "")
			attrs = append(attrs, attribute.String("apptown.principal.kind", pr.Kind))
			if pr.Instrument != "" {
				attrs = append(attrs, attribute.String("apptown.principal.instrument", pr.Instrument))
			}
		}
		span.SetAttributes(attrs...)
		span.End()
		in.httpDuration.Record(ctx, float64(now().Sub(started).Milliseconds()), metric.WithAttributes(
			attribute.String("http.route", pattern), attribute.String("http.request.method", r.Method), attribute.String("status", fmt.Sprintf("%dxx", rec.status/100))))
	})
}

func instrumentedPath(p string) bool {
	return strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/mcp") || strings.HasPrefix(p, "/git/") || strings.HasPrefix(p, "/oauth")
}

// statusWriter records the status for the span and stays transparent to the
// console websocket, streaming responses and the response controller.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		s.status = http.StatusSwitchingProtocols
		return h.Hijack()
	}
	return nil, nil, errors.New("response writer does not support hijacking")
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// attemptLinks is the root's registry of running step attempts, published by
// the step executor and looked up by the inference gateway, so an inference
// span links to the attempt it served. In-memory, derived, small: entries
// live only while an attempt runs.
type attemptKey struct {
	run, step ids.ID
	attempt   int64
}

type attemptLinks struct {
	mu sync.Mutex
	m  map[attemptKey]trace.SpanContext
}

func (l *attemptLinks) Publish(runID ids.ID, stepID ids.ID, attempt int64, sc trace.SpanContext) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[attemptKey{runID, stepID, attempt}] = sc
}

func (l *attemptLinks) Forget(runID ids.ID, stepID ids.ID, attempt int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, attemptKey{runID, stepID, attempt})
}

func (l *attemptLinks) Lookup(runID ids.ID, stepID ids.ID, attempt int64) (trace.SpanContext, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	sc, ok := l.m[attemptKey{runID, stepID, attempt}]
	return sc, ok
}

// BridgeLogs sends the standard logger's output to the telemetry log
// exporter as well as wherever it went before. The daemon calls it once,
// because the standard logger is process state and a library or an App must
// not reach for it. The returned function restores the previous output.
func BridgeLogs(a *App) func() {
	if a.telemetry == nil || a.telemetry.providers == nil {
		return func() {}
	}
	prev := log.Writer()
	log.SetOutput(io.MultiWriter(prev, &logLineBridge{ctx: context.WithoutCancel(a.life), logger: otelslog.NewLogger("apptownd", otelslog.WithLoggerProvider(a.telemetry.providers.Logger))}))
	return func() { log.SetOutput(prev) }
}

// CloseTelemetry flushes and stops the exporters. The daemon calls it after
// every producer has drained, with what remains of its shutdown budget, so
// the last spans and log lines are not lost and a silent receiver cannot
// hold the process past that budget: the library's own bound applies too.
func CloseTelemetry(a *App, ctx context.Context) error {
	if a == nil || a.telemetry == nil {
		return nil
	}
	return a.telemetry.close(ctx)
}
