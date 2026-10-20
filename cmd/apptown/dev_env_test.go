package main

import "testing"

// dev.env is sourced by a shell (set -a && . ./.apptown/dev.env), so each
// value is single-quoted: a wired connection string with spaces, a $ or a
// quote reaches the app whole, as written.
//
// Kills: values written unquoted (the shell would keep only host=localhost).
func TestProcessDevPreservesWiredConnectionString(t *testing.T) {
	got := devEnvFile(map[string]string{
		"CONNECTION_STRING": "host=localhost dbname=app",
		"PGHOST":            "localhost",
		"ODD":               `it's $HOME`,
	})
	want := "CONNECTION_STRING='host=localhost dbname=app'\nODD='it'\\''s $HOME'\nPGHOST='localhost'\n"
	if got != want {
		t.Fatalf("dev.env:\n%s\nwant:\n%s", got, want)
	}
}
