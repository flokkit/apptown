package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
)

var testSpec = func() string {
	raw, err := os.ReadFile("testdata/declaration.toml")
	if err != nil {
		panic(err)
	}
	return string(raw)
}()

func TestSecretSpecParse(t *testing.T) {
	t.Parallel()
	spec, err := ParseSpec([]byte(testSpec))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "quick-poll" || strings.Join(spec.ProfileNames(), ",") != "default,preview,standalone" {
		t.Fatalf("project/profiles: %s %v", spec.Name, spec.ProfileNames())
	}
	// preview inherits default and overrides one entry.
	pv := spec.Profiles["preview"]
	if len(pv) != 4 || pv["STRIPE_API_KEY"].Required || !pv["MIGRATION_DB_ROLE"].Required || !pv["SECRET_KEY_BASE"].Generate {
		t.Fatalf("preview profile: %+v", pv)
	}
	if pv["OPTIONAL_THING"].Required {
		t.Fatal("optional stays optional")
	}
	// standalone does not inherit.
	if sa := spec.Profiles["standalone"]; len(sa) != 1 || sa["ONLY_HERE"].Name != "ONLY_HERE" {
		t.Fatalf("standalone: %+v", sa)
	}
	// scopes
	if c, all := spec.ConsumersOf("SECRET_KEY_BASE"); all || strings.Join(c, ",") != "web,worker" {
		t.Fatalf("consumers of SECRET_KEY_BASE: %v all=%v", c, all)
	}
	if c, _ := spec.ConsumersOf("MIGRATION_DB_ROLE"); strings.Join(c, ",") != "init-migrate" {
		t.Fatalf("consumers of MIGRATION_DB_ROLE: %v", c)
	}
	// providers are parsed and reported as ignored, never honoured.
	if len(spec.Ignored) != 1 || !strings.Contains(spec.Ignored[0], "[providers]") {
		t.Fatalf("ignored: %v", spec.Ignored)
	}
	// wrong revision, missing description tolerated, bad scope refused
	if _, err := ParseSpec([]byte(strings.Replace(testSpec, `revision = "1.0"`, `revision = "2.0"`, 1))); err == nil {
		t.Fatal("revision 2.0 must be refused")
	}
	if _, err := ParseSpec([]byte(testSpec + "\n[scopes.ghost]\nsecrets = [\"NOPE\"]\n")); err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("undeclared scope secret must be refused: %v", err)
	}
	// a table AppTown never reads is refused, naming it, not silently dropped
	if _, err := ParseSpec([]byte(testSpec + "\n[secrets.DJANGO_SUPERUSER_PASSWORD]\ngenerate = true\n")); err == nil || !strings.Contains(err.Error(), "secrets.DJANGO_SUPERUSER_PASSWORD") {
		t.Fatalf("a stray [secrets.X] table must be refused by name: %v", err)
	}
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true}, {Name: "worker"}}, Init: []appcontract.InitContainer{{Name: "migrate"}}}
	if err := spec.CheckScopesAgainst(c); err != nil {
		t.Fatal(err)
	}
	c.Init = nil
	if err := spec.CheckScopesAgainst(c); err == nil || !strings.Contains(err.Error(), "init-migrate") {
		t.Fatalf("a scope naming a missing init step must be refused: %v", err)
	}
}

// No secret may be named like a variable AppTown sets (it would win on a
// Docker host and lose elsewhere), or start APPTOWN_, which is kept for the
// variables AppTown adds later. A secret's own name is still fine.
func TestASecretCannotTakeANameAppTownKeeps(t *testing.T) {
	t.Parallel()
	declare := func(name string) error {
		_, err := ParseSpec([]byte("[project]\nname = \"app\"\nrevision = \"1.0\"\n\n[profiles.default]\n" + name + " = { description = \"x\", required = true }\n"))
		return err
	}
	for _, name := range []string{"APPTOWN_BROWSER_ENV", "PORT", "DATA_DIR", "APPTOWN_ORIGINS", "APPTOWN_NEXT_THING"} {
		if err := declare(name); err == nil || !strings.Contains(err.Error(), "is AppTown's") {
			t.Errorf("a secret named %s: %v", name, err)
		}
	}
	if err := declare("APP_PORT"); err != nil {
		t.Errorf("an app's own name: %v", err)
	}
}

// The declaration is read from the project's own tree: a secrets.spec path
// that leaves it is refused before anything is read.
func TestASpecOutsideTheProjectIsNotRead(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	outside := filepath.Join(filepath.Dir(ws), "secretspec.toml")
	if err := os.WriteFile(outside, []byte("[project]\nname = \"x\"\nrevision = \"1.0\"\n\n[profiles.default]\nA = { description = \"a\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })
	for _, path := range []string{"../secretspec.toml", outside} {
		if spec, _, err := ReadSpec(ws, path); err == nil || spec != nil {
			t.Errorf("secrets.spec %s: %+v %v", path, spec, err)
		}
	}
}
