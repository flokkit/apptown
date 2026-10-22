package httpapi

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"gopkg.in/yaml.v3"
)

// The generated compose orders everything: needs healthy, init completed, then
// the app parts — the ECS dependsOn shape expressed in compose.
func TestGenerateDevComposeOrdering(t *testing.T) {
	t.Parallel()
	h := configurationFixture(t)

	c, err := appcontract.Parse([]byte(`
app:
  web: { build: ., ports: ["8080"], public: true }
init:
  - name: migrate
    build: .
    command: ["./migrate.sh"]
needs:
  postgres: { version: "16" }
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.generateDevCompose(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"migrate:",                       // the init service
		"restart: \"no\"",                // run-to-completion
		"service_completed_successfully", // web depends on init completing
		"service_healthy",                // web/init depend on postgres healthy
		"healthcheck:",                   // postgres carries a healthcheck
		"pg_isready",                     // ...the catalog one
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("generated compose missing %q:\n%s", want, out)
		}
	}
}

// The generator turns a contract into runnable compose: app parts + emulators.
func TestGenerateDevCompose(t *testing.T) {
	t.Parallel()
	h := configurationFixture(t)

	c, err := appcontract.Parse([]byte(`
app:
  web: { build: ., ports: ["8080"], public: true }
needs:
  postgres: { version: "16" }
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.generateDevCompose(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"services:", "web:", "postgres:", "image: postgres:16-alpine", "8080:8080", "DATABASE_URL"} {
		if !strings.Contains(out, want) {
			t.Fatalf("generated compose missing %q:\n%s", want, out)
		}
	}
}

func TestCanonicalPublicPortUsedByDevCompose(t *testing.T) {
	t.Parallel()
	h := configurationFixture(t)
	for _, tc := range []struct {
		source string
		port   int
	}{{"static: .", 80}, {"image: app:1", 8080}} {
		c, err := appcontract.Parse([]byte("app:\n  web: {" + tc.source + ", public: true}\n  worker: {image: worker:1}\n"))
		if err != nil {
			t.Fatal(err)
		}
		if c.PublicPart().Port != tc.port || c.Parts[1].Port != 0 {
			t.Fatalf("canonical ports: %+v", c.Parts)
		}
		raw, err := h.generateDevCompose(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		var compose struct {
			Services map[string]struct {
				Ports       []string
				Environment map[string]string
			}
		}
		if err := yaml.Unmarshal([]byte(raw), &compose); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(compose.Services["web"].Ports, []string{fmt.Sprintf("%d:%d", tc.port, tc.port)}) || len(compose.Services["worker"].Ports) != 0 {
			t.Fatalf("default port lost during rendering: %s", raw)
		}
	}
}

// A static site with a build runs under `apptown dev up` the way a deploy
// runs it (complaint #91): the build is listed for the CLI to export into a
// folder under .apptown/, and the part's service serves the build's output
// from that folder with the generated file server, probed on its port.
func TestDevComposeServesABuiltStaticSiteFromItsExportedBuild(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web: { static: { build: ., target: site, output: dist }, public: true, health: { readiness: { http: / } } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := configurationFixture(t).generateDevCompose(t.Context(), c)
	if err != nil {
		t.Fatalf("dev compose of a built static site: %v", err)
	}
	var compose struct {
		Services map[string]struct {
			Image       string         `yaml:"image"`
			Build       any            `yaml:"build"`
			Volumes     []string       `yaml:"volumes"`
			Healthcheck map[string]any `yaml:"healthcheck"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(raw), &compose); err != nil {
		t.Fatal(err)
	}
	web := compose.Services["web"]
	if web.Build != nil || !reflect.DeepEqual(web.Volumes, []string{"./.apptown/static/web/dist:/srv:ro"}) {
		t.Fatalf("the built site is not served from its export: %s", raw)
	}
	if hc := fmt.Sprint(web.Healthcheck["test"]); !strings.Contains(hc, ":80/") {
		t.Fatalf("the static site's probe does not use the file server's port 80: %s", hc)
	}
	want := []DevStaticBuild{{Part: "web", Context: ".", Target: "site", Output: "dist", Folder: ".apptown/static/web"}}
	if got := DevStaticBuilds(c); !reflect.DeepEqual(got, want) {
		t.Fatalf("builds = %+v, want %+v", got, want)
	}
}

// A value containing $ in the contract reaches the `apptown dev` services as
// it is written, as docker run -e gives it on a Docker host: a part's and
// an init step's environment and command, a part's healthcheck and data
// folder, and a need's environment, command, healthcheck and data folder.
// Compose interpolates $ in the file (the repo's .env included), so each $
// is written $$, and $$ is what Compose reads back as $.
func TestDevComposeKeepsEveryDollarLiteral(t *testing.T) {
	t.Parallel()
	h := configurationFixture(t)
	c, err := appcontract.Parse([]byte(`
app:
  web:
    build: .
    ports: ["8080"]
    public: true
    command: ["serve", "--key", "pa$word"]
    environment: { SECRET_KEY: "x${HOME}$2a$$y" }
    dataDir: /data/$tenant
  worker:
    build: .
    healthcheck: 'test "$PORT" = 8080'
init:
  - name: migrate
    build: .
    command: ["migrate", "pa$word"]
    environment: { SECRET_KEY: "x${HOME}$2a$$y" }
`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := h.generateDevCompose(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Command     []string          `yaml:"command"`
			Environment map[string]string `yaml:"environment"`
			Healthcheck struct {
				Test []string `yaml:"test"`
			} `yaml:"healthcheck"`
			Volumes []string `yaml:"volumes"`
		}
	}
	if err := yaml.Unmarshal([]byte(raw), &compose); err != nil {
		t.Fatal(err)
	}
	// A need's service, as both dev files render it.
	need := composeNeed(dependency.LocalService{Name: "s3", Image: "s3:1", Env: map[string]string{"ROOT_SECRET": "pa$word"},
		Entrypoint: []string{"/bin/sh", "-c"}, Command: []string{`exec gw "$ROOT"`}, Health: `test -n "$ROOT"`, Data: "/data/$x"})
	reads := func(v string) string { // Compose's rule: $$ is $; a lone $ interpolates
		if strings.Contains(strings.ReplaceAll(v, "$$", ""), "$") {
			return "(interpolated)"
		}
		return strings.ReplaceAll(v, "$$", "$")
	}
	web, worker, migrate := compose.Services["web"], compose.Services["worker"], compose.Services["migrate"]
	for _, c := range []struct{ what, written, want string }{
		{"web env", web.Environment["SECRET_KEY"], "x${HOME}$2a$$y"},
		{"web command", strings.Join(web.Command, " "), "serve --key pa$word"},
		{"worker healthcheck", strings.Join(worker.Healthcheck.Test, " "), `CMD-SHELL test "$PORT" = 8080`},
		{"migrate env", migrate.Environment["SECRET_KEY"], "x${HOME}$2a$$y"},
		{"migrate command", strings.Join(migrate.Command, " "), "migrate pa$word"},
		{"web data mount", strings.Join(web.Volumes, " "), "appdata-web:/data/$tenant"},
		{"need env", need["environment"].(map[string]string)["ROOT_SECRET"], "pa$word"},
		{"need command", strings.Join(need["command"].([]string), " "), `exec gw "$ROOT"`},
		{"need healthcheck", need["healthcheck"].(map[string]any)["test"].([]any)[1].(string), `test -n "$ROOT"`},
		{"need data", strings.Join(need["volumes"].([]string), " "), "/data/$x"},
	} {
		if got := reads(c.written); got != c.want {
			t.Errorf("%s: written %q, which Compose reads as %q; want %q\n%s", c.what, c.written, got, c.want, raw)
		}
	}
}

// A part's and an init step's build context and image select what the
// contract declares, whatever the Compose runner's environment or the
// repository's .env sets (review compose-dollar-code-2026-10-18, F1).
func TestDevComposeKeepsBuildContextsAndImagesLiteral(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte(`
app:
  web: {build: './service${FLAVOUR}', public: true}
  worker: {image: 'reg/worker:${TAG}'}
init:
  - name: migrate
    build: './migrate${FLAVOUR}'
  - name: seed
    image: 'reg/seed:${TAG}'
`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := configurationFixture(t).generateDevCompose(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Services map[string]struct {
			Build string `yaml:"build"`
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(raw), &file); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ what, written, want string }{
		{"web build", file.Services["web"].Build, "./service${FLAVOUR}"},
		{"migrate build", file.Services["migrate"].Build, "./migrate${FLAVOUR}"},
		{"worker image", file.Services["worker"].Image, "reg/worker:${TAG}"},
		{"seed image", file.Services["seed"].Image, "reg/seed:${TAG}"},
	} {
		if strings.Contains(strings.ReplaceAll(c.written, "$$", ""), "$") || strings.ReplaceAll(c.written, "$$", "$") != c.want {
			t.Errorf("%s: written %q; want %q as Compose reads it\n%s", c.what, c.written, c.want, raw)
		}
	}
}
