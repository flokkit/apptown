package appcontract

import (
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
