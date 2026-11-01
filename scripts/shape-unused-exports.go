//go:build ignore

// shape-unused-exports inventories exported declarations and their typed uses.
// Test references include the tagged tiers; those tests must not lose owner APIs.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

const module = "github.com/flokkit/apptown/"

type reference struct {
	File      string
	Offset    int
	Package   string
	Test      bool
	Own       bool
	Value     bool
	External  bool
	ExprStart int
	ExprEnd   int
	ArgsStart int
	Import    string
}
type entry struct {
	ID         string
	Package    string
	Name       string
	Kind       string
	File       string
	Offset     int
	Line       int
	Category   string
	Reason     string
	Production bool
	Receiver   string
	References []reference
}

func basePackage(s string) string { return strings.TrimSuffix(strings.Split(s, " [")[0], "_test") }
func support(s string) bool {
	n := filepath.Base(s)
	return strings.HasSuffix(n, "test") || strings.HasSuffix(n, "fake")
}
func named(t types.Type) *types.Named {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, _ := t.(*types.Named)
	return n
}

// Dynamic protocols use anonymous interfaces inside dependency implementations,
// so imported package scopes do not expose every required method spelling.
func dynamicProtocol(fn *types.Func) bool {
	sig := fn.Type().(*types.Signature)
	errorType := types.Universe.Lookup("error").Type()
	if sig.Results().Len() != 1 {
		return false
	}
	result := sig.Results().At(0).Type()
	switch fn.Name() {
	case "Unwrap":
		if sig.Params().Len() != 0 {
			return false
		}
		if types.Identical(result, errorType) {
			return true
		}
		if slice, ok := result.(*types.Slice); ok {
			return types.Identical(slice.Elem(), errorType)
		}
		if n, ok := result.(*types.Named); ok {
			return n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "net/http" && n.Obj().Name() == "ResponseWriter"
		}
	case "Is":
		return sig.Params().Len() == 1 && types.Identical(sig.Params().At(0).Type(), errorType) && types.Identical(result, types.Typ[types.Bool])
	case "As":
		if sig.Params().Len() != 1 || !types.Identical(result, types.Typ[types.Bool]) {
			return false
		}
		it, ok := sig.Params().At(0).Type().Underlying().(*types.Interface)
		return ok && it.NumMethods() == 0
	case "ExitCode":
		return sig.Params().Len() == 0 && types.Identical(result, types.Typ[types.Int])
	}
	return false
}

// linkerStamped names the variables the build sets with -ldflags -X: no Go
// code refers to them, and -X silently ignores a name that is not there.
var linkerStamped = map[string]bool{}

var stampFlag = regexp.MustCompile(`-X ([\w./-]+)\.(\w+)=`)

func main() {
	root, err := os.Getwd()
	for _, build := range []string{"Makefile", "deploy/Dockerfile"} {
		if b, err := os.ReadFile(filepath.Join(root, build)); err == nil {
			for _, m := range stampFlag.FindAllStringSubmatch(string(b), -1) {
				linkerStamped[m[1]+"."+m[2]] = true
			}
		}
	}
	if err != nil {
		panic(err)
	}
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports, Tests: true, BuildFlags: []string{"-tags=integration,journey,browser,pi,inference_docker"}}
	patterns := os.Args[1:]
	if len(patterns) > 0 && patterns[0] == "-production" {
		cfg.Tests = false
		patterns = patterns[1:]
	}
	if len(patterns) > 0 && patterns[0] == "--" {
		patterns = patterns[1:]
	}
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		panic(err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		os.Exit(1)
	}
	rows := map[string]*entry{}
	key := func(o types.Object, fs *token.FileSet) string {
		p := fs.Position(o.Pos())
		return fmt.Sprintf("%s:%d", p.Filename, p.Offset)
	}
	rel := func(f string) string {
		r, e := filepath.Rel(root, f)
		if e != nil {
			return f
		}
		return filepath.ToSlash(r)
	}
	var interfaces []*types.Interface
	reflective := map[types.Type]bool{}
	directReflection := map[string]bool{}
	embedded := map[string]bool{}
	var reflectType func(types.Type)
	reflectType = func(t types.Type) {
		if t == nil || reflective[t] {
			return
		}
		reflective[t] = true
		switch t := t.(type) {
		case *types.Named:
			reflectType(t.Underlying())
		case *types.Alias:
			reflectType(types.Unalias(t))
		case *types.Pointer:
			reflectType(t.Elem())
		case *types.Slice:
			reflectType(t.Elem())
		case *types.Array:
			reflectType(t.Elem())
		case *types.Map:
			reflectType(t.Key())
			reflectType(t.Elem())
		case *types.Struct:
			for i := 0; i < t.NumFields(); i++ {
				if t.Field(i).Exported() {
					reflectType(t.Field(i).Type())
				}
			}
		}
	}
	seenPackages := map[*types.Package]bool{}
	var collect func(*types.Package)
	collect = func(p *types.Package) {
		if p == nil || seenPackages[p] {
			return
		}
		seenPackages[p] = true
		for _, s := range p.Scope().Names() {
			o := p.Scope().Lookup(s)
			if n, ok := o.(*types.TypeName); ok {
				if it, ok := n.Type().Underlying().(*types.Interface); ok && it.NumMethods() > 0 {
					interfaces = append(interfaces, it)
				}
			}
		}
		for _, i := range p.Imports() {
			collect(i)
		}
	}
	objects := map[string]types.Object{}
	for _, p := range pkgs {
		collect(p.Types)
		// Reflection in tests also constrains the production declaration's spelling.
		for _, f := range p.Syntax {
			ast.Inspect(f, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				fn, ok := p.TypesInfo.Uses[sel.Sel].(*types.Func)
				if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "reflect" || (fn.Name() != "TypeOf" && fn.Name() != "ValueOf") {
					return true
				}
				n := named(p.TypesInfo.TypeOf(call.Args[0]))
				if n == nil {
					return true
				}
				st, ok := n.Underlying().(*types.Struct)
				if !ok {
					return true
				}
				directReflection[key(n.Obj(), p.Fset)] = true
				for i := 0; i < st.NumFields(); i++ {
					directReflection[key(st.Field(i), p.Fset)] = true
				}
				return true
			})
		}
		if !strings.HasPrefix(p.PkgPath, module+"internal/") {
			continue
		}
		for _, tv := range p.TypesInfo.Types {
			if tv.Type != nil {
				if it, ok := tv.Type.Underlying().(*types.Interface); ok && it.NumMethods() > 0 {
					interfaces = append(interfaces, it)
				}
			}
		}
		for _, f := range p.Syntax {
			file := p.Fset.Position(f.Pos()).Filename
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			add := func(id *ast.Ident, kind, owner, reason string) {
				o := p.TypesInfo.Defs[id]
				if o == nil || !o.Exported() {
					return
				}
				k := key(o, p.Fset)
				if rows[k] != nil {
					return
				}
				pos := p.Fset.Position(id.Pos())
				name := id.Name
				if owner != "" {
					name = owner + "." + name
				}
				r := &entry{ID: k, Package: basePackage(p.PkgPath), Name: name, Kind: kind, File: rel(file), Offset: pos.Offset, Line: pos.Line, References: []reference{}}
				if ast.IsGenerated(f) {
					reason = "generated code"
				}
				if support(r.Package) {
					reason = "deliberate test-support package"
				}
				if owner == "" && linkerStamped[p.PkgPath+"."+id.Name] {
					reason = "set by the linker at build (-X)"
				}
				if reason != "" {
					r.Category = "excluded"
					r.Reason = reason
				}
				if fn, ok := o.(*types.Func); ok {
					if recv := fn.Type().(*types.Signature).Recv(); recv != nil {
						r.Receiver = types.TypeString(recv.Type(), func(*types.Package) string { return "" })
					}
				}
				rows[k] = r
				objects[k] = o
			}
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					owner := ""
					reason := ""
					if fn, ok := p.TypesInfo.Defs[d.Name].(*types.Func); ok {
						if recv := fn.Type().(*types.Signature).Recv(); recv != nil {
							if n := named(recv.Type()); n != nil {
								owner = n.Obj().Name()
							}
						}
					}
					add(d.Name, "func", owner, reason)
				case *ast.GenDecl:
					for _, s := range d.Specs {
						switch s := s.(type) {
						case *ast.ValueSpec:
							for _, id := range s.Names {
								add(id, d.Tok.String(), "", "")
							}
						case *ast.TypeSpec:
							add(s.Name, "type", "", "")
							switch t := s.Type.(type) {
							case *ast.StructType:
								// Value records and wire structs retain exported fields. Stateful
								// services and composition holders are checked field by field.
								record := true
								for _, field := range t.Fields.List {
									switch field.Type.(type) {
									case *ast.FuncType:
										record = false
									}
									for _, n := range field.Names {
										if !n.IsExported() {
											record = false
										}
									}
								}
								if s.Name.Name == "Service" || s.Name.Name == "App" {
									record = false
								}
								tagged := false
								for _, field := range t.Fields.List {
									if field.Tag != nil {
										tagged = true
									}
								}
								if tagged {
									reflectType(p.TypesInfo.Defs[s.Name].Type())
								}
								for _, field := range t.Fields.List {
									if len(field.Names) == 0 {
										t := p.TypesInfo.TypeOf(field.Type)
										if n := named(t); n != nil {
											embedded[key(n.Obj(), p.Fset)] = true
										}
									}
								}
								for _, field := range t.Fields.List {
									reason := ""
									if record || tagged {
										reason = "wire/record field"
									}
									for _, id := range field.Names {
										add(id, "field", s.Name.Name, reason)
									}
								}
							case *ast.InterfaceType:
								for _, field := range t.Methods.List {
									for _, id := range field.Names {
										add(id, "method", s.Name.Name, "interface contract")
									}
								}
							}
						}
					}
				}
			}
			// Anonymous records and embedded fields are contracts too. They have
			// definitions in types.Info even though no top-level TypeSpec owns them.
			for id, o := range p.TypesInfo.Defs {
				if id == nil || o == nil || p.Fset.Position(id.Pos()).Filename != file {
					continue
				}
				if v, ok := o.(*types.Var); ok && v.IsField() {
					add(id, "field", "", "wire/record or embedded field")
				}
			}
		}
	}
	// A method's exported spelling is part of every interface it implements,
	// including interfaces supplied by the standard library and dependencies.
	byName := map[string][]*types.Interface{}
	for _, it := range interfaces {
		for i := 0; i < it.NumMethods(); i++ {
			byName[it.Method(i).Name()] = append(byName[it.Method(i).Name()], it)
		}
	}
	for k, r := range rows {
		if directReflection[k] {
			r.Category = "excluded"
			r.Reason = "declaration reached through reflection"
		}
		if embedded[k] {
			r.Category = "excluded"
			r.Reason = "embedded field contract"
		}
		if o, ok := objects[k].(*types.TypeName); ok && reflective[o.Type()] {
			r.Category = "excluded"
			r.Reason = "type reached through wire tags/reflection"
		}
		if r.Category != "" {
			continue
		}
		fn, ok := objects[k].(*types.Func)
		if !ok {
			continue
		}
		recv := fn.Type().(*types.Signature).Recv()
		if recv != nil && dynamicProtocol(fn) {
			r.Category = "excluded"
			r.Reason = "standard library dynamic interface contract"
			continue
		}
		if recv == nil {
			continue
		}
		n := named(recv.Type())
		if n == nil {
			continue
		}
		for _, it := range byName[fn.Name()] {
			if types.Implements(n, it) || types.Implements(types.NewPointer(n), it) {
				r.Category = "excluded"
				r.Reason = "implements an interface"
				break
			}
		}
	}
	seen := map[string]bool{}
	for _, p := range pkgs {
		calls := map[*ast.Ident]bool{}
		methodCalls := map[*ast.Ident]*ast.CallExpr{}
		imports := map[string]string{}
		for _, f := range p.Syntax {
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, "\"")
				alias := filepath.Base(path)
				if imp.Name != nil {
					alias = imp.Name.Name
				}
				imports[path] = alias
			}
		}
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					var callee func(ast.Expr)
					callee = func(e ast.Expr) {
						switch e := e.(type) {
						case *ast.Ident:
							calls[e] = true
						case *ast.SelectorExpr:
							calls[e.Sel] = true
							methodCalls[e.Sel] = c
						case *ast.IndexExpr:
							callee(e.X)
						case *ast.IndexListExpr:
							callee(e.X)
						case *ast.ParenExpr:
							callee(e.X)
						}
					}
					callee(c.Fun)
				}
				return true
			})
		}
		for id, o := range p.TypesInfo.Uses {
			if o.Pkg() == nil || !strings.HasPrefix(o.Pkg().Path(), module+"internal/") {
				continue
			}
			if fn, ok := o.(*types.Func); ok {
				o = fn.Origin()
			}
			if v, ok := o.(*types.Var); ok {
				o = v.Origin()
			}
			k := key(o, p.Fset)
			r := rows[k]
			if r == nil {
				continue
			}
			pos := p.Fset.Position(id.Pos())
			test := strings.HasSuffix(pos.Filename, "_test.go") || support(basePackage(p.PkgPath))
			own := basePackage(p.PkgPath) == r.Package
			rk := fmt.Sprintf("%s:%d", pos.Filename, pos.Offset)
			if seen[rk] {
				continue
			}
			seen[rk] = true
			_, fn := o.(*types.Func)
			value := fn && !calls[id]
			ref := reference{File: rel(pos.Filename), Offset: pos.Offset, Package: basePackage(p.PkgPath), Test: test, Own: own, Value: value, External: strings.HasSuffix(p.Name, "_test"), Import: imports[r.Package]}
			if c := methodCalls[id]; c != nil {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
					ref.ExprStart = p.Fset.Position(sel.X.Pos()).Offset
					ref.ExprEnd = p.Fset.Position(sel.X.End()).Offset
					ref.ArgsStart = p.Fset.Position(c.Lparen).Offset + 1
				}
			}
			r.References = append(r.References, ref)
			if !test && !own {
				r.Production = true
			}
			if value && r.Category == "" {
				r.Category = "excluded"
				r.Reason = "function used as a value"
			}
		}
	}
	result := []*entry{}
	for _, r := range rows {
		if r.Category == "" {
			r.Category = "a"
			r.Reason = "used only within its package"
			for _, ref := range r.References {
				if !ref.Own {
					r.Category = "review"
					r.Reason = "used by another package's tests"
					break
				}
			}
		}
		sort.Slice(r.References, func(i, j int) bool {
			a, b := r.References[i], r.References[j]
			if a.File == b.File {
				return a.Offset < b.Offset
			}
			return a.File < b.File
		})
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.File == b.File {
			return a.Offset < b.Offset
		}
		return a.File < b.File
	})
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		panic(err)
	}
}
