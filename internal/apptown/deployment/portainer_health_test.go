package deployment

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/portainer"
)

type readinessTransportClient struct {
	run func(context.Context, string, docker.Command) ([]byte, error)
}

func (c readinessTransportClient) Run(ctx context.Context, host string, cmd docker.Command) ([]byte, error) {
	return c.run(ctx, host, cmd)
}

// The external command boundary speaks HTTP over the actual owned Unix proxy.
// The real Docker CLI/probe protocol is covered by Portainer's disposable
// integration witness; this proves the production owner factory and cleanup.
func TestPortainerDiagnosticFactoryOwnsPinnedProxyAndCleanup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "captured-key" {
			t.Error("proxy changed credential snapshot")
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/api/endpoints/3":
			fmt.Fprint(w, `{"Id":3,"Name":"captured","Type":1}`)
		case "/api/endpoints/3/docker/version":
			fmt.Fprint(w, `{"Version":"test"}`)
		case "/api/endpoints/3/docker/v1.45/info":
			fmt.Fprint(w, `{"ID":"captured-daemon"}`)
		default:
			t.Errorf("unexpected captured route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	var socket string
	client := readinessTransportClient{run: func(ctx context.Context, host string, cmd docker.Command) ([]byte, error) {
		parsed, err := url.Parse(host)
		if err != nil || parsed.Scheme != "unix" || parsed.Path == "" {
			return nil, fmt.Errorf("no exact private socket: %q", host)
		}
		socket = parsed.Path
		if cmd.Timeout != 30*time.Second || len(cmd.Env) != 1 || cmd.Env[0] != "PROBE=value" {
			t.Errorf("command boundary lost timeout/environment: %+v", cmd)
		}
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		defer transport.CloseIdleConnections()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.45/info", nil)
		if err != nil {
			return nil, err
		}
		response, err := (&http.Client{Transport: transport}).Do(req)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return nil, fmt.Errorf("proxy HTTP %d", response.StatusCode)
		}
		return io.ReadAll(response.Body)
	}}
	providers := &Providers{hosts: &Hosts{settings: HostSettings{Scope: docker.Scope{Client: docker.Refusing{Attached: client}}}}}
	cfg := portainer.Target{PortainerURL: server.URL, PortainerKey: "captured-key", Scope: &portainer.Scope{Version: 1, APIBase: server.URL, EndpointID: 3, DaemonID: "captured-daemon"}}
	diagnostic, close, err := providers.portainerDiagnostic(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := close(); err != nil {
			t.Error(err)
		}
	}()
	cfg.Scope.EndpointID = 99
	cfg.PortainerKey = "rotated-key"
	cfg.PortainerURL = "http://invalid.test"
	got, err := diagnostic.Command(t.Context(), []string{"PROBE=value"}, "info")
	if err != nil || string(got) != `{"ID":"captured-daemon"}` {
		t.Fatalf("snapshot proxy: %q %v", got, err)
	}
	if err := close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(socket)); !os.IsNotExist(err) {
		t.Fatalf("owned proxy directory retained: %v", err)
	}
}
