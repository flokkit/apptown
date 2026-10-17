//go:build ignore

// shape-methods lists production methods declared on App. Count declarations,
// not call sites or naming conventions; pointer and value receivers both count.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

func appReceiver(expr ast.Expr) bool {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name == "App"
	case *ast.StarExpr:
		return appReceiver(expr.X)
	case *ast.ParenExpr:
		return appReceiver(expr.X)
	default:
		return false
	}
}

func main() {
	paths := os.Args[1:]
	if len(paths) > 0 && paths[0] == "--" {
		paths = paths[1:]
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: shape-methods [--] file.go ...")
		os.Exit(2)
	}
	methods := []string{}
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || !appReceiver(fn.Recv.List[0].Type) {
				continue
			}
			methods = append(methods, fmt.Sprintf("%s: App.%s", fset.Position(fn.Name.Pos()), fn.Name.Name))
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(methods); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
