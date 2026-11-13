//go:build unix

package execution

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
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
func configureCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = pipeWait
}

// pipeWait is a command's bound on its output (its WaitDelay). A variable so
// a test can show the bound without waiting a minute.
var pipeWait = time.Minute
