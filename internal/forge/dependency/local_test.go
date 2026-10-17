package dependency

import (
	"context"
	"flokkit.io/forge/internal/forge/dependency/internal/dependencysql"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/stock"
)

func stockKind(t *testing.T, name string) *Kind {
	t.Helper()
	raw, err := fs.ReadFile(stock.FS, "dependencies/"+name+".yaml")
	if err != nil {
		t.Fatal(err)
	}
	k, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestLocalResolution(t *testing.T) {
	t.Parallel()
	if k := stockKind(t, "errors"); k.Register == nil || k.Register.Connector != "glitchtip" || k.ResolveLocal(nil, "", nil) != nil {
		t.Fatalf("register-only kinds have no local service: %+v", k)
	}
	k := stockKind(t, "postgres")
	for _, tc := range []struct {
		name, host, image, url string
		params                 map[string]string
	}{
		{name: "defaults", image: "postgres:16-alpine", url: "postgres://app:app@postgres:5432/app?sslmode=disable"},
		{name: "overrides", params: map[string]string{"version": "15", "database": "sourdough", "user": "sourdough"},
			image: "postgres:15-alpine", url: "postgres://sourdough:app@postgres:5432/sourdough?sslmode=disable"},
		// The password is the forge's: a parameter naming one (refused
		// before this, RefusesNeeds) never reaches the service.
		{name: "password parameter", params: map[string]string{"password": "chosen"},
			image: "postgres:16-alpine", url: "postgres://app:app@postgres:5432/app?sslmode=disable"},
		{name: "process host", host: "localhost", params: map[string]string{"host": "ignored"},
			image: "postgres:16-alpine", url: "postgres://app:app@localhost:5432/app?sslmode=disable"},
		{name: "conformance host", host: "check.forge.local", image: "postgres:16-alpine", url: "postgres://app:app@check.forge.local:5432/app?sslmode=disable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := k.ResolveLocal(tc.params, tc.host, nil)
			if s.Name != "postgres" || s.Port != 5432 || s.Image != tc.image || s.Inject["DATABASE_URL"] != tc.url {
				t.Fatalf("resolved emulator: %+v", s)
			}
			if tc.name == "overrides" && (s.Env["POSTGRES_DB"] != "sourdough" || s.Env["POSTGRES_PASSWORD"] != "app") {
				t.Fatalf("emulator env: %+v", s.Env)
			}
		})
	}
}

func TestCatalogLocalUnreadableKinds(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	c := NewCatalog(db, nil)
	if _, err := c.Save(context.Background(), redis, "tester"); err != nil {
		t.Fatal(err)
	}
	// Deliberately corrupt a catalog declaration to exercise the read boundary.
	if _, err := dependencysql.New(db.SQL()).SaveCustomDependencyKind(context.Background(), dependencysql.SaveCustomDependencyKindParams{Capability: "redis", SpecYaml: "capability: [", Origin: "custom", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	assertUnknown := func() {
		t.Helper()
		p := c.ResolveLocal(t.Context(), []string{"redis"}, nil, "")
		if !reflect.DeepEqual(p.Unknown, []string{"redis"}) || len(p.Services)+len(p.Env) != 0 {
			t.Fatalf("unreadable kind must not produce partial service values: %+v", p)
		}
	}
	assertUnknown()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertUnknown()
}

func TestLocalValuesAreLiteralAndIsolated(t *testing.T) {
	t.Parallel()
	k, err := Parse(`capability: sample
defaults: {version: "16", database: app}
local:
  image: "sample:{version}"
  command: ["run", "{database}"]
  health: "check {database}"
  env: {DB: "{database}"}
  inject: {URL: "{host}/{database}/{unknown}"}
`)
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"database": "{version}"}
	s := k.ResolveLocal(params, "localhost", nil)
	if s.Env["DB"] != "{version}" || s.Command[1] != "{version}" || s.Health != "check {version}" || s.Inject["URL"] != "localhost/{version}/{unknown}" {
		t.Fatalf("parameter values must not be recursively expanded: %+v", s)
	}
	s.Env["DB"], s.Inject["URL"], s.Command[0] = "changed", "changed", "changed"
	next := k.ResolveLocal(nil, "", nil)
	if next.Env["DB"] != "app" || next.Command[0] != "run" || next.Inject["URL"] != "sample/app/{unknown}" || k.Defaults["database"] != "app" || params["database"] != "{version}" {
		t.Fatalf("resolution mutated its inputs or another result: %+v", next)
	}
}

func TestCatalogLocalPlan(t *testing.T) {
	t.Parallel()
	c := NewCatalog(testDB(t), nil)
	if _, _, err := c.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	p := c.ResolveLocal(t.Context(), []string{"s3", "redis", "errors", "quantum-db"}, nil, "")
	if !reflect.DeepEqual(p.Unknown, []string{"quantum-db"}) || len(p.Services) != 2 {
		t.Fatalf("known emulators, register-only and unknown: %+v", p)
	}
	if !strings.HasPrefix(p.Services[0].Image, "versity/versitygw:v") || !slices.Equal(p.Services[0].Entrypoint, []string{"/bin/sh", "-c"}) || len(p.Services[0].Command) != 1 || !strings.Contains(p.Services[0].Command[0], "posix /data") || !strings.HasPrefix(p.Services[1].Image, "redis:") {
		t.Fatalf("stock emulators: %+v", p.Services)
	}
	if p.Env["AWS_ENDPOINT_URL_S3"] != "http://s3:9000" || p.Env["REDIS_URL"] != "redis://:forge-redis@redis:6379" {
		t.Fatalf("combined connection environment: %+v", p.Env)
	}
	register := c.ResolveLocal(t.Context(), []string{"errors"}, nil, "localhost")
	if len(register.Services)+len(register.Env)+len(register.Unknown) != 0 {
		t.Fatalf("register-only kinds have no local fulfilment: %+v", register)
	}
	if _, err := c.Save(context.Background(), "capability: copy\nlocal:\n  image: sample\n  inject: {AWS_ENDPOINT_URL_S3: replacement}\n", "tester"); err != nil {
		t.Fatal(err)
	}
	if got := c.ResolveLocal(t.Context(), []string{"s3", "copy"}, nil, "").Env["AWS_ENDPOINT_URL_S3"]; got != "replacement" {
		t.Fatalf("later declared need must win environment collision: %q", got)
	}
	if empty := c.ResolveLocal(t.Context(), nil, nil, ""); empty.Env == nil || len(empty.Services)+len(empty.Unknown) != 0 {
		t.Fatalf("empty requirements: %+v", empty)
	}
}
