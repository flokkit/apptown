//go:build integration

package portainer

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

// The HTTP proxy has Portainer's Docker route shape and talks to the real
// disposable local daemon. This tests the actual CLI/proxy/diagnostic protocol,
// not a live Portainer installation or production deployment.
func TestIntegrationExactContainerReadinessThroughPortainerProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cli := docker.CLI{}
	out, err := cli.Run(ctx, "", docker.Command{Args: []string{"context", "inspect", "--format", "{{.Endpoints.docker.Host}}"}, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimSpace(string(out))
	endpoint, err := url.Parse(host)
	if err != nil || endpoint.Scheme != "unix" || endpoint.Path == "" {
		t.Fatalf("fixture requires an explicit local Unix Docker endpoint, got %q", host)
	}
	command := func(ctx context.Context, args ...string) ([]byte, error) {
		return cli.Run(ctx, host, docker.Command{Args: args, Timeout: 30 * time.Second})
	}
	image := "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	prefix := fmt.Sprintf("apptown-readiness-proof-%d", time.Now().UnixNano())
	names := []string{prefix + "-app", prefix + "-sibling"}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		for _, name := range names {
			out, err := command(cleanup, "rm", "-f", "-v", name)
			if err != nil && !strings.Contains(string(out), "No such container") {
				t.Errorf("cleanup %s: %v %s", name, err, out)
			}
		}
	})
	script := `mkdir -p /srv/cgi-bin /srv/_apptown
printf 'ready' > /srv/ready
printf 'release-id' > /srv/_apptown/release
printf '%s\n' '#!/bin/sh' 'if [ "$HTTP_HOST" != "app.example" ] || [ "$HTTP_X_FORWARDED_PROTO" != "https" ]; then printf "Status: 403 Forbidden\r\n\r\n"; else printf "Status: 200 OK\r\n\r\nready"; fi' > /srv/cgi-bin/ready
chmod +x /srv/cgi-bin/ready
exec httpd -f -p 8080 -h /srv`
	out, err = command(ctx, "run", "-d", "--name", names[0], "--network", "none", image, "sh", "-c", script)
	if err != nil {
		t.Fatalf("fixture app: %v %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	if len(id) != 64 {
		t.Fatalf("fixture app has no exact ID: %q", id)
	}
	if out, err = command(ctx, "run", "-d", "--name", names[1], "--network", "none", "--label", "apptown.class=container-result-owned", "--label", "apptown.deadline=1", image, "sleep", "120"); err != nil {
		t.Fatalf("sibling fixture: %v %s", err, out)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", endpoint.Path)
	}}
	defer transport.CloseIdleConnections()
	remote := &httputil.ReverseProxy{Director: func(r *http.Request) {
		r.URL.Scheme = "http"
		r.URL.Host = "docker"
		r.Host = "docker"
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/endpoints/3/docker")
		r.Header.Del("X-API-Key")
	}, Transport: transport}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/endpoints/3/docker/") || r.Header.Get("X-API-Key") != "fixture-key" {
			t.Errorf("wrong verified proxy route %s", r.URL.Path)
			w.WriteHeader(403)
			return
		}
		remote.ServeHTTP(w, r)
	}))
	defer server.Close()
	dir, err := os.MkdirTemp("/tmp", "apptown-probe-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cfg := &Target{PortainerURL: server.URL, PortainerKey: "fixture-key", Scope: &Scope{Version: 1, APIBase: server.URL, EndpointID: 3, DaemonID: "fixture"}}
	proxy, err := StartDockerProxy(dir, deploymentEnvironment{ID: 3, Type: 1}, func(context.Context) (*Target, error) { return cfg, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := proxy.Close(); err != nil {
			t.Errorf("close owned proxy: %v", err)
		}
	}()
	diagnostic := Diagnostic{allowed: func(context.Context) error { return nil }, Command: func(ctx context.Context, env []string, args ...string) ([]byte, error) {
		if args[0] == "create" {
			for i, arg := range args {
				if arg == "--name" {
					names = append(names, args[i+1])
				}
			}
		}
		return cli.Run(ctx, proxy.Host(), docker.Command{Args: args, Env: env, Timeout: 30 * time.Second})
	}}
	p := Probe{ToolImage: image, Port: 8080, Path: "/cgi-bin/ready", Expect: 200, Headers: map[string]string{"Host": "app.example", "X-Forwarded-Host": "app.example", "X-Forwarded-Proto": "https"}, StaticRelease: "release-id"}
	if err := diagnostic.sample(ctx, id, "run-1", "dep-1", p); err != nil {
		t.Fatalf("exact shaped readiness and static identity: %v", err)
	}
	p.Expect = 204
	if err := diagnostic.sample(ctx, id, "run-1", "dep-1", p); err == nil {
		t.Fatal("wrong status accepted")
	}
	p.Expect = 200
	p.StaticRelease = "wrong-release"
	if err := diagnostic.sample(ctx, id, "run-1", "dep-1", p); err == nil {
		t.Fatal("wrong static identity accepted")
	}
	p.StaticRelease = ""
	p.Path = "/ready"
	p.Headers = nil
	if err := diagnostic.sample(ctx, id, "run-1", "dep-1", p); err != nil {
		t.Fatalf("direct readiness: %v", err)
	}
	out, err = command(ctx, "inspect", "--format", "{{.State.Running}}", names[1])
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("sibling diagnostic was touched: %v %s", err, out)
	}
	for _, name := range names[2:] {
		out, err = command(ctx, "inspect", name)
		if err == nil || !strings.Contains(strings.ToLower(string(out)), "no such object: "+name) {
			t.Fatalf("owned diagnostic remains or removal unknown %s: %v %s", name, err, out)
		}
	}
}
