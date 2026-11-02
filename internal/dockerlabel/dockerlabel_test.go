package dockerlabel

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Names in string literals that share a label prefix but are not labels:
// each is refused as a label key elsewhere by this list's reason.
var notLabels = map[string]string{
	"forge.example":     "an example hostname in help and errors",
	"forge.local":       "a hostname",
	"forge.flokkit.io":  "a hostname",
	"forge.id":          "AppTown's identity setting",
	"forge.operation":   "the job queue's kind for an operation",
	"apptown.dev":       "AppTown's site, in schema ids and links",
	"apptown.local":     "a hostname",
	"apptown.cancelled": "a database driver's reason for a cancelled query",
}

// A label key, as AppTown writes it now (apptown.) or wrote it before 1.0
// (forge.): either spelled outside this package is a second list.
var apptownName = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])((?:apptown|forge)\.[a-z][a-z0-9.-]*[a-z0-9])`)

// spelledLabels returns each apptown.<name> or forge.<name> written in a string literal of a Go
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
			for _, m := range apptownName.FindAllStringSubmatch(v, -1) {
				if _, ok := notLabels[m[1]]; !ok {
					found = append(found, fmt.Sprintf("%s: %s", fset.Position(n.Pos()), m[1]))
				}
			}
		}
		return true
	})
	return found
}

// AppTown's label keys are spelled here only: an apptown.<name> or forge.<name> in a string
// literal of production Go elsewhere, known key or not, would be a second
// list that can drift from the containers already out in the world.
//
// Kills: a writer or reader that spells its own label key, including a new
// or misspelled one and one inside an escaped --format template.
func TestNoLabelKeyIsSpelledOutsideThisPackage(t *testing.T) {
	// Any exported constant can be used by a reader. Informational keys must
	// stay private, and every private label key belongs to that namespace.
	src, err := os.ReadFile("dockerlabel.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "dockerlabel.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.GenDecl)
		if !ok || d.Tok != token.CONST {
			return true
		}
		for _, spec := range d.Specs {
			v := spec.(*ast.ValueSpec)
			for i, value := range v.Values {
				lit, ok := value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				key, err := strconv.Unquote(lit.Value)
				if err != nil || !strings.HasPrefix(key, "apptown.") {
					continue
				}
				info := strings.HasPrefix(key, "apptown.info.")
				if info == ast.IsExported(v.Names[i].Name) {
					t.Errorf("%s: informational keys must be private; system keys must be exported and outside apptown.info.*", v.Names[i].Name)
				}
			}
		}
		return false
	})
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
	attrs["apptown.instance"] = "x"
	_ = attribute.String("apptown.project", "x")
	return []string{
		"{{.Names}}\t{{.Label \"apptown.class\"}}\t{{.Label \"apptown.created\"}}",
		"apptown.inferenc=1",
		` + "`{{index .Config.Labels \"apptown.owner\"}}`" + `,
		"forge.environment",
		"forge.example",
	}
}
`)
	got := strings.Join(spelledLabels(t, "p.go", src), "\n")
	for _, want := range []string{"apptown.class", "apptown.created", "apptown.inferenc", "apptown.owner", "forge.environment"} {
		if !strings.Contains(got, want) {
			t.Errorf("not seen: %s\n%s", want, got)
		}
	}
	for _, not := range []string{"apptown.instance", "apptown.project", "forge.example"} {
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
	for _, k := range imageKeys {
		if !regexp.MustCompile(`(?m)^LABEL ` + regexp.QuoteMeta(k) + `=`).Match(src) {
			t.Errorf("deploy/apptown-agent/Dockerfile does not set %s", k)
		}
	}
}

// Keys are lowercase and dotted, and none is listed twice.
func TestKeysAreOneCaseAndDistinct(t *testing.T) {
	shape := regexp.MustCompile(`^apptown(\.[a-z][a-z-]*)+$`)
	seen := map[string]bool{}
	for _, k := range keys {
		if !shape.MatchString(k) || seen[k] {
			t.Errorf("%s: not lowercase-dotted, or listed twice", k)
		}
		seen[k] = true
	}
	for _, k := range imageKeys {
		if !seen[k] {
			t.Errorf("image key %s is not in Keys", k)
		}
	}
}

// Literal expectations catch misspelled keys and parameter shadowing.
func TestInformationalLabels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		got, want []string
	}{
		{"standing", Informational("shop", "main"), []string{"--label", "apptown.info.project=shop", "--label", "apptown.info.deployment=main"}},
		{"change", PreviewInformational("shop", "Try the shop"), []string{"--label", "apptown.info.project=shop", "--label", "apptown.info.change=Try the shop"}},
		{"run", PreviewInformational("shop", ""), []string{"--label", "apptown.info.project=shop"}},
		{"empty", Informational("", ""), nil},
		{"multi-line title", PreviewInformational("shop", "Try\n\tthe  shop"), []string{"--label", "apptown.info.project=shop", "--label", "apptown.info.change=Try the shop"}},
		{"long title", PreviewInformational("shop", strings.Repeat("é", 61)), []string{"--label", "apptown.info.project=shop", "--label", "apptown.info.change=" + strings.Repeat("é", 60) + "…"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Fatalf("labels = %q, want %q", tc.got, tc.want)
			}
		})
	}
}
