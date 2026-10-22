package appcontract

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

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
