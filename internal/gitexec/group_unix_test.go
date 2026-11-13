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

	"github.com/flokkit/apptown/internal/poll"
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

// A canceled git returns within the bound on its output even when a process
// that left git's group still holds that output: killing the group does not
// reach it, and without the bound the caller waits until it lets go. Not
// parallel: it sets the process's environment and the bound.
func TestACanceledGitReturnsWhileAnEscapedChildHoldsItsOutput(t *testing.T) {
	dir := t.TempDir()
	escaped := filepath.Join(dir, "escaped")
	// The child starts a group of its own (setpgrp) and keeps git's stdout.
	scriptedGit(t, `perl -e 'setpgrp(0, 0); exec @ARGV' sleep 300 &
printf '%s' $! > "`+escaped+`"
wait
`)
	bound := pipeWait
	pipeWait = 100 * time.Millisecond
	t.Cleanup(func() { pipeWait = bound })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := Command(ctx, dir, Environ(), "fetch")
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Cancel only once the child has left git's group, so that killing the
	// group cannot reach it.
	var pid int
	t.Cleanup(func() {
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	if !poll.Until(context.Background(), 5*time.Second, 10*time.Millisecond, func() bool {
		raw, err := os.ReadFile(escaped)
		if err != nil {
			return false
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		group, err := syscall.Getpgid(pid)
		return pid > 0 && err == nil && group == pid
	}) {
		t.Fatal("the scripted git's child never left its group")
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a canceled git reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a canceled git held its caller while an escaped child held its output")
	}
}
