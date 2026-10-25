package deployment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
)

func providerAssociationWorld(t *testing.T) (*Service, *requestAuthority, func(string, string)) {
	t.Helper()
	db := testDB(t)
	dir := t.TempDir()
	projects := project.New(db, nil)
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	authority := &requestAuthority{}
	s := New(t.Context(), db, assembled(db, testRuntime{DataDir: dir, Projects: projects, Secrets: secrets, Integrations: integrations, Needs: dependency.NewCatalog(db, nil)}, Dependencies{Authority: authority}))
	t.Cleanup(func() {
		for _, proxy := range s.providers.portainerProxies {
			if err := proxy.Close(); err != nil {
				t.Error(err)
			}
			if err := os.RemoveAll(filepath.Dir(proxy.Socket)); err != nil {
				t.Error(err)
			}
		}
	})
	write := func(base, key string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("PT_URL="+base+"\nPT_KEY="+key+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return s, authority, write
}

func providerScopeServer(t *testing.T, key *atomic.Value, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-API-Key") != key.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/endpoints":
			fmt.Fprint(w, `[{"Id":3,"Name":"docker","Type":1}]`)
		case "/api/endpoints/3":
			fmt.Fprint(w, `{"Id":3,"Name":"docker","Type":1}`)
		case "/api/endpoints/3/docker/info":
			fmt.Fprint(w, `{"ID":"engine-a"}`)
		case "/api/endpoints/3/docker/version":
			fmt.Fprint(w, `{"Os":"linux","Arch":"amd64"}`)
		case "/api/endpoints/3/docker/_ping":
			fmt.Fprint(w, "OK")
		case "/api/stacks":
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestProviderAssociationAttachesAtomicallyAndProtectsConnection(t *testing.T) {
	s, authority, write := providerAssociationWorld(t)
	var key atomic.Value
	key.Store("first")
	var calls atomic.Int32
	server := providerScopeServer(t, &key, &calls)
	write(server.URL, "first")
	input := PortainerAttachment{Name: "customer-place", URLFrom: "PT_URL", APIKeyFrom: "PT_KEY", Environment: "docker", Domain: "apps.example"}
	authority.deny = errors.New("denied")
	if _, err := s.AttachPortainerTarget(t.Context(), input); err == nil {
		t.Fatal("unauthorized attach succeeded")
	}
	if calls.Load() != 0 {
		t.Fatal("provider observed before authority")
	}
	authority.deny = nil
	target, err := s.AttachPortainerTarget(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if target.DisplayLabel != target.Name {
		t.Fatalf("new attachment label %q, want %q", target.DisplayLabel, target.Name)
	}
	if renamed, err := s.EditTargetLabel(t.Context(), target.ID, target.DisplayLabel, "Customer environment"); err != nil || renamed.DisplayLabel != "Customer environment" {
		t.Fatalf("rename newly attached target: %+v %v", renamed, err)
	}
	if target.ConnectionID.IsZero() || len(target.ProviderScope) == 0 {
		t.Fatal("attachment omitted immutable connection or observed scope")
	}
	if got := authority.seen[len(authority.seen)-1]; got.Capability != access.CapPlatformTargets || !got.Context.Consequential {
		t.Fatalf("wrong attachment authority: %+v", got)
	}
	if _, err := s.AttachPortainerTarget(t.Context(), input); err == nil {
		t.Fatal("duplicate name replaced the target")
	}
	bindings, err := s.env.integrations.Bindings.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 {
		t.Fatalf("failed attachment left %d connections", len(bindings))
	}
	if err := s.env.integrations.Bindings.Delete(t.Context(), bindings[0].Name, "alice"); !errors.Is(err, integration.ErrAttached) {
		t.Fatalf("referenced connection deletion: %v", err)
	}
	if _, err := s.providers.PortainerConfig(t.Context(), target); err != nil {
		t.Fatal(err)
	}
}

func TestProviderAssociationRefusesChangedURLBeforeOrdinaryAndProxyRequests(t *testing.T) {
	s, _, write := providerAssociationWorld(t)
	var key atomic.Value
	key.Store("first")
	var calls atomic.Int32
	server := providerScopeServer(t, &key, &calls)
	write(server.URL, "first")
	target, err := s.AttachPortainerTarget(t.Context(), PortainerAttachment{Name: "place", URLFrom: "PT_URL", APIKeyFrom: "PT_KEY", Environment: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := s.providers.PortainerDataScope(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimPrefix(scope.Host, "unix://"))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	request := func() int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), "GET", "http://docker/_ping", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if got := request(); got != 200 {
		t.Fatalf("initial proxy: HTTP %d", got)
	}
	key.Store("rotated")
	write(server.URL, "rotated")
	if _, err := s.providers.PortainerConfig(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if got := request(); got != 200 {
		t.Fatalf("rotated proxy: HTTP %d", got)
	}
	var foreignCalls atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls.Add(1); w.WriteHeader(401) }))
	defer foreign.Close()
	write(foreign.URL, "rotated")
	if _, err := s.providers.PortainerConfig(t.Context(), target); err == nil {
		t.Fatal("changed URL accepted")
	}
	if got := request(); got != 502 {
		t.Fatalf("changed proxy: HTTP %d", got)
	}
	if foreignCalls.Load() != 0 {
		t.Fatalf("foreign destination received %d requests before refusal", foreignCalls.Load())
	}
}

func TestProviderAssociationObservesLegacyWithoutInventingScope(t *testing.T) {
	s, _, write := providerAssociationWorld(t)
	var key atomic.Value
	key.Store("first")
	var calls atomic.Int32
	server := providerScopeServer(t, &key, &calls)
	write(server.URL, "first")
	target, err := s.Targets.Put(t.Context(), &Target{Name: "legacy", Kind: KindPortainer, Status: TargetReady, Address: map[string]any{"urlFrom": "PT_URL", "apiKeyFrom": "PT_KEY", AddressEnvironment: "docker"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.providers.PortainerConfig(t.Context(), target); err == nil {
		t.Fatal("unassociated legacy target used credentials")
	}
	if calls.Load() != 0 {
		t.Fatal("unassociated ordinary use made a provider request")
	}
	preview, err := s.InspectProviderAssociation(t.Context(), target.Name, ProviderAssociationSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Associated || preview.Scope.DaemonID != "engine-a" {
		t.Fatalf("preview: %+v", preview)
	}
	unchanged, err := s.Targets.ByID(t.Context(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged.ProviderScope) != 0 || !unchanged.ConnectionID.IsZero() {
		t.Fatal("observation invented a durable association")
	}
	confirmed, err := s.AssociateProvider(t.Context(), target.Name, ProviderAssociationSelection{ExpectedRevision: preview.Revision, Expected: &preview.Scope, Resources: preview.Resources})
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed.Associated || confirmed.ConnectionID.IsZero() {
		t.Fatal("confirmed association missing")
	}
	if _, err := s.providers.PortainerConfig(t.Context(), target); err != nil {
		t.Fatal(err)
	}
}

func TestProviderAssociationNumericEndpointKeepsEditableDisplaySettings(t *testing.T) {
	s, _, write := providerAssociationWorld(t)
	var key atomic.Value
	key.Store("key")
	var calls atomic.Int32
	server := providerScopeServer(t, &key, &calls)
	write(server.URL, "key")
	target, err := s.AttachPortainerTarget(t.Context(), PortainerAttachment{Name: "numeric", EndpointID: 3, URLFrom: "PT_URL", APIKeyFrom: "PT_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Address[AddressEnvironment] != "docker" {
		t.Fatalf("numeric endpoint lost observed display name: %v", target.Address)
	}
	domain, environment := "changed.example", "docker"
	edited, err := s.Targets.EditSettings(t.Context(), target.Name, TargetSettingsEdit{ExpectedRevision: &target.SettingsRevision, Domain: &domain, Environment: &environment}, "alice")
	if err != nil || edited.Ingress["domain"] != domain {
		t.Fatalf("ordinary settings edit: %+v %v", edited, err)
	}
	if string(edited.ProviderScope) != string(target.ProviderScope) || edited.ConnectionID != target.ConnectionID {
		t.Fatal("display settings changed provider identity")
	}
}

func TestPortainerProxyUsesPrivateProcessLocalSocket(t *testing.T) {
	s, _, write := providerAssociationWorld(t)
	var key atomic.Value
	key.Store("key")
	var calls atomic.Int32
	server := providerScopeServer(t, &key, &calls)
	write(server.URL, "key")
	target, err := s.AttachPortainerTarget(t.Context(), PortainerAttachment{Name: "ipc", EndpointID: 3, Environment: "docker", URLFrom: "PT_URL", APIKeyFrom: "PT_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	// App mounts may use a host-shared TMPDIR; local IPC cannot depend on it.
	// A missing temporary root makes accidental use deterministic on every host.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "not-a-local-filesystem"))
	if _, err := s.providers.PortainerDataScope(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if len(s.providers.portainerProxies) != 1 {
		t.Fatal("missing proxy")
	}
	for _, proxy := range s.providers.portainerProxies {
		for path, want := range map[string]os.FileMode{filepath.Dir(proxy.Socket): 0700, proxy.Socket: 0600} {
			st, err := os.Stat(path)
			if err != nil || st.Mode().Perm() != want {
				t.Fatalf("private IPC %s: %v %v", path, st, err)
			}
		}
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", proxy.Socket)
		}}
		defer transport.CloseIdleConnections()
		client := http.Client{Transport: transport}
		response, err := client.Get("http://docker/version")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("local scoped proxy: HTTP %d", response.StatusCode)
		}
	}
}
