package appcontract

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/yamlkeys"
)

// The served schema and the parser describe the same document: every key the
// parser accepts has a schema property, and every schema property is a key the
// parser accepts. A field added to one without the other fails here, which is
// how the schema had drifted (version and health were missing).
func TestSchemaDescribesExactlyTheKeysTheParserAccepts(t *testing.T) {
	problems, err := yamlkeys.Compare(reflect.TypeOf(appYamlDoc{}), JSONSchema, "app.yaml")
	if err != nil {
		t.Fatalf("the schema is not JSON: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("schema and parser disagree:\n%s", strings.Join(problems, "\n"))
	}
}

// A field the parser refuses by name is refused by the schema too, so an
// editor or a model reading the schema is not told it may write it.
func TestSchemaRefusesCommandHealthChecks(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(JSONSchema, &schema); err != nil {
		t.Fatal(err)
	}
	at := func(path ...string) map[string]any {
		t.Helper()
		node := schema
		for _, key := range path {
			next, ok := node[key].(map[string]any)
			if !ok {
				t.Fatalf("schema has no %s", strings.Join(path, "."))
			}
			node = next
		}
		return node
	}
	part := at("properties", "app", "additionalProperties", "properties")
	health := at("properties", "app", "additionalProperties", "properties", "health", "properties")
	for name, node := range map[string]map[string]any{
		"healthcheck":           part["healthcheck"].(map[string]any),
		"health.liveness.exec":  health["liveness"].(map[string]any)["properties"].(map[string]any)["exec"].(map[string]any),
		"health.readiness.exec": health["readiness"].(map[string]any)["properties"].(map[string]any)["exec"].(map[string]any),
	} {
		if _, refused := node["not"]; !refused || node["type"] != nil {
			t.Errorf("schema accepts %s: %v", name, node)
		}
	}
}
