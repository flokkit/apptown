//go:build ignore

// Test-support packages are reachable only through test source imports.
package main

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	found := []string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "private" || d.Name() == "node_modules" || d.Name() == "vendor") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		name := filepath.Base(filepath.Dir(path))
		if strings.HasSuffix(name, "test") || strings.HasSuffix(name, "fake") {
			return nil
		}
		fs := token.NewFileSet()
		file, err := parser.ParseFile(fs, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			name := filepath.Base(p)
			if strings.HasPrefix(p, "github.com/flokkit/apptown/internal/") && (strings.HasSuffix(name, "test") || strings.HasSuffix(name, "fake")) {
				rel, _ := filepath.Rel(root, path)
				found = append(found, fmt.Sprintf("%s:%d: %s may be imported only by _test.go files or test-support packages", filepath.ToSlash(rel), fs.Position(imp.Pos()).Line, p))
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sort.Strings(found)
	if err := json.NewEncoder(os.Stdout).Encode(found); err != nil {
		panic(err)
	}
}
