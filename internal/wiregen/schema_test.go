package wiregen

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func schemas() *Schemas {
	s := NewSchemas("#/c/")
	s.Map(time.Time{}, map[string]any{"type": "string", "format": "date-time"})
	s.Map(json.RawMessage{}, map[string]any{})
	return s
}

// A schema says what encoding/json writes: the same properties as the
// TypeScript, required unless the encoder may leave them out, null where a
// pointer may be nil, and a state's words.
func TestSchemasFollowEncodingJSON(t *testing.T) {
	s := schemas()
	ref, err := s.Named("Row", Record{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ref, map[string]any{"$ref": "#/c/Row"}) {
		t.Fatalf("ref: %v", ref)
	}
	row := s.Components["Row"]
	props := row["properties"].(map[string]any)
	var names []string
	for k := range props {
		names = append(names, k)
	}
	slices.Sort(names)
	if want := []string{"Untagged", "createdAt", "id", "inline", "labels", "note", "owner", "previous", "raw", "state", "tags", "title"}; !slices.Equal(names, want) {
		t.Fatalf("properties %v, want %v", names, want)
	}
	required := row["required"].([]string)
	for _, optional := range []string{"note", "raw"} {
		if slices.Contains(required, optional) {
			t.Errorf("%s may be left out, yet is required", optional)
		}
	}
	if !slices.Contains(required, "title") || !slices.Contains(required, "owner") {
		t.Errorf("required: %v", required)
	}
	for name, want := range map[string]any{
		"id":       map[string]any{"type": "integer"},
		"state":    map[string]any{"$ref": "#/c/state"},
		"previous": map[string]any{"anyOf": []any{map[string]any{"$ref": "#/c/state"}, map[string]any{"type": "null"}}},
		"owner":    map[string]any{"anyOf": []any{map[string]any{"$ref": "#/c/Owner"}, map[string]any{"type": "null"}}},
		"tags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"labels":   map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer"}},
		"inline":   map[string]any{"type": "object", "properties": map[string]any{"N": map[string]any{"type": "integer"}}, "required": []string{"N"}},
	} {
		if !reflect.DeepEqual(props[name], want) {
			t.Errorf("%s: %v, want %v", name, props[name], want)
		}
	}
	if !reflect.DeepEqual(s.Components["state"], map[string]any{"type": "string", "enum": []string{"open", "closed"}}) {
		t.Errorf("state: %v", s.Components["state"])
	}
}

// A type that refers to itself is written once and referred to.
func TestARecursiveTypeIsReferredTo(t *testing.T) {
	type Tree struct {
		Children []Tree `json:"children"`
	}
	s := schemas()
	if _, err := s.Named("Tree", Tree{}); err != nil {
		t.Fatal(err)
	}
	items := s.Components["Tree"]["properties"].(map[string]any)["children"].(map[string]any)["items"]
	if !reflect.DeepEqual(items, map[string]any{"$ref": "#/c/Tree"}) {
		t.Fatalf("children: %v", items)
	}
}

// The rules the TypeScript keeps hold for schemas too: a self-encoding type
// must be mapped, two types cannot share a name, and a nil embedded pointer
// makes its fields optional.
func TestSchemasRefuseWhatTheyCannotDescribe(t *testing.T) {
	if _, err := schemas().Ref(struct{ S selfEncoding }{}); err == nil || !strings.Contains(err.Error(), "encodes itself") {
		t.Errorf("unmapped self-encoding type: %v", err)
	}
	s := schemas()
	if _, err := s.Named("Owner", Owner{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Named("Owner", Base{}); err == nil {
		t.Error("two types named Owner")
	}
	s = schemas()
	if _, err := s.Named("Opt", withOptionalBlock{}); err != nil {
		t.Fatal(err)
	}
	if req, _ := s.Components["Opt"]["required"].([]string); slices.Contains(req, "detail") {
		t.Errorf("a field of a nil embedded pointer is required: %v", req)
	}
}
