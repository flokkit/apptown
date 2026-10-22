package docker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// answerOnce is a probe's wait for a test that reads only how an answer is
// judged: the settle beat (never one second in these tests) passes at once,
// so an accepted answer is still asked again while running; the one-second
// retry stops the check after its one witnessed refusal.
func answerOnce(_ context.Context, d time.Duration) error {
	if d == time.Second {
		return errors.New("one refused answer is the evidence")
	}
	return nil
}

// The office-poll regression, deterministically: a behind-proxy-hardened app
// (200 only when the front-door secure signal arrives, else 301 → https) is
// cleared by the shaped probe and NOT by a naked one. Before the render, the
// naked probe's client followed the 301 into a dead TLS handshake and the boot
// read as never coming up (design/subsystems/healthchecks.md §3).
func TestProbeAppFrontDoorShaping(t *testing.T) {
	t.Parallel()
	probe := &Probe{}
	f := &fakeDaemon{exists: true, running: true}
	const container = "apptown-preview-change-hardened"

	// The hardened app: 200 iff X-Forwarded-Proto: https; otherwise redirect to
	// https like SECURE_SSL_REDIRECT does.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "https://"+r.Host+r.URL.Path, http.StatusMovedPermanently)
	}))
	t.Cleanup(srv.Close)

	fd := map[string]string{"X-Forwarded-Proto": "https"}

	// Shaped, expecting 200 (frontDoor readiness): the signal arrives → 200 → up.
	shaped := RenderColdBootProbe(&appcontract.Part{Name: "web", Public: true, Intent: &appcontract.Health{
		Readiness: &appcontract.Probe{HTTP: "/", Expect: 200},
	}}, fd)
	if up, tail := probe.check(t.Context(), Scope{Client: f}, container, srv.URL, 2*time.Second, shaped, answerOnce); !up {
		t.Fatalf("shaped probe expecting 200 should pass against the hardened app; tail=%q", tail)
	}

	// Same endpoint probed naked (requestContext: direct) expecting 200: the app
	// 301s, so it must NOT be read as ready.
	naked := RenderColdBootProbe(&appcontract.Part{Name: "web", Public: true, Intent: &appcontract.Health{
		Readiness: &appcontract.Probe{HTTP: "/", Expect: 200, RequestContext: "direct"},
	}}, fd)
	if naked.Headers != nil {
		t.Fatalf("direct probe must be unshaped: %+v", naked)
	}
	if up, _ := probe.check(t.Context(), Scope{Client: f}, container, srv.URL, 1500*time.Millisecond, naked, answerOnce); up {
		t.Fatalf("naked probe expecting 200 should NOT pass — the app 301s without the front-door signal")
	}
}

// A probe reads what it is answered to the end (up to a bound) before
// closing it, so its next check reuses the connection: a boot's checks make
// one connection to the app, not one per check, each of which would hold a
// local port for 30 s once closed. The connection is the probe's own:
// closing the default transport's idle connections (every
// httptest.Server.Close does) does not close it.
func TestAProbeReusesItsConnection(t *testing.T) {
	t.Parallel()
	var opened atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("<p>the app's page</p>\n", 200)))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			opened.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	probe := &Probe{Settle: time.Millisecond}
	for _, rendered := range []RenderedProbe{{Path: "/"}, {Path: "/", Expect: 200}} {
		if up, tail := probe.Check(t.Context(), Scope{Client: &fakeDaemon{exists: true, running: true}}, "app", srv.URL, 2*time.Second, rendered); !up {
			t.Fatalf("%+v: not up: %s", rendered, tail)
		}
		http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	}
	if n := opened.Load(); n != 1 {
		t.Fatalf("four checks opened %d connections, want 1", n)
	}
}
