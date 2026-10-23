package telemetry_test

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/flokkit/apptown/internal/telemetry"
	"github.com/flokkit/apptown/internal/trust"
)

// exportAll sends a span, a metric point and a log line to cfg's collector
// and flushes them.
func exportAll(t *testing.T, cfg telemetry.Config) error {
	t.Helper()
	cfg.ServiceName, cfg.MetricInterval, cfg.CloseTimeout = "trust", time.Hour, time.Second
	p, err := telemetry.Start(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	_, span := p.Tracer.Tracer("trust").Start(t.Context(), "export")
	span.End()
	counter, err := p.Meter.Meter("trust").Int64Counter("exports")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(t.Context(), 1)
	otelslog.NewLogger("trust", otelslog.WithLoggerProvider(p.Logger)).InfoContext(t.Context(), "export", slog.String("k", "v"))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	return p.Flush(ctx)
}

// install makes b the process's trust for use u alone.
func install(t *testing.T, u trust.Use, b trust.Bundle) {
	t.Helper()
	set, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{u: b})
	if err != nil {
		t.Fatal(err)
	}
	before := http.DefaultTransport.(*http.Transport).TLSClientConfig
	t.Cleanup(func() { http.DefaultTransport.(*http.Transport).TLSClientConfig = before; trust.Install(nil) })
	trust.Install(set)
}

func bundleOf(t *testing.T, srv *httptest.Server) trust.Bundle {
	t.Helper()
	b, err := trust.Parse(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A collector is one of AppTown's own connections: behind a company's
// TLS-inspecting proxy, or with a collector the company's authority signed,
// traces, metrics and logs reach it over HTTPS once that authority is the
// AppTown use's, and not when it is given only for another use. The
// exporters keep transports of their own, so http.DefaultTransport's roots
// do not reach them by themselves.
//
// Not parallel: it installs the process's trust.
func TestTheExportersTrustTheAppTownUse(t *testing.T) {
	collector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	endpoint := strings.TrimPrefix(collector.URL, "https://")

	t.Run("http, the AppTown use", func(t *testing.T) {
		install(t, trust.AppTown, bundleOf(t, collector))
		if err := exportAll(t, telemetry.Config{Endpoint: endpoint}); err != nil {
			t.Fatalf("export with the AppTown use's authority: %v", err)
		}
	})
	t.Run("http, another use", func(t *testing.T) {
		install(t, trust.Deployments, bundleOf(t, collector))
		if err := exportAll(t, telemetry.Config{Endpoint: endpoint}); err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("an authority given only for deployments was trusted: %v", err)
		}
	})
	t.Run("grpc, the AppTown use", func(t *testing.T) {
		// A gRPC collector with no services: past the handshake every export
		// is refused as unimplemented, which says the TLS was trusted.
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: collector.TLS.Certificates})))
		go func() { _ = srv.Serve(l) }()
		t.Cleanup(srv.Stop)
		install(t, trust.AppTown, bundleOf(t, collector))
		err = exportAll(t, telemetry.Config{Endpoint: l.Addr().String(), Protocol: "grpc"})
		if err == nil || strings.Contains(err.Error(), "certificate") || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("over gRPC the exports did not get past TLS to the collector: %v", err)
		}
		// One refusal per signal: traces, metrics and logs each got there.
		if n := strings.Count(strings.ToLower(err.Error()), "unimplemented"); n < 3 {
			t.Fatalf("%d of three signals reached the collector: %v", n, err)
		}
	})
}
