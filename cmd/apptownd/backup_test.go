package main

import (
	"path/filepath"
	"testing"
)

// After the daemon's flags come at most one command: backup (with -store)
// or restore, each with one archive, made absolute. Anything else is
// refused, as an argument always was.
func TestTheCommandsAfterTheFlags(t *testing.T) {
	t.Parallel()
	abs, _ := filepath.Abs("a.backup")
	for _, c := range []struct {
		args  []string
		want  command
		fails bool
	}{
		{args: nil, want: command{}},
		{args: []string{"backup", "a.backup"}, want: command{name: "backup", archive: abs}},
		{args: []string{"backup", "-store", "a.backup"}, want: command{name: "backup", archive: abs, store: true}},
		{args: []string{"restore", "a.backup"}, want: command{name: "restore", archive: abs}},
		{args: []string{"restore", "-store", "a.backup"}, fails: true},
		{args: []string{"backup"}, fails: true},
		{args: []string{"backup", "a", "b"}, fails: true},
		{args: []string{"serve"}, fails: true},
	} {
		got, err := parseCommand(c.args)
		if (err != nil) != c.fails || (!c.fails && got != c.want) {
			t.Errorf("%v: %+v %v", c.args, got, err)
		}
	}
}
