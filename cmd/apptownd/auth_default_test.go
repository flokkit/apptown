package main

import (
	"flag"
	"testing"
)

// An apptownd started with no -auth asks people to sign in: open mode,
// where a caller names itself, is chosen, never fallen into.
func TestSignInIsTheDefault(t *testing.T) {
	o, _ := parseDaemonFlags(flag.NewFlagSet("apptownd", flag.ContinueOnError), []string{"-data", "/x"})
	if o.authMode != "internal" {
		t.Fatalf("-auth defaults to %q, want internal", o.authMode)
	}
}
