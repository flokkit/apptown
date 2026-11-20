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
	files := mvn.files("https://mirror.example.com/repository/maven/")
	if len(files) != 1 || !strings.Contains(files[0].Content, "mirror.example.com") {
		t.Fatalf("maven settings must carry the mirror: %+v", files)
	}
	ng := registryAdapterFor("nuget")
	if env := ng.env("https://x/"); len(env) == 0 || !strings.Contains(env[0], "RestoreConfigFile") {
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

// A Docker Hub mirror (Nexus) answers an anonymous /v2/ with 401 and the
// registry token challenge. The check gets an anonymous token, as Docker
// does before a pull, and the /v2/ answer with it is the reading: 200 is
// healthy, 401 after the token is a failure that says so. No credential is
// sent to the token service.
//
// Kills: the challenge's 401 taken as the mirror's failure (live's
// docker-hub, "GET /v2/ → 401"), and a token taken as health without the
// /v2/ answer after it.
func TestADockerHubMirrorFollowsTheTokenChallenge(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		challenge bool // /v2/ without a token answers with a Bearer challenge
		accept    bool // /v2/ with the token answers 200
		tokenCode int
		ok        bool
		detail    string
	}{
		{"accepted", true, true, http.StatusOK, true, "GET /v2/ → 200 with an anonymous token"},
		{"refused after the token", true, false, http.StatusOK, false, "GET /v2/ → 401 with an anonymous token"},
		{"token refused", true, true, http.StatusForbidden, false, "GET /v2/ → 401; anonymous token: GET "},
		{"no challenge", false, true, http.StatusOK, false, "GET /v2/ → 401"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var tokenAuth atomic.Value
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v2/token":
					tokenAuth.Store(r.Header.Get("Authorization") + "|" + r.URL.Query().Get("service"))
					w.WriteHeader(tc.tokenCode)
					_, _ = w.Write([]byte(`{"token":"anon-token"}`))
				case "/v2/":
					if r.Header.Get("Authorization") == "Bearer anon-token" && tc.accept {
						_, _ = w.Write([]byte("{}"))
						return
					}
					if tc.challenge {
						w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/v2/token",service="Nexus Docker"`)
					}
					w.WriteHeader(http.StatusUnauthorized)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			h := ProbeRegistry("docker-hub", srv.URL)
			if h.OK != tc.ok || !strings.HasPrefix(h.Detail, tc.detail) {
				t.Fatalf("probe: %+v, want ok %v and %q", h, tc.ok, tc.detail)
			}
			if got, _ := tokenAuth.Load().(string); tc.challenge && got != "|Nexus Docker" {
				t.Fatalf("token request (authorization|service) %q, want no credential and the challenge's service", got)
			}
		})
	}
}
