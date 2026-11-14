package fix

import (
	"reflect"
	"testing"
)

func TestFixDeclarationsOnlyClassifyDependencies(t *testing.T) {
	for _, key := range []string{"list", "run", "runMajor", "runFor", "appliesTo"} {
		value := `"command"`
		if key == "appliesTo" {
			value = `["repository"]`
		}
		if _, err := Parse(`[{"kind":"update","ecosystem":"npm","` + key + `":` + value + `}]`); err == nil {
			t.Fatalf("removed %s accepted", key)
		}
	}
	ds, err := Parse(`[{"kind":"update","ecosystem":"npm","files":["package.json","package-lock.json","modules/*/deps.lock"]}]`)
	if err != nil {
		t.Fatal(err)
	}
	got := Classify([]string{"packages/a/package.json", "package-lock.json", "modules/a/deps.lock", "src/report.js", "mise.toml"}, ds, map[string]any{"mise.toml": map[string]string{"node": "22"}})
	want := []File{{"packages/a/package.json", "dependency"}, {"package-lock.json", "dependency"}, {"modules/a/deps.lock", "dependency"}, {"src/report.js", "other"}, {"mise.toml", "toolchain"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if got := Classify([]string{"composer.json"}, nil); got[0].Class != "other" {
		t.Fatal(got)
	}
}
