package main

import (
	"flag"
	"fmt"
	"strings"
)

// parseDaemonFlags reads the command line. -version is not one of the
// daemon's settings: it has no environment variable (bindEnv runs after it
// is answered), so only the command line asks.
func parseDaemonFlags(fs *flag.FlagSet, args []string) (*daemonFlags, bool) {
	o := defineFlags(fs)
	v := fs.Bool("version", false, "print this build's version and exit")
	_ = fs.Parse(args) // an ExitOnError set exits on a bad flag, as flag.Parse does
	return o, *v
}

// envName is the environment variable for a flag: APPTOWN_ plus the flag's
// name in capitals, with - as _ (db-url is APPTOWN_DB_URL).
func envName(flagName string) string {
	return "APPTOWN_" + settingName(flagName)
}

func settingName(flagName string) string {
	return strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// bindEnv sets every flag the command line did not set from its environment
// variable, when that is non-empty. The command line wins; the environment
// wins over the default. A malformed value is an error naming the variable.
func bindEnv(fs *flag.FlagSet, getenv func(string) string) error {
	onCommandLine := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { onCommandLine[f.Name] = true })
	var err error
	fs.VisitAll(func(f *flag.Flag) {
		// -version asks a question; it is not a setting. APPTOWN_VERSION is
		// the release a person chose (try.sh, the one-server compose file)
		// and reaches apptownd with the rest of the environment.
		if f.Name == "version" {
			return
		}
		if err != nil || onCommandLine[f.Name] {
			return
		}
		if v := getenv(envName(f.Name)); v != "" {
			if e := fs.Set(f.Name, v); e != nil {
				err = fmt.Errorf("%s=%q: %v", envName(f.Name), v, e)
			}
		}
	})
	return err
}

func daemonUsage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, "usage: apptownd [flags]\n\n")
	flag.PrintDefaults()
	fmt.Fprintf(out, `
Every flag can also be set in the environment as APPTOWN_ plus its name in
capitals with - as _ (for example -db-url is APPTOWN_DB_URL and -tier-domain is
APPTOWN_TIER_DOMAIN, comma-separated). A flag on the command line wins.
`)
}
