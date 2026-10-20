package main

import (
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/ids"
)

// parseArgs parses fs's flags wherever they appear among the positionals and
// returns the positionals in order: `apptown quick "add stats" -p waypoint -y`,
// `apptown retry 58 -c` and `apptown follow -y 58` all parse. Go's flag package
// stops at the first positional, which once swallowed trailing flags INTO a
// change body. A flag that takes a value consumes the next argument; "--"
// ends flags; an argument beginning "- " is a markdown bullet, never a flag.
// fs is ExitOnError, so a bad flag reports and exits 2.
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		// A markdown bullet ("- fix the tests") is text: agents write notes
		// that way. Anything else dash-shaped is a flag, so a mistyped one
		// (--projet) is refused instead of joining the task text.
		if len(a) < 2 || a[0] != '-' || a[1] == ' ' {
			positionals = append(positionals, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	_ = fs.Parse(flags) // ExitOnError: a parse error has already exited
	return positionals
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// newFlags is a command's flag set. A bad flag prints the command's flags
// and exits 2, as every command always has.
func newFlags(name string) *flag.FlagSet { return flag.NewFlagSet(name, flag.ExitOnError) }

// idFlag is a flag naming a record by its id; absent is Nil.
func idFlag(fs *flag.FlagSet, name, usage string) *ids.ID {
	var id ids.ID
	fs.TextVar(&id, name, ids.Nil, usage)
	return &id
}

// parseID reads a positive record id, naming what was expected when the
// argument is not one, before anything is sent.
func parseID(s, what string) (ids.ID, error) {
	id, err := ids.Parse(strings.TrimSpace(s))
	if err != nil || id.IsZero() {
		return ids.Nil, fmt.Errorf("%s must be an id, got %q", what, s)
	}
	return id, nil
}

// parseNumber reads a positive number within a scope (a release's), naming
// what was expected when the argument is not one.
func parseNumber(s, what string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a number, got %q", what, s)
	}
	return n, nil
}

// changeRef resolves what a person typed for a change: its id, its friendly
// form "<slug>#<n>" (the project's current slug and the change's number in
// it), or a bare number inside a project (-p, $APPTOWN_PROJECT, or the
// directory named after it), which is that project's change n. The forge
// resolves a friendly form; the id it answers is what commands send.
func changeRef(s, project string) (ids.ID, error) {
	s = strings.TrimSpace(s)
	if id, err := ids.Parse(s); err == nil && !id.IsZero() {
		return id, nil
	}
	if n, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64); err == nil && n > 0 {
		slug, err := resolveProject(project)
		if err != nil {
			return ids.Nil, fmt.Errorf("change %s: a bare number is a change within a project: %w", s, err)
		}
		s = fmt.Sprintf("%s#%d", slug, n)
	}
	slug, digits, found := strings.Cut(s, "#")
	if n, err := strconv.ParseInt(digits, 10, 64); !found || slug == "" || err != nil || n <= 0 {
		return ids.Nil, fmt.Errorf("a change is its id or <project>#<number>, got %q", s)
	}
	var view struct{ Change struct{ ID ids.ID } }
	if err := api.get("/changes/"+url.PathEscape(s), &view); err != nil {
		return ids.Nil, err
	}
	return view.Change.ID, nil
}

// runRef resolves what a person typed for a run: its id, its friendly form
// "<slug>#<n>" (the project's current slug and the run's number in it), or a
// bare number inside a project, which is that project's run n.
func runRef(s, project string) (ids.ID, error) {
	s = strings.TrimSpace(s)
	if id, err := ids.Parse(s); err == nil && !id.IsZero() {
		return id, nil
	}
	if n, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64); err == nil && n > 0 {
		slug, err := resolveProject(project)
		if err != nil {
			return ids.Nil, fmt.Errorf("run %s: a bare number is a run within a project: %w", s, err)
		}
		s = fmt.Sprintf("%s#%d", slug, n)
	}
	slug, digits, found := strings.Cut(s, "#")
	if n, err := strconv.ParseInt(digits, 10, 64); !found || slug == "" || err != nil || n <= 0 {
		return ids.Nil, fmt.Errorf("a run is its id or <project>#<number>, got %q", s)
	}
	var view struct{ Run struct{ ID ids.ID } }
	if err := api.get("/runs/"+url.PathEscape(s), &view); err != nil {
		return ids.Nil, err
	}
	return view.Run.ID, nil
}

// runArg resolves args[0] as a run (runRef) and rewrites it as the run's id.
func runArg(args []string, project string) error {
	id, err := runRef(args[0], project)
	if err != nil {
		return err
	}
	args[0] = id.String()
	return nil
}

// changeArg resolves args[0] as a change (changeRef) and rewrites it as the
// change's id, so a command can go on using the string in paths.
func changeArg(args []string, project string) error {
	id, err := changeRef(args[0], project)
	if err != nil {
		return err
	}
	args[0] = id.String()
	return nil
}

// numberedArg checks that args[0] names a record the forge numbers (a
// complaint, an eval): its id, or its number within the forge, which the
// forge resolves.
func numberedArg(args []string, what string) error {
	if n, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(args[0]), "#"), 10, 64); err == nil && n > 0 {
		args[0] = strconv.FormatInt(n, 10)
		return nil
	}
	return idArg(args, what)
}

// idArg checks that args[0] is a record id and rewrites it in canonical form,
// so a command can go on using the string in paths and messages.
func idArg(args []string, what string) error {
	id, err := parseID(args[0], what)
	if err != nil {
		return err
	}
	args[0] = id.String()
	return nil
}

// changeName is how a change is named to a person, "<slug>#<n>", a form
// every command takes back (changeRef); its id when its number is unknown.
func changeName(slug string, number int64, id ids.ID) string {
	if slug == "" || number <= 0 {
		return id.String()
	}
	return fmt.Sprintf("%s#%d", slug, number)
}
