package agentenv

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The registry adapters produce the env or config a build step reads, from
// the instance's mirror base — no hard-coded host.
func TestRegistryAdapters(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"npm", "pypi", "cargo", "maven", "nuget", "rubygems", "go-proxy"} {
		if registryAdapterFor(format) == nil {
			t.Errorf("no adapter for %s", format)
		}
	}
	mvn := registryAdapterFor("maven")
	files := mvn.Files("https://mirror.example.com/repository/maven/")
	if len(files) != 1 || !strings.Contains(files[0].Content, "mirror.example.com") {
		t.Fatalf("maven settings must carry the mirror: %+v", files)
	}
	ng := registryAdapterFor("nuget")
	if env := ng.Env("https://x/"); len(env) == 0 || !strings.Contains(env[0], "RestoreConfigFile") {
		t.Fatalf("nuget env: %v", env)
	}
}

// A registry is probed with one GET, and the reading names the method that
// ran: a failing first answer is never replaced by a second request's.
//
// Kills: a HEAD that failed retried as GET and reported as the HEAD.
func TestARegistryProbeIsOneRequestAndNamesIt(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("go source"))
	}))
	defer srv.Close()
	h := ProbeRegistry("golang-dist", srv.URL)
	if !h.OK || !strings.HasPrefix(h.Detail, "GET /go1.24.4.src.tar.gz → 200") || requests.Load() != 1 {
		t.Fatalf("probe: %+v after %d requests", h, requests.Load())
	}
	// A failing answer is the reading, after one request.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	requests.Store(0)
	if h := ProbeRegistry("golang-dist", failing.URL); h.OK || !strings.Contains(h.Detail, "503") || requests.Load() != 1 {
		t.Fatalf("a failing registry: %+v after %d requests", h, requests.Load())
	}
}
