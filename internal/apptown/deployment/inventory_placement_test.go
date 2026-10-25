package deployment

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/portainer"
)

func TestInventoryPlacementRequiresTheObservedDaemonAndCurrentAssociation(t *testing.T) {
	s, _, write := providerAssociationWorld(t)
	pid := createProjectFor(t, s.db, "placement", "Placement", project.Ordinary)
	var daemon atomic.Value
	daemon.Store("engine-a")
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/api/endpoints":
			fmt.Fprint(w, `[{"Id":3,"Name":"docker","Type":1}]`)
		case "/api/endpoints/3":
			fmt.Fprint(w, `{"Id":3,"Name":"docker","Type":1}`)
		case "/api/endpoints/3/docker/info":
			fmt.Fprintf(w, `{"ID":%q}`, daemon.Load())
		case "/api/stacks":
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected scope request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	write(server.URL, "key")
	var observed []deploy.Resource
	for _, name := range []string{"customer-east", "customer-west"} {
		target, err := s.AttachPortainerTarget(t.Context(), PortainerAttachment{Name: name, URLFrom: "PT_URL", APIKeyFrom: "PT_KEY", Environment: "docker"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.EditTargetLabel(t.Context(), target.ID, target.DisplayLabel, "Label "+name); err != nil {
			t.Fatal(err)
		}
		dep, err := s.Deployments.EnsureStanding(t.Context(), pid, name, target.ID, Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
		if err != nil {
			t.Fatal(err)
		}
		observed = append(observed, deploy.Resource{Kind: "volume", Name: name, Stack: portainer.StackName(portainer.StackNames(dep.ID)), ObservedDaemonID: "engine-a", Record: deploy.RecordUnrecorded})
	}
	claims, err := s.Inventory.Shared(t.Context(), observed)
	if err != nil || len(claims) != 2 {
		t.Fatalf("two current connections: %+v %v", claims, err)
	}
	for i, c := range claims {
		if c.Index != i || c.Target != "Label "+observed[i].Name || c.Deployment != observed[i].Name {
			t.Fatalf("wrong placement %+v", c)
		}
	}
	// Cached identity must remain the one observed, not the latest provider answer.
	other := append([]deploy.Resource(nil), observed...)
	for i := range other {
		other[i].ObservedDaemonID = "other-daemon"
	}
	if c, err := s.Inventory.Shared(t.Context(), other); err != nil || len(c) != 0 {
		t.Fatalf("different daemon fabricated shared ownership: %+v %v", c, err)
	}
	other[0].ObservedDaemonID = ""
	if _, err := s.Inventory.Shared(t.Context(), other); err == nil {
		t.Fatal("missing observed identity became an orphan")
	}
	if c, err := s.Inventory.Shared(t.Context(), []deploy.Resource{{Stack: "unknown-stack"}}); err != nil || len(c) != 0 {
		t.Fatalf("unknown label invented placement: %+v %v", c, err)
	}
	unavailable.Store(true)
	if _, err := s.Inventory.Shared(t.Context(), observed); err == nil {
		t.Fatal("unavailable association became an orphan")
	}
	unavailable.Store(false)
	daemon.Store("new-daemon")
	if _, err := s.Inventory.Shared(t.Context(), observed); err == nil {
		t.Fatal("changed provider daemon rebound cached observations")
	}
	daemon.Store("engine-a")
	write(server.URL+"/changed", "key")
	if _, err := s.Inventory.Shared(t.Context(), observed); err == nil {
		t.Fatal("changed credential destination became an orphan")
	}
	if observed[0].ObservedDaemonID != "engine-a" {
		t.Fatal("cached observation mutated")
	}
}

func TestDockerOriginsDoNotTrustPortainerConnectionDomains(t *testing.T) {
	s, _, _ := providerAssociationWorld(t)
	s.env.routes = Routing{PreviewDomain: "preview.example", PreviewScheme: "https", MainDomain: "apps.example", MainScheme: "https", EvalDomain: "eval.example", EvalScheme: "https"}
	for _, name := range []string{IntranetTargetName, "customer-office"} {
		if _, err := s.Targets.Put(t.Context(), &Target{Name: name, Kind: KindPortainer, Status: TargetReady, Ingress: map[string]any{"domain": "unrelated.example"}}); err != nil {
			t.Fatal(err)
		}
	}
	got := s.env.PublicOrigins(t.Context())
	if got != "https://*.preview.example,https://*.apps.example,https://*.eval.example" || strings.Contains(got, "unrelated") {
		t.Fatalf("Docker trusted unrelated connection domain: %s", got)
	}
}
