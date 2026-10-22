package appcontract_test

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

const publicWeb = "app:\n  web:\n    build: .\n    public: true\n"

// Each form below was accepted and then delivered differently from how it
// reads, ignored, or left for a target to collide on. Refused now, a 1.x
// release can give any of them a meaning without changing an app that
// wrote it. Each refusal names what to write instead.
func TestTheContractRefusesWhatItWouldNotUseAsWritten(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, yaml, says string }{
		// Environment values reach the app as written, or not at all.
		{"float value", publicWeb + "    environment: { PYTHON_VERSION: 3.10 }\n", "3.10 reads as a number"},
		{"null value", publicWeb + "    environment: { EMPTY: }\n", "has no value"},
		{"list value", publicWeb + "    environment: { L: [a, b] }\n", "a list is not a value"},
		{"map value", publicWeb + "    environment: { M: { a: b } }\n", "a map is not a value"},
		{"date value", publicWeb + "    environment: { D: 2024-01-01 }\n", "reads as a date"},
		{"hex integer", publicWeb + "    environment: { MODE: 0x10 }\n", "0x10 would not reach the app as written"},
		{"capital boolean", publicWeb + "    environment: { ON: True }\n", "True would not reach the app as written"},
		{"name with a space", publicWeb + "    environment: { \"MY VAR\": x }\n", "is not an environment variable name"},
		{"init value", publicWeb + "init:\n  - { name: migrate, image: m, environment: { V: 1.0 } }\n", "init step \"migrate\": environment: V"},
		{"profile value", publicWeb + "profiles:\n  big:\n    web: { environment: { V: ~ } }\n", "profiles: big: web: environment: V"},
		// AppTown's names.
		{"reserved prefix", publicWeb + "    environment: { APPTOWN_FEATURE: x }\n", "APPTOWN_FEATURE is AppTown's"},
		{"reserved prefix in init", publicWeb + "init:\n  - { name: migrate, image: m, environment: { APPTOWN_X: x } }\n", "APPTOWN_X is AppTown's"},
		{"reserved prefix in profile", publicWeb + "profiles:\n  big:\n    web: { environment: { APPTOWN_X: x } }\n", "APPTOWN_X is AppTown's"},
		{"generated secret", publicWeb + "secrets:\n  generate: { APPTOWN_KEY: deployment }\n", "APPTOWN_KEY is AppTown's"},
		// Names a target carries.
		{"upper-case part", "app:\n  Web: { build: ., public: true }\n", "app part \"Web\": a name is 2 to 32"},
		{"one-letter part", "app:\n  w: { build: ., public: true }\n", "app part \"w\": a name is 2 to 32"},
		{"part named app", "app:\n  app: { build: ., public: true }\n", "app is the name a target gives the public part"},
		{"part named init-", publicWeb + "  init-db: { build: . }\n", "names starting init- or apptown-"},
		{"part named apptown-", publicWeb + "  apptown-gate: { build: . }\n", "names starting init- or apptown-"},
		{"unnamed init step", publicWeb + "init:\n  - { image: m }\n", "init step 1 has no name"},
		{"init named like a part", "app:\n  migrate: { image: app, public: true }\ninit:\n  - { name: migrate, image: m }\n", "is both a part under app: and an init step"},
		{"init named like a need", publicWeb + "init:\n  - { name: postgres, image: m }\nneeds:\n  postgres: {}\n", "is both an init step and a need"},
		{"two init steps, one name", publicWeb + "init:\n  - { name: migrate, image: m }\n  - { name: migrate, image: n }\n", "two init steps are named"},
		{"init named apptown-", publicWeb + "init:\n  - { name: apptown-gate, image: m }\n", "names starting init- or apptown-"},
		// Static sites: AppTown's file server reads none of these.
		{"static environment", "app:\n  site: { static: public, public: true, environment: { API: x } }\n", "environment: is not used by a static site"},
		{"static command", "app:\n  site: { static: public, public: true, command: serve }\n", "command: is not used by a static site"},
		{"static dataDir", "app:\n  site: { static: public, public: true, dataDir: /data }\n", "dataDir: is not used by a static site"},
		{"static healthcheck", "app:\n  web: { build: ., public: true }\n  docs: { static: docs, healthcheck: \"true\" }\n", "healthcheck: is not used by a static site"},
		{"static profile environment", "app:\n  site: { static: public, public: true }\nprofiles:\n  big:\n    site: { environment: { API: x } }\n", "a static site has no environment"},
		// One port, published where AppTown chooses.
		{"two ports", "app:\n  web: { build: ., public: true, ports: [\"8080\", \"9090\"] }\n", "listens on one port"},
		{"host port", "app:\n  web: { build: ., public: true, ports: [\"3000:8080\"] }\n", "names a host port"},
		// The declaration is the project's own file.
		{"spec outside", publicWeb + "secrets: { spec: ../secretspec.toml }\n", "must be a file inside the project"},
		{"spec absolute", publicWeb + "secrets: { spec: /etc/secretspec.toml }\n", "must be a file inside the project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := appcontract.Parse([]byte(tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("want a refusal saying %q, got %v", tc.says, err)
			}
		})
	}
}

// What is accepted reaches the app exactly as it did before values were
// checked: text as written (quoted numbers keep their zeros), and whole
// numbers and booleans in the spelling fmt.Sprint always gave them.
func TestAcceptedEnvironmentValuesReachTheAppAsTheyAlwaysDid(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte(`app:
  web:
    build: .
    public: true
    ports: ["8080/tcp"]
    environment:
      PYTHON_VERSION: "3.10"
      WORKERS: 4
      NEGATIVE: -2
      DEBUG: true
      OFF: false
      EMPTY: ""
      lower_case: ok
      URL: https://example.org/a?b=c
      ANCHOR: &a shared
      ALIAS: *a
init:
  - { name: migrate, image: m, environment: { PORT: "5432", RETRIES: 3 } }
profiles:
  big:
    web: { environment: { WORKERS: 8 } }
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"PYTHON_VERSION": "3.10", "WORKERS": "4", "NEGATIVE": "-2", "DEBUG": "true", "OFF": "false", "EMPTY": "",
		"lower_case": "ok", "URL": "https://example.org/a?b=c", "ANCHOR": "shared", "ALIAS": "shared"}
	got := c.PublicPart().Env
	if len(got) != len(want) {
		t.Fatalf("environment: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	if c.PublicPart().Port != 8080 {
		t.Errorf("port: %d", c.PublicPart().Port)
	}
	// An init step is given no PORT by AppTown, so it may set its own.
	if env := c.Init[0].Env; env["PORT"] != "5432" || env["RETRIES"] != "3" {
		t.Errorf("init environment: %v", env)
	}
	if env := c.Profiles["big"]["web"].Env; env["WORKERS"] != "8" {
		t.Errorf("profile environment: %v", env)
	}
	// A static site keeps what its file server does read: readiness, limits
	// and its one port.
	if _, err := appcontract.Parse([]byte("app:\n  game: { static: ., public: true, ports: [\"80\"], health: { readiness: { http: /healthz } }, resources: { memory: 128Mi } }\nversion: 2\n")); err != nil {
		t.Errorf("a static site's readiness, resources and port: %v", err)
	}
}
