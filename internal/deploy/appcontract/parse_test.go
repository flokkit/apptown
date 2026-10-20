package appcontract_test

import (
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
)

func TestAppYamlParse(t *testing.T) {
	c, err := appcontract.Parse([]byte(`
app:
  web:
    build: .
    ports: ["8080"]
    public: true
    dataDir: /data
    environment: { LOG_LEVEL: info }
  worker:
    build: .
    command: ["node", "worker.js"]
needs:
  postgres:
    version: "16"
    properties:
      shared_buffers: 256MB
  s3: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(c.Parts))
	}
	// Public part projected onto the runtime fields.
	if c.PublicPart().Build != "." || c.PublicPart().Port != 8080 || c.PublicPart().DataDir != "/data" {
		t.Fatalf("incorrect public declaration: build=%q port=%d dataDir=%q", c.PublicPart().Build, c.PublicPart().Port, c.PublicPart().DataDir)
	}
	// needs: sorted, params from typed fields + properties escape hatch.
	if len(c.Needs) != 2 || c.Needs[0] != "postgres" || c.Needs[1] != "s3" {
		t.Fatalf("needs not parsed: %+v", c.Needs)
	}
	if c.NeedParams["postgres"]["version"] != "16" || c.NeedParams["postgres"]["shared_buffers"] != "256MB" {
		t.Fatalf("postgres params (typed + properties) wrong: %+v", c.NeedParams["postgres"])
	}
}

func TestAppYamlValidation(t *testing.T) {
	// Two public parts → error.
	if _, err := appcontract.Parse([]byte("app:\n  a: { build: ., public: true }\n  b: { build: ., public: true }\n")); err == nil {
		t.Fatal("two public parts must fail")
	}
	// No public part → error.
	if _, err := appcontract.Parse([]byte("app:\n  a: { build: . }\n")); err == nil {
		t.Fatal("zero public parts must fail")
	}
	// Two run sources on one part → error.
	if _, err := appcontract.Parse([]byte("app:\n  a: { build: ., image: x, public: true }\n")); err == nil {
		t.Fatal("build+image on one part must fail")
	}
}

// init containers + per-part healthcheck parse into the contract.
func TestAppYamlInitAndHealth(t *testing.T) {
	c, err := appcontract.Parse([]byte(`
app:
  web:
    build: .
    ports: ["8080"]
    public: true
    healthcheck: "wget -qO- localhost:8080/health || exit 1"
init:
  - name: migrate
    build: .
    command: ["./migrate.sh"]
  - name: seed
    build: .
    command: ["./seed.sh"]
needs:
  postgres: { version: "16" }
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Init) != 2 || c.Init[0].Name != "migrate" || c.Init[1].Name != "seed" {
		t.Fatalf("init not parsed in order: %+v", c.Init)
	}
	if len(c.Init[0].Command) == 0 || c.Init[0].Command[0] != "./migrate.sh" {
		t.Fatalf("init command wrong: %+v", c.Init[0])
	}
	var web *appcontract.Part
	for i := range c.Parts {
		if c.Parts[i].Name == "web" {
			web = &c.Parts[i]
		}
	}
	if web == nil || web.Health == "" {
		t.Fatalf("web healthcheck not parsed: %+v", web)
	}
}

// Complaints #39/#41: the documented static form declares no port, and the
// public port was previously left at 0 — docker was then asked for "0/tcp". A static part
// serves on the file server's 80.
func TestAppYamlStaticPartDefaultsPort(t *testing.T) {
	c, err := appcontract.Parse([]byte(`
app:
  game:
    static: .
    public: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicPart().Static != "." || c.PublicPart().Port != 80 {
		t.Fatalf("static part has incorrect file-server port: static=%q port=%d", c.PublicPart().Static, c.PublicPart().Port)
	}
}

// Only AppTown sets a static part's browser values; a contract cannot.
func TestAContractCannotSetTheForgesBrowserValues(t *testing.T) {
	t.Parallel()
	_, err := appcontract.Parse([]byte("app:\n  web:\n    static: .\n    public: true\n    environment: {APPTOWN_BROWSER_ENV: '{}'}\n"))
	if err == nil || !strings.Contains(err.Error(), "only AppTown sets") {
		t.Fatalf("a contract naming the reserved variable: %v", err)
	}
}

// A static site may be built first: the map form names the Dockerfile's
// folder, its stage and the folder in that stage's files that is the site.
// It is a static part like the folder form (port 80, the release witness),
// and the declaration says what to build, not how a framework works.
func TestAStaticPartMayDeclareItsBuild(t *testing.T) {
	c, err := appcontract.Parse([]byte("app:\n  web:\n    static: { build: ., target: site, output: ./dist/ }\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	pub := c.PublicPart()
	want := appcontract.StaticBuild{Context: ".", Target: "site", Output: "dist"}
	if pub.StaticBuild == nil || *pub.StaticBuild != want || pub.Static != "" || pub.Build != "" || pub.Port != 80 || !pub.IsStatic() {
		t.Fatalf("built static part: %+v %+v", pub, pub.StaticBuild)
	}
	if got := c.PublicRelease("abc"); got != "web@abc" {
		t.Fatalf("a built static site answers its release: %q", got)
	}
	folder, err := appcontract.Parse([]byte("app:\n  web: { static: public, public: true }\n"))
	if err != nil || folder.PublicPart().Static != "public" || folder.PublicPart().StaticBuild != nil {
		t.Fatalf("the folder form is unchanged: %+v %v", folder, err)
	}
}

// Each spelling the map form refuses, with words that say what to write.
func TestAStaticBuildIsRefusedWithWords(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"no output":          {"static: { build: . }", "needs build: (the folder with the Dockerfile) and output:"},
		"no build":           {"static: { output: dist }", "needs build:"},
		"an absolute output": {"static: { build: ., output: /srv }", "must be a folder inside the build stage's files"},
		"an escaping output": {"static: { build: ., output: dist/../.. }", "must be a folder inside"},
		"an unknown key":     {"static: { build: ., output: dist, command: x }", `has no field "command"`},
		"a list":             {"static: [dist]", "static: is a folder, or a map"},
		"with a build too":   {"build: .\n    static: { build: ., output: dist }", "exactly one of build:, image:, or static:"},
		"not the front door": {"static: { build: ., output: dist }\n    public: false\n  api:\n    build: .", "must be the public part"},
	} {
		_, err := appcontract.Parse([]byte("app:\n  web:\n    " + tc.yaml + "\n    public: true\n"))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// A rehearsal's data checks are GET lines of the probe grammar and nothing
// else: a check that could not run is refused when the contract is read,
// never discovered during a rehearsal.
func TestRehearsalChecksAreGetLines(t *testing.T) {
	const head = "app:\n  web: { image: x, public: true }\nrehearsal:\n  checks: "
	c, err := appcontract.Parse([]byte(head + `["GET /healthz/data", "GET  /api/orders?limit=1   expect 204"]` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.RehearsalChecks, "|"); got != "GET /healthz/data|GET /api/orders?limit=1 expect 204" {
		t.Fatalf("checks: %q", got)
	}
	for _, bad := range []string{`["POST /orders"]`, `["GET orders"]`, `["GET /x expect two"]`, `["GET /x expect 999"]`, `["readiness"]`} {
		if _, err := appcontract.Parse([]byte(head + bad + "\n")); err == nil || !strings.Contains(err.Error(), "rehearsal: checks") {
			t.Errorf("%s: %v, want refused", bad, err)
		}
	}
}
