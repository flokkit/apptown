//go:build unix

package execution

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// The process owns a group: cancellation reaches children as well as their
// shell, while the process itself runs; once it has exited, os/exec stops
// watching the context. Processes that deliberately create a new session
// escape that group; local execution is not a sandbox. Output is read to its
// end (no WaitDelay): os/exec starts a WaitDelay when the process exits as
// well as when its context is canceled, so with one a command that succeeded
// failed, its output cut short, whenever the copying goroutine or a slow
// OnLine fell behind by the delay. So anything still holding the output after
// the process has exited (a background child, a process that left the group)
// holds the run until it lets go, canceled or not.
func configureCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
