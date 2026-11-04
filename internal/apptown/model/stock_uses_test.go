package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every literal stock model request has a named job on the profile page.
// Check the source, too: adding a new caller of an existing slot needs a name.
func TestStockResolverCallSitesHaveUsage(t *testing.T) {
	t.Parallel()
	covered := map[string]int{}
	for _, use := range StockUses {
		if use.Job == "" {
			t.Errorf("unnamed use of %s", use.Slot)
		}
		for _, source := range use.Sources {
			covered[source+":"+use.Slot]++
		}
	}
	seen := map[string]int{}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 3 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Resolve" {
				return true
			}
			arg, ok := call.Args[2].(*ast.BasicLit)
			if !ok || arg.Kind != token.STRING {
				return true
			}
			slot, err := strconv.Unquote(arg.Value)
			if err != nil {
				t.Fatal(err)
			}
			if slot == "" {
				slot = "main-strong"
			}
			if strings.Contains(slot, "/") {
				return true
			}
			source, err := filepath.Rel("..", path)
			if err != nil {
				t.Fatal(err)
			}
			key := filepath.ToSlash(source) + ":" + slot
			seen[key]++
			if seen[key] > covered[key] {
				t.Errorf("%s has no StockUses entry", key)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, count := range covered {
		if seen[key] < count {
			t.Errorf("StockUses entry %s has no resolver call site", key)
		}
	}
	if strings.Contains(Gloss["fast"], "summar") {
		t.Error("fast must not claim change summaries")
	}
}
