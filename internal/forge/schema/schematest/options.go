// Package schematest holds the workflow language's checks that the runners'
// own tests call: only tests import it.
package schematest

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"flokkit.io/forge/internal/forge/schema"
)

// OptionsDecodedBy compares the JSON keys of decoded, the type a runner
// reads a step of kind into, with the kind's options and the keys AppTown
// records: the runner's own test calls it, so a field a runner reads that
// no save may write, or an option no runner reads, is a failing test.
func OptionsDecodedBy(kind string, decoded any) error {
	want, ok := schema.StepOptions(kind)
	if !ok {
		return fmt.Errorf("%s is not a step kind", kind)
	}
	recorded := schema.WrittenByAppTown(kind)
	want = append(want, recorded...)
	got := jsonKeys(reflect.TypeOf(decoded))
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		return fmt.Errorf("%s: the runner reads %v; a save may write and AppTown records %v", kind, got, want)
	}
	return nil
}

// jsonKeys is the top-level JSON object keys a value of t decodes from.
func jsonKeys(t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			out = append(out, jsonKeys(f.Type)...)
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}
