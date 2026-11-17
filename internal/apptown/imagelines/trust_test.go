package imagelines

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/imageline"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// A bound HTTPS mirror uses the same deployments trust as image pulls.
func TestRegistryReadingsUseDeploymentsTrustForMirror(t *testing.T) {
	// Serial: this test installs process-wide trust.
	var heads atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "library/caddy", "tags": []string{"2.11.9-alpine"}})
		case strings.Contains(r.URL.Path, "/manifests/"):
			if r.Method != "HEAD" {
				t.Errorf("manifest method %s", r.Method)
			}
			heads.Add(1)
			digest := "sha256:" + strings.Repeat("a", 64)
			if strings.HasSuffix(r.URL.Path, "/2.11.8-alpine") {
				digest = "sha256:" + strings.Repeat("b", 64)
			}
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.Header().Set("Content-Length", "200")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	bundle, err := trust.Parse(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	if err != nil {
		t.Fatal(err)
	}
	set, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{trust.Deployments: bundle}, trust.Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	previous := trust.Current()
	trust.Install(set)
	t.Cleanup(func() { trust.Install(previous) })
	repo, err := name.NewRepository(strings.TrimPrefix(server.URL, "https://") + "/library/caddy")
	if err != nil {
		t.Fatal(err)
	}
	// Control: the registry and configured CA work with the established transport.
	if _, err := remote.List(repo, remote.WithContext(t.Context()), remote.WithTransport(trust.Lazy(trust.Deployments))); err != nil {
		t.Fatalf("trusted registry control failed: %v", err)
	}
	s := New(suite.New(t), nil, func(context.Context) (string, error) { return server.URL, nil })
	if _, err := s.Answer(t.Context(), "caddy:2-alpine", imageline.Line); err != nil {
		t.Fatalf("configured deployments CA was ignored: %v", err)
	}
	// Candidate proof is always a new HEAD, even after Answer cached this line.
	before := heads.Load()
	for range 2 {
		digest, err := s.Digest(t.Context(), "caddy:2.11.8-alpine")
		if err != nil || digest != "sha256:"+strings.Repeat("b", 64) {
			t.Fatalf("candidate proof: %s %v", digest, err)
		}
	}
	if heads.Load() != before+2 {
		t.Fatalf("candidate proof reused a cached reading: %d HEADs", heads.Load()-before)
	}
}
