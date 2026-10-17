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
// shell. Processes that deliberately create a new session escape that group;
// local execution is not a sandbox.
func configureCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
}
