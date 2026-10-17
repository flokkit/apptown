//go:build !unix

package execution

import (
	"os/exec"
	"time"
)

// Non-Unix hosts retain CommandContext's direct-child cancellation semantics.
func configureCancellation(cmd *exec.Cmd) { cmd.WaitDelay = 2 * time.Second }
