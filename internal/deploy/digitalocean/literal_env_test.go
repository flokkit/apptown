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

// App Platform replaces ${NAME} in any environment value, secret or not,
// when NAME is one of its own variables, and has no escape. A declared
// value or secret that holds ${…} is refused at preflight and at render,
// before any request to DigitalOcean, naming the variable and the part and
// never the value. The driver's own references still go through.
func TestAppPlatformRefusesAValueItWouldRewriteBeforeAnyEffect(t *testing.T) {
	t.Parallel()
	const rewritten = "a${APP_URL}b"
	release := func(webEnv, initEnv string) deploy.Source {
		c := &appcontract.Contract{
			Parts: []appcontract.Part{{Name: "web", Public: true, Image: "app:1", Port: 8080, Env: map[string]string{"GREETING": webEnv}}},
			Init:  []appcontract.InitContainer{{Name: "migrate", Image: "app:1", Env: map[string]string{"MODE": initEnv}}},
		}
		return deploy.Source{Images: map[string]string{"web": "app:1"}, Init: map[string]string{"migrate": "app:1"}, Contract: c, Revision: "r1"}
	}
	bindings := deploy.NeedBindings{"postgres": {Env: map[string]string{"DATABASE_URL": "${postgres.DATABASE_URL}"}}}
	driver := func(t *testing.T) (*Driver, *dofake.API, *int) {
		api := &dofake.API{}
		api.SeedRegistry("forge-test", "basic", 5)
		server := httptest.NewServer(api.Handler())
		t.Cleanup(server.Close)
		pushes := 0
		push := func(_ *Driver, _ context.Context, _ *Client, _, _, _, _ string, _ func(string, ...any)) (string, error) {
			pushes++
			return "sha256:" + strings.Repeat("a", 64), nil
		}
		d := NewDriver(Dependencies{Slug: "expense", Client: func(context.Context) (*Client, Config, error) {
			return &Client{Base: server.URL, HTTP: server.Client()}, Config{Region: "syd", Registry: "forge-test"}, nil
		}}, Options{PushImage: push})
		return d, api, &pushes
	}
	for _, tc := range []struct {
		name, key, consumer string
		src                 deploy.Source
		secrets             scopedRenderSecrets
	}{
		{"declared environment", "GREETING", "web", release(rewritten, "plain"), nil},
		{"an init step's environment", "MODE", "init-migrate", release("plain", rewritten), nil},
		{"a part's secret", "SESSION_KEY", "web", release("plain", "plain"), scopedRenderSecrets{"web": {"SESSION_KEY": rewritten}}},
		{"an init step's secret", "MIGRATE_KEY", "init-migrate", release("plain", "plain"), scopedRenderSecrets{"init-migrate": {"MIGRATE_KEY": "x${nosuch.FOO}y"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, api, pushes := driver(t)
			pre := d.Preflight(context.Background(), deploy.Placement{Name: "cloud"}, tc.src, tc.secrets, func(string, ...any) {})
			_, render := d.Render(context.Background(), deploy.Placement{Name: "cloud"}, tc.src, bindings, tc.secrets, func(string, ...any) {})
			for what, err := range map[string]error{"preflight": pre, "render": render} {
				if err == nil || !deploy.IsTerminal(err) {
					t.Fatalf("%s did not refuse: %v", what, err)
				}
				msg := err.Error()
				if !strings.Contains(msg, tc.key) || !strings.Contains(msg, tc.consumer) || strings.Contains(msg, "APP_URL") || strings.Contains(msg, "nosuch") {
					t.Fatalf("%s: the refusal must name %s and %s and not the value: %s", what, tc.key, tc.consumer, msg)
				}
			}
			if n := len(api.Snapshot().Requests); n != 0 || *pushes != 0 {
				t.Fatalf("a refused release reached DigitalOcean: %d requests, %d pushes", n, *pushes)
			}
		})
	}
	// Values the platform leaves alone go through, and so do the driver's
	// own references to the platform's variables.
	t.Run("literal values and AppTown's references", func(t *testing.T) {
		t.Parallel()
		d, _, _ := driver(t)
		src, secrets := release("pa$word", "a${b"), scopedRenderSecrets{"web": {"HASH": "$2a$10$abc", "BRACE": "}${"}}
		if err := d.Preflight(context.Background(), deploy.Placement{Name: "cloud"}, src, secrets, func(string, ...any) {}); err != nil {
			t.Fatal(err)
		}
		art, err := d.Render(context.Background(), deploy.Placement{Name: "cloud"}, src, bindings, secrets, func(string, ...any) {})
		if err != nil {
			t.Fatal(err)
		}
		var spec AppSpec
		if err := json.Unmarshal(art.Body, &spec); err != nil {
			t.Fatal(err)
		}
		env := map[string]string{}
		for _, e := range spec.Services[0].Envs {
			env[e.Key] = e.Value
		}
		if env["GREETING"] != "pa$word" || env["HASH"] != "$2a$10$abc" || env["BRACE"] != "}${" || env["DATABASE_URL"] != "${postgres.DATABASE_URL}" || env["APPTOWN_ORIGINS"] != appURL {
			t.Fatalf("values changed: %v", env)
		}
	})
	// A version-2 need reference is AppTown's, not the app's literal text:
	// it goes through preflight and render as the platform's own reference,
	// and a value that reads a need credential is sent as a secret. Its
	// escape, $${needs., delivers literal ${…}, which the platform would
	// rewrite, so that is refused like any other.
	t.Run("need references", func(t *testing.T) {
		t.Parallel()
		d, _, _ := driver(t)
		wired := release("${needs.postgres.DATABASE_URL}", "${needs.postgres.DATABASE_URL}")
		wired.Contract.Version = 2
		// database.go's actual binding puts native credential references in Env.
		creds := deploy.NeedBindings{"postgres": {Env: map[string]string{"DATABASE_URL": "${postgres.DATABASE_URL}", "PGPASSWORD": "${postgres.PASSWORD}"}}}
		wired.Contract.Parts[0].Env["PASS"] = "${needs.postgres.PGPASSWORD}"
		none := scopedRenderSecrets{}
		if err := d.Preflight(context.Background(), deploy.Placement{Name: "cloud"}, wired, none, func(string, ...any) {}); err != nil {
			t.Fatal(err)
		}
		art, err := d.Render(context.Background(), deploy.Placement{Name: "cloud"}, wired, creds, none, func(string, ...any) {})
		if err != nil {
			t.Fatal(err)
		}
		var spec AppSpec
		if err := json.Unmarshal(art.Body, &spec); err != nil {
			t.Fatal(err)
		}
		env, kind := map[string]string{}, map[string]string{}
		for _, e := range spec.Services[0].Envs {
			env[e.Key], kind[e.Key] = e.Value, e.Type
		}
		if env["GREETING"] != "${postgres.DATABASE_URL}" || kind["GREETING"] != "SECRET" || kind["PASS"] != "SECRET" {
			t.Fatalf("wired values: %v %v", env, kind)
		}
		if job := spec.Jobs[0].Envs; len(job) == 0 {
			t.Fatal("the init step lost its environment")
		}
		escaped := release("$${needs.postgres.DATABASE_URL}", "plain")
		escaped.Contract.Version = 2
		if err := d.Preflight(context.Background(), deploy.Placement{Name: "cloud"}, escaped, none, func(string, ...any) {}); err == nil || !deploy.IsTerminal(err) {
			t.Fatalf("an escaped reference is literal ${…} and must be refused: %v", err)
		}
	})
}
