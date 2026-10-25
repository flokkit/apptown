//go:build ignore

// lint-check runs golangci-lint (.golangci.yml: typed house rules) and
// holds what it finds to scripts/lint-baseline.json: for each rule, a count
// per package of the violations that predate it. A package may fall below
// its count and may not rise above it, and a package with no count may have
// no violation at all, so a new time.Now or time.Sleep fails in a package
// that already has old ones as much as in a clean one.
//
// The counts live here rather than as path exclusions in .golangci.yml: an
// exclusion hides every violation in the files it names, a new one with the
// old, and --new-from-rev judges a diff against a revision rather than the
// tree, so it says nothing on main and forgets what is left to remove.
//
// Usage: go run scripts/lint-check.go [-lint golangci-lint] [-baseline file]
// [-issues file.json] [-v] [-tighten]
//
// -issues reads golangci-lint's JSON report from a file instead of running
// it. -tighten lowers the counts that fell and drops packages at zero; it
// never raises one: a rise is a hand edit of the baseline, in the commit
// that needs it, where review sees it.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// rules names each rule; markers identify forbidigo messages, while unchecked
// comes from errcheck. A hard rule
// has no baseline: every violation fails, and a count for it in the
// baseline file is refused, so none can be written back in.
var rules = []struct {
	name, marker string
	hard         bool
}{
	{"sleep", `forbidden because "sleep rule:`, false},
	{"clock", `forbidden because "clock rule:`, true},
	{"absence", `forbidden because "absence rule:`, false},
	{"context", `forbidden because "context rule:`, true},
	{"receipt", `forbidden because "receipt rule:`, true},
	{"settlement", `forbidden because "settlement rule:`, true},
	{"unchecked", "", true},
}

type report struct {
	// Report.Error is set when the analysis itself failed: what Issues
	// holds is then not everything, and must not be read as fewer.
	Report struct{ Error string }
	Issues []struct {
		FromLinter string
		Text       string
		Pos        struct {
			Filename string
			Line     int
		}
	}
}

type issue struct{ rule, pkg, pos, text string }

func main() {
	lint := flag.String("lint", "golangci-lint", "the golangci-lint binary")
	base := flag.String("baseline", "scripts/lint-baseline.json", "the baseline file")
	issuesFile := flag.String("issues", "", "golangci-lint's JSON report, instead of running it")
	verbose := flag.Bool("v", false, "list every violation")
	tighten := flag.Bool("tighten", false, "lower the baseline to the counts that fell")
	stale := flag.Bool("stale", true, "check the boundary allow-lists in .golangci.yml name live sites, each with its reason")
	flag.Parse()
	if *stale {
		if problems, err := allowListProblems(".golangci.yml"); err != nil {
			fail(err)
		} else if len(problems) > 0 {
			for _, p := range problems {
				fmt.Println("lint allow-list:", p)
			}
			os.Exit(1)
		}
	}
	baseline := map[string]map[string]int{}
	data, err := os.ReadFile(*base)
	if err != nil {
		fail(err)
	}
	if err := json.Unmarshal(data, &baseline); err != nil {
		fail(fmt.Errorf("%s: %w", *base, err))
	}
	for _, r := range rules {
		if r.hard && len(baseline[r.name]) > 0 {
			fail(fmt.Errorf("%s: the %s rule admits no baseline; remove its counts", *base, r.name))
		}
	}
	raw, err := issuesJSON(*lint, *issuesFile)
	if err != nil {
		fail(err)
	}
	found, err := classify(raw)
	if err != nil {
		fail(err)
	}
	if !compare(baseline, found, *verbose, *tighten) {
		os.Exit(1)
	}
	if *tighten {
		for _, r := range rules {
			if r.hard {
				delete(baseline, r.name)
			}
		}
		out, err := json.MarshalIndent(baseline, "", "  ")
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(*base, append(out, '\n'), 0o644); err != nil {
			fail(err)
		}
		fmt.Println("lint baseline written:", *base)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "lint-check:", err)
	os.Exit(2)
}

// issuesJSON is golangci-lint's report: read from file, or from a run over
// the module. A run that finds violations exits 1 and is not a failure; one
// that fails any other way, or writes no report, is.
func issuesJSON(lint, file string) ([]byte, error) {
	if file != "" {
		return os.ReadFile(file)
	}
	dir, err := os.MkdirTemp("", "lint-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "issues.json")
	// Every violation, as many as there are, each one counted even when
	// two share a line (whatever the configuration says), and exit status
	// 1 meaning only that some were found.
	// Parallel runners: lanes on one machine lint their own worktrees at
	// once, and the default lock would fail every run but the first.
	cmd := exec.Command(lint, "run", "--allow-parallel-runners", "--output.json.path="+out, "--output.text.path=/dev/null", "--show-stats=false",
		"--max-issues-per-linter=0", "--max-same-issues=0", "--uniq-by-line=false", "--issues-exit-code=1", "./...")
	cmd.Stderr = os.Stderr
	// Success, or issues found, and nothing else: golangci-lint writes its
	// report before deciding a failed analysis's exit status, so a report
	// existing does not make a run complete.
	if runErr := cmd.Run(); runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("golangci-lint failed: %w", runErr)
		}
	}
	raw, err := os.ReadFile(out)
	if err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("golangci-lint wrote no report: %v", err)
	}
	return raw, nil
}

func classify(raw []byte) ([]issue, error) {
	var r report
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("golangci-lint's report: %w", err)
	}
	if r.Report.Error != "" {
		return nil, fmt.Errorf("golangci-lint's analysis failed: %s", r.Report.Error)
	}
	var found []issue
	for _, i := range r.Issues {
		// Anything but a house rule (a type-check error, say) means the run
		// could not see the code it judges.
		if i.FromLinter != "forbidigo" && i.FromLinter != "errcheck" {
			return nil, fmt.Errorf("%s:%d: %s: %s", i.Pos.Filename, i.Pos.Line, i.FromLinter, i.Text)
		}
		rule := ""
		for _, r := range rules {
			if r.marker != "" && strings.Contains(i.Text, r.marker) {
				rule = r.name
				break
			}
		}
		file := filepath.ToSlash(i.Pos.Filename)
		if i.FromLinter == "errcheck" {
			var err error
			file, err = reportFile(file)
			if err != nil {
				return nil, err
			}
			if !uncheckedScope(file) {
				return nil, fmt.Errorf("%s: errcheck outside its declared production scope", file)
			}
			rule = "unchecked"
		}
		if rule == "" {
			return nil, fmt.Errorf("%s:%d: a forbidigo rule this check does not know: %s", i.Pos.Filename, i.Pos.Line, i.Text)
		}
		found = append(found, issue{rule: rule, pkg: path.Dir(file), pos: fmt.Sprintf("%s:%d", file, i.Pos.Line), text: i.Text})
	}
	allowed, err := uncheckedDeclarations(found)
	if err != nil {
		return nil, err
	}
	kept := found[:0]
	for _, i := range found {
		if i.rule != "unchecked" || !allowed[i.pos] {
			kept = append(kept, i)
		}
	}
	return kept, nil
}

// uncheckedScope is deliberately two direct production packages, not their
// helper subpackages. Keep the errcheck path exclusion in .golangci.yml aligned.
func uncheckedScope(file string) bool {
	dir := path.Dir(filepath.ToSlash(file))
	return (dir == "internal/database" || dir == "internal/gitexec") && strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, "_test.go")
}

func reportFile(file string) (string, error) {
	if filepath.IsAbs(file) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		file, err = filepath.Rel(cwd, file)
		if err != nil {
			return "", err
		}
	}
	return filepath.ToSlash(filepath.Clean(file)), nil
}

// uncheckedDeclarations binds a reason to one actual analyzer diagnostic, not
// a function or a count allowance. Parse Go comments so strings cannot declare
// exceptions. Enumerate the analyzed files even when there are no diagnostics,
// so an obsolete declaration or nolint cannot disappear behind a green report.
func uncheckedDeclarations(found []issue) (map[string]bool, error) {
	counts := map[string]int{}
	for _, i := range found {
		if i.rule == "unchecked" {
			counts[i.pos]++
		}
	}
	raw, err := os.ReadFile(".golangci.yml")
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	ctx := build.Default
	if tags := lookup(&doc, "run", "build-tags"); tags != nil {
		for _, tag := range tags.Content {
			ctx.BuildTags = append(ctx.BuildTags, tag.Value)
		}
	}
	allowed := map[string]bool{}
	for _, dir := range []string{"internal/database", "internal/gitexec"} {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			file := path.Join(dir, entry.Name())
			if entry.IsDir() || !uncheckedScope(file) {
				continue
			}
			match, err := ctx.MatchFile(dir, entry.Name())
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
			set := token.NewFileSet()
			tree, err := parser.ParseFile(set, file, nil, parser.ParseComments)
			if err != nil {
				return nil, err
			}
			for _, group := range tree.Comments {
				for _, comment := range group.List {
					text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
					pos := fmt.Sprintf("%s:%d", file, set.Position(comment.Pos()).Line)
					nolint := strings.TrimLeft(comment.Text, "/ ")
					if strings.HasPrefix(nolint, "nolint") {
						directive, _, _ := strings.Cut(nolint, "//")
						// Match golangci's generic/all and case-insensitive linter names.
						forbidden := !strings.HasPrefix(directive, "nolint:") || strings.HasPrefix(directive, "nolint:all")
						for _, name := range strings.Split(strings.TrimPrefix(directive, "nolint:"), ",") {
							name = strings.ToLower(strings.TrimSpace(name))
							forbidden = forbidden || name == "all" || name == "errcheck"
						}
						if forbidden {
							return nil, fmt.Errorf("%s: errcheck requires an unchecked-error site reason, not nolint", pos)
						}
					}
					const marker = "unchecked-error:"
					if !strings.HasPrefix(text, marker) {
						continue
					}
					if strings.TrimSpace(strings.TrimPrefix(text, marker)) == "" {
						return nil, fmt.Errorf("%s: unchecked-error has no reason", pos)
					}
					if counts[pos] != 1 || allowed[pos] {
						return nil, fmt.Errorf("%s: unchecked-error needs exactly one diagnostic on its line, found %d", pos, counts[pos])
					}
					allowed[pos] = true
				}
			}
		}
	}
	return allowed, nil
}

func compare(baseline map[string]map[string]int, found []issue, verbose, tighten bool) bool {
	byRule := map[string]map[string][]issue{}
	for _, r := range rules {
		byRule[r.name] = map[string][]issue{}
		if baseline[r.name] == nil {
			baseline[r.name] = map[string]int{}
		}
	}
	for _, i := range found {
		byRule[i.rule][i.pkg] = append(byRule[i.rule][i.pkg], i)
	}
	ok, fell := true, false
	for _, r := range rules {
		held := baseline[r.name]
		pkgs := map[string]bool{}
		for p := range byRule[r.name] {
			pkgs[p] = true
		}
		for p := range held {
			pkgs[p] = true
		}
		ordered := make([]string, 0, len(pkgs))
		for p := range pkgs {
			ordered = append(ordered, p)
		}
		sort.Strings(ordered)
		total, allowed := 0, 0
		for _, p := range ordered {
			n, b := len(byRule[r.name][p]), held[p]
			total, allowed = total+n, allowed+b
			switch {
			case n > b:
				fmt.Printf("lint %-5s %-42s %4d  baseline %4d  WORSE\n", r.name, p, n, b)
				for _, i := range byRule[r.name][p] {
					fmt.Printf("    %s: %s\n", i.pos, i.text)
				}
				ok = false
			case n < b:
				fmt.Printf("lint %-5s %-42s %4d  baseline %4d  lower\n", r.name, p, n, b)
				fell = true
				if n == 0 {
					delete(held, p)
				} else {
					held[p] = n
				}
			case verbose:
				fmt.Printf("lint %-5s %-42s %4d  baseline %4d  ok\n", r.name, p, n, b)
				for _, i := range byRule[r.name][p] {
					fmt.Printf("    %s\n", i.pos)
				}
			}
		}
		fmt.Printf("lint %-5s %4d violations, baseline %4d\n", r.name, total, allowed)
	}
	if fell && !tighten {
		fmt.Println("lint counts fell: record them with `go run scripts/lint-check.go -tighten` in this commit")
	}
	return ok
}

// allowLists are the rules held to sites in the lint configuration, and
// the call each site's line must make.
var allowLists = []struct {
	marker string
	calls  []string
	exact  bool
}{
	{"absence rule:", []string{"polltest.Holds("}, false},
	{"context rule:", []string{"context.Background(", "context.TODO("}, false},
	{"receipt rule:", []string{".BestEffort"}, true},
	{"settlement rule:", []string{".Retry"}, true},
}

// testsOnly is the exclusion that takes tests and their helper packages out
// of a production rule; it names no site.
const testsOnly = `(_test\.go$|(test|fake)/[^/]+\.go$)`

// allowListProblems checks the allow-lists in the lint configuration: each
// exclusion of an allow-listed rule says why above it, and names a site
// that is there. An entry with a source is one call: its path is one file,
// and exactly one line of it making the rule's call matches the source.
// One without is a whole directory (Holds' own tests), which must exist,
// or the exclusion of tests.
func allowListProblems(config string) ([]string, error) {
	raw, err := os.ReadFile(config)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", config, err)
	}
	rules := lookup(&doc, "linters", "exclusions", "rules")
	if rules == nil {
		return nil, fmt.Errorf("%s: no linters.exclusions.rules", config)
	}
	var problems []string
	for _, rule := range rules.Content {
		fields := map[string]string{}
		for i := 0; i+1 < len(rule.Content); i += 2 {
			fields[rule.Content[i].Value] = rule.Content[i+1].Value
		}
		var calls []string
		exact := false
		for _, l := range allowLists {
			if strings.Contains(fields["text"], l.marker) {
				calls = l.calls
				exact = l.exact
			}
		}
		if calls == nil || (fields["source"] == "" && fields["path"] == testsOnly) {
			continue
		}
		where := fmt.Sprintf("%s:%d (path %s)", config, rule.Line, fields["path"])
		if strings.TrimSpace(strings.TrimLeft(rule.HeadComment, "# ")) == "" && (len(rule.Content) == 0 || strings.TrimSpace(rule.Content[0].HeadComment) == "") {
			problems = append(problems, where+": no reason above it")
		}
		path := strings.TrimPrefix(fields["path"], "^")
		if fields["source"] == "" {
			if exact {
				problems = append(problems, where+": requires an exact file and source")
				continue
			}
			if st, err := os.Stat(strings.TrimSuffix(path, "/")); err != nil || !st.IsDir() {
				problems = append(problems, where+": names no directory that exists")
			}
			continue
		}
		file := strings.ReplaceAll(strings.TrimSuffix(path, "$"), `\.`, ".")
		if exact && fields["path"] != "^"+regexp.QuoteMeta(file)+"$" {
			problems = append(problems, where+": path is not one exact file")
			continue
		}
		source, err := regexp.Compile(fields["source"])
		if err != nil {
			problems = append(problems, where+": its source is no regexp: "+err.Error())
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			problems = append(problems, where+": its file is gone")
			continue
		}
		sites := 0
		for _, line := range strings.Split(string(body), "\n") {
			for _, call := range calls {
				if strings.Contains(line, call) && source.MatchString(line) {
					if exact {
						sites += strings.Count(line, call)
					} else {
						sites++
					}
					break
				}
			}
		}
		if sites != 1 {
			problems = append(problems, fmt.Sprintf("%s: its source matches %d calls of %s, not one", where, sites, strings.Join(calls, " or ")))
		}
	}
	return problems, nil
}

// lookup is the node under the keys of nested mappings, nil when absent.
func lookup(n *yaml.Node, keys ...string) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	for _, key := range keys {
		var next *yaml.Node
		for i := 0; n != nil && n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				next = n.Content[i+1]
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}
