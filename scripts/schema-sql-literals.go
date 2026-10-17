//go:build ignore

// Extract Go string literals and dynamic Exec arguments without reading comments
// as code. Ownership is checked separately; this tool does not parse SQL.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type item struct {
	File            string `json:"file"`
	Line            int    `json:"line"`
	Declaration     string `json:"declaration"`
	Text            string `json:"text"`
	Dynamic         bool   `json:"dynamic,omitempty"`
	Call            string `json:"call,omitempty"`
	DeclarationHash string `json:"declaration_hash"`
}

func scan(root string, tests bool) ([]item, error) {
	out := []item{}
	for _, base := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, base), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") != tests {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fs := token.NewFileSet()
			f, err := parser.ParseFile(fs, path, src, parser.ParseComments)
			if err != nil {
				return err
			}
			if ast.IsGenerated(f) {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			for _, decl := range f.Decls {
				name := ""
				switch node := decl.(type) {
				case *ast.FuncDecl:
					name = node.Name.Name
					if node.Recv != nil {
						t := node.Recv.List[0].Type
						if star, ok := t.(*ast.StarExpr); ok {
							t = star.X
						}
						if id, ok := t.(*ast.Ident); ok {
							name = id.Name + "." + name
						}
					}
				case *ast.GenDecl:
					for _, spec := range node.Specs {
						if v, ok := spec.(*ast.ValueSpec); ok {
							for _, n := range v.Names {
								name += n.Name + ","
							}
						}
					}
					name = strings.TrimSuffix(name, ",")
				}
				start, end := fs.Position(decl.Pos()).Offset, fs.Position(decl.End()).Offset
				hash := sha256.Sum256(src[start:end])
				fingerprint := hex.EncodeToString(hash[:])
				// Track the innermost enclosing call so a caller can tell an
				// executed statement from a string that merely NAMES one — a
				// fault-injection selector is not SQL the test runs.
				stack := []string{}
				emit := func(n ast.Node, text string, dynamic bool) {
					call := ""
					for i := len(stack) - 1; i >= 0; i-- {
						if stack[i] != "" {
							call = stack[i]
							break
						}
					}
					out = append(out, item{rel, fs.Position(n.Pos()).Line, name, text, dynamic, call, fingerprint})
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					if n == nil {
						if len(stack) > 0 {
							stack = stack[:len(stack)-1]
						}
						return true
					}
					if c, ok := n.(*ast.CallExpr); ok {
						fn := ""
						switch f := c.Fun.(type) {
						case *ast.Ident:
							fn = f.Name
						case *ast.SelectorExpr:
							fn = f.Sel.Name
						}
						stack = append(stack, fn)
					} else {
						stack = append(stack, "")
					}
					if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						value, err := strconv.Unquote(lit.Value)
						if err == nil {
							emit(lit, value, false)
						}
					}
					if c, ok := n.(*ast.CallExpr); ok {
						if sel, ok := c.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Exec" || sel.Sel.Name == "ExecContext") {
							i := 0
							if sel.Sel.Name == "ExecContext" {
								i = 1
							}
							if len(c.Args) > i {
								arg := c.Args[i]
								if _, literal := arg.(*ast.BasicLit); !literal {
									emit(arg, string(src[fs.Position(arg.Pos()).Offset:fs.Position(arg.End()).Offset]), true)
								}
							}
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func main() {
	// -tests reads the _test.go files instead of the production ones, so the
	// test-SQL gate and the handwritten gate share one parser and one idea of
	// what a statement is.
	args, tests := os.Args[1:], false
	if len(args) > 0 && args[0] == "-tests" {
		args, tests = args[1:], true
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: schema-sql-literals [-tests] root")
		os.Exit(2)
	}
	items, err := scan(args[0], tests)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(items); err != nil {
		panic(err)
	}
}
