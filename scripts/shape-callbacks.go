//go:build ignore

// Inventory dependency bindings that retain App. This is local syntax analysis,
// not general escape analysis.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
)

type inventory struct {
	fs        *token.FileSet
	holders   map[string]bool
	callbacks map[string]bool
	methods   map[string]bool
	aliases   map[*ast.Object]ast.Expr
	apps      map[*ast.Object]bool
	found     []string
}

func name(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return name(e.X)
	case *ast.SelectorExpr:
		return name(e.X) + "." + e.Sel.Name
	case *ast.ArrayType:
		return "[]" + name(e.Elt)
	case *ast.MapType:
		return "map[" + name(e.Key) + "]" + name(e.Value)
	}
	return "anonymous"
}
func (v *inventory) app(e ast.Expr, seen map[*ast.Object]bool) bool {
	switch e := e.(type) {
	case *ast.ParenExpr:
		return v.app(e.X, seen)
	case *ast.Ident:
		if e.Obj != nil && v.apps[e.Obj] {
			return true
		}
		if e.Obj != nil && !seen[e.Obj] {
			seen[e.Obj] = true
			return v.app(v.aliases[e.Obj], seen)
		}
	}
	return false
}
func (v *inventory) retains(e ast.Expr, seen map[*ast.Object]bool) bool {
	switch e := e.(type) {
	case *ast.ParenExpr:
		return v.retains(e.X, seen)
	case *ast.UnaryExpr:
		return v.retains(e.X, seen)
	case *ast.Ident:
		if e.Obj != nil && !seen[e.Obj] {
			seen[e.Obj] = true
			return v.retains(v.aliases[e.Obj], seen)
		}
	case *ast.CompositeLit:
		return v.holders[name(e.Type)]
	case *ast.SelectorExpr:
		return v.app(e.X, map[*ast.Object]bool{}) && v.methods[e.Sel.Name]
	case *ast.FuncLit:
		retained := false
		ast.Inspect(e.Body, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && v.app(id, map[*ast.Object]bool{}) {
				retained = true
			}
			return !retained
		})
		return retained
	case *ast.CallExpr:
		// A named local helper returning a callback may bind App in a closure.
		// Ordinary calls (including immediately invoked closures) are not captures.
		if v.callbacks[name(e.Fun)] {
			for _, arg := range e.Args {
				if v.app(arg, map[*ast.Object]bool{}) || v.retains(arg, seen) {
					return true
				}
			}
		}
	}
	return false
}
func (v *inventory) walk(n ast.Node) {
	ast.Inspect(n, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			callee := name(x.Fun)
			base := callee[strings.LastIndex(callee, ".")+1:]
			if strings.HasPrefix(base, "New") || strings.HasPrefix(base, "new") {
				for i, arg := range x.Args {
					if v.retains(arg, map[*ast.Object]bool{}) {
						v.found = append(v.found, fmt.Sprintf("%s: %s.argument%d retains App", v.fs.Position(arg.Pos()), callee, i))
					}
				}
			}
		case *ast.AssignStmt:
			if len(x.Lhs) == len(x.Rhs) {
				for i, lhs := range x.Lhs {
					if indexed, ok := lhs.(*ast.IndexExpr); ok && v.retains(x.Rhs[i], map[*ast.Object]bool{}) {
						v.found = append(v.found, fmt.Sprintf("%s: %s.registration retains App", v.fs.Position(lhs.Pos()), name(indexed.X)))
					}
				}
			}
		}
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		owner := name(lit.Type)
		// These objects implement transport itself, rather than a subsystem port.
		if owner == "App" || owner == "mcpCall" || owner == "http.Server" || owner == "websocket.Upgrader" || owner == "httputil.ReverseProxy" {
			return false
		}
		if v.holders[owner] {
			return false
		} // counted at its installation, not its fields
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if v.retains(kv.Value, map[*ast.Object]bool{}) {
				v.found = append(v.found, fmt.Sprintf("%s: %s.%s retains App", v.fs.Position(kv.Pos()), owner, name(kv.Key)))
			} else {
				v.walk(kv.Value)
			}
		}
		return false
	})
}
func main() {
	v := inventory{fs: token.NewFileSet(), holders: map[string]bool{}, callbacks: map[string]bool{}, methods: map[string]bool{}, aliases: map[*ast.Object]ast.Expr{}, apps: map[*ast.Object]bool{}}
	files := []*ast.File{}
	for _, path := range os.Args[2:] {
		f, err := parser.ParseFile(v.fs, path, nil, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		files = append(files, f)
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.TypeSpec:
				if s, ok := n.Type.(*ast.StructType); ok {
					for _, field := range s.Fields.List {
						if name(field.Type) == "App" {
							v.holders[n.Name.Name] = true
						}
					}
				}
				if _, ok := n.Type.(*ast.FuncType); ok {
					v.callbacks[n.Name.Name] = true
				}
			case *ast.FuncDecl:
				if n.Body != nil {
					ast.Inspect(n.Body, func(node ast.Node) bool {
						if ret, ok := node.(*ast.ReturnStmt); ok {
							for _, expr := range ret.Results {
								if _, ok := expr.(*ast.FuncLit); ok {
									v.callbacks[n.Name.Name] = true
								}
							}
						}
						return true
					})
				}
				if n.Recv != nil && name(n.Recv.List[0].Type) == "App" {
					v.methods[n.Name.Name] = true
				}
				if n.Type.Results != nil {
					for _, r := range n.Type.Results.List {
						if _, ok := r.Type.(*ast.FuncType); ok {
							v.callbacks[n.Name.Name] = true
						}
					}
				}
			case *ast.Field:
				if name(n.Type) == "App" {
					for _, id := range n.Names {
						if id.Obj != nil {
							v.apps[id.Obj] = true
						}
					}
				}
			case *ast.AssignStmt:
				if len(n.Lhs) == len(n.Rhs) {
					for i, lhs := range n.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && id.Obj != nil {
							v.aliases[id.Obj] = n.Rhs[i]
						}
					}
				}
			case *ast.ValueSpec:
				if len(n.Names) == len(n.Values) {
					for i, id := range n.Names {
						v.aliases[id.Obj] = n.Values[i]
					}
				}
			}
			return true
		})
	}
	// Resolve helpers returning a named function type after collecting type names.
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Type.Results != nil {
				for _, r := range fn.Type.Results.List {
					if v.callbacks[name(r.Type)] {
						v.callbacks[fn.Name.Name] = true
					}
				}
			}
		}
	}
	for _, f := range files {
		v.walk(f)
	}
	sort.Strings(v.found)
	if v.found == nil {
		v.found = []string{}
	}
	if err := json.NewEncoder(os.Stdout).Encode(v.found); err != nil {
		panic(err)
	}
}
