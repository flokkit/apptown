package portainer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifiedScopeRefusesChangedBaseBeforeSendingCredentials(t *testing.T) {
	foreignCalls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls++; w.WriteHeader(http.StatusUnauthorized) }))
	defer foreign.Close()
	expected := Scope{Version: 1, APIBase: "https://recorded.example", EndpointID: 1, DaemonID: "recorded-daemon"}
	err := VerifyScope(context.Background(), Target{PortainerURL: foreign.URL, PortainerKey: "must-not-be-disclosed"}, expected)
	if err == nil {
		t.Fatal("changed API base accepted")
	}
	if foreignCalls != 0 {
		t.Fatalf("changed API base received %d authenticated observation requests", foreignCalls)
	}
}

func TestScopeUsesSelectedEndpointAndAcceptsSameDestinationKeyRotation(t *testing.T) {
	key := "first-key"
	daemon := "daemon-a"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-API-Key") != key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/endpoints/7":
			fmt.Fprint(w, `{"Id":7,"Type":1}`)
		case "/api/endpoints/7/docker/info":
			fmt.Fprintf(w, `{"ID":%q}`, daemon)
		default:
			t.Errorf("unexpected endpoint selection %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	cfg := Target{PortainerURL: server.URL, PortainerKey: key, EnvName: "a display name"}
	scope, err := ObserveScope(context.Background(), cfg, 7)
	if err != nil {
		t.Fatal(err)
	}
	if scope.EndpointID != 7 || scope.DaemonID != daemon || scope.APIBase != server.URL {
		t.Fatalf("wrong scope: %+v", scope)
	}
	key = "rotated-key"
	cfg.PortainerKey, cfg.EnvName = key, "renamed display label"
	if err := VerifyScope(context.Background(), cfg, scope); err != nil {
		t.Fatal(err)
	}
	daemon = "replacement-daemon"
	if err := VerifyScope(context.Background(), cfg, scope); err == nil {
		t.Fatal("replacement daemon accepted")
	}
	if calls != 6 {
		t.Fatalf("expected three paired observations, got %d requests", calls)
	}
}

func TestPortainerCredentialRequestsDoNotFollowRedirects(t *testing.T) {
	foreignCalls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls++; w.WriteHeader(http.StatusOK) }))
	defer foreign.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	status, _, err := JSON(context.Background(), &Target{PortainerURL: source.URL, PortainerKey: "private-key"}, "GET", "/api/endpoints", nil)
	if err == nil || !strings.Contains(err.Error(), "redirect refused") || foreignCalls != 0 {
		t.Fatalf("redirect response=%d error=%v foreign requests=%d", status, err, foreignCalls)
	}
}

func TestCanonicalAPIBaseRetainsDestinationBoundaries(t *testing.T) {
	cases := map[string]string{
		"https://EXAMPLE.test:443/base/": "https://example.test/base",
		"http://example.test:80":         "http://example.test",
		"https://example.test:9443/base": "https://example.test:9443/base",
		"http://[::1]:9000/":             "http://[::1]:9000",
	}
	for input, want := range cases {
		got, err := CanonicalAPIBase(input)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"file:///tmp/server", "https://user:secret@example.test", "https://example.test?key=secret", "https://example.test/#fragment", "https://example.test?", "https://example.test/base%2F"} {
		if _, err := CanonicalAPIBase(input); err == nil {
			t.Errorf("accepted ambiguous credential destination %q", input)
		}
	}
}
