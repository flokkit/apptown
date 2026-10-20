package appcontract

import (
	"fmt"
	"strings"
	"testing"
)

func TestReviewConversionPreservesLiteralEnvironmentValues(t *testing.T) {
	for _, value := range []string{"memory: 512M", "${needs.future.value}", "${needs."} {
		t.Run(value, func(t *testing.T) {
			doc := fmt.Sprintf("app:\n  web:\n    image: x\n    public: true\n    environment:\n      CONFIG: %q\n", value)
			out, changed, err := ConvertToV2(doc)
			if err != nil || !changed {
				t.Fatalf("conversion: %v, changed %v", err, changed)
			}
			c, err := Parse([]byte(out))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := PrepareWired(c, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := plan.Parts[0].Env["CONFIG"]; got != value {
				t.Fatalf("upgrade changed delivered value: %q -> %q", value, got)
			}
		})
	}
}

const wiredDoc = `version: 2
app:
  web:
    image: wordpress
    public: true
    environment:
      WORDPRESS_DB_HOST: "${needs.mariadb.DB_HOST}:${needs.mariadb.DB_PORT}"
      LITERAL: "$${needs.mariadb.DB_HOST} and $${HOME} and ${HOME}"
init:
  - name: migrate
    image: wordpress
    environment: { DSN: "${needs.mariadb.DB_HOST}" }
needs:
  mariadb: {}
`

// A version-2 contract's references are replaced by the need's values on
// every part and init step; $${needs. is the literal text, and nothing else
// in a value changes meaning.
//
// Kills: references left unreplaced; the escape not applied; $${HOME} or
// ${HOME} rewritten; init steps not wired.
func TestNeedReferencesAreWiredAndOnlyTheirEscapeIsSpecial(t *testing.T) {
	c, err := Parse([]byte(wiredDoc))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareWired(c, map[string]string{"DB_HOST": "db", "DB_PORT": "3306"})
	if err != nil {
		t.Fatal(err)
	}
	web := plan.Parts[0].Env
	if web["WORDPRESS_DB_HOST"] != "db:3306" {
		t.Errorf("wired value: %q", web["WORDPRESS_DB_HOST"])
	}
	if web["LITERAL"] != "${needs.mariadb.DB_HOST} and $${HOME} and ${HOME}" {
		t.Errorf("literal value: %q", web["LITERAL"])
	}
	if web["DB_HOST"] != "db" {
		t.Errorf("the need's own variables must still be given: %v", web)
	}
	if plan.Init[0].Env["DSN"] != "db" {
		t.Errorf("init step not wired: %v", plan.Init[0].Env)
	}
	refs := c.NeedRefs()
	if len(refs) != 3 || refs[0] != (NeedRef{Part: "web", Key: "WORDPRESS_DB_HOST", Need: "mariadb", Var: "DB_HOST"}) {
		t.Errorf("references: %+v", refs)
	}
	if _, err := PrepareWired(c, map[string]string{"DB_HOST": "db"}); err == nil || !strings.Contains(err.Error(), "${needs.mariadb.DB_PORT}") {
		t.Errorf("a value the target did not give must be named: %v", err)
	}
}

// Parse refuses a malformed reference and one naming an undeclared need,
// naming the field; a version-1 contract delivers ${needs. as written.
//
// Kills: checkNeedRefs not called; the version-1 exemption removed.
func TestBadNeedReferencesAreRefusedAtParse(t *testing.T) {
	part := "app:\n  web:\n    image: x\n    public: true\n    environment: { A: %q }\nneeds:\n  postgres: {}\n"
	for v, want := range map[string]string{
		"${needs.redis.REDIS_URL}": "does not declare under needs",
		"${needs.postgres.pghost}": "starts a need reference that is not one",
		"${needs.postgres}":        "starts a need reference that is not one",
	} {
		_, err := Parse([]byte("version: 2\n" + strings.Replace(part, "%q", `"`+v+`"`, 1)))
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "environment: A") {
			t.Errorf("%s: got %v, want %q naming the field", v, err, want)
		}
	}
	c, err := Parse([]byte(strings.Replace(part, "%q", `"${needs.redis.X}"`, 1)))
	if err != nil {
		t.Fatalf("version 1 delivers ${needs. as written: %v", err)
	}
	plan, err := PrepareWired(c, nil)
	if err != nil || plan.Parts[0].Env["A"] != "${needs.redis.X}" {
		t.Errorf("version 1 value: %v %v", plan, err)
	}
}

// Converting a version-1 contract to version 2 keeps every value delivered
// byte for byte (a ${needs. becomes its escape) and writes memory in Mi/Gi;
// running it again changes nothing.
//
// Kills: the ${needs. escape dropped; units not rewritten; the version not
// set; a second conversion changing the file.
func TestConvertingToVersionTwoKeepsEveryValue(t *testing.T) {
	v1 := `# my app
app:
  web:
    image: x
    public: true
    resources: { memory: 512M, cpu: 1 }
    environment:
      A: "${needs.postgres.PGHOST}"
      B: "$${needs.postgres.PGHOST} $${HOME} $$${needs.x.Y} ${HOME}"
profiles:
  prod:
    web: { resources: { memory: 2G } }
init:
  - { name: m, image: x, environment: { C: "${needs.postgres.PGHOST}" } }
needs:
  postgres: { resources: { memory: 256M }, version: "16" }
`
	before, err := Parse([]byte(v1))
	if err != nil {
		t.Fatal(err)
	}
	out, changed, err := ConvertToV2(v1)
	if err != nil || !changed || !strings.HasPrefix(out, "version: 2\n# my app") {
		t.Fatalf("converted: %v %v\n%s", changed, err, out)
	}
	after, err := Parse([]byte(out))
	if err != nil || after.Version != 2 {
		t.Fatalf("converted contract: %v", err)
	}
	if after.Parts[0].Resources != before.Parts[0].Resources || after.NeedResources["postgres"] != before.NeedResources["postgres"] || after.Profiles["prod"]["web"].Resources != before.Profiles["prod"]["web"].Resources {
		t.Errorf("resources changed meaning: %+v / %+v", after, before)
	}
	values := map[string]string{"PGHOST": "db"}
	p1, err1 := PrepareWired(before, values)
	p2, err2 := PrepareWired(after, values)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	for _, k := range []string{"A", "B"} {
		if p1.Parts[0].Env[k] != p2.Parts[0].Env[k] {
			t.Errorf("%s delivered %q before, %q after", k, p1.Parts[0].Env[k], p2.Parts[0].Env[k])
		}
	}
	if p1.Init[0].Env["C"] != p2.Init[0].Env["C"] {
		t.Errorf("init value changed: %q -> %q", p1.Init[0].Env["C"], p2.Init[0].Env["C"])
	}
	again, changed, err := ConvertToV2(out)
	if err != nil || changed || again != out {
		t.Errorf("a second conversion must change nothing: %v %v", changed, err)
	}
}

// A contract whose environment shares values through YAML anchors or
// aliases is not converted in place (an edit could change a value nobody
// meant to change): the conversion says so and leaves the text as it was.
//
// Kills: the anchor refusal removed (the aliased value or the command would
// change silently).
func TestAContractWithAnchorsIsLeftForAPerson(t *testing.T) {
	for name, doc := range map[string]string{
		"alias in the environment":  "app:\n  web:\n    image: x\n    public: true\n    command: [echo, &shared \"${needs.postgres.PGHOST}\"]\n    environment: { CONFIG: *shared }\nneeds:\n  postgres: {}\n",
		"anchor in the environment": "app:\n  web:\n    image: x\n    public: true\n    environment: { CONFIG: &shared \"${needs.postgres.PGHOST}\" }\n    command: [echo, *shared]\nneeds:\n  postgres: {}\n",
	} {
		out, changed, err := ConvertToV2(doc)
		if err == nil || changed || out != doc || !strings.Contains(err.Error(), "anchors") {
			t.Errorf("%s: want a refusal naming anchors and the text unchanged, got %v %v", name, changed, err)
		}
	}
}
