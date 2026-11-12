//go:build !unix

package execution

import "os/exec"

// Non-Unix hosts retain CommandContext's direct-child cancellation, and read
// output to its end as Unix hosts do (cancel_unix.go).
func configureCancellation(*exec.Cmd) {}
