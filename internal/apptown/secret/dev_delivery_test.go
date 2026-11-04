package secret

import (
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

func TestDevDeliveryScopesDefaultsFreshValuesAndWithheldNames(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte(`app:
  web: {image: "app:1", public: true}
init:
  - name: install
    image: app:1
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"default", "alpha"} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()
			spec, err := ParseSpec([]byte(`[project]
name = "test"
revision = "1.0"
[profiles.` + profile + `]
KEY = {generate = true, default = "unused"}
EMPTY = {default = ""}
LITERAL = {default = "it's $HOME"}
CREDENTIAL = {required = true}
OPTIONAL = {required = false}
[profiles.zulu]
EXCLUDED = {required = true}
[scopes.web]
secrets = ["KEY", "EMPTY", "LITERAL", "CREDENTIAL"]
[scopes.init-install]
secrets = ["KEY", "OPTIONAL"]
`))
			if err != nil {
				t.Fatal(err)
			}
			d, withheld, err := DevDelivery(spec, c)
			if err != nil {
				t.Fatal(err)
			}
			if d.Profile != profile || !reflect.DeepEqual(withheld, []string{"CREDENTIAL", "OPTIONAL"}) {
				t.Fatalf("profile %q withheld %v", d.Profile, withheld)
			}
			web, init := d.EnvFor("web"), d.EnvFor("init-install")
			if web["KEY"] == "" || web["KEY"] == "unused" || init["KEY"] != web["KEY"] {
				t.Fatal("generated value must be shared by its consumers")
			}
			if v, ok := web["EMPTY"]; !ok || v != "" {
				t.Fatal("empty default missing")
			}
			if web["LITERAL"] != "it's $HOME" || len(init) != 1 || len(web) != 3 {
				t.Fatalf("wrong scoped defaults: %v %v", web, init)
			}
			again, _, err := DevDelivery(spec, c)
			if err != nil {
				t.Fatal(err)
			}
			if again.EnvFor("web")["KEY"] == web["KEY"] {
				t.Fatal("generated key reused")
			}
			spec.Scopes = nil
			all, _, err := DevDelivery(spec, c)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(all.EnvFor("web"), all.EnvFor("init-install")) {
				t.Fatal("unscoped secrets must reach all consumers")
			}
			spec.Scopes = map[string][]string{"unknown": {"KEY"}}
			if _, _, err := DevDelivery(spec, c); err == nil {
				t.Fatal("unknown consumer accepted")
			}
		})
	}
}
