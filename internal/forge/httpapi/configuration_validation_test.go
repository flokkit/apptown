package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The public validator and local-compose door must refuse replication before
// reaching the catalogue or generating an execution plan. No catalogue is wired.
func TestConfigurationRefusesUnsupportedReplicasBeforeEffects(t *testing.T) {
	t.Parallel()
	h := Configuration{}
	for _, count := range []string{"1", "2"} {
		body, err := json.Marshal(map[string]string{"appYaml": "app:\n  web: {image: example/web:1, public: true, replicas: " + count + "}\n"})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/api/v1/config/validate", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		h.ConfigValidate(w, req)
		var result ContractUnparsed
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || result.OK != (count == "1") {
			t.Fatalf("replicas %s validation: %d %s", count, w.Code, w.Body.String())
		}
		if count == "1" {
			continue
		}
		if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "replicas: 2 is not supported") {
			t.Fatalf("unsupported replication must be explained: %+v", result)
		}
		req = httptest.NewRequest("POST", "/api/v1/dev-compose", strings.NewReader(string(body)))
		w = httptest.NewRecorder()
		h.DevCompose(w, req)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "replicas: 2 is not supported") {
			t.Fatalf("unsupported replication must not generate compose: %d %s", w.Code, w.Body.String())
		}
	}
}

// Configuration validation reports malformed contracts and secret declarations.
func TestConfigurationValidationHTTP(t *testing.T) {
	t.Parallel()
	h := configurationFixture(t)
	// config validate: a good contract passes, a bad need fails with a reason.
	post := func(path, body string) (int, string) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ConfigValidate(w, req)
		return w.Code, w.Body.String()
	}
	if c, b := post("/api/v1/config/validate", `{"appYaml":""}`); c != 400 {
		t.Fatalf("empty appYaml: %d %s", c, b)
	}
	if c, b := post("/api/v1/config/validate", `{"appYaml":"app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  nonsense: {}\n"}`); c != 200 || !strings.Contains(b, "nonsense") {
		t.Fatalf("a bad need must be reported: %d %s", c, b)
	}
	// A built worker runs wherever the app runs, so it earns no warning; a
	// declared seed, which nothing runs, says so instead of promising data.
	workerAndSeed := `{"appYaml":"app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    seed: ./load-fixtures\n  worker:\n    build: ./worker\n"}`
	if c, b := post("/api/v1/config/validate", workerAndSeed); c != 200 || strings.Contains(b, "only the public part") || !strings.Contains(b, "declares seed:, which AppTown does not run anywhere") {
		t.Fatalf("worker and seed warnings: %d %s", c, b)
	}
	// secretspec.toml is judged by boot's own parser, so a shape boot would
	// refuse fails validation first.
	good := `{"appYaml":"app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n","secretSpec":"[DJANGO_SUPERUSER_PASSWORD]\ngenerate = true\n"}`
	if c, b := post("/api/v1/config/validate", good); c != 200 || !strings.Contains(b, "revision") || strings.Contains(b, `"ok":true`) {
		t.Fatalf("a secretspec without [project] revision must be refused: %d %s", c, b)
	}
}
