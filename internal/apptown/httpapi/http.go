package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

// noNullLists writes every nil list and nil map a response can reach as an
// empty one, at any depth: Go writes nil as null, the UI reads a list's
// length, and AppTown's first browser error was exactly that
// ("remediations": null when none had run). It is why the generated web types
// (wire.gen.ts) can say T[] rather than T[] | null. A field tagged omitempty
// or omitzero stays absent when unset; byte slices, raw JSON and types that
// encode themselves are left alone. The caller's value is never modified.
func noNullLists(v any) any {
	if v == nil {
		return nil
	}
	return emptyLists(reflect.ValueOf(v)).Interface()
}

var jsonMarshaler = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

func encodesItself(t reflect.Type) bool {
	return t.Implements(jsonMarshaler) || reflect.PointerTo(t).Implements(jsonMarshaler)
}

func emptyLists(v reflect.Value) reflect.Value {
	t := v.Type()
	if encodesItself(t) {
		return v
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(t).Elem()
		out.Set(emptyLists(v.Elem()))
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		p := reflect.New(t.Elem())
		p.Elem().Set(emptyLists(v.Elem()))
		return p
	case reflect.Struct:
		out := reflect.New(t).Elem()
		out.Set(v)
		for i := range out.NumField() {
			f, sf := out.Field(i), t.Field(i)
			if !f.CanSet() {
				continue
			}
			if tag := sf.Tag.Get("json"); tag == "-" || (isEmptyContainer(f) && (strings.Contains(tag, ",omitempty") || strings.Contains(tag, ",omitzero"))) {
				continue
			}
			f.Set(emptyLists(f))
		}
		return out
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return v
		}
		if v.IsNil() {
			return reflect.MakeSlice(t, 0, 0)
		}
		out := reflect.MakeSlice(t, v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(emptyLists(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.MakeMap(t)
		}
		out := reflect.MakeMapWithSize(t, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), emptyLists(iter.Value()))
		}
		return out
	}
	return v
}

func isEmptyContainer(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Slice, reflect.Map:
		return v.Len() == 0
	case reflect.Pointer, reflect.Interface:
		return v.IsNil()
	}
	return false
}

// Done is the answer to a command that has nothing else to say.
type Done struct {
	OK bool `json:"ok"`
}

func WriteJSON(w http.ResponseWriter, code int, v any) {
	v = noNullLists(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, code int, format string, args ...any) {
	WriteJSON(w, code, map[string]string{"error": fmt.Sprintf(format, args...)})
}

// PathID reads a record's id from the path.
func PathID(r *http.Request, name string) (ids.ID, error) {
	return ids.Parse(r.PathValue(name))
}

// PathNumber reads a number from the path: an attempt, or a number within
// a project (a release's).
func PathNumber(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

// QueryInt reads an optional integer query parameter: dflt when absent,
// 400 (and false) when present but not a number.
func QueryInt(w http.ResponseWriter, r *http.Request, name string, dflt int) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return dflt, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		WriteError(w, 400, "bad %s", name)
		return 0, false
	}
	return n, true
}
