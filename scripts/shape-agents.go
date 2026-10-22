//go:build ignore

// Guard the agent policy/harness boundaries using Go syntax and import paths.
// This is not whole-program effect analysis: callback semantics still need review.
package main

import (
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

type findings struct {
	Policy     []string `json:"policy"`
	Settlement []string `json:"settlement"`
}

func inside(path, dir string) bool { return path == dir || strings.HasPrefix(path, dir+"/") }

func main() {
	result := findings{Policy: []string{}, Settlement: []string{}}
	fs := token.NewFileSet()
	for _, path := range os.Args[2:] {
		clean := "/" + filepath.ToSlash(path)
		policy := strings.Contains(clean, "/agentenv/")
		harness := strings.Contains(clean, "/agentharness/")
		if !policy && !harness {
			continue
		}
		f, err := parser.ParseFile(fs, path, nil, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		add := func(dst *[]string, pos token.Pos, message string) {
			*dst = append(*dst, fmt.Sprintf("%s: %s", fs.Position(pos), message))
		}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if policy && (inside(p, "github.com/flokkit/apptown/internal/apptown/agentharness") || inside(p, "github.com/flokkit/apptown/internal/console/pi")) {
				add(&result.Policy, im.Pos(), "environment imports harness "+p)
			}
			// Adapters consume public environment facts, not domain services or facades
			// that could settle work on their behalf. An aliased/dot import is still an edge.
			if harness && inside(p, "github.com/flokkit/apptown/internal/apptown") && !inside(p, "github.com/flokkit/apptown/internal/apptown/agentharness") && !inside(p, "github.com/flokkit/apptown/internal/apptown/agentenv") {
				add(&result.Settlement, im.Pos(), "harness imports domain or composition owner "+p)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if policy {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					value, _ := strconv.Unquote(lit.Value)
					for _, forbidden := range []string{".pi/", "pi-home", "pi-subagents", "@narumitw/pi-lsp", "defaultProjectTrust", "disableBuiltins", "settings.json", "models.json"} {
						if strings.Contains(value, forbidden) {
							add(&result.Policy, lit.Pos(), "environment contains Pi configuration artifact "+forbidden)
							break
						}
					}
				}
			}
			if harness {
				// Known settlement handles must not be disguised behind a local interface.
				if iface, ok := n.(*ast.InterfaceType); ok {
					for _, field := range iface.Methods.List {
						for _, method := range field.Names {
							switch method.Name {
							case "Claim", "Finish", "RecordNotice", "AddArtifact", "ApplyCompletion":
								add(&result.Settlement, method.Pos(), "harness accepts workflow capability "+method.Name)
							}
						}
					}
				}
			}
			return true
		})
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
