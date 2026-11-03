package appcontract

import (
	"encoding/json"
	"github.com/google/jsonschema-go/jsonschema"
	"slices"
	"strings"
	"testing"
)

// Every source form owns an ordered test declaration; pulled images have no source to test.
func TestSourcePartsDeclareTests(t *testing.T) {
	for _, source := range []string{"build: api", "static: site", "static: {build: ui, output: dist}"} {
		c, err := Parse([]byte("version: 2\napp:\n  web:\n    public: true\n    " + source + "\n    test: [\"first\", \"second\", \"\"]\n"))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(c.Parts[0].Test, []string{"first", "second", ""}) {
			t.Fatalf("tests: %v", c.Parts[0].Test)
		}
	}
	for _, declaration := range []string{`["true"]`, "[]"} {
		_, err := Parse([]byte("app: {web: {image: nginx:1, public: true, test: " + declaration + "}}"))
		if err == nil || !strings.Contains(err.Error(), "test:") {
			t.Fatalf("image tests: %v", err)
		}
	}
}

func TestTestsSchemaAcceptsOnlySourceDeclarations(t *testing.T) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(JSONSchema, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		source      string
		declaration any
		valid       bool
	}{
		{"build", []any{"first", ""}, true},
		{"static", []any{"first"}, true},
		{"build", "go test ./...", false},
		{"build", []any{12}, false},
		{"image", []any{"first"}, false},
		{"image", []any{}, false},
	} {
		part := map[string]any{tc.source: ".", "public": true, "test": tc.declaration}
		err := resolved.Validate(map[string]any{"app": map[string]any{"web": part}})
		if (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}
