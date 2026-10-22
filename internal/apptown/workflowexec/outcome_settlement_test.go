package workflowexec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
)

// A step's outcome write that fails once is written again within the shared
// bound, so the step does not stay running with its verdict lost. The tests
// step stands for every runner that settles through Execution.Settle.
func TestAFailedOutcomeWriteIsTriedAgain(t *testing.T) {
	t.Parallel()
	f, ch, _ := testsWorld(t, map[string]any{"command": "cat marker.txt"})
	seen := &markersSeen{}
	f.checks = seen.run
	run := testsRun(t, f, ch)
	dbtest.FailNextStatement(t, f.db, "-- name: FinishAttempt ")
	step := f.execute(t, run, "tests")
	if step.Status != workflow.StatusPass || len(seen.all()) != 1 {
		t.Fatalf("after one failed outcome write the step is %s (%s), tests ran %d times; want pass, run once", step.Status, step.Evidence, len(seen.all()))
	}
}

// No step runner drops a Finish's error: a Finish whose result is discarded
// (a bare statement or `_ =`) leaves the step running when the write fails.
// Runners settle through Execution.Settle, which retries within the bound
// and reports what still fails, or use Finish's error.
func TestNoRunnerDiscardsAnOutcomeWrite(t *testing.T) {
	t.Parallel()
	discards := func(call ast.Expr) bool {
		c, ok := call.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Finish" && len(c.Args) == 1
	}
	for _, dir := range []string{".", "../verification"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch s := n.(type) {
				case *ast.ExprStmt:
					if discards(s.X) {
						t.Errorf("%s: a step outcome's Finish result is discarded; use Settle", fset.Position(s.Pos()))
					}
				case *ast.AssignStmt:
					if len(s.Rhs) == 1 && discards(s.Rhs[0]) {
						if id, ok := s.Lhs[0].(*ast.Ident); ok && id.Name == "_" {
							t.Errorf("%s: a step outcome's Finish result is discarded; use Settle", fset.Position(s.Pos()))
						}
					}
				}
				return true
			})
		}
	}
}
