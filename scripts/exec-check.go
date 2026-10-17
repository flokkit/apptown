//go:build ignore

// exec-check counts the places Go code starts a process, per package, and
// holds them to scripts/exec-baseline.json. Starting a process is owned: git
// through internal/gitexec, a generic command through internal/execution,
// the docker CLI through internal/deploy/docker, and the rest of the
// baseline's "allowed" packages, each with its reason. A process started
// anywhere else does without what its owner guarantees (the environment,
// the process group, timeouts, how output comes back), so every other
// package is held at a count that may fall and may not rise.
//
// What counts is decided by the type checker, so renamed and dot imports,
// aliases, defined types, embedding and elided literal types fall out of
// it rather than out of spelling. Counted:
//   - a reference to exec.Command or exec.CommandContext, os.StartProcess,
//     or syscall's or x/sys/unix's Exec, ForkExec and StartProcess, called
//     or not (a function value counts);
//   - a command made by value: a composite literal of exec.Cmd (or of a type
//     defined from it; []*exec.Cmd{{...}} included), new or make of one, and
//     a variable or field that holds one by value, itself or in a slice,
//     array, map or channel, named or not. A *exec.Cmd parameter or
//     result, which an owner hands out, does not count.
//
// Test files count as well as production: a test that starts docker or git
// itself runs it without the owner's settings. Each directory's files are
// checked together, every build variant at once, with every import but
// os/exec, os and syscall standing in empty (x/sys/unix's stand-in declares
// its starters); type errors that leaves are expected and ignored.
//
// Separately, git started by name (exec.Command or CommandContext with the
// constant "git" as the program) is allowed in the one file the baseline
// names, even among the allowed packages, and the rule fails if that file
// starts none, since it would then see nothing.
//
// It catches process starts written in this module in the ordinary ways;
// it is not a defence against deliberate evasion. Out of scope, and review's
// job: a command value built from a type another package exports (a wrapper
// embedding exec.Cmd, an alias of it, a named list of commands), containers
// of commands passed around, reflection, cgo, raw syscalls, a program name
// computed at run time, and other modules.
//
// A package is a directory. Subdirectories are packages of their own, so an
// allowed package's test helpers are held like anyone else's.
//
// Usage: go run scripts/exec-check.go [-root dir] [-baseline file] [-v] [-tighten]
//
// -tighten lowers the counts that fell and drops packages that reached
// zero. It never raises one: a rise is a hand edit of the baseline, with its
// reason, in the commit that needs it, where review sees it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/constant"
	goimporter "go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// held is a package that starts processes without being an owner: how many
// starts it had when the gate last tightened, and why they remain.
type held struct {
	Count int    `json:"count"`
	Why   string `json:"why"`
}

type gitRule struct {
	File string `json:"file"`
	Why  string `json:"why"`
}

type baseline struct {
	Allowed map[string]string `json:"allowed"`
	// Git is the one file that may start git by name, even among the
	// allowed packages: every other git goes through its constructor.
	Git      gitRule         `json:"git"`
	Packages map[string]held `json:"packages"`
}

// start is one place a process can be started, or (git) one where git is
// started by name, which is held to a rule of its own and not counted.
type start struct {
	pos, what string
	git       bool
	at        token.Pos
}

// starters names, per import path, the selectors that start a process.
var starters = map[string]map[string]bool{
	"os/exec":               {"Command": true, "CommandContext": true},
	"os":                    {"StartProcess": true},
	"syscall":               {"Exec": true, "ForkExec": true, "StartProcess": true},
	"golang.org/x/sys/unix": {"Exec": true, "ForkExec": true},
}

func main() {
	root := flag.String("root", ".", "repository root")
	path := flag.String("baseline", "", "baseline file (default <root>/scripts/exec-baseline.json)")
	verbose := flag.Bool("v", false, "list every start")
	tighten := flag.Bool("tighten", false, "lower the baseline to the counts that fell")
	flag.Parse()
	if *path == "" {
		*path = filepath.Join(*root, "scripts", "exec-baseline.json")
	}
	b, err := load(*path)
	if err != nil {
		fail(err)
	}
	found, err := scan(*root)
	if err != nil {
		fail(err)
	}
	if !report(*root, b, found, *verbose, *tighten) {
		os.Exit(1)
	}
	if *tighten {
		if err := save(*path, b); err != nil {
			fail(err)
		}
		fmt.Println("exec baseline written:", *path)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "exec-check:", err)
	os.Exit(2)
}

func load(path string) (*baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if b.Git.File == "" {
		return nil, fmt.Errorf("%s names no file that starts git", path)
	}
	if len(b.Allowed) == 0 {
		return nil, fmt.Errorf("%s names no allowed package", path)
	}
	if b.Packages == nil {
		b.Packages = map[string]held{}
	}
	return &b, nil
}

func save(path string, b *baseline) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// scan returns every process start under root by package directory. Like the
// go tool it skips hidden and underscore directories, testdata, vendor,
// node_modules and other modules. A file that does not parse fails the scan.
func scan(root string) (map[string][]start, error) {
	fset := token.NewFileSet()
	dirs := map[string][]*ast.File{} // package directory -> its files
	var order []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(filepath.Dir(rel))
		if dirs[pkg] == nil {
			order = append(order, pkg)
		}
		dirs[pkg] = append(dirs[pkg], file)
		return nil
	})
	if err != nil {
		return nil, err
	}
	imp := newImporter(fset)
	out := map[string][]start{}
	for _, pkg := range order {
		if s := packageStarts(fset, root, imp, pkg, dirs[pkg]); len(s) > 0 {
			out[pkg] = s
		}
	}
	return out, nil
}

// importer type-checks, from source, only the packages that can start a
// process; every other import is an empty stand-in, so a type another
// package declares is unknown here (which is why a command value built from
// one is out of scope), and the type errors that leaves are ignored.
type importer struct {
	source types.Importer
	seen   map[string]*types.Package
}

func newImporter(fset *token.FileSet) *importer {
	return &importer{source: goimporter.ForCompiler(fset, "source", nil), seen: map[string]*types.Package{}}
}

func (i *importer) Import(path string) (*types.Package, error) {
	if p, ok := i.seen[path]; ok {
		return p, nil
	}
	var p *types.Package
	if path == "os/exec" || path == "os" || path == "syscall" {
		var err error
		if p, err = i.source.Import(path); err != nil {
			return nil, err
		}
		if path == "os/exec" {
			cmdStruct = p.Scope().Lookup("Cmd").Type().Underlying()
		}
	} else {
		p = types.NewPackage(path, path[strings.LastIndex(path, "/")+1:])
		// A stand-in declares the starters of a package with no source here
		// (x/sys/unix), so a reference binds to them however it is imported,
		// dot imports included. The calls do not type-check; that is ignored.
		for name := range starters[path] {
			p.Scope().Insert(types.NewFunc(token.NoPos, p, name, types.NewSignatureType(nil, nil, nil, nil, nil, false)))
		}
		p.MarkComplete()
	}
	i.seen[path] = p
	return p, nil
}

// packageStarts type-checks one directory's files, each package clause on
// its own (a package and its external tests), every build variant together,
// and returns their starts in source order.
func packageStarts(fset *token.FileSet, root string, imp *importer, dir string, files []*ast.File) []start {
	byName := map[string][]*ast.File{}
	for _, f := range files {
		byName[f.Name.Name] = append(byName[f.Name.Name], f)
	}
	var found []start
	for name, group := range byName {
		info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
		conf := types.Config{Importer: imp, Error: func(error) {}, FakeImportC: true}
		_, _ = conf.Check(dir+"#"+name, fset, group, info)
		for _, f := range group {
			found = append(found, fileStarts(fset, root, f, info)...)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].at < found[j].at })
	return found
}

// fileStarts finds, with the type checker's answers, where a file starts a
// process or makes a command it could start.
func fileStarts(fset *token.FileSet, root string, file *ast.File, info *types.Info) []start {
	var found []start
	add := func(n ast.Node, what string, git bool) {
		p := fset.Position(n.Pos())
		rel, _ := filepath.Rel(root, p.Filename)
		found = append(found, start{pos: fmt.Sprintf("%s:%d", filepath.ToSlash(rel), p.Line), what: what, git: git, at: n.Pos()})
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			// A reference to a function that starts a process, called or
			// not, however its package was imported or renamed.
			if fn, ok := info.Uses[n].(*types.Func); ok && fn.Pkg() != nil && starters[fn.Pkg().Path()][fn.Name()] {
				if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() == nil {
					add(n, fn.Pkg().Path()+"."+fn.Name(), false)
				}
			}
			// A variable or field that holds a command by value.
			if v, ok := info.Defs[n].(*types.Var); ok && holdsCmd(v.Type()) {
				add(n, "exec.Cmd value", false)
			}
		case *ast.CompositeLit:
			// Elided types included: []*exec.Cmd{{...}} makes a command.
			if t := info.Types[n].Type; t != nil && (isCmd(t) || isCmd(pointee(t))) {
				add(n, "exec.Cmd value", false)
			}
		case *ast.CallExpr:
			if id, ok := ast.Unparen(n.Fun).(*ast.Ident); ok && len(n.Args) > 0 {
				if b, ok := info.Uses[id].(*types.Builtin); ok && (b.Name() == "new" || b.Name() == "make") {
					if t := info.Types[n.Args[0]].Type; t != nil && holdsCmd(t) {
						add(n, "exec.Cmd value", false)
					}
				}
			}
			// git named as the program: the git-by-name rule's concern. The
			// start itself is counted where the function is referenced.
			if fn := calledFunc(info, n.Fun); fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == "os/exec" && (fn.Name() == "Command" || fn.Name() == "CommandContext") {
				program := 0
				if fn.Name() == "CommandContext" {
					program = 1
				}
				if len(n.Args) > program {
					if v := info.Types[n.Args[program]].Value; v != nil && v.Kind() == constant.String && constant.StringVal(v) == "git" {
						add(n, "git started by name", true)
					}
				}
			}
		}
		return true
	})
	return found
}

func calledFunc(info *types.Info, fun ast.Expr) *types.Func {
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		fn, _ := info.Uses[f].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		fn, _ := info.Uses[f.Sel].(*types.Func)
		return fn
	}
	return nil
}

func pointee(t types.Type) types.Type {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		return p.Elem()
	}
	return nil
}

// isCmd reports exec.Cmd itself, an alias of it, or a type defined from it.
func isCmd(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj.Pkg() != nil && obj.Pkg().Path() == "os/exec" && obj.Name() == "Cmd" {
		return true
	}
	return cmdStruct != nil && types.Identical(named.Underlying(), cmdStruct)
}

// holdsCmd reports a type whose values hold a command by value: the command
// itself, or an array, slice, map or channel of them. A command in a struct
// is counted where its field is declared.
func holdsCmd(t types.Type) bool { return holds(t, map[types.Type]bool{}) }

// holds is holdsCmd remembering the types it has entered: a container can
// refer to itself (type Tree map[string]Tree), and a type already being
// looked at adds nothing.
func holds(t types.Type, seen map[types.Type]bool) bool {
	if t == nil {
		return false
	}
	if isCmd(t) {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	// A named container (type Commands []exec.Cmd) holds what its
	// underlying type holds.
	switch u := t.Underlying().(type) {
	case *types.Array:
		return holds(u.Elem(), seen)
	case *types.Slice:
		return holds(u.Elem(), seen)
	case *types.Map:
		return holds(u.Elem(), seen)
	case *types.Chan:
		return holds(u.Elem(), seen)
	}
	return false
}

// cmdStruct is exec.Cmd's underlying struct, for types defined from it.
var cmdStruct types.Type

func report(root string, b *baseline, all map[string][]start, verbose, tighten bool) bool {
	ok := true
	found, gitSeen := map[string][]start{}, false
	for p, list := range all {
		for _, s := range list {
			switch {
			case !s.git:
				found[p] = append(found[p], s)
			case strings.HasPrefix(s.pos, b.Git.File+":"):
				gitSeen = true
			default:
				fmt.Printf("exec %s starts git by name: use gitexec.Command (or gittest) instead\n", s.pos)
				ok = false
			}
		}
	}
	// The rule sees the start it allows, so it can see the others.
	if !gitSeen {
		fmt.Printf("exec %s starts no git by name: name the file that does in the baseline\n", b.Git.File)
		ok = false
	}
	pkgs := map[string]bool{}
	for p := range found {
		pkgs[p] = true
	}
	for p := range b.Packages {
		pkgs[p] = true
	}
	for p := range b.Allowed {
		pkgs[p] = true
		if info, err := os.Stat(filepath.Join(root, p)); err != nil || !info.IsDir() {
			fmt.Printf("exec allowed package %s does not exist: remove it from the baseline\n", p)
			ok = false
		}
	}
	ordered := make([]string, 0, len(pkgs))
	for p := range pkgs {
		ordered = append(ordered, p)
	}
	sort.Strings(ordered)
	fell := false
	for _, p := range ordered {
		n := len(found[p])
		h, isHeld := b.Packages[p]
		if _, allowed := b.Allowed[p]; allowed {
			fmt.Printf("exec %-42s %4d  allowed\n", p, n)
			if isHeld {
				fmt.Printf("    %s is allowed and also held: remove it from packages\n", p)
				ok = false
			}
			list(found[p], verbose)
			continue
		}
		switch {
		case !isHeld:
			fmt.Printf("exec %-42s %4d  baseline    -  NEW: start it through an allowed package's interface\n", p, n)
			list(found[p], true)
			ok = false
		case n > h.Count:
			fmt.Printf("exec %-42s %4d  baseline %4d  WORSE: start it through an allowed package's interface\n", p, n, h.Count)
			list(found[p], true)
			ok = false
		case n < h.Count:
			fmt.Printf("exec %-42s %4d  baseline %4d  lower\n", p, n, h.Count)
			list(found[p], verbose)
			fell = true
			if n == 0 {
				delete(b.Packages, p)
			} else {
				b.Packages[p] = held{Count: n, Why: h.Why}
			}
		default:
			fmt.Printf("exec %-42s %4d  baseline %4d  ok\n", p, n, h.Count)
			list(found[p], verbose)
		}
	}
	if fell && !tighten {
		fmt.Println("exec counts fell: record them with `go run scripts/exec-check.go -tighten` in this commit")
	}
	return ok
}

func list(starts []start, show bool) {
	if !show {
		return
	}
	for _, s := range starts {
		fmt.Printf("    %s: %s\n", s.pos, s.what)
	}
}
