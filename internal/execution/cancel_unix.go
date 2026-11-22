//go:build unix

package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/flokkit/apptown/internal/poll"
)

// The process owns a group: cancellation reaches children as well as their
// shell, while the process itself runs. Processes that deliberately create a
// new session escape that group; local execution is not a sandbox. Output is
// read to its end unless something still holds it pipeWait after the process
// has exited or its context is done (os/exec's WaitDelay, which starts at
// whichever comes first); then the process is killed if it still runs, its
// pipes are closed and the run fails. The bound keeps cancellation and the
// run's timeout guarantees: a process that left the group still holding the
// output cannot hold a canceled run past it (that process is not killed;
// only the pipes are closed). It is a minute because it also
// runs from a successful exit: a 2 s bound turned succeeding commands into
// failures whenever the copying goroutine or a slow OnLine fell that far
// behind on a loaded machine.
//
// One kill of the group is not enough on macOS: a process in the middle of
// fork when the group is signalled can leave a child that missed the signal
// but is still in the group (Linux restarts such a fork). The group is killed
// again until killing it fails (normally because it is empty), so that child
// is reached too; Wait returns only after that. The repeats are bounded by
// pipeWait, and os/exec starts WaitDelay only once Cancel returns, so a
// member that cannot die and still holds the output can hold a canceled run
// for two bounds, not one.
func configureCancellation(ctx context.Context, cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		group := -cmd.Process.Pid
		err := syscall.Kill(group, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		poll.Until(context.WithoutCancel(ctx), pipeWait, 10*time.Millisecond, func() bool {
			return syscall.Kill(group, syscall.SIGKILL) != nil
		})
		return err
	}
	cmd.WaitDelay = pipeWait
}

// pipeWait is a command's bound on its output (its WaitDelay). A variable so
// a test can show the bound without waiting a minute.
var pipeWait = time.Minute
