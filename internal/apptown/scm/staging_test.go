package scm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

func TestStageAllExcludesAppTownRuntime(t *testing.T) {
	t.Parallel()
	// Change 99: the build agent replaced .gitignore and `git add -A`
	// committed .apptown/pi-home (docker credentials, model keys) and pushed
	// it. StageAll must exclude .apptown by PATHSPEC, trusting no gitignore,
	// while still staging the one tree-worthy file: the app contract.
	wt := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = wt
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	// A hostile .gitignore — exactly what the agent wrote: no .apptown rules.
	if err := os.WriteFile(filepath.Join(wt, ".gitignore"), []byte("/bin/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustWrite := func(rel, body string) {
		t.Helper()
		p := filepath.Join(wt, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("main.go", "package main")
	mustWrite(".apptown/app.yaml", "app:\n  web:\n    static: .\n    public: true\n")
	mustWrite(".apptown/pi-home/.docker/config.json", `{"auths":{"reg":{"auth":"SECRET"}}}`)
	mustWrite(".apptown/sessions/transcript.jsonl", `{"secret":"tokens"}`)
	// .forge, the runtime folder's name before the AppTown rename, is
	// retired and never source.
	mustWrite(".forge/pi-home/.docker/config.json", `{"auths":{"reg":{"auth":"OLD"}}}`)

	if err := StageAll(t.Context(), wt); err != nil {
		t.Fatal(err)
	}

	out, err := gittest.Command("-C", wt, "diff", "--cached", "--name-only").Output()
	if err != nil {
		t.Fatal(err)
	}
	staged := string(out)
	if !strings.Contains(staged, "main.go") || !strings.Contains(staged, ".gitignore") {
		t.Fatalf("real work not staged:\n%s", staged)
	}
	if !strings.Contains(staged, ".apptown/app.yaml") {
		t.Fatalf("the contract must be staged:\n%s", staged)
	}
	if strings.Contains(staged, "pi-home") || strings.Contains(staged, "sessions") {
		t.Fatalf("AppTown runtime leaked into the index:\n%s", staged)
	}
}
