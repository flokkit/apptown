//go:build !unix

package gitexec

import "os/exec"

// Non-Unix hosts keep CommandContext's direct-child cancellation.
func ownGroup(*exec.Cmd) {}
