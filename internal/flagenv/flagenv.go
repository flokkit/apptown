// Package flagenv reads a program's flags from the environment: every flag
// is also APPTOWN_ plus its name in capitals with - as _ (-db-url is
// APPTOWN_DB_URL). A flag on the command line wins; the environment wins
// over the default. apptownd and apptown-worker both read their settings
// this way, so an operator sets either the same two ways.
package flagenv

import (
	"flag"
	"fmt"
	"strings"
)

// Name is the environment variable for a flag.
func Name(flagName string) string {
	return "APPTOWN_" + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// Bind sets every flag the command line did not set from its environment
// variable, when that is non-empty. questions are flags that ask something
// (-version) rather than set something: they have no variable. A malformed
// value is an error naming the variable.
func Bind(fs *flag.FlagSet, getenv func(string) string, questions ...string) error {
	skip := map[string]bool{}
	for _, q := range questions {
		skip[q] = true
	}
	fs.Visit(func(f *flag.Flag) { skip[f.Name] = true })
	var err error
	fs.VisitAll(func(f *flag.Flag) {
		if err != nil || skip[f.Name] {
			return
		}
		if v := getenv(Name(f.Name)); v != "" {
			if e := fs.Set(f.Name, v); e != nil {
				err = fmt.Errorf("%s=%q: %v", Name(f.Name), v, e)
			}
		}
	})
	return err
}
