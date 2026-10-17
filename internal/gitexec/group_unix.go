//go:build unix

package gitexec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownGroup starts git as the leader of a process group, and has
// cancellation kill the group: git's children as well as git. A child that
// deliberately starts a session of its own escapes it.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
