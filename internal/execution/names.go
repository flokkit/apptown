package execution

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"
)

// UniqueName is prefix with a random suffix: a container name no other run
// shares, even one started in the same instant (a fleet check starts one
// per project at once). A suffix from the clock repeats; this does not.
func UniqueName(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	return prefix + "-" + hex.EncodeToString(b[:])
}

// StepContainer names a container a workflow step runs: apptown-step-<id>-
// <what> (agent, script, review, scan, tests…), before UniqueName's suffix
// where the step runs more than one. Every container a step starts is named
// here, so StepContainerPrefix finds them all when the step is interrupted.
func StepContainer(stepID ids.ID, what string) string {
	return StepContainerPrefix(stepID) + what
}

// StepContainerPrefix begins the name of every container step stepID runs.
func StepContainerPrefix(stepID ids.ID) string { return fmt.Sprintf("apptown-step-%d-", stepID) }
