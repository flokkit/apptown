//go:build unix

package gitexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"flokkit.io/forge/internal/poll"
)

// Canceling a git kills what it started as well: a helper git left running
// would hold its pipes or keep talking to a remote. Every process git
// started is gone once its process group is. Not parallel: it sets the
// process's environment.
func TestCommandCancellationStopsGitsChildren(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	// git leads a group of its own: it starts a child that would outlive
	// any test, then writes its pid (the group's id) and waits.
	scriptedGit(t, `sleep 300 &
printf '%s' $$ > "`+started+`"
wait
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := Command(ctx, dir, Environ(), "fetch")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var pgid int
	if !poll.Until(context.Background(), 5*time.Second, 10*time.Millisecond, func() bool {
		raw, err := os.ReadFile(started)
		pgid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && pgid > 0
	}) {
		t.Fatal("the scripted git never started")
	}
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	// The group is git's own, so its end below says something.
	if err := syscall.Kill(-pgid, 0); err != nil {
		t.Fatalf("git does not lead a process group of its own: %v", err)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("a canceled git reported success")
	}
	if !poll.Until(context.Background(), 5*time.Second, 10*time.Millisecond, func() bool {
		return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
	}) {
		t.Fatal("git's process group outlived the cancellation")
	}
}
