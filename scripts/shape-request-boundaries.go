//go:build ignore

// shape-request-boundaries locates request-body and registration bypasses.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	out := map[string][]string{"jsondecode": {}, "routepolicies": {}}
	fs := token.NewFileSet()
	for _, path := range os.Args[1:] {
		if path == "--" || strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fs, path, nil, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		base := filepath.Base(path)
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CompositeLit); ok {
				forbidden := false
				if typ, ok := c.Type.(*ast.Ident); ok {
					switch typ.Name {
					case "routeSpec", "requiredPolicy", "memberPolicy", "administratorPolicy", "exceptionalPolicy":
						forbidden = f.Name.Name == "apptown" && base != "routes.go"
					case "toolCommand":
						forbidden = f.Name.Name == "mcpapi" && base != "catalog.go"
					}
				}
				if typ, ok := c.Type.(*ast.ArrayType); ok && f.Name.Name == "apptown" && base != "routes.go" {
					if id, ok := typ.Elt.(*ast.Ident); ok && id.Name == "routeSpec" {
						for _, e := range c.Elts {
							if _, raw := e.(*ast.CompositeLit); raw {
								forbidden = true
							}
						}
					}
				}
				if forbidden {
					out["routepolicies"] = append(out["routepolicies"], fs.Position(c.Pos()).String()+": raw registration policy")
				}
			}
			return true
		})
		if f.Name.Name != "httpapi" {
			continue
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// The raw body contracts are the bounded opaque webhook payload and
			// the Git bundle a project starts from, which its owner spools
			// within the operator's limit (projectlifecycle.SeedFromBundle).
			if base == "json_command.go" && fn.Name.Name == "jsonCommand" || base == "webhook.go" && fn.Name.Name == "Webhook" ||
				base == "repository_setup.go" && fn.Name.Name == "SeedFromBundle" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				bypass := false
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Body" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Obj != nil {
						if field, ok := id.Obj.Decl.(*ast.Field); ok {
							if star, ok := field.Type.(*ast.StarExpr); ok {
								if typ, ok := star.X.(*ast.SelectorExpr); ok && typ.Sel.Name == "Request" {
									bypass = true
								}
							}
						}
					}
				}
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && (id.Name == "ReadJSON" || id.Name == "ReadJSONLimit") {
						bypass = true
					}
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "NewDecoder" {
						bypass = true
					}
				}
				if bypass {
					out["jsondecode"] = append(out["jsondecode"], fs.Position(n.Pos()).String()+": request decoding outside adapter")
				}
				return true
			})
		}
	}
	json.NewEncoder(os.Stdout).Encode(out)
}
