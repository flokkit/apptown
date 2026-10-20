package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
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
	raw, err := h.generateDevCompose(t.Context(), c)
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
