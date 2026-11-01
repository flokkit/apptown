package gitexec

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// ErrTransactionRefused means git refused to prepare the update, usually
// because the ref no longer held the expected value or was locked. The ref is
// unchanged.
var ErrTransactionRefused = errors.New("git refused the ref update")

// transaction is one prepared `git update-ref --stdin` transaction. Git holds
// the ref's lock from Prepare until Commit finishes, or until its input closes. If the process
// that prepared it dies before sending commit, git reads end of input and
// aborts, so a prepared transaction never completes on its own.
//
// It runs without a context on purpose: cancelling the caller must not kill
// git between writing a ref and releasing its lock.
type transaction struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	stderr *bytes.Buffer
	done   bool
	home   func() // removes the hardened command's HOME
}

// PrepareUpdate locks ref in the repository at dir and verifies it holds old
// ("" means the ref must not exist). The ref moves to next only on Commit.
func PrepareUpdate(ctx context.Context, dir, ref, next, old string) (*transaction, error) {
	if ref == "" || next == "" {
		return nil, fmt.Errorf("a ref update needs its ref and new value")
	}
	if old == "" {
		old = strings.Repeat("0", len(next))
	}
	// Hardened, as in the hub (HubRun): the ref transaction runs no hook.
	// Not cancelled with the caller: once prepared, git holds the ref's
	// lock until Commit or Abort, and killing it there strands the lock.
	cmd, home, err := hubCommand(context.WithoutCancel(ctx), dir, []string{"update-ref", "--stdin"})
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		home()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		home()
		return nil, err
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		home()
		return nil, err
	}
	t := &transaction{cmd: cmd, stdin: stdin, out: bufio.NewReader(stdout), stderr: stderr, home: home}
	if _, err := fmt.Fprintf(stdin, "start\nupdate %s %s %s\nprepare\n", ref, next, old); err != nil {
		return nil, t.refused(err)
	}
	for _, want := range []string{"start: ok", "prepare: ok"} {
		if err := t.expect(want); err != nil {
			return nil, t.refused(err)
		}
	}
	return t, nil
}

// Commit moves the ref. An error means the outcome is not established: the
// caller must observe the ref (and its lock) before deciding what happened.
func (t *transaction) Commit() error {
	if t.done {
		return fmt.Errorf("transaction already finished")
	}
	t.done = true
	defer t.home()
	if _, err := io.WriteString(t.stdin, "commit\n"); err != nil {
		_ = t.cmd.Wait() // unchecked-error: send failure is returned as unknown outcome; reaping does not claim the ref committed.
		return fmt.Errorf("sending commit: %w", err)
	}
	reply := t.expect("commit: ok")
	_ = t.stdin.Close() // unchecked-error: the reply and process exit are checked; closing input is not commit evidence.
	if err := t.cmd.Wait(); err != nil {
		return fmt.Errorf("git update-ref: %w: %s", err, strings.TrimSpace(t.stderr.String()))
	}
	return reply
}

func (t *transaction) expect(want string) error {
	line, err := t.out.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading git's reply: %w", err)
	}
	if got := strings.TrimSpace(line); got != want {
		return fmt.Errorf("git replied %q, not %q", got, want)
	}
	return nil
}

func (t *transaction) refused(cause error) error {
	t.done = true
	_ = t.stdin.Close() // unchecked-error: refusal occurred before commit was sent; closing input preserves that refusal.
	_ = t.cmd.Wait()    // unchecked-error: refusal occurred before commit was sent; reaping preserves that refusal.
	t.home()
	detail := strings.TrimSpace(t.stderr.String())
	if detail == "" {
		detail = cause.Error()
	}
	return fmt.Errorf("%w: %s", ErrTransactionRefused, detail)
}
