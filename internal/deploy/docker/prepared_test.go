package docker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// recordingClient records every Docker command, and answers ports.
type recordingClient struct {
	disk  diskspacetest.Daemon
	mu    sync.Mutex
	calls []string
}

func (c *recordingClient) Run(_ context.Context, _ string, req Command) ([]byte, error) {
	c.mu.Lock()
	c.calls = append(c.calls, strings.Join(req.Args, " "))
	c.mu.Unlock()
	if out, err, ok := c.disk.Roomy(req.Args); ok {
		return out, err
	}
	if out, ok := ownBuilderAnswer(req.Args); ok {
		return out, nil
	}
	args := req.Args
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	switch {
	case len(args) > 0 && args[0] == "port":
		return []byte("127.0.0.1:40001\n"), nil
	case len(args) > 1 && args[0] == "context" && args[1] == "show":
		return []byte("default\n"), nil
	case len(args) > 0 && args[0] == "image":
		return []byte("\n"), nil
	case len(args) > 2 && args[0] == "inspect" && strings.Contains(args[2], ".State.Health"):
		return []byte("healthy\n"), nil // a declared health check passes at once
	}
	return nil, nil
}

type upProbe struct{}

func (upProbe) Check(context.Context, Scope, string, string, time.Duration, RenderedProbe) (bool, string) {
	return true, ""
}

// liveDeps are resolvers as an AppTown has them: secrets per consumer, a needs
// catalog, the integrations' environment and browser values, and routes.
type liveSecrets struct{ version string }

func (s liveSecrets) Resolve(context.Context, string, string, string, *appcontract.Contract, func(context.Context) error) (Delivery, error) {
	return Delivery{Env: func(consumer string) map[string]string {
		return map[string]string{"SECRET_FOR": "value-for-" + consumer}
	}, Receipts: []SecretReceipt{{Name: "SECRET_FOR", Store: "builtin", Version: s.version, Consumers: []string{"web"}}}, Profile: "default"}, nil
}

// liveNeeds fulfils only the needs a contract declares: PostgreSQL, with
// the health check the catalog gives it, so a boot takes the readiness path
// rather than the no-health-check grace.
type liveNeeds struct{}

func (liveNeeds) Fulfil(ctx context.Context, needs []string, _ map[string]map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
	if !slices.Contains(needs, "postgres") {
		return nil, nil, nil
	}
	return []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16-alpine", Health: "pg_isready -U app -d app"}}, map[string]string{"DATABASE_URL": "postgres://db"}, nil
}
func (liveNeeds) Data(context.Context, string) (string, error) {
	return "/var/lib/postgresql/data", nil
}

type liveContributions struct{ browser string }

func (c liveContributions) EnvFor(context.Context) ([]string, error) {
	return []string{"ERRORS_DSN=https://key@errors/1"}, nil
}
func (c liveContributions) BrowserEnv(context.Context) (string, error) { return c.browser, nil }

type liveRoutes struct{}

func (liveRoutes) FrontDoorHeaders() map[string]string {
	return map[string]string{"X-Forwarded-Proto": "https"}
}
func (liveRoutes) PublicOrigins(ctx context.Context) string {
	return "https://change-1.previews.example"
}

const preparedContract = "version: 1\napp:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n  worker:\n    build: .\n    command: [\"work\"]\ninit:\n  - {name: migrate, build: ., command: [\"migrate\"]}\nneeds:\n  postgres: {version: \"16\"}\n"

func bootOnce(t *testing.T, deps Dependencies, c *appcontract.Contract, ws string, at time.Time) []string {
	t.Helper()
	client := &recordingClient{}
	if _, err := NewHost(deps).Boot(context.Background(), BootRequest{Docker: Scope{Client: client, Now: func() time.Time { return at }}, Name: "apptown-preview-change-1", StackID: "pvsnap-change-1",
		Workspace: ws, Slug: "app", Deployment: "preview", Probe: time.Second, Scratch: t.TempDir()}, c); err != nil {
		t.Fatalf("boot: %v\n%s", err, strings.Join(client.calls, "\n"))
	}
	return client.calls
}

// A boot from prepared values is the same boot as one from AppTown's own
// resolvers: the same containers, with the same secrets per consumer, needs,
// environment and routes. The prepared value's public part holds none of
// the secrets, the integrations' environment or the login.
func TestAPreparedBootIsTheSameBoot(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte(preparedContract))
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := os.WriteFile(ws+"/Dockerfile", []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	live := Dependencies{StaticServer: testStaticServer, DFImage: testDF, Secrets: liveSecrets{}, Needs: liveNeeds{}, Integrations: liveContributions{"{}"}, Routes: liveRoutes{}, Probe: upProbe{}}
	prepared, err := PrepareBoot(t.Context(), live, &RegistryCredential{Host: "registry.example", Username: "u", Token: "secret-token"}, "app", "preview", ws, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, consumer := range []string{"web", "worker", "init-migrate"} {
		if prepared.Secrets[consumer]["SECRET_FOR"] != "value-for-"+consumer {
			t.Fatalf("secrets for %s: %v", consumer, prepared.Secrets)
		}
	}
	at := time.Unix(1700000000, 0)
	liveCalls := bootOnce(t, live, c, ws, at)
	// The declared PostgreSQL carries its health check, and the boot waits
	// on its answer before anything runs against it.
	asked, initRan := -1, -1
	for i, call := range liveCalls {
		switch {
		case strings.HasPrefix(call, "run -d") && strings.Contains(call, "--network-alias postgres") && !strings.Contains(call, "--health-cmd pg_isready"):
			t.Fatalf("the declared service runs without its health check: %q", call)
		case strings.HasPrefix(call, "inspect -f {{if .State.Health}}") && strings.HasSuffix(call, Current.Service("pvsnap-change-1", "postgres")):
			asked = i
		case strings.HasPrefix(call, "run --rm") && strings.Contains(call, Current.Init("pvsnap-change-1", "migrate")):
			initRan = i
		}
	}
	if asked < 0 || initRan < asked {
		t.Fatalf("the boot did not wait on the declared service's health before its init (asked at %d, init at %d):\n%s", asked, initRan, strings.Join(liveCalls, "\n"))
	}
	withoutLogin := prepared
	withoutLogin.Registry = nil
	// Both boots must agree even when their creation timestamps differ.
	preparedCalls := bootOnce(t, withoutLogin.Dependencies(upProbe{}, testImages()), c, ws, at.Add(time.Second))
	if normalised(liveCalls) != normalised(preparedCalls) {
		t.Fatalf("a prepared boot differs from a live one:\nlive:\n%s\nprepared:\n%s", strings.Join(liveCalls, "\n"), strings.Join(preparedCalls, "\n"))
	}
	public, _ := jsonOf(prepared.Public())
	for _, secret := range []string{"value-for-", "ERRORS_DSN", "secret-token"} {
		if strings.Contains(public, secret) {
			t.Fatalf("the public part holds %q: %s", secret, public)
		}
	}
}

// The identity's digest follows the public values: an attachment's browser
// value, a secret binding's version or the fingerprint of the secret values
// changing without the source changing is a new incarnation, so
// a running preview is not kept with the old values.
func TestTheDigestFollowsThePublicValues(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    static: .\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	prepare := func(browser string) string {
		p, err := PrepareBoot(t.Context(), Dependencies{StaticServer: testStaticServer, DFImage: testDF, Secrets: liveSecrets{}, Needs: liveNeeds{}, Integrations: liveContributions{browser}, Routes: liveRoutes{}}, nil, "app", "", t.TempDir(), c)
		if err != nil {
			t.Fatal(err)
		}
		return p.Public().Digest()
	}
	if a, b := prepare(`{"DSN":"a"}`), prepare(`{"DSN":"a"}`); a != b {
		t.Fatal("the same values gave two digests")
	}
	if a, b := prepare(`{"DSN":"a"}`), prepare(`{"DSN":"b"}`); a == b {
		t.Fatal("a browser value changed and the digest did not")
	}
	withSecrets := func(version string) Prepared {
		p, err := PrepareBoot(t.Context(), Dependencies{StaticServer: testStaticServer, DFImage: testDF, Secrets: liveSecrets{version}, Needs: liveNeeds{}, Integrations: liveContributions{"{}"}, Routes: liveRoutes{}}, nil, "app", "", t.TempDir(), c)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	v1, v2 := withSecrets("1"), withSecrets("2")
	if v1.Public().Digest() != withSecrets("1").Public().Digest() {
		t.Fatal("the same secret version gave two digests")
	}
	if v1.Public().Digest() == v2.Public().Digest() {
		t.Fatal("a secret binding's version changed and the digest did not")
	}
	other := v1
	other.SecretsFingerprint = "another"
	if v1.Public().Digest() == other.Public().Digest() {
		t.Fatal("the secrets' fingerprint changed and the digest did not")
	}
}

// Every prepared boot, with a registry login or without one, runs each
// command with a Docker configuration of its own, which holds its login
// before anything is built or pulled, is never made by `docker login`, and
// is gone with the boot; two boots never share one.
func TestEveryPreparedBootHasAConfigurationOfItsOwn(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte(preparedContract))
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := os.WriteFile(ws+"/Dockerfile", []byte("FROM registry.example/base:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	live := Dependencies{StaticServer: testStaticServer, DFImage: testDF, Secrets: liveSecrets{}, Needs: liveNeeds{}, Integrations: liveContributions{"{}"}, Routes: liveRoutes{}, Probe: upProbe{}}
	configs := map[string]bool{}
	for _, login := range []*RegistryCredential{{Host: "registry.example", Username: "u", Token: "first"}, {Host: "registry.example", Username: "u", Token: "second"}, nil} {
		p, err := PrepareBoot(t.Context(), live, login, "app", "", ws, c)
		if err != nil {
			t.Fatal(err)
		}
		calls := bootOnce(t, p.Dependencies(upProbe{}, testImages()), c, ws, time.Unix(1700000000, 0))
		var config string
		for _, call := range calls {
			if call == "context show" {
				continue
			}
			fields := strings.Fields(call)
			if len(fields) < 3 || fields[0] != "--config" {
				t.Fatalf("a command of the boot without its own configuration: %q", call)
			}
			if fields[2] == "login" {
				t.Fatalf("the boot logged in through Docker: %q", call)
			}
			if config == "" {
				config = fields[1]
			}
			if fields[1] != config {
				t.Fatalf("a boot changed configuration: %q", call)
			}
		}
		if _, err := os.Stat(config); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the boot's configuration outlived it")
		}
		if configs[config] {
			t.Fatal("two boots shared a configuration")
		}
		configs[config] = true
	}
}

// The prepared value carries the contract it was prepared for, through
// serialization, and its digest follows it; another contract cannot boot
// with values prepared for this one.
func TestPreparedValuesBelongToTheirContract(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte(preparedContract))
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	live := Dependencies{StaticServer: testStaticServer, DFImage: testDF, Secrets: liveSecrets{}, Needs: liveNeeds{}, Integrations: liveContributions{"{}"}, Routes: liveRoutes{}, Probe: upProbe{}}
	p, err := PrepareBoot(t.Context(), live, nil, "app", "", ws, c)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var sent Prepared
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sent.Contract, c) || sent.Public().Digest() != p.Public().Digest() {
		t.Fatalf("the contract did not survive serialization:\n%+v\n%+v", sent.Contract, c)
	}
	other, err := appcontract.Parse([]byte(strings.Replace(preparedContract, "public: true", "public: true\n    command: [\"serve\", \"--other\"]", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preparedContract, "public: true") || SameContract(other, c) {
		t.Fatal("the substitute contract is not another one")
	}
	q, err := PrepareBoot(t.Context(), live, nil, "app", "", ws, other)
	if err != nil {
		t.Fatal(err)
	}
	if q.Public().Digest() == p.Public().Digest() {
		t.Fatal("another contract with the same answers has the same digest")
	}
	_, err = NewHost(sent.Dependencies(upProbe{}, testImages())).Boot(context.Background(), BootRequest{Docker: Scope{Client: &recordingClient{}}, Name: "apptown-preview-change-1",
		StackID: "pvsnap-change-1", Workspace: ws, Slug: "app", Deployment: "preview", Probe: time.Second, Scratch: t.TempDir()}, other)
	if err == nil || AsBootError(err).Phase != PhaseSecrets {
		t.Fatalf("another contract booted with these values: %v", err)
	}
}

func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// normalised is a boot's commands without their creation times or
// configuration folder, each as its sorted words: environment flags come
// from maps, in no fixed order.
func normalised(calls []string) string {
	created := regexp.MustCompile(regexp.QuoteMeta(dockerlabel.Created) + `=\S+`)
	// A disk read names its container afresh each time and stamps its
	// deadline from the clock.
	probe := regexp.MustCompile(`apptown-result-[0-9a-f]+|apptown\.deadline=\d+`)
	config := regexp.MustCompile(`^--config \S+ `)
	var out []string
	for _, call := range calls {
		call = config.ReplaceAllString(call, "")
		if call == "context show" {
			continue // a prepared boot asks, for its own configuration, as a build does for its builder
		}
		words := strings.Fields(probe.ReplaceAllString(created.ReplaceAllString(call, dockerlabel.Created), "result"))
		sort.Strings(words)
		out = append(out, strings.Join(words, " "))
	}
	return strings.Join(out, "\n")
}
