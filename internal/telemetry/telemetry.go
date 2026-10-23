// Package telemetry builds OpenTelemetry providers that export over OTLP. It
// knows nothing of AppTown: a caller names an endpoint and a resource and
// gets a tracer, a meter and a logger provider, plus one bounded Close.
package telemetry

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/credentials"

	"github.com/flokkit/apptown/internal/trust"
)

// Config names where to export and what the exporting process is.
type Config struct {
	Endpoint string            // host:port of the OTLP receiver
	Protocol string            // "http" (default) or "grpc"; all three signals follow it
	Insecure bool              // plain TCP; the LAN case
	Headers  map[string]string // sent with every export, for receivers that want one
	// The resource every signal carries.
	ServiceName      string
	ServiceNamespace string
	Attributes       map[string]string
	// Bounds. Zero means the defaults below.
	MetricInterval time.Duration // how often the meter pushes
	CloseTimeout   time.Duration // the most Close waits for a final flush
}

const (
	DefaultMetricInterval = 15 * time.Second
	DefaultCloseTimeout   = 5 * time.Second
)

// Providers is what Start hands back. The three providers are what a program
// instruments with; Close flushes and stops them within the configured bound.
type Providers struct {
	Tracer trace.TracerProvider
	Meter  metric.MeterProvider
	Logger *sdklog.LoggerProvider

	closeTimeout time.Duration
	tp           *sdktrace.TracerProvider
	mp           *sdkmetric.MeterProvider
}

// Start builds exporters and providers for cfg. It opens no connection
// eagerly for HTTP; a bad endpoint surfaces on the first export, which the
// SDKs report through their error handler rather than by failing the program.
func Start(ctx context.Context, cfg Config) (*Providers, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("telemetry: an endpoint is required")
	}
	if cfg.ServiceName == "" {
		return nil, errors.New("telemetry: a service name is required")
	}
	proto := strings.ToLower(strings.TrimSpace(cfg.Protocol))
	if proto == "" {
		proto = "http"
	}
	if proto != "http" && proto != "grpc" {
		return nil, fmt.Errorf("telemetry: unknown protocol %q (http or grpc)", cfg.Protocol)
	}
	res, err := buildResource(cfg)
	if err != nil {
		return nil, err
	}

	// The collector is one of AppTown's own connections: it trusts the
	// AppTown use (internal/trust). The exporters keep transports of their
	// own, so the roots are given to each; nil keeps their default.
	var roots *tls.Config
	if pool := trust.Current().Pool(trust.AppTown); pool != nil {
		roots = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	var traceExporter sdktrace.SpanExporter
	if proto == "grpc" {
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint), otlptracegrpc.WithHeaders(cfg.Headers)}
		if cfg.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		} else if roots != nil {
			opts = append(opts, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(roots)))
		}
		traceExporter, err = otlptracegrpc.New(ctx, opts...)
	} else {
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.Endpoint), otlptracehttp.WithHeaders(cfg.Headers)}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		} else if roots != nil {
			opts = append(opts, otlptracehttp.WithTLSClientConfig(roots))
		}
		traceExporter, err = otlptracehttp.New(ctx, opts...)
	}
	if err != nil {
		return nil, fmt.Errorf("telemetry: trace exporter: %w", err)
	}
	var metricExporter sdkmetric.Exporter
	var logExporter sdklog.Exporter
	if proto == "grpc" {
		mopts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.Endpoint), otlpmetricgrpc.WithHeaders(cfg.Headers)}
		lopts := []otlploggrpc.Option{otlploggrpc.WithEndpoint(cfg.Endpoint), otlploggrpc.WithHeaders(cfg.Headers)}
		if cfg.Insecure {
			mopts = append(mopts, otlpmetricgrpc.WithInsecure())
			lopts = append(lopts, otlploggrpc.WithInsecure())
		} else if roots != nil {
			mopts = append(mopts, otlpmetricgrpc.WithTLSCredentials(credentials.NewTLS(roots)))
			lopts = append(lopts, otlploggrpc.WithTLSCredentials(credentials.NewTLS(roots)))
		}
		if metricExporter, err = otlpmetricgrpc.New(ctx, mopts...); err != nil {
			return nil, fmt.Errorf("telemetry: metric exporter: %w", err)
		}
		if logExporter, err = otlploggrpc.New(ctx, lopts...); err != nil {
			return nil, fmt.Errorf("telemetry: log exporter: %w", err)
		}
	} else {
		mopts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(cfg.Endpoint), otlpmetrichttp.WithHeaders(cfg.Headers)}
		lopts := []otlploghttp.Option{otlploghttp.WithEndpoint(cfg.Endpoint), otlploghttp.WithHeaders(cfg.Headers)}
		if cfg.Insecure {
			mopts = append(mopts, otlpmetrichttp.WithInsecure())
			lopts = append(lopts, otlploghttp.WithInsecure())
		} else if roots != nil {
			mopts = append(mopts, otlpmetrichttp.WithTLSClientConfig(roots))
			lopts = append(lopts, otlploghttp.WithTLSClientConfig(roots))
		}
		if metricExporter, err = otlpmetrichttp.New(ctx, mopts...); err != nil {
			return nil, fmt.Errorf("telemetry: metric exporter: %w", err)
		}
		if logExporter, err = otlploghttp.New(ctx, lopts...); err != nil {
			return nil, fmt.Errorf("telemetry: log exporter: %w", err)
		}
	}

	interval := cfg.MetricInterval
	if interval <= 0 {
		interval = DefaultMetricInterval
	}
	closeTimeout := cfg.CloseTimeout
	if closeTimeout <= 0 {
		closeTimeout = DefaultCloseTimeout
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traceExporter), sdktrace.WithResource(res))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(interval))), sdkmetric.WithResource(res))
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)), sdklog.WithResource(res))
	return &Providers{Tracer: tp, Meter: mp, Logger: lp, closeTimeout: closeTimeout, tp: tp, mp: mp}, nil
}

// Flush pushes what is buffered without stopping anything.
func (p *Providers) Flush(ctx context.Context) error {
	return errors.Join(p.tp.ForceFlush(ctx), p.mp.ForceFlush(ctx), p.Logger.ForceFlush(ctx))
}

// Close flushes and shuts the providers down, waiting at most the close
// timeout in total. A receiver that never answers costs that long and no
// more; the error says so.
func (p *Providers) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.closeTimeout)
	defer cancel()
	return errors.Join(p.tp.Shutdown(ctx), p.mp.Shutdown(ctx), p.Logger.Shutdown(ctx))
}

func buildResource(cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{semconv.ServiceName(cfg.ServiceName)}
	if cfg.ServiceNamespace != "" {
		attrs = append(attrs, semconv.ServiceNamespace(cfg.ServiceNamespace))
	}
	for k, v := range cfg.Attributes {
		attrs = append(attrs, attribute.String(k, v))
	}
	// Schemaless, so the merge never conflicts with the SDK default's schema
	// version; the keys are the semantic-convention ones regardless.
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attrs...))
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}
	return res, nil
}
