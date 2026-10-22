package appcontract

import (
	"strings"
	"testing"
)

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
