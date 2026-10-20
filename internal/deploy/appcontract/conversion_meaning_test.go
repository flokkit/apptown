package appcontract

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// A YAML escape and a comment must not let conversion escape the comment
// while turning the original literal value into an active need reference.
func TestConversionPreservesAnEncodedLiteral(t *testing.T) {
	doc := "app:\n  web:\n    image: x\n    public: true\n    environment: { A: \"\\u0024{needs.postgres.PGHOST}\" } # ${needs.postgres.PGHOST}\nneeds:\n  postgres: {}\n"
	before, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	out, changed, err := ConvertToV2(doc)
	if err != nil {
		return
	} // A named refusal is an acceptable outcome.
	if !changed {
		t.Fatal("valid version-1 contract was silently skipped")
	}
	after, err := Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	p, err := PrepareWired(after, map[string]string{"PGHOST": "db"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Parts[0].Env["A"], before.Parts[0].Env["A"]; got != want {
		t.Fatalf("conversion reported success but changed delivered value from %q to %q:\n%s", want, got, out)
	}
}

func TestSchemaRefusesBareMemoryOnlyInVersionTwo(t *testing.T) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(JSONSchema, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{1, 2} {
		doc := map[string]any{"version": version, "app": map[string]any{"web": map[string]any{"image": "x", "public": true, "resources": map[string]any{"memory": "512M"}}}}
		err := resolved.Validate(doc)
		if version == 1 && err != nil {
			t.Errorf("schema rejected v1 memory: %v", err)
		}
		if version == 2 && err == nil {
			t.Error("schema accepted version: 2 memory: 512M, which Parse refuses")
		}
	}
}
