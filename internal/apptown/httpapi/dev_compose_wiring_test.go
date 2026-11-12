package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"gopkg.in/yaml.v3"
)

const reviewWiredDev = `version: 2
app:
  web:
    image: app:1
    public: true
    environment: { CACHE_URL: "${needs.redis.REDIS_URL}" }
init:
  - name: migrate
    image: app:1
    environment: { CACHE_URL: "${needs.redis.REDIS_URL}" }
needs:
  redis: {}
`

func TestContainerDevResolvesNeedReferences(t *testing.T) {
	h := configurationFixture(t)
	c, err := appcontract.Parse([]byte(reviewWiredDev))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := h.generateDevCompose(t.Context(), c, "", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(raw), &compose); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "migrate"} {
		env := compose.Services[name].Environment
		if got, want := deploy.ComposeRead(env["CACHE_URL"]), deploy.ComposeRead(env["REDIS_URL"]); got != want {
			t.Errorf("%s CACHE_URL = %q, want %q", name, got, want)
		}
	}
}

func TestContainerModeAllowsDifferentConsumerEnvironments(t *testing.T) {
	h := configurationFixture(t)
	doc := reviewWiredDev
	// The init step and web run in separate containers; only process mode
	// needs to refuse their different values for one variable.
	before := "environment: { CACHE_URL: \"${needs.redis.REDIS_URL}\" }"
	pos := strings.LastIndex(doc, before)
	doc = doc[:pos] + strings.Replace(doc[pos:], before, "environment: { CACHE_URL: \"${needs.redis.REDIS_URL}/1\" }", 1)
	body, err := json.Marshal(map[string]string{"appYaml": doc, "mode": "full"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.DevCompose(w, httptest.NewRequest("POST", "/api/v1/dev-compose", strings.NewReader(string(body))))
	if w.Code != 200 {
		t.Fatalf("container mode refused independent environments: status %d: %s", w.Code, w.Body.String())
	}
}

// The shipped WordPress boot receives its install password only in install,
// while keys shared by web and install have one fresh value per request.
func TestWordPressDevDeliversScopedFreshSecrets(t *testing.T) {
	t.Parallel()
	h := configurationFixture(t)
	app, err := os.ReadFile("../../../stock/seeds/wordpress/.apptown/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile("../../../stock/seeds/wordpress/secretspec.toml")
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	for range 2 {
		body, err := json.Marshal(map[string]string{"appYaml": string(app), "secretSpec": string(spec), "mode": "full"})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.DevCompose(w, httptest.NewRequest("POST", "/api/v1/dev-compose", bytes.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("dev compose: %d %s", w.Code, w.Body.String())
		}
		var out DevComposition
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var compose struct {
			Services map[string]struct {
				Environment map[string]string `yaml:"environment"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(out.Compose), &compose); err != nil {
			t.Fatal(err)
		}
		web, install := compose.Services["web"].Environment, compose.Services["install"].Environment
		if install["WP_ADMIN_PASSWORD"] == "" {
			t.Fatal("install has no admin password")
		}
		if _, ok := web["WP_ADMIN_PASSWORD"]; ok {
			t.Fatal("admin password reached web")
		}
		if web["AUTH_KEY"] == "" || web["AUTH_KEY"] != install["AUTH_KEY"] {
			t.Fatal("web and install need the same generated key")
		}
		if previous == install["WP_ADMIN_PASSWORD"] {
			t.Fatal("password reused across requests")
		}
		previous = install["WP_ADMIN_PASSWORD"]
		if !reflect.DeepEqual(out.Withheld, []string{"WP_HOME"}) {
			t.Fatalf("withheld: %v", out.Withheld)
		}
	}
}

// apptown dev up gives the app its address as every deployment does: an app
// made from the WordPress seed that requires an explicit address exited in
// its install step without it (demo complaint #39). The address is where the public part's
// published port answers for the person: their session's Docker host.
func TestDevUpGivesTheAppItsLocalAddress(t *testing.T) {
	t.Parallel()
	h := configurationFixture(t)
	app, err := os.ReadFile("../../../stock/seeds/wordpress/.apptown/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{"": "http://localhost:8080", "dind": "http://dind:8080"} {
		body, err := json.Marshal(map[string]string{"appYaml": string(app), "mode": "full", "host": host})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.DevCompose(w, httptest.NewRequest("POST", "/api/v1/dev-compose", bytes.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("dev compose: %d %s", w.Code, w.Body.String())
		}
		var out DevComposition
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var compose struct {
			Services map[string]struct {
				Environment map[string]string `yaml:"environment"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(out.Compose), &compose); err != nil {
			t.Fatal(err)
		}
		web, install := compose.Services["web"].Environment, compose.Services["install"].Environment
		if got := deploy.ComposeRead(install["APPTOWN_APP_URL"]); got != want {
			t.Errorf("host %q: install APPTOWN_APP_URL = %q, want %q", host, got, want)
		}
		if got := deploy.ComposeRead(web["APPTOWN_APP_URL"]); got != want {
			t.Errorf("host %q: web APPTOWN_APP_URL = %q, want %q", host, got, want)
		}
		if got := deploy.ComposeRead(web["APPTOWN_ORIGINS"]); got != want {
			t.Errorf("host %q: web APPTOWN_ORIGINS = %q, want %q", host, got, want)
		}
	}
}
