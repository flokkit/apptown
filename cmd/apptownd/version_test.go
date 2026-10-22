package main

import (
	"flag"
	"io"
	"testing"
)

// -version is read from the command line, beside the daemon's settings,
// and is false unless asked. (main answers it before bindEnv reads the
// environment, so APPTOWN_VERSION never turns a daemon into a version print.)
func TestApptowndVersionIsAskedOnTheCommandLine(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("apptownd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o, asked := parseDaemonFlags(fs, []string{"-version", "-data", "/elsewhere"})
	if !asked || o.data != "/elsewhere" {
		t.Fatalf("-version: asked %v, data %q", asked, o.data)
	}
	if _, asked := parseDaemonFlags(flag.NewFlagSet("apptownd", flag.ContinueOnError), []string{"-data", "/x"}); asked {
		t.Fatal("version asked without -version")
	}
}
