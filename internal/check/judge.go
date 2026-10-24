package check

import (
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

// A Judge holds the three capabilities evaluating a check can need: run a
// command in a container, ask a model a checklist, probe a URL. A capability
// it was not given is not an error — the checks that need it report
// cannot-assess with the reason.
type Judge struct {
	// Run executes one container. The caller supplies it rather than a bare
	// execution.Runner because a host adds its own cache mounts, labels and
	// ownership to every run it owns.
	Run func(execution.Request) execution.Outcome
	// ShImage is the image sh checks run in, the tool versions' shell, from
	// the snapshot the caller's unit of work took. "": they error.
	ShImage string
	// Complete asks a model for one answer. nil: rubrics cannot be assessed.
	Complete Completer
	// Probe makes one HTTP request. nil: runtime checks cannot be assessed.
	Probe Prober
	// Scanners resolves a tool check's name to the tool it names. nil: tool
	// checks cannot be assessed.
	Scanners Scanners
	// RunImage runs a command INSIDE an already-built image, with nothing
	// mounted: the exec-image self-check, which must see the app as the app
	// sees itself. nil: those checks cannot be assessed.
	RunImage func(image string, argv, env []string) (exitCode int, output string)
	// Readiness resolves what path a `readiness` probe means for this source —
	// the app's own declaration, which only the caller can read. nil or false:
	// the check reports does-not-conform, as an app that is served while
	// declaring nothing about when it is ready has not met the way of working.
	Readiness func(src Source) (string, bool)
	// NamePrefix names the containers a run owns, so a host can find them.
	NamePrefix string
	// Now is the judge's clock, for how long an evaluation took; nil is the
	// real clock.
	Now func() time.Time
}
