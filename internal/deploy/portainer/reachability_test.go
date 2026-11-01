package portainer

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

// A lost connection and an answered rejection have different meanings.
func TestProviderConnectionFailureIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	_, _, err := JSON(t.Context(), &Target{PortainerURL: server.URL}, "GET", "/api/endpoints", nil)
	if deploy.IsUnreachable(err) {
		t.Fatalf("answered rejection became unreachable: %v", err)
	}
	server.Close()
	_, _, err = JSON(t.Context(), &Target{PortainerURL: server.URL}, "GET", "/api/endpoints", nil)
	if !deploy.IsUnreachable(err) {
		t.Fatalf("lost connection was not typed: %v", err)
	}
}
