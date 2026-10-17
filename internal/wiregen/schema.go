package wiregen

import (
	"fmt"
	"reflect"
	"strings"
)

// Schemas writes JSON Schema (draft 2020-12, as OpenAPI 3.1 uses it) for Go
// values sent as JSON, by the same rules as the TypeScript: a struct's fields
// are what encoding/json writes, a field it may leave out is not required,
// and a type that encodes itself is written as the schema the caller maps.
// Named types become components, referred to by $ref; an object accepts
// properties it does not list, since a field may be added.
type Schemas struct {
	// Mapped types are written as the given schema, not declared.
	Mapped map[reflect.Type]map[string]any
	// Prefix is what a $ref puts before a component's name.
	Prefix     string
	Components map[string]map[string]any
	names      map[reflect.Type]string
	used       map[string]reflect.Type
}

// NewSchemas writes components referred to as prefix + name, for example
// "#/components/schemas/".
func NewSchemas(prefix string) *Schemas {
	return &Schemas{Mapped: map[reflect.Type]map[string]any{}, Prefix: prefix,
		Components: map[string]map[string]any{}, names: map[reflect.Type]string{}, used: map[string]reflect.Type{}}
}

// Map writes values of v's type as schema.
func (s *Schemas) Map(v any, schema map[string]any) { s.Mapped[reflect.TypeOf(v)] = schema }

// Ref is the schema of a value of v's type, declaring the components it
// reaches.
func (s *Schemas) Ref(v any) (map[string]any, error) { return s.ref(reflect.TypeOf(v)) }

// Named declares v's type as the component name and refers to it.
func (s *Schemas) Named(name string, v any) (map[string]any, error) {
	t := reflect.TypeOf(v)
	if err := s.name(t, name); err != nil {
		return nil, err
	}
	if err := s.declare(t); err != nil {
		return nil, err
	}
	return map[string]any{"$ref": s.Prefix + name}, nil
}

func (s *Schemas) name(t reflect.Type, name string) error {
	if strings.ContainsAny(name, "[]/ ") {
		return fmt.Errorf("%v would be named %q; name it explicitly", t, name)
	}
	if other, ok := s.used[name]; ok && other != t {
		return fmt.Errorf("two types would be named %s: %v and %v", name, other, t)
	}
	s.names[t], s.used[name] = name, t
	return nil
}

// declare writes t's component once; a type reached again while it is
// being written (a recursive one) is referred to, not written again.
func (s *Schemas) declare(t reflect.Type) error {
	name := s.names[t]
	if _, ok := s.Components[name]; ok {
		return nil
	}
	s.Components[name] = map[string]any{}
	body, err := s.body(t)
	if err != nil {
		delete(s.Components, name)
		return fmt.Errorf("%s: %w", name, err)
	}
	s.Components[name] = body
	return nil
}

// body is a named type's own schema.
func (s *Schemas) body(t reflect.Type) (map[string]any, error) {
	if words, ok := enumWords(t); ok {
		return map[string]any{"type": "string", "enum": words}, nil
	}
	if t.Kind() == reflect.Struct && !implements(t, marshaler) && !implements(t, textMarshaler) {
		return s.object(t)
	}
	return s.inline(t)
}

// object is a struct's schema: its properties and the ones always written.
func (s *Schemas) object(t reflect.Type) (map[string]any, error) {
	props := map[string]any{}
	required := []string{}
	for _, f := range members(t) {
		p, err := s.ref(f.typ)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.goName, err)
		}
		props[f.name] = p
		if !f.optional {
			required = append(required, f.name)
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out, nil
}

// ref is how a value of type t is written where it appears.
func (s *Schemas) ref(t reflect.Type) (map[string]any, error) {
	if m, ok := s.Mapped[t]; ok {
		return m, nil
	}
	if name, ok := s.names[t]; ok {
		return map[string]any{"$ref": s.Prefix + name}, nil
	}
	if _, ok := enumWords(t); ok {
		return s.enqueue(t)
	}
	if t.Kind() == reflect.Struct && t.Name() != "" && !implements(t, marshaler) && !implements(t, textMarshaler) {
		return s.enqueue(t)
	}
	return s.inline(t)
}

// inline is the schema of an unnamed (or non-struct) type, written in place.
func (s *Schemas) inline(t reflect.Type) (map[string]any, error) {
	// A pointer encodes as null or as its value, whatever the value's type.
	if t.Kind() == reflect.Pointer {
		inner, err := s.ref(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"anyOf": []any{inner, map[string]any{"type": "null"}}}, nil
	}
	if implements(t, marshaler) || implements(t, textMarshaler) {
		return nil, fmt.Errorf("%v encodes itself; map it to a schema", t)
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Interface:
		return map[string]any{}, nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "contentEncoding": "base64"}, nil
		}
		inner, err := s.ref(t.Elem())
		if err != nil {
			return nil, err
		}
		// A nil slice encodes as null; the forge's responses write empty
		// lists instead (TestEveryNilListAndMapIsWrittenEmpty).
		return map[string]any{"type": "array", "items": inner}, nil
	case reflect.Map:
		inner, err := s.ref(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": inner}, nil
	case reflect.Struct:
		return s.object(t)
	}
	return nil, fmt.Errorf("no schema for %v", t)
}

func (s *Schemas) enqueue(t reflect.Type) (map[string]any, error) {
	if err := s.name(t, t.Name()); err != nil {
		return nil, err
	}
	if err := s.declare(t); err != nil {
		return nil, err
	}
	return map[string]any{"$ref": s.Prefix + t.Name()}, nil
}
