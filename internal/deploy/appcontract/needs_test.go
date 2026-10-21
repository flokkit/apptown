package appcontract

import (
	"strings"
	"testing"
)

// Native needs preserve per-dependency parameters.
func TestNativeNeedsParameters(t *testing.T) {
	t.Parallel()
	c, err := Parse([]byte(`
needs:
  postgres: {version: "16", database: sourdough}
  s3: {}
app:
  web:
    public: true
    build: .
    ports: ["8080"]
`))
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic order (sorted): postgres, s3.
	if len(c.Needs) != 2 || c.Needs[0] != "postgres" || c.Needs[1] != "s3" {
		t.Fatalf("map-form needs not parsed in order: %+v", c.Needs)
	}
	if c.NeedParams["postgres"]["version"] != "16" || c.NeedParams["postgres"]["database"] != "sourdough" {
		t.Fatalf("postgres params not captured: %+v", c.NeedParams)
	}
	if _, ok := c.NeedParams["s3"]; ok {
		t.Fatalf("empty params should not populate NeedParams: %+v", c.NeedParams)
	}
}

// A need's parameters are single values: one that is a list or a map
// refuses the contract naming the field, rather than being dropped for the
// catalog's default. The old properties: pass-through is refused: a need
// takes only the parameters its kind declares (dependency.RefusesDeclaration).
func TestNeedParametersAreSingleValues(t *testing.T) {
	t.Parallel()
	const app = "app:\n  web:\n    public: true\n    build: .\nneeds:\n  postgres:\n"
	c, err := Parse([]byte(app + "    database: sourdough\n    version: 17\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p := c.NeedParams["postgres"]; p["database"] != "sourdough" || p["version"] != "17" {
		t.Fatalf("params: %+v", c.NeedParams)
	}
	for need, want := range map[string]string{
		"    database: [sourdough]\n":           "needs: postgres: database: a parameter is a single value, not a list",
		"    database: {name: sourdough}\n":     "needs: postgres: database: a parameter is a single value, not a map",
		"    database:\n":                       "needs: postgres: database: a parameter is a single value, not empty",
		"    properties: {shared_buffers: 1}\n": ".apptown/app.yaml: needs: postgres: properties: is not read; write each parameter the need takes directly under needs.postgres",
	} {
		if _, err := Parse([]byte(app + need)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v (want %q)", need, err, want)
		}
	}
}

// A need declared disposable (a cache) is never copied and starts empty;
// only true and false are read, so a misspelling refuses the contract
// rather than silently protecting or not protecting the data.
func TestADisposableNeedIsSaidInOneOfTwoWords(t *testing.T) {
	t.Parallel()
	const app = "app:\n  web:\n    public: true\n    build: .\nneeds:\n  postgres: {}\n  redis:\n"
	c, err := Parse([]byte(app + "    disposable: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Disposable("redis") || c.Disposable("postgres") {
		t.Fatalf("disposable: redis %v postgres %v", c.Disposable("redis"), c.Disposable("postgres"))
	}
	if c, err := Parse([]byte(app + "    disposable: false\n")); err != nil || c.Disposable("redis") {
		t.Fatalf("disposable false: %v %v", err, c)
	}
	if _, err := Parse([]byte(app + "    disposable: yes please\n")); err == nil || !strings.Contains(err.Error(), "needs: redis: disposable is true or false") {
		t.Fatalf("misspelt: %v", err)
	}
}
