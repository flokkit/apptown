//go:build integration

package verification

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/localtls"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// The real browser in the agent image, on this machine's Docker: what is
// captured is the preview's own page, never another site's. A container
// reaches this test's servers at host.docker.internal.

func shotsAgentImage() string {
	if img := os.Getenv("APPTOWN_TEST_AGENT_IMAGE"); img != "" {
		return img
	}
	return "apptown-agent"
}

func localShots(tls *console.LocalTLS) *screenshots {
	var local *LocalAuthority
	if tls != nil {
		local = &LocalAuthority{Prelude: console.LocalTLSPrelude(*tls)}
	}
	return NewScreenshots(Executor{Run: LocalChecks(execution.Runner{}, nil), Local: true, AgentImage: shotsAgentImage()}, local)
}

// listen serves h on every interface, so a container reaches it.
func listen(t *testing.T, h http.Handler, config *tls.Config) int {
	t.Helper()
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	if config != nil {
		l = tls.NewListener(l, config)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

func page(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}
}

func TestIntegrationScreenshotsStayOnThePreview(t *testing.T) {
	other := listen(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dot.svg" {
			w.Header().Set("Content-Type", "image/svg+xml")
			fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="red"/></svg>`)
			return
		}
		page("<h1>Another site</h1>")(w, r)
	}), nil)
	elsewhere := fmt.Sprintf("http://host.docker.internal:%d/", other)
	mux := http.NewServeMux()
	mux.HandleFunc("/", page(fmt.Sprintf(`<h1>Home</h1><img src="http://host.docker.internal:%d/dot.svg">`, other)))
	mux.HandleFunc("/same", page(`<script>location="/"</script>`))
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, elsewhere, http.StatusFound) })
	mux.HandleFunc("/script", page(fmt.Sprintf(`<h1>Leaving</h1><script>setTimeout(()=>location=%q,50)</script>`, elsewhere)))
	// The preview and the other site differ by port: another origin.
	base := fmt.Sprintf("http://host.docker.internal:%d/", listen(t, mux, nil))

	c := localShots(nil).Take(context.Background(), base, []string{"/same", "/redirect", "/script"})
	if c.Unavailable != "" {
		t.Fatalf("unavailable: %s", c.Unavailable)
	}
	got := map[string]bool{}
	for _, s := range c.Shots {
		got[s.Path+" "+s.View] = true
		if !strings.HasPrefix(string(s.PNG), "\x89PNG") {
			t.Fatalf("%s %s is not a PNG", s.Path, s.View)
		}
	}
	for _, want := range []string{"/ desktop", "/ phone", "/same desktop"} {
		if !got[want] {
			t.Fatalf("%s was not captured: %+v missing %v", want, got, c.Missing)
		}
	}
	for _, left := range []string{"/redirect", "/script"} {
		if got[left+" desktop"] {
			t.Fatalf("%s left the preview and was captured anyway", left)
		}
		found := false
		for _, m := range c.Missing {
			if strings.HasPrefix(m, left+" · desktop: left the preview for "+elsewhere) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: no 'left the preview' reason in %v", left, c.Missing)
		}
	}
}

// On an AppTown with its own authority, the capture trusts that authority
// as a chat does (it is the local part of the extra certificates), and
// nothing else: no certificate check is turned off.
// Not parallel: it installs the process's trust.
func TestIntegrationScreenshotsTrustTheLocalAuthorityOnly(t *testing.T) {
	dir := t.TempDir()
	ca, err := localtls.Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, localtls.CertFile))
	if err != nil {
		t.Fatal(err)
	}
	local, err := trust.Parse(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	trusttest.Install(t, trust.Bundle{}, local)
	port := listen(t, page("<h1>Trial</h1>"), ca.TLSConfig())
	trial := &console.LocalTLS{CACert: caPEM, Port: port, Target: fmt.Sprintf("host.docker.internal:%d", port)}
	c := localShots(trial).Take(context.Background(), fmt.Sprintf("https://change-1.preview.localhost:%d/", port), nil)
	if c.Unavailable != "" || len(c.Shots) != 2 {
		t.Fatalf("the trial's own page: %d shots, unavailable %q, missing %v", len(c.Shots), c.Unavailable, c.Missing)
	}

	// A certificate the authority did not issue still fails.
	untrusted := httptest.NewUnstartedServer(page("<h1>Untrusted</h1>"))
	untrusted.Listener.Close()
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	untrusted.Listener = l
	untrusted.StartTLS()
	t.Cleanup(untrusted.Close)
	c = localShots(trial).Take(context.Background(), fmt.Sprintf("https://host.docker.internal:%d/", l.Addr().(*net.TCPAddr).Port), nil)
	if len(c.Shots) != 0 || !strings.Contains(c.Unavailable, "ERR_CERT") {
		t.Fatalf("an untrusted certificate was accepted: %d shots, unavailable %q", len(c.Shots), c.Unavailable)
	}
}

// Behind a company's TLS-inspecting proxy, with no local HTTPS: the capture
// trusts the deployments authority through Chromium's own store, and only
// once it is configured.
// Not parallel: it installs the process's trust.
func TestIntegrationScreenshotsTrustTheDeploymentsAuthority(t *testing.T) {
	company := trusttest.New(t, "Company Inspecting CA")
	port := company.Serve(t, page("<h1>Behind the proxy</h1>"))
	at := fmt.Sprintf("https://host.docker.internal:%d/", port)
	if c := localShots(nil).Take(context.Background(), at, nil); len(c.Shots) != 0 || !strings.Contains(c.Unavailable, "ERR_CERT") {
		t.Fatalf("without the authority configured: %d shots, unavailable %q", len(c.Shots), c.Unavailable)
	}
	trusttest.Install(t, company.Bundle(t), trust.Bundle{})
	if c := localShots(nil).Take(context.Background(), at, nil); c.Unavailable != "" || len(c.Shots) != 2 {
		t.Fatalf("with the deployments authority: %d shots, unavailable %q, missing %v", len(c.Shots), c.Unavailable, c.Missing)
	}
}
