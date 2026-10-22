//go:build ignore

// shape-aliases reports declarations that alias subsystem names. It uses Go's
// parser so local assignments, comments and strings cannot look like aliases.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: shape-aliases subsystem,... file.go ...")
		os.Exit(2)
	}
	subsystems := map[string]bool{}
	for _, name := range strings.Split(os.Args[1], ",") {
		subsystems["github.com/flokkit/apptown/internal/"+name] = true         // a library (console)
		subsystems["github.com/flokkit/apptown/internal/apptown/"+name] = true // an AppTown subsystem
	}
	found := []string{}
	fset := token.NewFileSet()
	for _, path := range os.Args[2:] {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		imports := map[string]bool{}
		for _, imp := range file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !subsystems[path] {
				continue
			}
			name := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imports[name] = true
		}
		record := func(name *ast.Ident, expr ast.Expr) {
			for {
				paren, ok := expr.(*ast.ParenExpr)
				if !ok {
					break
				}
				expr = paren.X
			}
			selector, ok := expr.(*ast.SelectorExpr)
			if !ok {
				return
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || !imports[pkg.Name] {
				return
			}
			pos := fset.Position(name.Pos())
			found = append(found, fmt.Sprintf("%s:%d: %s = %s.%s", path, pos.Line, name.Name, pkg.Name, selector.Sel.Name))
		}
		for _, decl := range file.Decls {
			decl, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range decl.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					if spec.Assign.IsValid() {
						record(spec.Name, spec.Type)
					}
				case *ast.ValueSpec:
					if len(spec.Names) == len(spec.Values) {
						for i, name := range spec.Names {
							record(name, spec.Values[i])
						}
					}
				}
			}
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(found); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
