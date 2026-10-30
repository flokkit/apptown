package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/numbered"
)

// parseArgs parses fs's flags wherever they appear among the positionals and
// returns the positionals in order: `apptown quick "add stats" -p waypoint -y`,
// `apptown retry 58 -c` and `apptown follow -y 58` all parse. Go's flag package
// stops at the first positional, which once swallowed trailing flags INTO a
// change body. A flag that takes a value consumes the next argument; "--"
// ends flags; an argument beginning "- " is a markdown bullet, never a flag.
// A bad flag is reported with the command's flags and exits 64 (exitUsage);
// -h prints them and exits 0.
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
	if err := fs.Parse(flags); err != nil {
		// The flag package has printed the error and the flags.
		if errors.Is(err, flag.ErrHelp) {
			exitProcess(0)
		} else {
			exitProcess(exitUsage)
		}
	}
	return positionals
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// helpLines is a command's lines in the help table; the table refers to the
// commands, which build flag sets, so it is bound at init.
var helpLines func(name string) string

// newFlags is a command's flag set; parseArgs ends the CLI on a bad flag.
// A bad flag (or -h) prints the command's lines from the help table, the one
// place usage is written, then its flags. The name's first word is the command
// ("eval start" is `eval`).
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		if help := helpLines(strings.Fields(name + " ")[0]); help != "" {
			fmt.Fprintf(fs.Output(), "usage:\n%s\n", help)
		}
		fs.PrintDefaults()
	}
	return fs
}

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
		return ids.Nil, usagef("%s must be an id, got %q", what, s)
	}
	return id, nil
}

// parseNumber reads a positive number within a scope (a release's), naming
// what was expected when the argument is not one.
func parseNumber(s, what string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	if err != nil || n <= 0 {
		return 0, usagef("%s must be a number, got %q", what, s)
	}
	return n, nil
}

// changeRef resolves what a person typed for a change: its id, its friendly
// form "<slug>#<n>" (the project's current slug and the change's number in
// it), or a bare number inside a project (-p, $APPTOWN_PROJECT, or the
// directory named after it), which is that project's change n. AppTown
// resolves a friendly form; the id it answers is what commands send.
func changeRef(s, project string) (ids.ID, error) {
	id, ref, err := numberedRef(s, project, "change")
	if err != nil || ref == "" {
		return id, err
	}
	var view struct{ Change struct{ ID ids.ID } }
	err = api.get("/changes/"+url.PathEscape(ref), &view)
	return view.Change.ID, err
}

// runRef resolves what a person typed for a run: its id, its friendly form
// "<slug>#<n>" (the project's current slug and the run's number in it), or a
// bare number inside a project, which is that project's run n.
func runRef(s, project string) (ids.ID, error) {
	id, ref, err := numberedRef(s, project, "run")
	if err != nil || ref == "" {
		return id, err
	}
	var view struct{ Run struct{ ID ids.ID } }
	err = api.get("/runs/"+url.PathEscape(ref), &view)
	return view.Run.ID, err
}

// numberedRef reads what a person typed for a change or a run (what): an
// id, or the friendly form (numbered.Parse) for AppTown to resolve, a bare
// number becoming <project>#<n>.
func numberedRef(s, project, what string) (ids.ID, string, error) {
	s = strings.TrimSpace(s)
	if id, err := ids.Parse(s); err == nil && !id.IsZero() {
		return id, "", nil
	}
	if n, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64); err == nil && n > 0 {
		slug, err := resolveProject(project)
		if err != nil {
			return ids.Nil, "", fmt.Errorf("%s %s: a bare number is a %s within a project: %w", what, s, what, err)
		}
		s = fmt.Sprintf("%s#%d", slug, n)
	}
	if _, _, ok := numbered.Parse(s); !ok {
		return ids.Nil, "", usagef("a %s is its id or <project>#<number>, got %q", what, s)
	}
	return ids.Nil, s, nil
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

// numberedArg checks that args[0] names a record AppTown numbers (a
// complaint, an eval): its id, or its number within AppTown, which the
// AppTown resolves.
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
