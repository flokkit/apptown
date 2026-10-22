package httpapi

import (
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/stamp"
	"github.com/flokkit/apptown/internal/wiregen"
)

var updateWire = flag.Bool("update-wire", false, "rewrite web/src/wire.gen.ts from the Go response types")

// wireTypes are the responses whose TypeScript the web client imports instead
// of declaring by hand. A handler group registers its response types from its
// own wire_*_test.go file in an init, so groups never edit one shared list;
// the generator sorts them, so the output does not depend on file order.
var wireTypes []wiregen.Named

func registerWire(named ...wiregen.Named) { wireTypes = append(wireTypes, named...) }

// wireMapped are types that encode themselves (a MarshalJSON), each written as
// the TypeScript name of a registered type describing what it encodes.
var wireMapped = map[reflect.Type]string{}

func registerWireMapped(v any, ts string) { wireMapped[reflect.TypeOf(v)] = ts }

func init() {
	registerWire(
		wiregen.Named{Name: "Done", Value: Done{}},
		wiregen.Named{Name: "IssuedToken", Value: IssuedToken{}},
		wiregen.Named{Name: "TokenList", Value: TokenList{}},
		wiregen.Named{Name: "UserList", Value: UserList{}},
		wiregen.Named{Name: "GroupList", Value: GroupList{}},
		wiregen.Named{Name: "RoleCatalog", Value: RoleCatalog{}},
		wiregen.Named{Name: "GrantList", Value: GrantList{}},
		wiregen.Named{Name: "ApprovalBrief", Value: ApprovalBrief{}},
		wiregen.Named{Name: "ApprovalPolicy", Value: ApprovalPolicy{}},
	)
}

const wireHeader = `// Generated from the Go response types by internal/apptown/httpapi's
// TestWebTypesAreCurrent. Do not edit: change the Go type and run
//   go test ./internal/apptown/httpapi -run TestWebTypesAreCurrent -update-wire

`

// The web client's generated types match the Go responses they describe. A
// server change that renames or drops a field fails here until the file is
// regenerated, and then fails the web typecheck wherever a page relied on it.
func TestWebTypesAreCurrent(t *testing.T) {
	g := wiregen.New()
	g.Map(stamp.Time{}, "string")
	g.Map(json.RawMessage{}, "unknown")
	g.Map(time.Time{}, "string")
	// A record id is a UUID; Nil (none) encodes as null.
	g.Map(ids.ID{}, "string | null")
	g.Map(workflow.OptionalRunState{}, `RunState | ""`)
	for t, ts := range wireMapped {
		g.Mapped[t] = ts
	}
	named := append([]wiregen.Named(nil), wireTypes...)
	sort.Slice(named, func(i, j int) bool { return named[i].Name < named[j].Name })
	src, err := g.Declare(named...)
	if err != nil {
		t.Fatal(err)
	}
	want := wireHeader + src
	path := filepath.Join("..", "..", "..", "web", "src", "wire.gen.ts")
	if *updateWire {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("web/src/wire.gen.ts is stale; run: go test ./internal/apptown/httpapi -run TestWebTypesAreCurrent -update-wire")
	}
}

// A typed response keeps the list guarantee maps have: a nil slice field is
// written as an empty list, never null.
func TestTypedResponsesWriteEmptyListsNotNull(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, 200, TokenList{})
	if body := w.Body.String(); body != "{\"tokens\":[]}\n" {
		t.Fatalf("body: %q", body)
	}
}

// The rule reaches every depth, so a generated T[] is never null: lists and
// maps inside list elements, behind pointers and inside maps are written
// empty; an optional one stays absent; raw JSON and an explicit nil value are
// left as they are; and the caller's value is not modified.
// selfEncoded writes its own JSON, so WriteJSON must leave its insides alone.
type selfEncoded struct{ List []string }

func (s selfEncoded) MarshalJSON() ([]byte, error) { return json.Marshal(s.List) }

func TestEveryNilListAndMapIsWrittenEmpty(t *testing.T) {
	type leaf struct {
		Names []string          `json:"names"`
		Index map[string]int    `json:"index"`
		Maybe []string          `json:"maybe,omitempty"`
		Raw   json.RawMessage   `json:"raw,omitempty"`
		Extra map[string]string `json:"extra,omitzero"`
	}
	type Shared struct {
		Tags []string `json:"tags"`
	}
	type root struct {
		Shared
		Rows  []leaf          `json:"rows"`
		One   *leaf           `json:"one"`
		None  *leaf           `json:"none"`
		Loose map[string]any  `json:"loose"`
		Doc   json.RawMessage `json:"doc"`
		Own   selfEncoded     `json:"own"`
	}
	in := root{Rows: []leaf{{}}, One: &leaf{}, Loose: map[string]any{"list": []int(nil), "gone": nil}}
	w := httptest.NewRecorder()
	WriteJSON(w, 200, in)
	want := `{"tags":[],"rows":[{"names":[],"index":{}}],"one":{"names":[],"index":{}},"none":null,"loose":{"gone":null,"list":[]},"doc":null,"own":null}` + "\n"
	if w.Body.String() != want {
		t.Fatalf("got  %s want %s", w.Body.String(), want)
	}
	if in.Rows[0].Names != nil || in.One.Index != nil || in.Loose["list"].([]int) != nil || in.Tags != nil {
		t.Fatal("the caller's value was modified")
	}
}

// WriteJSON cannot reach the fields of an embedded unexported struct (reflect
// will not set them), so a nil list there went out as null: the adoption
// response's packs did, 28 September. No response type may embed one.
func TestNoResponseEmbedsAnUnexportedStruct(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string)
	walk = func(rt reflect.Type, path string) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := range rt.NumField() {
			f := rt.Field(i)
			if f.Anonymous && !f.IsExported() && f.Type.Kind() == reflect.Struct {
				t.Errorf("%s embeds unexported %s; export it so its lists are written empty", path, f.Type)
			}
			if f.IsExported() || f.Anonymous {
				walk(f.Type, path+"."+f.Name)
			}
		}
	}
	for _, n := range wireTypes {
		walk(reflect.TypeOf(n.Value), n.Name)
	}
}

// A response type declared inside a handler cannot be registered, so the web
// never gets it generated and declares its own copy (the release and workflow
// lists did, until 28 September). Any struct with JSON tags declared inside a
// function in this package is a response, and must be declared at package
// level and registered.
func TestResponseTypesAreDeclaredWhereTheyCanBeGenerated(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ds, ok := n.(*ast.DeclStmt)
				if !ok {
					return true
				}
				for _, spec := range ds.Decl.(*ast.GenDecl).Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, field := range st.Fields.List {
						if field.Tag != nil && strings.Contains(field.Tag.Value, "json:") {
							t.Errorf("%s: %s in %s is a response declared inside a function; declare it at package level and register it", fset.Position(ts.Pos()), ts.Name.Name, fn.Name.Name)
							break
						}
					}
				}
				return true
			})
		}
	}
}

// Every response field is camelCase, as the API's other fields are; the one
// exception is an OpenVEX statement, whose field names are the standard's own.
//
// Kills: a response field named after a table (`conformance_reviews`).
func TestResponseFieldsAreCamelCase(t *testing.T) {
	got, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "wire.gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	field := regexp.MustCompile(`^\s+([A-Za-z0-9]+_[A-Za-z0-9_]+)\??:`)
	standard := map[string]bool{"action_statement": true, "impact_statement": true} // OpenVEX
	for i, line := range strings.Split(string(got), "\n") {
		if m := field.FindStringSubmatch(line); m != nil && !standard[m[1]] {
			t.Errorf("wire.gen.ts:%d: %s is not camelCase", i+1, m[1])
		}
	}
}
