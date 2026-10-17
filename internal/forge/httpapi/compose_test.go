package httpapi

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
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

// A static site with a build is the forge's to build: `forge dev up`
// refuses it with words instead of rendering a service that cannot serve.
func TestDevComposeRefusesABuiltStaticSiteWithWords(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web: { static: { build: ., output: dist }, public: true }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configurationFixture(t).generateDevCompose(t.Context(), c); err == nil || !strings.Contains(err.Error(), "does not run: run its own dev server") {
		t.Fatalf("dev compose of a built static site: %v", err)
	}
}
