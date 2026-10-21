package gitexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A commit ends with git's automatic maintenance; AppTown's git runs it in
// the foreground, so nothing git started is left running once the command
// returns. A detached maintenance outlived the commit and took
// objects/maintenance.lock in a repository a test was copying (GitHub CI,
// git 2.55, 21 Oct). git's own trace names the maintenance it started.
func TestAutomaticMaintenanceStaysInsideTheCommand(t *testing.T) {
	dir := t.TempDir()
	trace := filepath.Join(t.TempDir(), "trace")
	git := func(args ...string) {
		t.Helper()
		cmd := Command(context.Background(), dir, Environ("GIT_TRACE2_EVENT="+trace), args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "first")
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	var maintenance []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, `"maintenance","run"`) && strings.Contains(line, `"argv"`) {
			maintenance = append(maintenance, line)
		}
	}
	if len(maintenance) == 0 {
		t.Skip("this git starts no automatic maintenance after a commit")
	}
	for _, line := range maintenance {
		if strings.Contains(line, `"--detach"`) {
			t.Fatalf("the commit's maintenance detaches from git: %s", line)
		}
	}
}
