package docker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestDockerScopeRejectsMissingClientAndCanceledCalls(t *testing.T) {
	t.Parallel()
	if _, err := (Scope{}).Run(context.Background(), "info"); err == nil {
		t.Fatal("unconfigured scope reached a default daemon")
	}
	dk := Scope{Client: &fakeClient{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dk.Command(ctx, Command{Args: []string{"info"}}); err != context.Canceled {
		t.Fatal(err)
	}
	if len(dk.Client.(*fakeClient).args) != 0 {
		t.Fatal("canceled command reached transport")
	}
}

func TestDockerLivenessUsesSelectedHostAndHeaders(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Host, "localhost:") || r.URL.Path != "/live" || r.Header.Get("X-Forwarded-Proto") != "https" {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	_, rawPort, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(rawPort)
	dk := (Scope{}).WithHost("ssh://selected", "localhost", "")
	if !ProbeLiveness(context.Background(), dk, "app", port, RenderedProbe{Path: "/live", Expect: 204}, map[string]string{"X-Forwarded-Proto": "https"}) {
		t.Fatal("HTTP probe ignored selected host or front-door headers")
	}
}
