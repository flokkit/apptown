package dockerlabel

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Names in string literals that share AppTown. prefix but are not labels:
// each is refused as a label key elsewhere by this list's reason.
var notLabels = map[string]string{
	"forge.example":    "an example hostname in help and errors",
	"forge.local":      "a hostname",
	"forge.flokkit.io": "a hostname",
	"forge.id":         "AppTown's identity setting",
	"forge.operation":  "the job queue's kind for an operation",
}

var forgeName = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])(forge\.[a-z][a-z0-9.-]*[a-z0-9])`)

// spelledLabels returns each AppTown.<name> written in a string literal of a Go
// source, after unquoting, so an escaped "\"forge.class\"" in a --format
// template is seen as a raw-string one is. Telemetry attribute names are not
// labels: literals inside attribute.X(...) calls and attrs[...] are skipped.
func spelledLabels(t *testing.T, name string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "attribute" {
					return false
				}
			}
		case *ast.IndexExpr:
			if x, ok := n.X.(*ast.Ident); ok && x.Name == "attrs" {
				return false
			}
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(n.Value)
			if err != nil {
				return true
			}
			for _, m := range forgeName.FindAllStringSubmatch(v, -1) {
				if _, ok := notLabels[m[1]]; !ok {
					found = append(found, fmt.Sprintf("%s: %s", fset.Position(n.Pos()), m[1]))
				}
			}
		}
		return true
	})
	return found
}

// Forge's label keys are spelled here only: an AppTown.<name> in a string
// literal of production Go elsewhere, known key or not, would be a second
// list that can drift from the containers already out in the world.
//
// Kills: a writer or reader that spells its own label key, including a new
// or misspelled one and one inside an escaped --format template.
func TestNoLabelKeyIsSpelledOutsideThisPackage(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.Contains(p, filepath.Join("internal", "dockerlabel")) {
				return err
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for _, f := range spelledLabels(t, p, src) {
				t.Errorf("spells a label key: %s", f)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The scan sees the spellings a maintainer would write (SN-C01): an escaped
// --format template, a misspelled key, a raw-string template; and it leaves
// telemetry attributes alone.
func TestTheLabelScanSeesEverySpelling(t *testing.T) {
	src := []byte(`package p

func f(attrs map[string]string) []string {
	attrs["forge.instance"] = "x"
	_ = attribute.String("forge.project", "x")
	return []string{
		"{{.Names}}\t{{.Label \"forge.class\"}}\t{{.Label \"forge.created\"}}",
		"forge.inferenc=1",
		` + "`{{index .Config.Labels \"forge.owner\"}}`" + `,
		"forge.example",
	}
}
`)
	got := strings.Join(spelledLabels(t, "p.go", src), "\n")
	for _, want := range []string{"forge.class", "forge.created", "forge.inferenc", "forge.owner"} {
		if !strings.Contains(got, want) {
			t.Errorf("not seen: %s\n%s", want, got)
		}
	}
	for _, not := range []string{"forge.instance", "forge.project", "forge.example"} {
		if strings.Contains(got, not) {
			t.Errorf("not a label, but refused: %s\n%s", not, got)
		}
	}
}

// The agent image's labels are read by these keys, so its build writes
// every one of them.
//
// Kills: an image key the Dockerfile never sets (a step would record no version).
func TestTheAgentImageWritesEveryImageKey(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "deploy", "apptown-agent", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range ImageKeys {
		if !regexp.MustCompile(`(?m)^LABEL ` + regexp.QuoteMeta(k) + `=`).Match(src) {
			t.Errorf("deploy/apptown-agent/Dockerfile does not set %s", k)
		}
	}
}

// Keys are lowercase and dotted, and none is listed twice.
func TestKeysAreOneCaseAndDistinct(t *testing.T) {
	shape := regexp.MustCompile(`^forge(\.[a-z][a-z-]*)+$`)
	seen := map[string]bool{}
	for _, k := range Keys {
		if !shape.MatchString(k) || seen[k] {
			t.Errorf("%s: not lowercase-dotted, or listed twice", k)
		}
		seen[k] = true
	}
	for _, k := range ImageKeys {
		if !seen[k] {
			t.Errorf("image key %s is not in Keys", k)
		}
	}
}
