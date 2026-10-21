//go:build ignore

// shape-exports lists the exported top-level names of the Go files it is
// given: types, functions (not methods), constants and variables. It uses
// Go's parser, so grouped declarations count once per name and comments or
// strings cannot look like declarations.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
)

func main() {
	found := []string{}
	fset := token.NewFileSet()
	for _, path := range os.Args[1:] {
		if path == "--" { // go run's separator between its files and ours
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					found = append(found, "func "+d.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							found = append(found, "type "+s.Name.Name)
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								found = append(found, d.Tok.String()+" "+n.Name)
							}
						}
					}
				}
			}
		}
	}
	sort.Strings(found)
	out, _ := json.Marshal(found)
	fmt.Println(string(out))
}
