package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDevComposeHTTPDistinguishesHostNeedsFromFullApplication(t *testing.T) {
	t.Parallel()
	h := configurationFixture(t)
	source := `app:
  web: {build: ., command: ["./web"], ports: ["8080"], public: true}
  worker: {image: worker:1, command: ["./worker"]}
needs:
  postgres: {version: "16"}
`
	for _, mode := range []string{"", "full"} {
		raw, _ := json.Marshal(map[string]string{"appYaml": source, "mode": mode})
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(raw)))
		w := httptest.NewRecorder()
		h.DevCompose(w, r)
		var out struct {
			Compose, Procfile string
			Env               map[string]string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
			t.Fatalf("compose: %d %s %v", w.Code, w.Body.String(), err)
		}
		var compose struct {
			Services map[string]struct {
				Image       string
				Ports       []string
				Environment map[string]string
			}
		}
		if err := yaml.Unmarshal([]byte(out.Compose), &compose); err != nil {
			t.Fatal(err)
		}
		_, hasWeb := compose.Services["web"]
		if hasWeb != (mode == "full") || compose.Services["postgres"].Image != "postgres:16-alpine" || !strings.Contains(out.Env["DATABASE_URL"], "localhost:5432") || out.Procfile != "web: ./web\nworker: ./worker\n" {
			t.Fatalf("wrong local run plan for %q: %+v", mode, out)
		}
		if mode == "" && (len(compose.Services) != 1 || len(compose.Services["postgres"].Ports) != 1 || compose.Services["postgres"].Ports[0] != "5432:5432") {
			t.Fatalf("needs must be reachable from host: %s", out.Compose)
		}
		if mode == "full" && !strings.Contains(compose.Services["web"].Environment["DATABASE_URL"], "postgres:5432") {
			t.Fatalf("container needs address: %s", out.Compose)
		}
	}
	for _, body := range []string{`{`, `{"appYaml":"app: ["}`, `{"appYaml":"app: {web: {image: web:1, public: true}}"}`, `{"appYaml":"app: {web: {image: web:1, public: true}}\nneeds: {unknown: {}}"}`} {
		w := httptest.NewRecorder()
		h.DevCompose(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("invalid compose request: %d %s", w.Code, w.Body.String())
		}
	}
}

// A process on a machine whose Docker is another host (a chat's own daemon,
// DOCKER_HOST=tcp://dind:2375) reaches the needs where their ports are
// published: the needs' env names the host the caller gives, so the
// documented `apptown dev` then `. .apptown/dev.env` runs the app's tests
// there (complaint #94). A host that is not a plain name is refused.
func TestDevComposeEnvNamesTheHostTheNeedsAnswerOn(t *testing.T) {
	t.Parallel()
	h := configurationFixture(t)
	source := "app:\n  web: {build: ., ports: [\"8080\"], public: true}\nneeds:\n  postgres: {version: \"16\"}\n"
	for host, want := range map[string]string{"": "localhost:5432", "dind": "dind:5432"} {
		raw, _ := json.Marshal(map[string]string{"appYaml": source, "host": host})
		w := httptest.NewRecorder()
		h.DevCompose(w, httptest.NewRequest("POST", "/", strings.NewReader(string(raw))))
		var out struct{ Env map[string]string }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
			t.Fatalf("compose for host %q: %d %s", host, w.Code, w.Body.String())
		}
		if !strings.Contains(out.Env["DATABASE_URL"], "@"+want+"/") || out.Env["PGHOST"] != strings.TrimSuffix(want, ":5432") {
			t.Fatalf("host %q: DATABASE_URL %q PGHOST %q", host, out.Env["DATABASE_URL"], out.Env["PGHOST"])
		}
	}
	for _, bad := range []string{"dind:2375", "a/b", "-x", "a b"} {
		raw, _ := json.Marshal(map[string]string{"appYaml": source, "host": bad})
		w := httptest.NewRecorder()
		h.DevCompose(w, httptest.NewRequest("POST", "/", strings.NewReader(string(raw))))
		if w.Code != 400 {
			t.Fatalf("host %q accepted: %d", bad, w.Code)
		}
	}
}
