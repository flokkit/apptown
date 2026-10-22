package audit

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A call-site source heuristic over the project kinds: each is written
// somewhere, and no argument at a call writing one is spelled with a
// change, run, session, chat or console. It reads spelling, not values or
// types: a payload variable that carries a changeId passes it (an ADR edit
// does), which is why such a line is still read for its references and
// follows the work it names.
func TestProjectKindsAreWrittenWithoutObviousWork(t *testing.T) {
	root := filepath.Join("..", "..")
	work := regexp.MustCompile(`(?i)change|\brun\b|runid|run\.|session|chat|console`)
	fs := token.NewFileSet()
	written := map[string]bool{}
	var tainted []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fs, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for i, arg := range call.Args {
				kind := literalKind(arg)
				if kind == "" {
					continue
				}
				for listed := range projectKinds {
					if listed != kind && !(strings.HasSuffix(kind, ".") && strings.HasPrefix(listed, kind)) {
						continue
					}
					written[listed] = true
					for j, other := range call.Args {
						if j == i {
							continue
						}
						var b strings.Builder
						_ = printer.Fprint(&b, fs, other)
						if work.MatchString(b.String()) {
							tainted = append(tainted, listed+" ("+fs.Position(call.Pos()).String()+"): "+b.String())
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var unwritten []string
	for kind := range projectKinds {
		if !written[kind] {
			unwritten = append(unwritten, kind)
		}
	}
	sort.Strings(unwritten)
	sort.Strings(tainted)
	if len(unwritten) > 0 || len(tainted) > 0 {
		t.Fatalf("project kinds no writer records: %v\nproject kinds written with work: %v", unwritten, tainted)
	}
}

// literalKind is a string literal argument, or the literal start of one
// built by joining strings.
func literalKind(e ast.Expr) string {
	for {
		b, ok := e.(*ast.BinaryExpr)
		if !ok || b.Op != token.ADD {
			break
		}
		e = b.X
	}
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, _ := strconv.Unquote(lit.Value)
	return s
}
