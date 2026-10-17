package verification

// The `tool` evaluator at the gate (design/subsystems/conformance.md §13): a
// guidance check whose evaluation is "run this named scanner binding and turn
// its result into a verdict/finding". The judgement itself is the library's
// (internal/check, tool.go); what is here is what only the forge can supply —
// the binding a name resolves to, the app image an exec-image check runs
// inside, the environment it runs with, and the dispositions that say a
// person has already excused what the tool reported.
//
// Result shape matches runShChecks so `forge check`, the land gate, and the
// fleet review render tool checks beside sh checks with no special-casing.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/conformance"
)

// toolImageReady pulls a tool's image ahead of the run, on the executor's
// machine, when it does not have it, so the first run's verdict carries the
// tool's words and not docker's pull progress (reading-list's first gofmt
// verdict read "Pulling fs layer…" where it should have named the file).
func toolImageReady(ctx context.Context, images CheckImage, image string) {
	if image != "" {
		_ = images(ctx, execution.ImageRequest{Tag: image, Pull: image})
	}
}

// runInImage runs a command INSIDE an already-resolved app image (by ID) and
// returns the exit code and output: nothing is mounted and there is no
// network, so the app is seen as the app sees itself, for at most the scan
// timeout. The image is resolved separately (and shared) via imageForCommit.
func runInImage(ctx context.Context, run CheckRun, imageID string, command, env []string) (exitCode int, output string) {
	out := run(ctx, execution.Request{Image: imageID, Argv: command, Env: env, Network: "none",
		Timeout: check.ScanTimeout, Name: execution.UniqueName("forge-exec-image")})
	if !out.Answered() {
		// No answer (it timed out, did not run, or its cleanup failed):
		// an errored check, with the reason.
		reason := fmt.Sprintf("%s (%v)", out.Termination, errors.Join(out.RunErr, out.CleanupErr))
		return -1, strings.TrimSpace(reason + "\n" + string(out.Output))
	}
	return out.ExitCode, string(out.Output)
}

// ToolContext carries what a tool check needs beyond its definition: the staged
// committed tree (source scans + the image build context), the commit it came
// from (the shared image is content-addressed by it), the project (registry
// login), and whether a cache miss may build the app image — true in the loop
// (forge check) and at the gate, false in the read-only fleet sweep.
type ToolContext struct {
	Context    context.Context
	Exceptions []conformance.Disposition // non-nil is the admitted review snapshot, including an empty one
	ProjectID  ids.ID
	SHA        string
	BuildDir   string
	AllowBuild bool
}
