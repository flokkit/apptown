package main

import (
	"flag"
	"fmt"

	"github.com/flokkit/apptown/internal/flagenv"
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

// bindEnv sets every flag the command line did not set from its environment
// variable. -version asks a question; it is not a setting. APPTOWN_VERSION
// is the release a person chose (try.sh, the one-server compose file) and
// reaches apptownd with the rest of the environment.
func bindEnv(fs *flag.FlagSet, getenv func(string) string) error {
	return flagenv.Bind(fs, getenv, "version")
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
