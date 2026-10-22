// Package yamlkeys holds a published JSON Schema to the Go types a YAML
// parser decodes into: every key the parser accepts is a schema property,
// every property is a key the parser accepts, and a type the parser reads
// strictly is closed in the schema. Schema tests use it so a field added to
// one side without the other fails, which is how such schemas drift.
package yamlkeys

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// Compare walks typ (the parser's document type) and the schema node that
// should describe it, and returns each disagreement in words, sorted.
func Compare(typ reflect.Type, schema []byte, at string) ([]string, error) {
	var node map[string]any
	if err := json.Unmarshal(schema, &node); err != nil {
		return nil, err
	}
	var problems []string
	compare(typ, node, at, &problems)
	sort.Strings(problems)
	return problems, nil
}

func compare(typ reflect.Type, node map[string]any, at string, problems *[]string) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	// A yaml.Node holds a value as written, of any shape: the parser
	// judges it itself, so it has no keys to compare.
	if typ.PkgPath() == "gopkg.in/yaml.v3" && typ.Name() == "Node" {
		return
	}
	switch typ.Kind() {
	case reflect.Struct:
		props, _ := node["properties"].(map[string]any)
		seen := map[string]bool{}
		walkFields(typ, func(key string, f reflect.StructField) {
			seen[key] = true
			child, ok := props[key].(map[string]any)
			if !ok {
				*problems = append(*problems, at+"."+key+": accepted by the parser, missing from the schema")
				return
			}
			compare(f.Type, child, at+"."+key, problems)
		})
		for key := range props {
			if !seen[key] {
				*problems = append(*problems, at+"."+key+": in the schema, refused by the parser")
			}
		}
		if node["additionalProperties"] != false {
			*problems = append(*problems, at+": the parser refuses unknown keys, so the schema must too")
		}
	case reflect.Map:
		if child, ok := node["additionalProperties"].(map[string]any); ok {
			compare(typ.Elem(), child, at+".*", problems)
		}
	case reflect.Slice:
		if child, ok := node["items"].(map[string]any); ok {
			compare(typ.Elem(), child, at+"[]", problems)
		}
	}
}

// walkFields visits each yaml key of a struct, descending into inline
// structs (an inline map holds keys the type does not name, which the
// caller's schema describes by pattern).
func walkFields(typ reflect.Type, visit func(string, reflect.StructField)) {
	for i := range typ.NumField() {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")
		if len(tag) > 1 && tag[1] == "inline" {
			if f.Type.Kind() == reflect.Struct {
				walkFields(f.Type, visit)
			}
			continue
		}
		if tag[0] == "" || tag[0] == "-" {
			continue
		}
		visit(tag[0], f)
	}
}
