//go:build ignore

// container-output-check refuses production Go that reads what a command
// in a container printed, outside internal/containerresult. The Docker
// daemon can lose a short-lived container's stdout at its end and still
// report exit 0, so an answer AppTown acts on is read from a file the
// command wrote (containerresult.Run, RunIn) and a copy of data out of a
// container carries a trailer its receiver checks (docker.Scope.RunStep).
// Exit codes and error text are not results: a command run only for its
// status, or whose output only explains a failure, is not refused.
//
// What it reads, in Go under cmd/ and internal/ (test files and test helper
// packages, a directory named *test or *fake, aside):
//   - a docker command led by run or exec: a []string literal whose first
//     element is "run" or "exec" (inside an append too), the leading
//     string arguments of a call, or a local variable assigned one;
//   - not detached (-d, --detach) and not a terminal (-t, -it, -ti,
//     --tty): neither prints a result;
//   - read when the call it is given to has its first result used anywhere
//     but in error text: an argument of fmt.Errorf, errors.New or a log
//     call, reached only through string, strings.TrimSpace, strs.Truncate,
//     strs.Tail, a redaction, fmt.Sprintf, fmt.Sprint or a + of strings
//     (a formatted result is followed to where it goes, so returning one is
//     a read); or when a docker.Command literal it is given in has a Stdout.
//   - a call to a method named OK answers only whether the command
//     succeeded, and is not a read.
//
// Out of scope, and review's: a command built in another function and
// returned (docker.Step's argv, execution.Runner's, which are the owners
// of pass/fail work and framed copies), a process started with exec.Command
// (who may start one is exec-check's rule; the one stream started that way,
// a chat's save, is a git bundle git verifies on import), docker logs, and
// deliberate evasion.
//
// There is no baseline: any site is a failure.
//
// Usage: go run scripts/container-output-check.go [-root dir] [-v]
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// owner is the package whose reads are the one path.
const owner = "internal/containerresult"

func main() {
	root := flag.String("root", ".", "repository root")
	verbose := flag.Bool("v", false, "list every docker run or exec seen")
	flag.Parse()
	found, seen, err := scan(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "container-output-check:", err)
		os.Exit(2)
	}
	if *verbose {
		fmt.Printf("%d docker run or exec commands seen outside %s\n", seen, owner)
	}
	if len(found) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "container-output-check: %d place(s) read what a container printed as a result.\n", len(found))
	fmt.Fprintf(os.Stderr, "Read an answer from a file with internal/containerresult (docker.Scope.Result, ExecResult), and a copy of data through docker.Scope.RunStep, which holds it to its trailer:\n")
	for _, f := range found {
		fmt.Fprintln(os.Stderr, "  "+f)
	}
	os.Exit(1)
}

func scan(root string) (found []string, seen int, err error) {
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path == root {
				return nil
			}
			name := d.Name()
			top := strings.SplitN(rel, "/", 2)[0]
			switch {
			case top != "cmd" && top != "internal",
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" || name == "node_modules",
				strings.HasSuffix(name, "test") || strings.HasSuffix(name, "fake"),
				rel == owner:
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		f, n := check(fset, root, file)
		found = append(found, f...)
		seen += n
		return nil
	})
	sort.Strings(found)
	return found, seen, err
}

func str(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	return s, err == nil
}

// argvLit is a []string literal led by run or exec.
func argvLit(e ast.Expr) (*ast.CompositeLit, bool) {
	cl, ok := e.(*ast.CompositeLit)
	if !ok || len(cl.Elts) == 0 {
		return nil, false
	}
	if at, ok := cl.Type.(*ast.ArrayType); !ok || at.Len != nil || fmt.Sprint(at.Elt) != "string" {
		return nil, false
	}
	s, ok := str(cl.Elts[0])
	return cl, ok && (s == "run" || s == "exec")
}

// isArgv says e is a docker run or exec command: such a literal, a
// variable holding one, or an append to either.
func isArgv(e ast.Expr, vars map[*ast.Object]bool) bool {
	switch e := e.(type) {
	case *ast.CompositeLit:
		cl, ok := argvLit(e)
		return ok && !quiet(cl.Elts)
	case *ast.Ident:
		return e.Obj != nil && vars[e.Obj]
	case *ast.CallExpr:
		return callName(e) == "append" && len(e.Args) > 0 && isArgv(e.Args[0], vars)
	}
	return false
}

// quiet says the command prints no result: detached, or a terminal.
func quiet(elts []ast.Expr) bool {
	for _, e := range elts {
		switch s, _ := str(e); s {
		case "-d", "--detach", "-t", "-it", "-ti", "--tty":
			return true
		}
	}
	return false
}

func callName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
		return "." + f.Sel.Name
	}
	return ""
}

func method(c *ast.CallExpr) string {
	if f, ok := c.Fun.(*ast.SelectorExpr); ok {
		return f.Sel.Name
	}
	return ""
}

func check(fset *token.FileSet, root string, f *ast.File) (found []string, seen int) {
	parents := map[ast.Node]ast.Node{}
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	// Variables holding a docker run or exec command.
	argvVars := map[*ast.Object]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, r := range as.Rhs {
			if i >= len(as.Lhs) {
				break
			}
			id, ok := as.Lhs[i].(*ast.Ident)
			if !ok || id.Obj == nil {
				continue
			}
			if isArgv(r, argvVars) {
				argvVars[id.Obj] = true
			}
		}
		return true
	})
	report := func(n ast.Node, why string) {
		p := fset.Position(n.Pos())
		rel, _ := filepath.Rel(root, p.Filename)
		found = append(found, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), p.Line, why))
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch name := callName(call); {
		case name == "append" || name == "len", name == "exec.Command" || name == "exec.CommandContext":
			return true
		}
		docker := false
		for i, a := range call.Args {
			if s, ok := str(a); ok {
				docker = (s == "run" || s == "exec") && i+2 < len(call.Args) && !quiet(call.Args)
				break
			}
		}
		for _, a := range call.Args {
			ast.Inspect(a, func(m ast.Node) bool {
				switch m := m.(type) {
				case *ast.FuncLit, *ast.CallExpr:
					// A call inside is its own (an append is followed:
					// it builds the command it is given to).
					if c, ok := m.(*ast.CallExpr); ok && callName(c) == "append" {
						return true
					}
					return false
				case *ast.CompositeLit:
					if cl, ok := argvLit(m); ok && !quiet(cl.Elts) {
						docker = true
					}
				case *ast.Ident:
					if m.Obj != nil && argvVars[m.Obj] {
						docker = true
					}
				}
				return true
			})
		}
		if !docker {
			return true
		}
		seen++
		if method(call) == "OK" {
			return true
		}
		for _, a := range call.Args {
			if cl, ok := a.(*ast.CompositeLit); ok {
				for _, e := range cl.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok && fmt.Sprint(kv.Key) == "Stdout" {
						report(call, "a docker run or exec whose stdout is streamed out")
					}
				}
			}
		}
		if read(call, parents) {
			report(call, "what a docker run or exec printed is read")
		}
		return true
	})
	return found, seen
}

// sinks make error text or a log line of what they are given.
var sinks = map[string]bool{"fmt.Errorf": true, "errors.New": true, "log.Printf": true, "log.Print": true, "log.Println": true}

// through keep what they are given text, and are followed to where their
// result goes.
var through = map[string]bool{"string": true, "strings.TrimSpace": true, "strs.Truncate": true, "strs.Tail": true, "fmt.Sprintf": true, "fmt.Sprint": true}

// diagnostic says n reaches a sink's arguments only through wrappers that
// keep it text.
func diagnostic(n ast.Node, parents map[ast.Node]ast.Node) bool {
	for p := parents[n]; p != nil; n, p = p, parents[p] {
		switch p := p.(type) {
		case *ast.CallExpr:
			if p.Fun == n {
				return false
			}
			name := callName(p)
			if sinks[name] {
				return true
			}
			if !through[name] && method(p) != "redacted" && method(p) != "Redact" {
				return false
			}
		case *ast.BinaryExpr:
			if p.Op != token.ADD {
				return false
			}
		case *ast.ParenExpr:
		default:
			return false
		}
	}
	return false
}

// read says the call's first result is used other than in error text.
func read(call *ast.CallExpr, parents map[ast.Node]ast.Node) bool {
	switch p := parents[call].(type) {
	case *ast.ExprStmt:
		return false
	case *ast.AssignStmt:
		id, ok := p.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == "_" {
			return false
		}
		if id.Obj == nil {
			return true
		}
		fn := enclosing(p, parents)
		used := false
		ast.Inspect(fn, func(m ast.Node) bool {
			u, ok := m.(*ast.Ident)
			if !ok || u == id || u.Obj != id.Obj {
				return true
			}
			if as, ok := parents[u].(*ast.AssignStmt); ok && len(as.Lhs) > 0 && as.Lhs[0] == u {
				return true // assigned again, not read
			}
			if !diagnostic(u, parents) {
				used = true
			}
			return true
		})
		return used
	}
	return !diagnostic(call, parents)
}

func enclosing(n ast.Node, parents map[ast.Node]ast.Node) ast.Node {
	for p := parents[n]; p != nil; p = parents[p] {
		switch p.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			return p
		}
	}
	return n
}
