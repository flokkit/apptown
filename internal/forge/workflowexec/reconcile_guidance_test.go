package workflowexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
)

// The catch-up a landing that is behind main tells a person to run gives
// the merge a plain message. Without -m, git names the merge by the
// remote's address, the forge's internal one ("Merge branch 'main' of
// http://192.0.2.10:7778/..."), and the project's history keeps it.
func TestTheCatchUpCommandGivesTheMergeAPlainMessage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	hub, chat := filepath.Join(dir, "hub"), filepath.Join(dir, "chat")
	git := func(in string, args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = in
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(in, file string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(in, file), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		git(in, "add", "-A")
		git(in, "commit", "-q", "-m", file)
	}
	if err := os.MkdirAll(hub, 0o755); err != nil {
		t.Fatal(err)
	}
	git(hub, "init", "-q", "-b", "main")
	commit(hub, "README.md")
	git(dir, "clone", "-q", hub, chat)
	git(chat, "switch", "-q", "-c", "forge/change-00000000-0001-7000-8000-000000000000")
	commit(chat, "fix.go")
	commit(hub, "later.go")

	reason := behindMain("main", "forge/change-00000000-0001-7000-8000-000000000000")["reason"].(string)
	_, command, found := strings.Cut(reason, "chat (")
	if !found {
		t.Fatalf("no chat instruction in %q", reason)
	}
	if command, _, found = strings.Cut(command, "), then"); !found {
		t.Fatalf("no catch-up command in %q", reason)
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = chat
	cmd.Env = append(cmd.Env, "APPTOWN_MAIN_REMOTE="+hub, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_EDITOR=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", command, err, out)
	}
	if subject := git(chat, "log", "-1", "--format=%s"); subject != "Merge main into forge/change-00000000-0001-7000-8000-000000000000" {
		t.Fatalf("the merge's message is %q", subject)
	}
}
