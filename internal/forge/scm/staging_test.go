package scm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
)

func TestStageAllExcludesForgeRuntime(t *testing.T) {
	t.Parallel()
	// Change 99: the build agent replaced .gitignore and `git add -A`
	// committed .forge/pi-home (docker credentials, model keys) and pushed
	// it. StageAll must exclude .forge by PATHSPEC, trusting no gitignore,
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
	// A hostile .gitignore — exactly what the agent wrote: no .forge rules.
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
	mustWrite(".forge/app.yaml", "app:\n  web:\n    static: .\n    public: true\n")
	mustWrite(".forge/pi-home/.docker/config.json", `{"auths":{"reg":{"auth":"SECRET"}}}`)
	mustWrite(".forge/sessions/transcript.jsonl", `{"secret":"tokens"}`)

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
	if !strings.Contains(staged, ".forge/app.yaml") {
		t.Fatalf("the contract must be staged:\n%s", staged)
	}
	if strings.Contains(staged, "pi-home") || strings.Contains(staged, "sessions") {
		t.Fatalf("forge runtime leaked into the index:\n%s", staged)
	}
}
