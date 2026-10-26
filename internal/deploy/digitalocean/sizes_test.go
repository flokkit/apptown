package digitalocean

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
)

func sizedDriver(t *testing.T, api *dofake.API) *Driver {
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	push := func(_ *Driver, _ context.Context, _ *Client, _, _, _, _ string, _ func(string, ...any)) (string, error) {
		return "sha256:" + strings.Repeat("a", 64), nil
	}
	return NewDriver(Dependencies{Slug: "expense", Client: func(context.Context) (*Client, Config, error) {
		return &Client{Base: server.URL, HTTP: server.Client()}, Config{Region: "syd", Registry: "forge-test", InstanceSize: "configured-size"}, nil
	}}, Options{PushImage: push})
}

// A part that declares resources runs on the cheapest current App Platform
// size that covers them, read from the platform's own list; a part that
// declares none keeps the configured size.
func TestAnAppPlatformPartRunsOnTheCheapestSizeThatCoversIt(t *testing.T) {
	t.Parallel()
	api := &dofake.API{}
	c, err := appcontract.Parse([]byte(`app:
  web: {image: app:1, public: true, resources: {memory: 512M}}
  worker: {image: app:1, resources: {memory: 1G, cpu: 2}}
  cron: {image: app:1}
`))
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	art, err := sizedDriver(t, api).Render(context.Background(), deploy.Placement{Name: "cloud"},
		deploy.Source{Images: map[string]string{"web": "app:1", "worker": "app:1", "cron": "app:1"}, Contract: c, Revision: "selected"},
		deploy.NeedBindings{}, scopedRenderSecrets{}, func(f string, a ...any) { said = append(said, f) })
	if err != nil {
		t.Fatal(err)
	}
	var spec AppSpec
	if err := json.Unmarshal(renderedSpecBody(t, art), &spec); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, comp := range append(spec.Services, spec.Workers...) {
		got[comp.Name] = comp.InstanceSizeSlug
	}
	// 512M: the deprecated basic-xxs costs the same and is never chosen.
	want := map[string]string{"web": "apps-s-1vcpu-0.5gb", "worker": "apps-s-2vcpu-4gb", "cron": "configured-size"}
	for part, size := range want {
		if got[part] != size {
			t.Errorf("%s runs on %q, want %q (all: %v)", part, got[part], size, got)
		}
	}
	if len(said) == 0 {
		t.Errorf("the release log does not say which size a part's resources became")
	}
}

// A part whose declared resources no size covers, or a size list the
// AppTown cannot read, stops the release before anything is made at the
// platform.
func TestAnAppPlatformPartNoSizeCoversIsRefusedBeforeAnyEffect(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		app      string
		fail     bool
		want     string
		terminal bool
	}{
		"no size covers it":       {app: "app:\n  web: {image: app:1, public: true, resources: {memory: 64G}}\n", want: "no App Platform instance size has 64G", terminal: true},
		"the list cannot be read": {app: "app:\n  web: {image: app:1, public: true, resources: {memory: 1G}}\n", fail: true, want: "reading App Platform's instance sizes"},
		// The managed Postgres is made at the integration's database size:
		// a per-deployment size is not offered, and is refused by name.
		"a named local size":     {app: "app: {web: {image: app:1, public: true}}\nneeds: {mariadb: {size: small}}\n", want: "App Platform does not support catalogue sizes", terminal: true},
		"a need declares a size": {app: "app:\n  web: {image: app:1, public: true}\nneeds:\n  postgres: {resources: {memory: 2G}}\n", want: "postgres declares resources, but App Platform makes its managed Postgres at the integration's database size", terminal: true},
	} {
		t.Run(name, func(t *testing.T) {
			api := &dofake.API{}
			if tc.fail {
				api.Fail("GET", "/v2/apps/tiers/instance_sizes", 503)
			}
			c, err := appcontract.Parse([]byte(tc.app))
			if err != nil {
				t.Fatal(err)
			}
			_, err = sizedDriver(t, api).Render(context.Background(), deploy.Placement{Name: "cloud"},
				deploy.Source{Images: map[string]string{"web": "app:1"}, Contract: c, Revision: "selected"},
				deploy.NeedBindings{}, scopedRenderSecrets{}, func(string, ...any) {})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error naming %q", err, tc.want)
			}
			if deploy.IsTerminal(err) != tc.terminal {
				t.Errorf("terminal %v, want %v: an uncovered part needs the contract changed; an unread list may be read later", deploy.IsTerminal(err), tc.terminal)
			}
			for _, r := range api.Snapshot().Requests {
				if r.Method != "GET" {
					t.Errorf("%s %s happened before the refusal", r.Method, r.Path)
				}
			}
		})
	}
}

// A size App Platform marks for deprecation is never chosen, however
// cheap: a new app on it would be moved off it.
func TestADeprecatedSizeIsNeverChosen(t *testing.T) {
	t.Parallel()
	sizes := []InstanceSize{
		{Slug: "old-cheap", CPUs: 1, MemoryBytes: 1 << 30, USDPerMonth: 1, Deprecated: true},
		{Slug: "current", CPUs: 1, MemoryBytes: 1 << 30, USDPerMonth: 10},
	}
	got, err := SizeFor(sizes, appcontract.Resources{MemoryBytes: 512 << 20})
	if err != nil || got.Slug != "current" {
		t.Errorf("SizeFor chose %q (%v), want current", got.Slug, err)
	}
}
