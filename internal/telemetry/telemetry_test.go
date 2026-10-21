package telemetry_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"flokkit.io/forge/internal/telemetry"
	"flokkit.io/forge/internal/telemetry/otlpfake"
)

func start(t *testing.T, r *otlpfake.Receiver, headers map[string]string) *telemetry.Providers {
	t.Helper()
	p, err := telemetry.Start(t.Context(), telemetry.Config{
		Endpoint: r.Endpoint(), Insecure: true, Headers: headers,
		ServiceName: "forge-test", ServiceNamespace: "forge", Attributes: map[string]string{"apptown.instance": "unit"},
		MetricInterval: time.Hour, CloseTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Every signal reaches the receiver carrying the configured resource.
func TestProvidersExportEverySignalWithTheResource(t *testing.T) {
	t.Parallel()
	r := otlpfake.New()
	defer r.Close()
	p := start(t, r, map[string]string{"X-Probe": "yes"})
	ctx := t.Context()

	_, span := p.Tracer.Tracer("test").Start(ctx, "unit.span")
	span.SetAttributes(attribute.String("apptown.project", "coin-flip"))
	span.End()
	counter, _ := p.Meter.Meter("test").Int64Counter("apptown_unit_events_total")
	counter.Add(ctx, 2, metric.WithAttributes(attribute.String("kind", "test")))
	logger := otelslog.NewLogger("test", otelslog.WithLoggerProvider(p.Logger))
	logger.InfoContext(ctx, "unit log line", slog.String("apptown.run", "42"))

	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	spans, metrics, logs := r.Spans(), r.Metrics(), r.Logs()
	if len(spans) != 1 || spans[0].Name != "unit.span" || spans[0].Attributes["apptown.project"] != "coin-flip" {
		t.Fatalf("spans: %+v", spans)
	}
	// A cumulative counter is re-exported on shutdown; every export must say 2.
	if len(metrics) == 0 {
		t.Fatal("no metric arrived")
	}
	for _, m := range metrics {
		if m.Name != "apptown_unit_events_total" || m.Value != 2 || m.Attributes["kind"] != "test" {
			t.Fatalf("metrics: %+v", metrics)
		}
	}
	if len(logs) != 1 || logs[0].Body != "unit log line" || logs[0].Attributes["apptown.run"] != "42" {
		t.Fatalf("logs: %+v", logs)
	}
	for _, res := range []map[string]string{spans[0].Resource, metrics[0].Resource, logs[0].Resource} {
		if res["service.name"] != "forge-test" || res["service.namespace"] != "forge" || res["apptown.instance"] != "unit" {
			t.Fatalf("resource: %+v", res)
		}
	}
	for _, h := range r.Headers() {
		if h.Get("X-Probe") != "yes" {
			t.Fatalf("header did not reach the wire: %+v", h)
		}
	}
	if !r.Contains("coin-flip") || r.Contains("not-sent") {
		t.Fatal("canary search is wrong")
	}
}

// A receiver that never answers costs Close its bound and no more.
func TestCloseIsBoundedAgainstASilentReceiver(t *testing.T) {
	t.Parallel()
	r := otlpfake.New()
	defer r.Close()
	r.NeverAnswer()
	p := start(t, r, nil)
	_, span := p.Tracer.Tracer("test").Start(t.Context(), "stuck")
	span.End()
	started := time.Now()
	err := p.Close(context.Background())
	if took := time.Since(started); took > 4*time.Second {
		t.Fatalf("Close took %s against a silent receiver", took)
	}
	if err == nil {
		t.Fatal("Close against a silent receiver must report the flush failure")
	}
}

// Configuration mistakes fail at Start, not on the first export.
func TestStartRefusesAnIncompleteConfig(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]telemetry.Config{
		"no endpoint": {ServiceName: "x"},
		"no service":  {Endpoint: "127.0.0.1:1"},
		"bad proto":   {Endpoint: "127.0.0.1:1", ServiceName: "x", Protocol: "carrier-pigeon"},
	} {
		if _, err := telemetry.Start(t.Context(), cfg); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}
