package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gate image builds from a context holding only go.mod and the module's
// own packages the gate imports (the build has no go.sum, so it cannot fetch
// anything). Every module package the gate reaches must be copied by the
// Dockerfile and let through by its .dockerignore, or the image stops
// building, as it did when the gate first imported internal/clock.
func TestTheGateImageCopiesEveryModulePackageTheGateImports(t *testing.T) {
	const module = "github.com/flokkit/apptown/"
	root := filepath.Join("..", "..")
	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	ignore, err := os.ReadFile("Dockerfile.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	todo := []string{"cmd/apptown-gate"}
	for len(todo) > 0 {
		dir := todo[0]
		todo = todo[1:]
		if seen[dir] {
			continue
		}
		seen[dir] = true
		files, _ := filepath.Glob(filepath.Join(root, dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range src.Imports {
				if p := strings.Trim(imp.Path.Value, `"`); strings.HasPrefix(p, module) {
					todo = append(todo, strings.TrimPrefix(p, module))
				}
			}
			name := filepath.ToSlash(strings.TrimPrefix(f, root+string(filepath.Separator)))
			if !strings.Contains(string(dockerfile), name) && !strings.Contains(string(dockerfile), "COPY "+dir+"/ ") {
				t.Errorf("the Dockerfile does not copy %s", name)
			}
			if !strings.Contains(string(ignore), "!"+name) && !strings.Contains(string(ignore), "!"+dir+"/\n") {
				t.Errorf("Dockerfile.dockerignore keeps out %s", name)
			}
		}
	}
}
