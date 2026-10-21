package digitalocean

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/digitalocean/dofake"
)

type scopedRenderSecrets map[string]map[string]string

func (s scopedRenderSecrets) EnvFor(consumer string) map[string]string { return s[consumer] }

func TestDigitalOceanRendersCanonicalPortsAndScopedConnections(t *testing.T) {
	t.Parallel()
	api := &dofake.API{}
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	push := func(_ *Driver, _ context.Context, _ *Client, _, _, _, _ string, _ func(string, ...any)) (string, error) {
		return "sha256:" + strings.Repeat("a", 64), nil
	}
	driver := NewDriver(Dependencies{Slug: "expense", Client: func(context.Context) (*Client, Config, error) {
		return &Client{Base: server.URL, HTTP: server.Client()}, Config{Region: "syd", Registry: "forge-test"}, nil
	}}, Options{PushImage: push})
	c, err := appcontract.Parse([]byte(`app:
  web: {image: app:1, public: true, environment: {PUBLIC_ONLY: web, DATABASE_URL: wrong}}
  worker: {image: worker:1, environment: {WORKER_ONLY: worker, DATABASE_URL: wrong}}
init:
  - {name: migrate, image: migrate:1, environment: {DATABASE_URL: wrong}}
`))
	if err != nil {
		t.Fatal(err)
	}
	// The contract refuses APPTOWN_ORIGINS in environment; one stored from
	// before that refusal may carry it, and AppTown's value must still win.
	c.PublicPart().Env["APPTOWN_ORIGINS"] = "https://typed.example"
	before, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := driver.Render(context.Background(), deploy.Placement{Name: "cloud"}, deploy.Source{Images: map[string]string{"web": "app:1", "worker": "worker:1"}, Init: map[string]string{"migrate": "migrate:1"}, Contract: c, Revision: "selected"}, deploy.NeedBindings{"postgres": {Env: map[string]string{"DATABASE_URL": "resolved"}}}, scopedRenderSecrets{"worker": {"WORKER_SECRET": "scoped"}}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	var spec AppSpec
	if err := json.Unmarshal(artifact.Body, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Services) != 1 || spec.Services[0].HTTPPort != 8080 || len(spec.Workers) != 1 || len(spec.Jobs) != 1 {
		t.Fatalf("canonical port did not reach provider: %+v", spec)
	}
	for _, comp := range []Component{spec.Services[0], spec.Workers[0], spec.Jobs[0]} {
		env := map[string]string{}
		for _, v := range comp.Envs {
			env[v.Key] = v.Value
		}
		if env["DATABASE_URL"] != "resolved" {
			t.Fatalf("need connection overridden in %s", comp.Name)
		}
		if comp.Name != "web" && env["PUBLIC_ONLY"] != "" {
			t.Fatalf("public env leaked to %s", comp.Name)
		}
		// The public part is told where a browser reaches it, as on Docker:
		// the platform's URL for the app, over what the app typed.
		if (env["APPTOWN_ORIGINS"] == "${APP_URL}") != (comp.Name == "web") {
			t.Fatalf("APPTOWN_ORIGINS in %s: %q", comp.Name, env["APPTOWN_ORIGINS"])
		}
		if (env["WORKER_SECRET"] == "scoped") != (comp.Name == "worker") {
			t.Fatalf("secret scope changed in %s", comp.Name)
		}
	}
	after, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("provider rendering mutated declaration")
	}
}
