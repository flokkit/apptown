package gitexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execfile"
)

// fakeGit puts a git first on PATH that appends its environment, one
// variable per line and a line "--" after each call, to the file it returns.
func fakeGit(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	dump := filepath.Join(t.TempDir(), "env")
	script := "#!/bin/sh\nenv >> '" + dump + "'\necho -- >> '" + dump + "'\n"
	if err := execfile.Write(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dump
}

// calls reads the environments fakeGit recorded, one per call.
func calls(t *testing.T, dump string) [][]string {
	t.Helper()
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, call := range strings.Split(string(b), "--\n") {
		if strings.TrimSpace(call) != "" {
			out = append(out, strings.Split(strings.TrimSpace(call), "\n"))
		}
	}
	return out
}

// inheritedGit is what an AppTown started from a Git hook, or by a person who
// set them, carries: each would point git at another repository, index,
// object store or configuration.
var inheritedGit = map[string]string{
	"GIT_DIR": "/elsewhere/.git", "GIT_WORK_TREE": "/elsewhere", "GIT_INDEX_FILE": "/elsewhere/index",
	"GIT_COMMON_DIR": "/elsewhere/.git", "GIT_OBJECT_DIRECTORY": "/elsewhere/objects",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": "/elsewhere/objects", "GIT_CEILING_DIRECTORIES": "/",
	"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.hooksPath", "GIT_CONFIG_VALUE_0": "/elsewhere/hooks",
	"GIT_CONFIG_PARAMETERS": "'core.bare=true'", "GIT_CONFIG_GLOBAL": "/elsewhere/config",
	"GIT_OPTIONAL_LOCKS": "1", "GIT_NO_REPLACE_OBJECTS": "1",
}

// No GIT_* variable AppTown inherited reaches a git it starts, whichever
// way it starts it: the plain commands, the hardened ones in its own
// repositories and folders, and a remote fetch. What a caller sets on
// purpose does (GIT_OPTIONAL_LOCKS=0, RunEnv's variables, the hardened
// commands' own). Not parallel: it sets the process's environment.
func TestNoInheritedGitVariableReachesTheForgesGit(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dump := fakeGit(t)
	for k, v := range inheritedGit {
		t.Setenv(k, v)
	}
	ctx := context.Background()
	_, _ = Run(ctx, dir, "status")
	_, _ = RunEnv(ctx, dir, []string{"GIT_AUTHOR_NAME=on purpose"}, "status")
	_, _ = Output(ctx, dir, "rev-parse", "HEAD")
	_, _ = OutputBytes(ctx, dir, nil, "rev-parse", "HEAD")
	_ = ExtractCommit(ctx, dir, "HEAD", t.TempDir())
	_, _ = Staged(ctx, dir)
	_, _ = HubRun(ctx, dir, "rev-parse", "HEAD")
	_, _ = HubOutput(ctx, dir, nil, "rev-parse", "HEAD")
	_, _ = confined(ctx, dir, "rev-parse", "HEAD")
	_, _ = runRemote(ctx, "", Remote{URL: "https://example.invalid/r.git", Pin: "example.invalid:443:192.0.2.1", Password: "p"}, "ls-remote", "https://example.invalid/r.git")
	got := calls(t, dump)
	if len(got) != 10 {
		t.Fatalf("%d calls recorded, want 10", len(got))
	}
	for i, env := range got {
		for _, kv := range env {
			name, value, _ := strings.Cut(kv, "=")
			if want, ok := inheritedGit[name]; ok && value == want {
				t.Errorf("call %d: the inherited %s reached git", i, kv)
			}
		}
	}
	for i := range 6 { // the plain commands: no system or global config
		for _, wanted := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull} {
			if !strings.Contains("\n"+strings.Join(got[i], "\n")+"\n", "\n"+wanted+"\n") {
				t.Errorf("call %d lacks %s", i, wanted)
			}
		}
	}
	for i, wanted := range map[int]string{0: "GIT_OPTIONAL_LOCKS=0", 1: "GIT_AUTHOR_NAME=on purpose", 6: "GIT_CONFIG_NOSYSTEM=1", 9: "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains("\n"+strings.Join(got[i], "\n")+"\n", "\n"+wanted+"\n") {
			t.Errorf("call %d lacks %s, set on purpose", i, wanted)
		}
	}
}

// Command gives git exactly the environment it is passed: AppTown CLI,
// acting in a person's repository as them, passes their whole environment,
// GIT_* included, and nothing keeps their own git config from them. Not
// parallel: it sets the process's environment.
func TestCommandGivesGitExactlyTheEnvironmentPassed(t *testing.T) {
	dump := fakeGit(t)
	t.Setenv("GIT_DIR", "/theirs/.git")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	_ = Command(context.Background(), t.TempDir(), os.Environ(), "status").Run()
	joined := "\n" + strings.Join(calls(t, dump)[0], "\n") + "\n"
	for _, kv := range []string{"GIT_DIR=/theirs/.git", "GIT_CONFIG_COUNT=1"} {
		if !strings.Contains(joined, "\n"+kv+"\n") {
			t.Errorf("the person's %s did not reach git", kv)
		}
	}
	for _, kv := range RepositoryConfigOnly {
		if strings.Contains(joined, "\n"+kv+"\n") {
			t.Errorf("%s keeps the person's own config from their git", kv)
		}
	}
}

// AppTown's git reads only the repository's own config: a setting in the
// global config of the user the apptown runs as (here commit signing with a
// signer that always fails, in HOME/.gitconfig and the XDG location) does
// not reach a plain Run, so a commit behaves the same on every host. Not
// parallel: it sets the process's environment.
func TestTheMachinesGitConfigDoesNotReachThePlainCommands(t *testing.T) {
	dir := repo(t)
	home := t.TempDir()
	signing := "[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = false\n"
	for _, path := range []string{filepath.Join(home, ".gitconfig"), filepath.Join(home, "xdg", "git", "config")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(signing), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	if out, err := Run(t.Context(), dir, "commit", "-q", "--allow-empty", "-m", "unsigned"); err != nil {
		t.Fatalf("the home config's signing reached AppTown's git: %v: %s", err, out)
	}
	if out, err := Output(t.Context(), dir, "config", "--get", "commit.gpgsign"); err == nil {
		t.Fatalf("AppTown's git reads commit.gpgsign=%s from the home config", out)
	}
}
