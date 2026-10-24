package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// apptown dev writes the connection env (and a Procfile) before it starts the
// needs, and a write that fails stops it there: nothing starts, and nothing
// says the files were written.
//
// Kills: discarding the dev.env write error and starting the needs.
func TestDevStopsWhenItsEnvFileCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("PATH", "")                                                       // no docker client: a start that is reached fails differently
	if err := os.MkdirAll(filepath.Join(dir, devEnvPath), 0o755); err != nil { // dev.env cannot be a file
		t.Fatal(err)
	}
	if err := os.WriteFile(appYamlPath, []byte("name: app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeAppTown(t, func(method, path string) (int, string) {
		return 200, `{"compose":"services: {}\n","env":{"PGHOST":"localhost"}}`
	})
	out, err := run(t, "dev")
	if err == nil || !strings.Contains(err.Error(), "dev.env") || strings.Contains(out, "written") {
		t.Fatalf("dev with an unwritable env file: %q %v", out, err)
	}
}

// A command whose act succeeded but whose answer cannot be read says so; it
// never goes on with an empty answer as if it were AppTown's.
//
// Kills: ignoring the decode of the approve, run-step, deletion and
// send-back answers.
func TestAnUnreadableAnswerIsNotAnEmptyOne(t *testing.T) {
	const step = "00000000-0007-7000-8000-000000000000"
	const change = "00000000-0009-7000-8000-000000000000"
	for name, args := range map[string][]string{
		"approve":  {"approve", step, "-y"},
		"run-step": {"run-step", step},
		"delete":   {"delete", "shop", "--cancel"},
		"sendback": {"sendback", change, "again", "-y", "--no-follow"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeAppTown(t, func(method, path string) (int, string) {
				if strings.HasSuffix(path, "/question") { // what approving means, read before the act
					return 200, `{"step":"accept","means":"you accept it"}`
				}
				return 200, "<html>a proxy's page</html>"
			})
			out, err := run(t, args...)
			if err == nil || !strings.Contains(err.Error(), "could not be read") {
				t.Fatalf("%s with an unreadable answer: %q %v", name, out, err)
			}
		})
	}
}
