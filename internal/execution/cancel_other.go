//go:build !unix

package execution

import (
	"os/exec"
	"time"
)

// Non-Unix hosts retain CommandContext's direct-child cancellation, with the
// same bound on output as Unix hosts (cancel_unix.go).
func configureCancellation(cmd *exec.Cmd) { cmd.WaitDelay = pipeWait }

// pipeWait is a command's bound on its output (its WaitDelay).
var pipeWait = time.Minute
