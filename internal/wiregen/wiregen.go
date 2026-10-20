// Package wiregen writes TypeScript declarations for Go values that are sent
// as JSON, following encoding/json's own rules: field names from json tags,
// "-" skipped, omitempty and omitzero fields optional, embedded structs
// flattened, and a type that encodes itself (a MarshalJSON) mapped to a
// TypeScript type the caller names. It exists so a web client's types are
// derived from the server's instead of kept in step by hand. Schemas writes
// JSON Schema for the same values by the same rules, for AppTown's API
// description (stock/docs/api/openapi.json).
package wiregen

import (
	"database/sql/driver"
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Named is a Go value's TypeScript name. The value's type is what is declared.
type Named struct {
	Name  string
	Value any
}

// Generator declares types in the order they are first reached.
type Generator struct {
	// Mapped types are written as the given TypeScript, not declared: for
	// types that encode themselves (a timestamp, a raw JSON value).
	Mapped map[reflect.Type]string
	names  map[reflect.Type]string
	used   map[string]reflect.Type
	decls  []string
	queue  []reflect.Type
}

func New() *Generator {
	return &Generator{Mapped: map[reflect.Type]string{}, names: map[reflect.Type]string{}, used: map[string]reflect.Type{}}
}

// Map writes values of v's type as ts.
func (g *Generator) Map(v any, ts string) { g.Mapped[reflect.TypeOf(v)] = ts }

// Declare adds the named types and everything they reach, and returns the
// TypeScript source.
func (g *Generator) Declare(types ...Named) (string, error) {
	for _, n := range types {
		t := reflect.TypeOf(n.Value)
		if err := g.name(t, n.Name); err != nil {
			return "", err
		}
		g.queue = append(g.queue, t)
	}
	for len(g.queue) > 0 {
		t := g.queue[0]
		g.queue = g.queue[1:]
		decl, err := g.declare(t)
		if err != nil {
			return "", err
		}
		g.decls = append(g.decls, decl)
	}
	return strings.Join(g.decls, "\n"), nil
}

// builtin names TypeScript types the generated file itself relies on or that
// readers expect to mean the standard thing; a declaration would shadow them.
var builtin = map[string]bool{"Record": true, "Partial": true, "Required": true, "Readonly": true, "Pick": true, "Omit": true,
	"Array": true, "Promise": true, "Map": true, "Set": true, "Date": true, "Error": true, "Object": true,
	"String": true, "Number": true, "Boolean": true}

func (g *Generator) name(t reflect.Type, name string) error {
	if builtin[name] {
		return fmt.Errorf("%v would be named %s, which shadows TypeScript's own; name it explicitly", t, name)
	}
	if other, ok := g.used[name]; ok && other != t {
		return fmt.Errorf("two types would be named %s: %v and %v", name, other, t)
	}
	g.names[t], g.used[name] = name, t
	return nil
}

var (
	marshaler     = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshaler = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	valuer        = reflect.TypeOf((*driver.Valuer)(nil)).Elem()
	stringer      = reflect.TypeOf((*fmt.Stringer)(nil)).Elem()
)

// declare writes one named type's declaration.
func (g *Generator) declare(t reflect.Type) (string, error) {
	name := g.names[t]
	if words, ok := enumWords(t); ok {
		quoted := make([]string, len(words))
		for i, w := range words {
			quoted[i] = fmt.Sprintf("%q", w)
		}
		return fmt.Sprintf("export type %s = %s\n", name, strings.Join(quoted, " | ")), nil
	}
	if t.Kind() != reflect.Struct {
		body, err := g.ref(t)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("export type %s = %s\n", name, body), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "export type %s = {\n", name)
	fields, err := g.fields(t)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	for _, f := range fields {
		fmt.Fprintf(&b, "  %s\n", f)
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// fields lists a struct's JSON fields as TypeScript members. Every
// collected field's type is reached, in order, even one a collision drops:
// that order decides where the declarations it queues are written.
func (g *Generator) fields(t reflect.Type) ([]string, error) {
	ts := map[*field]string{}
	all := collect(t, 0)
	for i := range all {
		s, err := g.ref(all[i].typ)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", all[i].goName, err)
		}
		ts[&all[i]] = s
	}
	var out []string
	for _, f := range winners(all) {
		optional := ""
		if f.optional {
			optional = "?"
		}
		out = append(out, fmt.Sprintf("%s%s: %s", tsKey(f.name), optional, ts[f]))
	}
	return out, nil
}

// field is one member of a struct's JSON: its name, whether encoding/json
// may leave it out, and the Go type of its value.
type field struct {
	name, goName string
	optional     bool
	typ          reflect.Type
	depth        int
	tagged       bool
}

// members lists a struct's JSON fields in order, embedded structs flattened
// as encoding/json does, including its rule for names that collide: the
// shallowest field wins, and at equal depth a single tagged field wins;
// otherwise every field with that name is dropped.
func members(t reflect.Type) []field {
	var out []field
	for _, f := range winners(collect(t, 0)) {
		out = append(out, *f)
	}
	return out
}

// winners are the collected fields encoding/json writes.
func winners(all []field) []*field {
	byName := map[string][]field{}
	for _, f := range all {
		byName[f.name] = append(byName[f.name], f)
	}
	var out []*field
	for i, f := range all {
		if winner, ok := dominant(byName[f.name]); ok && winner == f {
			out = append(out, &all[i])
		}
	}
	return out
}

func dominant(fs []field) (field, bool) {
	best := fs[0].depth
	for _, f := range fs {
		best = min(best, f.depth)
	}
	var at []field
	for _, f := range fs {
		if f.depth == best {
			at = append(at, f)
		}
	}
	if len(at) == 1 {
		return at[0], true
	}
	var tagged []field
	for _, f := range at {
		if f.tagged {
			tagged = append(tagged, f)
		}
	}
	if len(tagged) == 1 {
		return tagged[0], true
	}
	return field{}, false
}

func collect(t reflect.Type, depth int) []field {
	var out []field
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft, viaPointer := f.Type, false
			if ft.Kind() == reflect.Pointer {
				ft, viaPointer = ft.Elem(), true
			}
			if ft.Kind() == reflect.Struct && !implements(ft, marshaler) {
				// A nil embedded pointer drops all its fields from the JSON.
				for _, member := range collect(ft, depth+1) {
					if viaPointer {
						member.optional = true
					}
					out = append(out, member)
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		tagged := name != ""
		if name == "" {
			name = f.Name
		}
		optional := strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero")
		out = append(out, field{name: name, goName: f.Name, optional: optional, typ: f.Type, depth: depth, tagged: tagged})
	}
	return out
}

// ref is how a field of type t is written.
func (g *Generator) ref(t reflect.Type) (string, error) {
	if ts, ok := g.Mapped[t]; ok {
		return ts, nil
	}
	if name, ok := g.names[t]; ok {
		return name, nil
	}
	if _, ok := enumWords(t); ok {
		return g.enqueue(t)
	}
	// A pointer encodes as null or as its value, whatever the value's type.
	if t.Kind() == reflect.Pointer {
		inner, err := g.ref(t.Elem())
		return inner + " | null", err
	}
	if implements(t, marshaler) || implements(t, textMarshaler) {
		return "", fmt.Errorf("%v encodes itself; map it to a TypeScript type", t)
	}
	switch t.Kind() {
	case reflect.String:
		return "string", nil
	case reflect.Bool:
		return "boolean", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return "number", nil
	case reflect.Interface:
		return "unknown", nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return "string", nil // []byte is base64 text
		}
		inner, err := g.ref(t.Elem())
		if strings.Contains(inner, " ") {
			inner = "(" + inner + ")"
		}
		return inner + "[]", err
	case reflect.Map:
		inner, err := g.ref(t.Elem())
		return "Record<string, " + inner + ">", err
	case reflect.Struct:
		if t.Name() == "" {
			fields, err := g.fields(t)
			return "{ " + strings.Join(fields, "; ") + " }", err
		}
		return g.enqueue(t)
	}
	return "", fmt.Errorf("no TypeScript for %v", t)
}

func (g *Generator) enqueue(t reflect.Type) (string, error) {
	if err := g.name(t, t.Name()); err != nil {
		return "", err
	}
	g.queue = append(g.queue, t)
	return t.Name(), nil
}

// enumWords recognises a small state enum: an integer type that stores itself
// as text and spells each valid value, valid values running up from 1. Its
// TypeScript is the union of those words.
func enumWords(t reflect.Type) ([]string, bool) {
	if t.Kind() != reflect.Uint8 || !implements(t, valuer) || !implements(t, stringer) || !implements(t, marshaler) {
		return nil, false
	}
	var words []string
	for i := 1; i < 256; i++ {
		v := reflect.New(t).Elem()
		v.SetUint(uint64(i))
		stored, err := v.Interface().(driver.Valuer).Value()
		if err != nil {
			break
		}
		word, ok := stored.(string)
		if !ok {
			return nil, false
		}
		words = append(words, word)
	}
	return words, len(words) > 0
}

func implements(t, iface reflect.Type) bool {
	return t.Implements(iface) || reflect.PointerTo(t).Implements(iface)
}

func tsKey(name string) string {
	for _, r := range name {
		if !(r == '_' || r == '$' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return fmt.Sprintf("%q", name)
		}
	}
	return name
}
