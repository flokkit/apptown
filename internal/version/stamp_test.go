package version

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"testing"
)

// The build stamps the version with -ldflags -X by variable name, and the
// linker silently ignores a name that does not exist or is not a string
// variable: a renamed variable makes every build report dev-unknown. Each
// name the Makefile and the image build stamp must be a package-level
// string variable here.
func TestEveryStampNamesAVariable(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "version.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]bool{}
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.VAR {
			for _, s := range g.Specs {
				for _, n := range s.(*ast.ValueSpec).Names {
					vars[n.Name] = true
				}
			}
		}
	}
	stamp := regexp.MustCompile(`-X github\.com/flokkit/apptown/internal/version\.(\w+)=`)
	found := 0
	for _, path := range []string{"../../Makefile", "../../deploy/Dockerfile"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range stamp.FindAllSubmatch(b, -1) {
			found++
			if !vars[string(m[1])] {
				t.Errorf("%s stamps version.%s, which version.go does not declare", path, m[1])
			}
		}
	}
	if found == 0 {
		t.Fatal("no version stamps found in the Makefile or the image build")
	}
}
